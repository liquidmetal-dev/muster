package session

import (
	"slices"
	"sync"
	"time"
)

// Metrics receives the observations of the sessions (requirement OBS-1).
// The calls must not block: sessions make them on their own goroutines.
type Metrics interface {
	// ObserveClaimAttempt records one Acquire call; exhausted is true when
	// the pool had no VM.
	ObserveClaimAttempt(pool string, exhausted bool)
	// ObserveClaim records the time from CLAIMING to the claimed VM,
	// backoff included.
	ObserveClaim(pool string, d time.Duration)
	// ObserveOnline records the time from the creation of the session to
	// its runner coming online.
	ObserveOnline(pool string, d time.Duration)
	// ObserveFinished records a finished session. d is the time from its
	// creation; it is zero when adopted is true, because the registry did
	// not create the session.
	ObserveFinished(pool string, outcome State, reason Reason, d time.Duration, adopted bool)
	// ObserveRelease records one VM release attempt during the cleanup of
	// a session that finished with outcome and reason.
	ObserveRelease(pool string, outcome State, reason Reason, ok bool)
}

// NopMetrics discards every observation.
type NopMetrics struct{}

// ObserveClaimAttempt discards the observation.
func (NopMetrics) ObserveClaimAttempt(string, bool) {}

// ObserveClaim discards the observation.
func (NopMetrics) ObserveClaim(string, time.Duration) {}

// ObserveOnline discards the observation.
func (NopMetrics) ObserveOnline(string, time.Duration) {}

// ObserveFinished discards the observation.
func (NopMetrics) ObserveFinished(string, State, Reason, time.Duration, bool) {}

// ObserveRelease discards the observation.
func (NopMetrics) ObserveRelease(string, State, Reason, bool) {}

type finishKey struct {
	pool    string
	outcome State
	reason  Reason
}

type durationKey struct {
	pool    string
	outcome State
}

type releaseKey struct {
	pool   string
	reason Reason
	ok     bool
}

// Counters is an in-memory Metrics, safe for concurrent use. The zero value
// is ready to use.
type Counters struct {
	mu        sync.Mutex
	attempts  map[string]int
	exhausted map[string]int
	claims    map[string][]time.Duration
	onlines   map[string][]time.Duration
	finished  map[finishKey]int
	durations map[durationKey][]time.Duration
	adopted   map[string]int
	releases  map[releaseKey]int
}

func (c *Counters) init() {
	if c.attempts == nil {
		c.attempts = map[string]int{}
		c.exhausted = map[string]int{}
		c.claims = map[string][]time.Duration{}
		c.onlines = map[string][]time.Duration{}
		c.finished = map[finishKey]int{}
		c.durations = map[durationKey][]time.Duration{}
		c.adopted = map[string]int{}
		c.releases = map[releaseKey]int{}
	}
}

// ObserveClaimAttempt counts one attempt.
func (c *Counters) ObserveClaimAttempt(pool string, exhausted bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.init()
	c.attempts[pool]++
	if exhausted {
		c.exhausted[pool]++
	}
}

// ObserveClaim keeps d.
func (c *Counters) ObserveClaim(pool string, d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.init()
	c.claims[pool] = append(c.claims[pool], d)
}

// ObserveOnline keeps d.
func (c *Counters) ObserveOnline(pool string, d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.init()
	c.onlines[pool] = append(c.onlines[pool], d)
}

// ObserveFinished counts the session and keeps d unless it was adopted.
func (c *Counters) ObserveFinished(pool string, outcome State, reason Reason, d time.Duration, adopted bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.init()
	c.finished[finishKey{pool, outcome, reason}]++
	if adopted {
		c.adopted[pool]++
		return
	}
	k := durationKey{pool, outcome}
	c.durations[k] = append(c.durations[k], d)
}

// ObserveRelease counts one release attempt.
func (c *Counters) ObserveRelease(pool string, _ State, reason Reason, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.init()
	c.releases[releaseKey{pool, reason, ok}]++
}

// Attempts returns the claim attempts of pool, and how many were exhausted.
func (c *Counters) Attempts(pool string) (total, exhausted int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.attempts[pool], c.exhausted[pool]
}

// Claims returns the observed claim times of pool.
func (c *Counters) Claims(pool string) []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.claims[pool])
}

// Onlines returns the observed times to online of pool.
func (c *Counters) Onlines(pool string) []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.onlines[pool])
}

// Finished returns the number of sessions of pool that finished with
// outcome and reason.
func (c *Counters) Finished(pool string, outcome State, reason Reason) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.finished[finishKey{pool, outcome, reason}]
}

// Durations returns the durations of the sessions of pool that finished
// with outcome, adopted sessions excluded.
func (c *Counters) Durations(pool string, outcome State) []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.durations[durationKey{pool, outcome}])
}

// Adopted returns the number of finished adopted sessions of pool.
func (c *Counters) Adopted(pool string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.adopted[pool]
}

// Releases returns the release attempts of pool for sessions that
// finished with reason.
func (c *Counters) Releases(pool string, reason Reason) (ok, failed int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.releases[releaseKey{pool, reason, true}], c.releases[releaseKey{pool, reason, false}]
}
