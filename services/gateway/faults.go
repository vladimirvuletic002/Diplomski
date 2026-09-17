package main

import (
	"net/http"
	"time"
)

// injectFaults fails or delays responses per ERROR_RATE and EXTRA_LATENCY_MS,
// so a release can be failed on either success rate or latency.
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
				// Named even here, so a faulty release is never an anonymous error.
				Chain: []chainHop{{Service: a.cfg.serviceName, Version: a.cfg.version}},
				Error: "injected fault",
			})
			return
		}

		next.ServeHTTP(w, r)
	})
}
