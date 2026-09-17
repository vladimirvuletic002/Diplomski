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

var version = "dev"

const (
	shutdownTimeout   = 15 * time.Second
	readHeaderTimeout = 5 * time.Second
)

type app struct {
	cfg       config
	log       *slog.Logger
	metering  *meteringClient
	randFloat func() float64 // injectable so fault injection is deterministic in tests
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
	a := &app{
		cfg:       cfg,
		log:       logger,
		metering:  newMeteringClient(cfg.meteringURL, cfg.upstreamTimeout),
		randFloat: rand.Float64,
	}

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
			"metering_url", cfg.meteringURL,
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

	// Canary steps terminate pods constantly; dropping in-flight requests would
	// surface as 5xx and read as a bad release
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

	mux.Handle("GET /{$}", a.instrument("/", a.injectFaults(http.HandlerFunc(a.handleAggregate))))
	mux.Handle("GET /healthz", http.HandlerFunc(a.handleHealthz))
	mux.Handle("GET /metrics", promhttp.Handler())

	return mux
}
