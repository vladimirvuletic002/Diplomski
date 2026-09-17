package main

import (
	"encoding/json"
	"net/http"
)

// summaryResponse reports every service that took part, in call order
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

// handleSummary fans out to aggregation-service and assembles the version chain
func (a *app) handleSummary(w http.ResponseWriter, r *http.Request) {
	self := chainHop{Service: a.cfg.serviceName, Version: a.cfg.version}

	upstream, err := a.aggregation.fetchAggregate(r.Context())
	if err != nil {
		// 502 so the failure registers as a 5xx here too: a bad release anywhere
		// in the chain should degrade everything in front of it, not hide behind
		// a 200.
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

// buildChain includes a hop only once it has identified itself, so a chain that
// stops early means that service never answered at all
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
