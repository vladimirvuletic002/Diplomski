package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// meteringClient talks to metering-service
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

// upstreamReadings mirrors only the part of metering's response this service needs
type upstreamReadings struct {
	Service string `json:"service"`
	Version string `json:"version"`
	Data    struct {
		Readings []reading `json:"readings"`
	} `json:"data"`
}

// fetchReadings calls metering-service, propagating the request context so a
// cancelled client request does not leave the call hanging
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
		// metering names itself even on an error, so decode best-effort and hand
		// the identity back. Dropping it here would make a failing release
		// anonymous downstream — exactly the version that needs identifying
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
