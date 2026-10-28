package clock

import (
	"sort"
	"sync"
	"time"
)

// Manual is a Clock that moves only when Advance or Set is called. Timers
// fire, in deadline order, when the clock reaches their deadline. A timer
// whose deadline is at or before Now fires at once. Manual is safe for
// concurrent use.
type Manual struct {
	mu      sync.Mutex
	now     time.Time
	waiters []*manualTimer
}

// NewManual returns a Manual clock whose time is start.
func NewManual(start time.Time) *Manual {
	return &Manual{now: start}
}

// Now returns the current manual time.
func (m *Manual) Now() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.now
}

// NewTimer returns a timer that fires when the manual time reaches Now+d.
func (m *Manual) NewTimer(d time.Duration) Timer {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := &manualTimer{m: m, c: make(chan time.Time, 1)}
	m.armLocked(t, m.now.Add(d))
	return t
}

// After returns a channel that receives the manual time once it reaches Now+d.
func (m *Manual) After(d time.Duration) <-chan time.Time {
	return m.NewTimer(d).C()
}

// Advance moves the clock forward by d and fires every timer that is due.
func (m *Manual) Advance(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.setLocked(m.now.Add(d))
}

// Set moves the clock to t and fires every timer that is due.
func (m *Manual) Set(t time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.setLocked(t)
}

func (m *Manual) setLocked(t time.Time) {
	m.now = t
	m.fireDueLocked()
}

// armLocked registers t with the given deadline and fires it at once when
// the deadline is not in the future.
func (m *Manual) armLocked(t *manualTimer, deadline time.Time) {
	t.deadline = deadline
	t.active = true
	m.waiters = append(m.waiters, t)
	m.fireDueLocked()
}

func (m *Manual) fireDueLocked() {
	sort.SliceStable(m.waiters, func(i, j int) bool {
		return m.waiters[i].deadline.Before(m.waiters[j].deadline)
	})
	kept := m.waiters[:0]
	for _, w := range m.waiters {
		if !w.deadline.After(m.now) {
			w.active = false
			select {
			case w.c <- m.now:
			default:
			}
			continue
		}
		kept = append(kept, w)
	}
	m.waiters = kept
}

func (m *Manual) removeLocked(t *manualTimer) bool {
	for i, w := range m.waiters {
		if w == t {
			m.waiters = append(m.waiters[:i], m.waiters[i+1:]...)
			return true
		}
	}
	return false
}

type manualTimer struct {
	m        *Manual
	c        chan time.Time
	deadline time.Time
	active   bool
}

func (t *manualTimer) C() <-chan time.Time { return t.c }

func (t *manualTimer) Stop() bool {
	t.m.mu.Lock()
	defer t.m.mu.Unlock()
	if !t.active {
		return false
	}
	t.active = false
	return t.m.removeLocked(t)
}

func (t *manualTimer) Reset(d time.Duration) bool {
	t.m.mu.Lock()
	defer t.m.mu.Unlock()
	wasActive := t.active
	if wasActive {
		t.m.removeLocked(t)
	}
	// Drain a stale value so the next fire is observable.
	select {
	case <-t.c:
	default:
	}
	t.m.armLocked(t, t.m.now.Add(d))
	return wasActive
}
