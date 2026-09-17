package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// aggregationClient talks to aggregation-service
type aggregationClient struct {
	baseURL string
	http    *http.Client
}

func newAggregationClient(baseURL string, timeout time.Duration) *aggregationClient {
	return &aggregationClient{
		baseURL: baseURL,
		http:    &http.Client{Timeout: timeout},
	}
}

// upstreamAggregate mirrors aggregation's response
type upstreamAggregate struct {
	Service  string `json:"service"`
	Version  string `json:"version"`
	Upstream struct {
		Service string `json:"service"`
		Version string `json:"version"`
	} `json:"upstream"`
	Data aggregateData `json:"data"`
}

// fetchAggregate calls aggregation-service, propagating the request context so a
// cancelled client request does not leave the call hanging
func (c *aggregationClient) fetchAggregate(ctx context.Context) (upstreamAggregate, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/", nil)
	if err != nil {
		return upstreamAggregate{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return upstreamAggregate{}, fmt.Errorf("call aggregation-service: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// aggregation reports itself and its upstream even on an error, so decode
		// best-effort and keep whatever identity survived. Without this a failing
		// release drops out of the chain and the dashboard cannot name it
		var partial upstreamAggregate
		_ = json.NewDecoder(resp.Body).Decode(&partial)
		return partial, fmt.Errorf("aggregation-service returned %s", resp.Status)
	}

	var out upstreamAggregate
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return upstreamAggregate{}, fmt.Errorf("decode aggregation-service response: %w", err)
	}
	return out, nil
}
