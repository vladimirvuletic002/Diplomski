package main

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// config is entirely env-driven, which is what makes the faulty release used in
// the rollback demo a configuration change rather than a separate build.
type config struct {
	port         string
	version      string
	serviceName  string
	errorRate    float64
	extraLatency time.Duration
	logLevel     slog.Level

	// Configurable, so routing this call through the ingress — where canary
	// weights apply — stays a deployment decision rather than a code change.
	aggregationURL  string
	upstreamTimeout time.Duration
}

// loadConfig fails fast on bad input: silently ignoring a typo'd ERROR_RATE
// would quietly invalidate the canary analysis.
func loadConfig() (config, error) {
	errorRate, err := envFloat("ERROR_RATE", 0)
	if err != nil {
		return config{}, err
	}
	if errorRate < 0 || errorRate > 1 {
		return config{}, fmt.Errorf("ERROR_RATE must be between 0.0 and 1.0, got %v", errorRate)
	}

	latencyMS, err := envInt("EXTRA_LATENCY_MS", 0)
	if err != nil {
		return config{}, err
	}
	if latencyMS < 0 {
		return config{}, fmt.Errorf("EXTRA_LATENCY_MS must not be negative, got %d", latencyMS)
	}

	timeoutMS, err := envInt("UPSTREAM_TIMEOUT_MS", 2000)
	if err != nil {
		return config{}, err
	}
	if timeoutMS <= 0 {
		return config{}, fmt.Errorf("UPSTREAM_TIMEOUT_MS must be positive, got %d", timeoutMS)
	}

	logLevel, err := envLogLevel("LOG_LEVEL", slog.LevelInfo)
	if err != nil {
		return config{}, err
	}

	aggregationURL := envOr("AGGREGATION_URL", "http://localhost:8082")
	if err := validateURL("AGGREGATION_URL", aggregationURL); err != nil {
		return config{}, err
	}

	return config{
		port:            envOr("PORT", "8080"),
		version:         envOr("VERSION", version),
		serviceName:     envOr("SERVICE_NAME", "gateway"),
		errorRate:       errorRate,
		extraLatency:    time.Duration(latencyMS) * time.Millisecond,
		logLevel:        logLevel,
		aggregationURL:  strings.TrimRight(aggregationURL, "/"),
		upstreamTimeout: time.Duration(timeoutMS) * time.Millisecond,
	}, nil
}

func validateURL(key, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s: %q is not a valid URL: %w", key, raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%s: %q must use http or https", key, raw)
	}
	if u.Host == "" {
		return fmt.Errorf("%s: %q is missing a host", key, raw)
	}
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envFloat(key string, def float64) (float64, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return def, nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a valid number", key, raw)
	}
	return v, nil
}

func envInt(key string, def int) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a valid integer", key, raw)
	}
	return v, nil
}

func envLogLevel(key string, def slog.Level) (slog.Level, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return def, nil
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(strings.ToUpper(raw))); err != nil {
		return 0, fmt.Errorf("%s: %q is not a valid log level (debug, info, warn, error)", key, raw)
	}
	return level, nil
}
