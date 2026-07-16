package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testApp(cfg config, upstreamURL string) *app {
	if cfg.serviceName == "" {
		cfg.serviceName = "aggregation-service"
	}
	if cfg.version == "" {
		cfg.version = "v0.0.0-test"
	}
	cfg.meteringURL = upstreamURL
	return &app{
		cfg:       cfg,
		log:       slog.New(slog.NewJSONHandler(io.Discard, nil)),
		metering:  newMeteringClient(upstreamURL, 2*time.Second),
		randFloat: func() float64 { return 0.5 },
	}
}

// fakeMetering stands in for metering-service.
func fakeMetering(t *testing.T, status int, body string) *httptest.Server {
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
  "service": "metering-service",
  "version": "v1.0.0",
  "data": {"readings": [
    {"meterId": "meter-001", "kWh": 10},
    {"meterId": "meter-002", "kWh": 20},
    {"meterId": "meter-003", "kWh": 30}
  ]}
}`

func TestComputeAggregate(t *testing.T) {
	tests := []struct {
		name     string
		readings []reading
		want     aggregateData
	}{
		{
			name:     "empty",
			readings: nil,
			want:     aggregateData{},
		},
		{
			name:     "single reading",
			readings: []reading{{KWh: 7.5}},
			want:     aggregateData{MeterCount: 1, TotalKWh: 7.5, AverageKWh: 7.5, MinKWh: 7.5, MaxKWh: 7.5},
		},
		{
			name:     "several readings",
			readings: []reading{{KWh: 10}, {KWh: 20}, {KWh: 30}},
			want:     aggregateData{MeterCount: 3, TotalKWh: 60, AverageKWh: 20, MinKWh: 10, MaxKWh: 30},
		},
		{
			name:     "rounds to two decimals",
			readings: []reading{{KWh: 1}, {KWh: 1}, {KWh: 1.001}},
			want:     aggregateData{MeterCount: 3, TotalKWh: 3, AverageKWh: 1, MinKWh: 1, MaxKWh: 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := computeAggregate(tt.readings); got != tt.want {
				t.Errorf("computeAggregate() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// The response must name both this service's version and the upstream's — that
// pairing is what demonstrates independent versioning of dependent services.
func TestAggregateReportsBothVersions(t *testing.T) {
	upstream := fakeMetering(t, http.StatusOK, okUpstreamBody)
	a := testApp(config{version: "v1.2.3"}, upstream.URL)

	rec := httptest.NewRecorder()
	a.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var got aggregateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Version != "v1.2.3" {
		t.Errorf("version = %q, want %q", got.Version, "v1.2.3")
	}
	if got.Upstream.Version != "v1.0.0" {
		t.Errorf("upstream version = %q, want %q", got.Upstream.Version, "v1.0.0")
	}
	if got.Upstream.Service != "metering-service" {
		t.Errorf("upstream service = %q, want %q", got.Upstream.Service, "metering-service")
	}
	if got.Data.TotalKWh != 60 {
		t.Errorf("total = %v, want 60", got.Data.TotalKWh)
	}
}

// A failing upstream must show up as a 5xx here too, so that a bad metering
// release degrades the success rate of its dependents rather than hiding.
func TestUpstreamFailureBecomesBadGateway(t *testing.T) {
	upstream := fakeMetering(t, http.StatusInternalServerError, `{"error":"injected fault"}`)
	a := testApp(config{}, upstream.URL)

	rec := httptest.NewRecorder()
	a.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
}

// The version that failed is the single most important thing to report: a bad
// release must stay attributable, so an upstream error body that names a version
// has to survive into this service's own error response.
func TestFailedUpstreamStillReportsItsVersion(t *testing.T) {
	upstream := fakeMetering(t, http.StatusInternalServerError,
		`{"service":"metering-service","version":"v2.0.0","error":"injected fault"}`)
	a := testApp(config{}, upstream.URL)

	rec := httptest.NewRecorder()
	a.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}

	var got errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Upstream.Version != "v2.0.0" {
		t.Errorf("upstream version = %q, want %q", got.Upstream.Version, "v2.0.0")
	}
	if got.Upstream.Service != "metering-service" {
		t.Errorf("upstream service = %q, want %q", got.Upstream.Service, "metering-service")
	}
}

func TestUnreachableUpstreamBecomesBadGateway(t *testing.T) {
	// Port 1 is reserved and closed; the dial fails immediately.
	a := testApp(config{}, "http://127.0.0.1:1")

	rec := httptest.NewRecorder()
	a.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
}

func TestFaultInjectionSkipsUpstreamCall(t *testing.T) {
	upstream := fakeMetering(t, http.StatusOK, okUpstreamBody)
	a := testApp(config{errorRate: 1}, upstream.URL)
	a.randFloat = func() float64 { return 0 }

	rec := httptest.NewRecorder()
	a.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

// Probe and scrape endpoints must survive a fully faulty configuration,
// otherwise a bad release would be restarted or go unobserved instead of
// being detected and rolled back.
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
		{"upstream url without scheme", "METERING_URL", "metering:8081"},
		{"upstream url with bad scheme", "METERING_URL", "ftp://metering:8081"},
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

func TestLoadConfigTrimsTrailingSlash(t *testing.T) {
	t.Setenv("METERING_URL", "http://metering-service:8081/")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.meteringURL != "http://metering-service:8081" {
		t.Errorf("meteringURL = %q, want %q", cfg.meteringURL, "http://metering-service:8081")
	}
}
