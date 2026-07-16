package main

import (
	"net/http"
	"time"
)

// injectFaults degrades responses on purpose, driven entirely by ERROR_RATE and
// EXTRA_LATENCY_MS. Two failure modes are supported so a release can be failed
// either on success rate or on latency.
//
// This is deliberately applied only to business routes. A faulty version must
// stay live and scrapeable for the canary analysis to observe it failing: if
// /healthz returned errors the kubelet would simply restart the pod, and if
// /metrics did, Prometheus would lose the very signal the rollback depends on.
func (a *app) injectFaults(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.cfg.extraLatency > 0 {
			select {
			case <-time.After(a.cfg.extraLatency):
			case <-r.Context().Done():
				return
			}
		}

		if a.cfg.errorRate > 0 && a.randFloat() < a.cfg.errorRate {
			a.log.Warn("injected fault", "route", r.URL.Path, "error_rate", a.cfg.errorRate)
			writeJSON(w, http.StatusInternalServerError, errorResponse{
				Service: a.cfg.serviceName,
				Version: a.cfg.version,
				// Named even here, so a faulty gateway release is attributable to
				// its own version rather than showing up as an anonymous error.
				Chain: []chainHop{{Service: a.cfg.serviceName, Version: a.cfg.version}},
				Error: "injected fault",
			})
			return
		}

		next.ServeHTTP(w, r)
	})
}
