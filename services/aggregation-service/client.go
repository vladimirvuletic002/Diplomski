package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// meteringClient talks to metering-service.
type meteringClient struct {
	baseURL string
	http    *http.Client
}

func newMeteringClient(baseURL string, timeout time.Duration) *meteringClient {
	return &meteringClient{
		baseURL: baseURL,
		http:    &http.Client{Timeout: timeout},
	}
}

// upstreamReadings mirrors the part of metering-service's response this service
// depends on. It is kept minimal on purpose: the less of the upstream contract
// consumed here, the more freely metering can version independently.
type upstreamReadings struct {
	Service string `json:"service"`
	Version string `json:"version"`
	Data    struct {
		Readings []reading `json:"readings"`
	} `json:"data"`
}

// fetchReadings retrieves readings from metering-service. The request context is
// propagated so a cancelled client request does not leave the call hanging.
func (c *meteringClient) fetchReadings(ctx context.Context) (upstreamReadings, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/", nil)
	if err != nil {
		return upstreamReadings{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return upstreamReadings{}, fmt.Errorf("call metering-service: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// metering-service names itself and its version even on an error, so decode
		// the body best-effort and hand it back alongside the error. Losing that
		// here would make a failing release anonymous to everything downstream —
		// exactly the version that most needs to be identifiable.
		var partial upstreamReadings
		_ = json.NewDecoder(resp.Body).Decode(&partial)
		return partial, fmt.Errorf("metering-service returned %s", resp.Status)
	}

	var out upstreamReadings
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return upstreamReadings{}, fmt.Errorf("decode metering-service response: %w", err)
	}
	return out, nil
}
