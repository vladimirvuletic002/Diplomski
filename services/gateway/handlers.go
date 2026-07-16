package main

import (
	"encoding/json"
	"net/http"
)

// summaryResponse reports the version of every service that took part in
// serving the request, in call order. The chain is a list rather than named
// fields so the UI can render it generically: whichever service is mid-canary,
// the version split shows up without the UI knowing which one to expect.
type summaryResponse struct {
	Service string        `json:"service"`
	Version string        `json:"version"`
	Chain   []chainHop    `json:"chain"`
	Data    aggregateData `json:"data"`
}

type chainHop struct {
	Service string `json:"service"`
	Version string `json:"version"`
}

type aggregateData struct {
	MeterCount int     `json:"meterCount"`
	TotalKWh   float64 `json:"totalKWh"`
	AverageKWh float64 `json:"averageKWh"`
	MinKWh     float64 `json:"minKWh"`
	MaxKWh     float64 `json:"maxKWh"`
}

type errorResponse struct {
	Service string     `json:"service"`
	Version string     `json:"version"`
	Chain   []chainHop `json:"chain"`
	Error   string     `json:"error"`
}

type healthResponse struct {
	Service string `json:"service"`
	Version string `json:"version"`
	Status  string `json:"status"`
}

// handleSummary fans out to aggregation-service and assembles the version chain.
func (a *app) handleSummary(w http.ResponseWriter, r *http.Request) {
	self := chainHop{Service: a.cfg.serviceName, Version: a.cfg.version}

	upstream, err := a.aggregation.fetchAggregate(r.Context())
	if err != nil {
		// Reported as 502 so an upstream failure registers as a 5xx here too: a
		// bad release anywhere in the chain should degrade the success rate of
		// everything in front of it rather than hiding behind a 200.
		a.log.Error("upstream call failed", "upstream", a.cfg.aggregationURL, "error", err)
		writeJSON(w, http.StatusBadGateway, errorResponse{
			Service: a.cfg.serviceName,
			Version: a.cfg.version,
			Chain:   buildChain(self, upstream),
			Error:   "aggregation-service unavailable",
		})
		return
	}

	writeJSON(w, http.StatusOK, summaryResponse{
		Service: a.cfg.serviceName,
		Version: a.cfg.version,
		Chain:   buildChain(self, upstream),
		Data:    upstream.Data,
	})
}

// buildChain assembles the version chain from whatever the upstream managed to
// report. Hops are included only once they have identified themselves, so a
// chain that stops early means the next service never answered at all — as
// opposed to answering with an error, which still yields a named hop.
func buildChain(self chainHop, upstream upstreamAggregate) []chainHop {
	chain := []chainHop{self}
	if upstream.Service == "" {
		return chain
	}
	chain = append(chain, chainHop{Service: upstream.Service, Version: upstream.Version})
	if upstream.Upstream.Service == "" {
		return chain
	}
	return append(chain, chainHop{Service: upstream.Upstream.Service, Version: upstream.Upstream.Version})
}

func (a *app) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{
		Service: a.cfg.serviceName,
		Version: a.cfg.version,
		Status:  "ok",
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
