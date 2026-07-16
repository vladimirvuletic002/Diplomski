package main

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// The version label is what lets the canary analysis isolate the new version's
// traffic. Without it a success rate computed over the whole service blends
// stable and canary together, and a canary that fails every request barely
// moves the blended number while it only holds a small traffic weight.
var (
	requestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total HTTP requests by service, version, method, route and status.",
		},
		[]string{"service", "version", "method", "route", "status"},
	)

	requestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request latency by service, version, method and route.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"service", "version", "method", "route"},
	)
)

// statusRecorder captures the status code on its way out to the client.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// instrument records metrics for one route. The route label is the registered
// pattern rather than the request URL, which keeps label cardinality bounded.
func (a *app) instrument(route string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		requestsTotal.WithLabelValues(
			a.cfg.serviceName, a.cfg.version, r.Method, route, strconv.Itoa(rec.status),
		).Inc()
		requestDuration.WithLabelValues(
			a.cfg.serviceName, a.cfg.version, r.Method, route,
		).Observe(time.Since(start).Seconds())
	})
}
