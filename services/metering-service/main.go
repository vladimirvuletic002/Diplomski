package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// version is baked in at build time with -ldflags "-X main.version=v1.0.0" so it
// always matches the image tag it shipped in. The VERSION env var overrides it
// for local development, where there is no build pipeline to do the baking.
var version = "dev"

const (
	shutdownTimeout   = 15 * time.Second
	readHeaderTimeout = 5 * time.Second
)

type app struct {
	cfg config
	log *slog.Logger
	// randFloat is injectable so fault injection is deterministic under test.
	randFloat func() float64
}

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.logLevel}))
	a := &app{cfg: cfg, log: logger, randFloat: rand.Float64}

	srv := &http.Server{
		Addr:              ":" + cfg.port,
		Handler:           a.routes(),
		ReadHeaderTimeout: readHeaderTimeout,
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("listening",
			"service", cfg.serviceName,
			"version", cfg.version,
			"port", cfg.port,
			"error_rate", cfg.errorRate,
			"extra_latency", cfg.extraLatency.String(),
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-serveErr:
		return fmt.Errorf("listen: %w", err)
	case <-ctx.Done():
		stop()
	}

	// Draining matters for the rollout: pods are terminated every time a canary
	// scales down, and cutting in-flight requests would surface as 5xx that the
	// analysis reads as a bad release.
	logger.Info("shutdown signal received, draining connections")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}

	logger.Info("shutdown complete")
	return nil
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()

	// Only business traffic is instrumented and fault-injected. Probe and scrape
	// traffic is excluded on purpose: kubelet probes are frequent and always
	// succeed, so counting them would dilute the success rate that decides
	// whether a release is rolled back.
	mux.Handle("GET /{$}", a.instrument("/", a.injectFaults(http.HandlerFunc(a.handleReadings))))
	mux.Handle("GET /healthz", http.HandlerFunc(a.handleHealthz))
	mux.Handle("GET /metrics", promhttp.Handler())

	return mux
}
