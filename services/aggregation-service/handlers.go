package main

import (
	"encoding/json"
	"math"
	"net/http"
	"time"
)

// reading mirrors one metering-service sample.
type reading struct {
	MeterID   string    `json:"meterId"`
	KWh       float64   `json:"kWh"`
	Timestamp time.Time `json:"timestamp"`
}

// aggregateResponse reports both this service's version and the version of the
// metering-service instance that served the underlying data. Surfacing the
// upstream version is what makes independent versioning of dependent services
// visible: the chain can show v1 aggregation reading from v2 metering.
type aggregateResponse struct {
	Service  string        `json:"service"`
	Version  string        `json:"version"`
	Upstream upstreamInfo  `json:"upstream"`
	Data     aggregateData `json:"data"`
}

type upstreamInfo struct {
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
	Service string `json:"service"`
	Version string `json:"version"`
	// Upstream is reported whenever the upstream identified itself, including on
	// failure, so a bad release stays attributable to a specific version.
	Upstream upstreamInfo `json:"upstream"`
	Error    string       `json:"error"`
}

type healthResponse struct {
	Service string `json:"service"`
	Version string `json:"version"`
	Status  string `json:"status"`
}

// handleAggregate fetches readings from metering-service and derives totals.
func (a *app) handleAggregate(w http.ResponseWriter, r *http.Request) {
	upstream, err := a.metering.fetchReadings(r.Context())
	if err != nil {
		// A failing upstream is reported as 502 so it registers as a 5xx in this
		// service's metrics too: a bad metering release should be visible in the
		// success rate of everything that depends on it.
		a.log.Error("upstream call failed", "upstream", a.cfg.meteringURL, "error", err)
		writeJSON(w, http.StatusBadGateway, errorResponse{
			Service: a.cfg.serviceName,
			Version: a.cfg.version,
			Upstream: upstreamInfo{
				Service: upstream.Service,
				Version: upstream.Version,
			},
			Error: "metering-service unavailable",
		})
		return
	}

	writeJSON(w, http.StatusOK, aggregateResponse{
		Service: a.cfg.serviceName,
		Version: a.cfg.version,
		Upstream: upstreamInfo{
			Service: upstream.Service,
			Version: upstream.Version,
		},
		Data: computeAggregate(upstream.Data.Readings),
	})
}

// computeAggregate derives totals from readings. Kept pure so it can be tested
// without a server or an upstream.
func computeAggregate(readings []reading) aggregateData {
	if len(readings) == 0 {
		return aggregateData{}
	}

	total := 0.0
	minKWh := readings[0].KWh
	maxKWh := readings[0].KWh
	for _, r := range readings {
		total += r.KWh
		minKWh = min(minKWh, r.KWh)
		maxKWh = max(maxKWh, r.KWh)
	}

	return aggregateData{
		MeterCount: len(readings),
		TotalKWh:   round2(total),
		AverageKWh: round2(total / float64(len(readings))),
		MinKWh:     round2(minKWh),
		MaxKWh:     round2(maxKWh),
	}
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
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
