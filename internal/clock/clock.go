// Package clock abstracts time so that code with timers can run on the wall
// clock in production and on a manual clock in tests. Real returns the wall
// clock. NewManual returns a clock that only moves when a test calls Advance
// or Set.
package clock

import "time"

// Clock is the subset of package time that timer-driven code needs.
type Clock interface {
	// Now returns the current time of this clock.
	Now() time.Time
	// NewTimer returns a timer that fires once, d after Now.
	NewTimer(d time.Duration) Timer
	// After returns a channel that receives the time once, d after Now.
	After(d time.Duration) <-chan time.Time
}

// Timer is the subset of time.Timer that timer-driven code needs.
type Timer interface {
	// C is the channel the timer fires on.
	C() <-chan time.Time
	// Stop prevents the timer from firing. It returns true when the call
	// stopped the timer and false when the timer already fired or stopped.
	Stop() bool
	// Reset re-arms the timer to fire d after Now. It returns true when the
	// timer was active.
	Reset(d time.Duration) bool
}

type realClock struct{}

// Real returns the wall clock.
func Real() Clock { return realClock{} }

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) NewTimer(d time.Duration) Timer         { return realTimer{time.NewTimer(d)} }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

type realTimer struct{ t *time.Timer }

func (r realTimer) C() <-chan time.Time        { return r.t.C }
func (r realTimer) Stop() bool                 { return r.t.Stop() }
func (r realTimer) Reset(d time.Duration) bool { return r.t.Reset(d) }
