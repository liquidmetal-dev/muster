package webhook

import "sync"

// Outcomes passed to Metrics.ObserveDelivery.
const (
	OutcomeHandled      = "handled"
	OutcomeDuplicate    = "duplicate"
	OutcomeIgnored      = "ignored"
	OutcomeBadSignature = "bad_signature"
	OutcomeBadRequest   = "bad_request"
	OutcomeTooLarge     = "too_large"
)

// Metrics receives one ObserveDelivery per request.
type Metrics interface {
	// ObserveDelivery records a delivery. action is the workflow_job
	// action, or "" when it is not known or the event is not workflow_job.
	ObserveDelivery(action, outcome string)
}

// NopMetrics discards every observation.
type NopMetrics struct{}

// ObserveDelivery discards the observation.
func (NopMetrics) ObserveDelivery(string, string) {}

// Counters is an in-memory Metrics, safe for concurrent use. The zero value
// is ready to use.
type Counters struct {
	mu     sync.Mutex
	counts map[[2]string]int
}

// ObserveDelivery counts one delivery of action with outcome.
func (c *Counters) ObserveDelivery(action, outcome string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.counts == nil {
		c.counts = map[[2]string]int{}
	}
	c.counts[[2]string{action, outcome}]++
}

// Count returns the number of deliveries of action observed with outcome.
func (c *Counters) Count(action, outcome string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[[2]string{action, outcome}]
}
