package clock_test

import (
	"testing"
	"time"

	"github.com/liquidmetal-dev/muster/internal/clock"
)

var start = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

func mustBeEmpty(t *testing.T, c <-chan time.Time, what string) {
	t.Helper()
	select {
	case <-c:
		t.Fatalf("%s fired but must not", what)
	default:
	}
}

func mustFire(t *testing.T, c <-chan time.Time, what string) time.Time {
	t.Helper()
	select {
	case v := <-c:
		return v
	default:
		t.Fatalf("%s did not fire", what)
		return time.Time{}
	}
}

func TestRealNowAdvances(t *testing.T) {
	c := clock.Real()
	a := c.Now()
	b := c.Now()
	if b.Before(a) {
		t.Fatalf("second Now %v is before first %v", b, a)
	}
}

func TestManualNowIsFixed(t *testing.T) {
	m := clock.NewManual(start)
	if got := m.Now(); !got.Equal(start) {
		t.Fatalf("Now = %v, want %v", got, start)
	}
	if got := m.Now(); !got.Equal(start) {
		t.Fatalf("second Now = %v, want %v", got, start)
	}
}

func TestManualAdvanceFiresDueTimers(t *testing.T) {
	m := clock.NewManual(start)
	t1 := m.NewTimer(time.Second)
	t2 := m.NewTimer(2 * time.Second)
	m.Advance(time.Second)
	if got := mustFire(t, t1.C(), "t1"); !got.Equal(start.Add(time.Second)) {
		t.Fatalf("t1 fired with %v", got)
	}
	mustBeEmpty(t, t2.C(), "t2")
	m.Advance(time.Second)
	mustFire(t, t2.C(), "t2")
}

func TestManualTimerPastDeadlineFiresAtOnce(t *testing.T) {
	m := clock.NewManual(start)
	mustFire(t, m.NewTimer(0).C(), "zero timer")
	mustFire(t, m.NewTimer(-time.Second).C(), "negative timer")
}

func TestManualStopPreventsFire(t *testing.T) {
	m := clock.NewManual(start)
	tm := m.NewTimer(time.Second)
	if !tm.Stop() {
		t.Fatal("first Stop returned false")
	}
	m.Advance(time.Second)
	mustBeEmpty(t, tm.C(), "stopped timer")
	if tm.Stop() {
		t.Fatal("second Stop returned true")
	}
}

func TestManualResetRearms(t *testing.T) {
	m := clock.NewManual(start)
	tm := m.NewTimer(time.Second)
	m.Advance(time.Second)
	mustFire(t, tm.C(), "first fire")
	tm.Reset(3 * time.Second)
	m.Advance(2 * time.Second)
	mustBeEmpty(t, tm.C(), "reset timer at +3s")
	m.Advance(time.Second)
	if got := mustFire(t, tm.C(), "reset timer"); !got.Equal(start.Add(4 * time.Second)) {
		t.Fatalf("fired with %v, want %v", got, start.Add(4*time.Second))
	}
}

func TestManualAfter(t *testing.T) {
	m := clock.NewManual(start)
	c := m.After(time.Second)
	mustBeEmpty(t, c, "After before Advance")
	m.Advance(time.Second)
	mustFire(t, c, "After")
}

func TestManualSet(t *testing.T) {
	m := clock.NewManual(start)
	tm := m.NewTimer(2 * time.Second)
	m.Set(start.Add(5 * time.Second))
	mustFire(t, tm.C(), "timer after Set")
	if got := m.Now(); !got.Equal(start.Add(5 * time.Second)) {
		t.Fatalf("Now = %v", got)
	}
}
