package main

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// config holds every runtime knob. All of it is env-driven so that one image can
// behave differently per environment — in particular so the faulty v2 used in the
// rollback demo is a configuration change rather than a separate build.
type config struct {
	port         string
	version      string
	serviceName  string
	errorRate    float64
	extraLatency time.Duration
	logLevel     slog.Level
}

// loadConfig reads configuration from the environment and fails fast on bad
// input: a service that silently ignores a typo'd ERROR_RATE would quietly
// invalidate the canary analysis.
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

	logLevel, err := envLogLevel("LOG_LEVEL", slog.LevelInfo)
	if err != nil {
		return config{}, err
	}

	return config{
		port:         envOr("PORT", "8081"),
		version:      envOr("VERSION", version),
		serviceName:  envOr("SERVICE_NAME", "metering-service"),
		errorRate:    errorRate,
		extraLatency: time.Duration(latencyMS) * time.Millisecond,
		logLevel:     logLevel,
	}, nil
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
