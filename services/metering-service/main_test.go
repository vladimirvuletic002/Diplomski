package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"testing"
)

func mustSeededRand() func() float64 {
	return rand.New(rand.NewPCG(1, 2)).Float64
}

func testApp(cfg config) *app {
	if cfg.serviceName == "" {
		cfg.serviceName = "metering-service"
	}
	if cfg.version == "" {
		cfg.version = "v0.0.0-test"
	}
	return &app{
		cfg:       cfg,
		log:       slog.New(slog.NewJSONHandler(io.Discard, nil)),
		randFloat: func() float64 { return 0.5 },
	}
}

// Every response must name the version that served it, the whole demo rests on
// being able to attribute a response to a release
func TestReadingsResponseCarriesVersion(t *testing.T) {
	a := testApp(config{version: "v1.2.3"})

	rec := httptest.NewRecorder()
	a.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var got readingsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Version != "v1.2.3" {
		t.Errorf("version = %q, want %q", got.Version, "v1.2.3")
	}
	if got.Service != "metering-service" {
		t.Errorf("service = %q, want %q", got.Service, "metering-service")
	}
	if len(got.Data.Readings) != meterCount {
		t.Errorf("readings = %d, want %d", len(got.Data.Readings), meterCount)
	}
}

func TestFaultInjection(t *testing.T) {
	tests := []struct {
		name       string
		errorRate  float64
		randValue  float64
		wantStatus int
	}{
		{"disabled", 0, 0.0, http.StatusOK},
		{"rand above rate", 0.3, 0.9, http.StatusOK},
		{"rand below rate", 0.3, 0.1, http.StatusInternalServerError},
		{"always fails", 1, 0.99, http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := testApp(config{errorRate: tt.errorRate})
			a.randFloat = func() float64 { return tt.randValue }

			rec := httptest.NewRecorder()
			a.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}

// Probe and scrape endpoints must survive a fully faulty configuration,
// otherwise a bad release would be restarted or go unobserved instead of
// being detected and rolled back
func TestHealthzAndMetricsIgnoreFaults(t *testing.T) {
	a := testApp(config{errorRate: 1})
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
		{"negative error rate", "ERROR_RATE", "-0.1"},
		{"non-numeric error rate", "ERROR_RATE", "high"},
		{"negative latency", "EXTRA_LATENCY_MS", "-5"},
		{"bad log level", "LOG_LEVEL", "chatty"},
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

func TestVersionEnvOverridesBakedDefault(t *testing.T) {
	t.Setenv("VERSION", "v9.9.9")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.version != "v9.9.9" {
		t.Errorf("version = %q, want %q", cfg.version, "v9.9.9")
	}
}

func TestErrorRateIsStatisticallyHonoured(t *testing.T) {
	a := testApp(config{errorRate: 0.3})
	a.randFloat = mustSeededRand()

	const runs = 10000
	failures := 0
	handler := a.routes()
	for range runs {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code == http.StatusInternalServerError {
			failures++
		}
	}

	got := float64(failures) / runs
	if got < 0.27 || got > 0.33 {
		t.Errorf("failure ratio = %.3f, want ~0.30", got)
	}
}
