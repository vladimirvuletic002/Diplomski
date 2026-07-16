package main

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"time"
)

const meterCount = 5

// reading is one simulated electricity meter sample.
type reading struct {
	MeterID   string    `json:"meterId"`
	KWh       float64   `json:"kWh"`
	Timestamp time.Time `json:"timestamp"`
}

// readingsResponse carries the serving version alongside the payload so every
// hop in the chain can be attributed to a specific release.
type readingsResponse struct {
	Service string       `json:"service"`
	Version string       `json:"version"`
	Data    readingsData `json:"data"`
}

type readingsData struct {
	Readings []reading `json:"readings"`
}

type errorResponse struct {
	Service string `json:"service"`
	Version string `json:"version"`
	Error   string `json:"error"`
}

type healthResponse struct {
	Service string `json:"service"`
	Version string `json:"version"`
	Status  string `json:"status"`
}

// handleReadings returns randomised consumption readings. The business logic is
// intentionally trivial — the infrastructure around it is the subject here.
func (a *app) handleReadings(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	readings := make([]reading, 0, meterCount)
	for i := range meterCount {
		readings = append(readings, reading{
			MeterID:   fmt.Sprintf("meter-%03d", i+1),
			KWh:       math.Round((rand.Float64()*40+5)*100) / 100,
			Timestamp: now,
		})
	}

	writeJSON(w, http.StatusOK, readingsResponse{
		Service: a.cfg.serviceName,
		Version: a.cfg.version,
		Data:    readingsData{Readings: readings},
	})
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
