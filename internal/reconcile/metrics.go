package reconcile

import "sync"

// Decision actions.
const (
	ActionAdopt        = "adopt"
	ActionReleaseLease = "release_lease"
	ActionDeleteRunner = "delete_runner"
)

// Decision outcomes.
const (
	OutcomeOK     = "ok"
	OutcomeFailed = "failed"
)

// Metrics receives one ObserveDecision per reconciliation decision.
type Metrics interface {
	ObserveDecision(action, outcome string)
}

// NopMetrics discards every observation.
type NopMetrics struct{}

// ObserveDecision discards the observation.
func (NopMetrics) ObserveDecision(string, string) {}

// Counters is an in-memory Metrics, safe for concurrent use. The zero value
// is ready to use.
type Counters struct {
	mu        sync.Mutex
	decisions map[[2]string]int
}

// ObserveDecision counts one decision.
func (c *Counters) ObserveDecision(action, outcome string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.decisions == nil {
		c.decisions = map[[2]string]int{}
	}
	c.decisions[[2]string{action, outcome}]++
}

// Decisions returns the number of decisions observed for action with
// outcome.
func (c *Counters) Decisions(action, outcome string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.decisions[[2]string{action, outcome}]
}
