package github

import (
	"context"
	"net/http"
	"sync"
)

// Metrics receives one ObserveCall per HTTP attempt and one ObserveError per
// failed attempt (requirement GH-5). Endpoint is a route template such as
// "GET /orgs/{org}/actions/runners", never a raw path, so label cardinality
// stays bounded.
type Metrics interface {
	// ObserveCall records an attempt; status is 0 when no response arrived.
	ObserveCall(endpoint string, status int)
	// ObserveError records a failure of kind "rate_limited", "server",
	// "network", "timeout" or "client".
	ObserveError(endpoint string, kind string)
}

// NopMetrics discards every observation.
type NopMetrics struct{}

// ObserveCall discards the observation.
func (NopMetrics) ObserveCall(string, int) {}

// ObserveError discards the observation.
func (NopMetrics) ObserveError(string, string) {}

// Counters is an in-memory Metrics, safe for concurrent use. The zero value
// is ready to use.
type Counters struct {
	mu     sync.Mutex
	calls  map[string]int
	errors map[[2]string]int
}

// ObserveCall counts one attempt for endpoint.
func (c *Counters) ObserveCall(endpoint string, _ int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.calls == nil {
		c.calls = map[string]int{}
	}
	c.calls[endpoint]++
}

// ObserveError counts one failure of kind for endpoint.
func (c *Counters) ObserveError(endpoint, kind string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.errors == nil {
		c.errors = map[[2]string]int{}
	}
	c.errors[[2]string{endpoint, kind}]++
}

// Calls returns the number of attempts observed for endpoint.
func (c *Counters) Calls(endpoint string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[endpoint]
}

// Errors returns the number of failures of kind observed for endpoint.
func (c *Counters) Errors(endpoint, kind string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.errors[[2]string{endpoint, kind}]
}

const (
	endpointAccessToken  = "POST /app/installations/{id}/access_tokens" //nolint:gosec // a route template, not a credential
	endpointInstallation = "GET /app/installations/{id}"
	endpointUser         = "GET /user"
)

type endpointKey struct{}

func withEndpoint(ctx context.Context, endpoint string) context.Context {
	return context.WithValue(ctx, endpointKey{}, endpoint)
}

func endpointFrom(ctx context.Context, r *http.Request) string {
	if e, ok := ctx.Value(endpointKey{}).(string); ok {
		return e
	}
	return r.Method + " unknown"
}
