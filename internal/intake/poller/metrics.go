package poller

import (
	"sync"
	"time"
)

// Outcomes passed to Metrics.ObservePoll.
const (
	OutcomeOK          = "ok"
	OutcomeError       = "error"
	OutcomeRateLimited = "rate_limited"
)

// Metrics receives one ObservePoll per repository listed.
type Metrics interface {
	// ObservePoll records one listing of repo that took d.
	ObservePoll(repo, outcome string, d time.Duration)
}

// NopMetrics discards every observation.
type NopMetrics struct{}

// ObservePoll discards the observation.
func (NopMetrics) ObservePoll(string, string, time.Duration) {}

// Counters is an in-memory Metrics, safe for concurrent use. The zero value
// is ready to use.
type Counters struct {
	mu        sync.Mutex
	counts    map[[2]string]int
	durations map[string]time.Duration
}

// ObservePoll counts one listing of repo with outcome and adds d to the
// repo's total duration.
func (c *Counters) ObservePoll(repo, outcome string, d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.counts == nil {
		c.counts = map[[2]string]int{}
		c.durations = map[string]time.Duration{}
	}
	c.counts[[2]string{repo, outcome}]++
	c.durations[repo] += d
}

// Count returns the number of listings of repo observed with outcome.
func (c *Counters) Count(repo, outcome string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[[2]string{repo, outcome}]
}

// Duration returns the total duration observed for repo.
func (c *Counters) Duration(repo string) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.durations[repo]
}
