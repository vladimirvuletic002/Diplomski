package main

import (
	"encoding/json"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testApp(cfg config, upstreamURL string) *app {
	if cfg.serviceName == "" {
		cfg.serviceName = "gateway"
	}
	if cfg.version == "" {
		cfg.version = "v0.0.0-test"
	}
	cfg.aggregationURL = upstreamURL
	return &app{
		cfg:         cfg,
		log:         slog.New(slog.NewJSONHandler(io.Discard, nil)),
		aggregation: newAggregationClient(upstreamURL, 2*time.Second),
		ui:          template.Must(template.ParseFS(uiFS, "ui/index.html")),
		randFloat:   func() float64 { return 0.5 },
	}
}

// fakeAggregation stands in for aggregation-service
func fakeAggregation(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

const okUpstreamBody = `{
  "service": "aggregation-service",
  "version": "v1.1.0",
  "upstream": {"service": "metering-service", "version": "v2.0.0"},
  "data": {"meterCount": 3, "totalKWh": 60, "averageKWh": 20, "minKWh": 10, "maxKWh": 30}
}`

// The chain is the demo's core contract: every service that touched the request,
// in call order, each with the version that served it.
func TestSummaryReturnsFullVersionChain(t *testing.T) {
	upstream := fakeAggregation(t, http.StatusOK, okUpstreamBody)
	a := testApp(config{version: "v1.0.0"}, upstream.URL)

	rec := httptest.NewRecorder()
	a.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/summary", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var got summaryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	want := []chainHop{
		{Service: "gateway", Version: "v1.0.0"},
		{Service: "aggregation-service", Version: "v1.1.0"},
		{Service: "metering-service", Version: "v2.0.0"},
	}
	if len(got.Chain) != len(want) {
		t.Fatalf("chain length = %d, want %d", len(got.Chain), len(want))
	}
	for i, hop := range want {
		if got.Chain[i] != hop {
			t.Errorf("chain[%d] = %+v, want %+v", i, got.Chain[i], hop)
		}
	}
	if got.Data.TotalKWh != 60 {
		t.Errorf("total = %v, want 60", got.Data.TotalKWh)
	}
}

// When nothing upstream answers at all, the chain is just the gateway
func TestUnreachableUpstreamYieldsGatewayOnlyChain(t *testing.T) {
	a := testApp(config{version: "v1.0.0"}, "http://127.0.0.1:1")

	rec := httptest.NewRecorder()
	a.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/summary", nil))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}

	var got errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got.Chain) != 1 || got.Chain[0].Version != "v1.0.0" {
		t.Errorf("chain = %+v, want single gateway hop at v1.0.0", got.Chain)
	}
}

// The failure demo depends on this: when a canary further down the chain is
// serving errors, the dashboard must still be able to say *which version* is
// producing them. A 502 that forgets the chain makes the bad release anonymous.
func TestFailedChainStillCarriesEveryVersion(t *testing.T) {
	upstream := fakeAggregation(t, http.StatusBadGateway, `{
      "service": "aggregation-service",
      "version": "v1.1.0",
      "upstream": {"service": "metering-service", "version": "v2.0.0"},
      "error": "metering-service unavailable"
    }`)
	a := testApp(config{version: "v1.0.0"}, upstream.URL)

	rec := httptest.NewRecorder()
	a.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/summary", nil))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}

	var got errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	want := []chainHop{
		{Service: "gateway", Version: "v1.0.0"},
		{Service: "aggregation-service", Version: "v1.1.0"},
		{Service: "metering-service", Version: "v2.0.0"},
	}
	if len(got.Chain) != len(want) {
		t.Fatalf("chain = %+v, want %d hops", got.Chain, len(want))
	}
	for i, hop := range want {
		if got.Chain[i] != hop {
			t.Errorf("chain[%d] = %+v, want %+v", i, got.Chain[i], hop)
		}
	}
}

// A gateway that injects its own fault must still name itself
func TestInjectedFaultNamesGatewayVersion(t *testing.T) {
	upstream := fakeAggregation(t, http.StatusOK, okUpstreamBody)
	a := testApp(config{errorRate: 1, version: "v2.0.0"}, upstream.URL)
	a.randFloat = func() float64 { return 0 }

	rec := httptest.NewRecorder()
	a.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/summary", nil))

	var got errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got.Chain) != 1 || got.Chain[0] != (chainHop{Service: "gateway", Version: "v2.0.0"}) {
		t.Errorf("chain = %+v, want single gateway hop at v2.0.0", got.Chain)
	}
}

// The dashboard must keep rendering even when the release is completely broken
func TestUIStaysUpUnderTotalFault(t *testing.T) {
	a := testApp(config{errorRate: 1, version: "v1.0.0"}, "http://127.0.0.1:1")
	a.randFloat = func() float64 { return 0 }

	rec := httptest.NewRecorder()
	a.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type = %q, want text/html", ct)
	}
	if !strings.Contains(rec.Body.String(), `content="v1.0.0"`) {
		t.Error("rendered page does not carry the gateway version")
	}
}

func TestFaultInjectionAppliesToAPIOnly(t *testing.T) {
	upstream := fakeAggregation(t, http.StatusOK, okUpstreamBody)
	a := testApp(config{errorRate: 1}, upstream.URL)
	a.randFloat = func() float64 { return 0 }

	rec := httptest.NewRecorder()
	a.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/summary", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("/api/summary status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

// Probe and scrape endpoints must survive a fully faulty configuration
func TestHealthzAndMetricsIgnoreFaults(t *testing.T) {
	a := testApp(config{errorRate: 1}, "http://127.0.0.1:1")
	a.randFloat = func() float64 { return 0 }

	for _, route := range []string{"/healthz", "/metrics"} {
		rec := httptest.NewRecorder()
		a.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, route, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s status = %d, want %d", route, rec.Code, http.StatusOK)
		}
	}
}

func TestLoadConfigRejectsBadValues(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{"error rate above one", "ERROR_RATE", "1.5"},
		{"non-numeric error rate", "ERROR_RATE", "high"},
		{"negative latency", "EXTRA_LATENCY_MS", "-5"},
		{"zero timeout", "UPSTREAM_TIMEOUT_MS", "0"},
		{"bad log level", "LOG_LEVEL", "chatty"},
		{"upstream url without scheme", "AGGREGATION_URL", "aggregation:8082"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.key, tt.value)
			if _, err := loadConfig(); err == nil {
				t.Fatalf("loadConfig() with %s=%s returned no error", tt.key, tt.value)
			}
		})
	}
}
