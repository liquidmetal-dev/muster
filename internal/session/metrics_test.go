package session_test

import (
	"slices"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	"github.com/liquidmetal-dev/muster/internal/session"
)

func withCounters(m *session.Counters) envOption {
	return withConfig(func(c *session.Config) { c.Metrics = m })
}

func TestMetricsClaimAndOnline(t *testing.T) {
	m := &session.Counters{}
	e := newEnv(t, withCounters(m))
	e.Pool.FailNext("ClaimVM", codes.ResourceExhausted)
	c := e.capture()
	e.start()
	id := e.create(testPool)
	e.driveState(id, time.Second, session.Starting)
	claimedBy := e.elapsed()
	c.waitStdin(t)
	e.pollOnline(id)
	e.waitState(id, session.Running)
	onlineBy := e.elapsed()

	if total, exhausted := m.Attempts(testPool); total != 2 || exhausted != 1 {
		t.Fatalf("Attempts = (%d, %d), want (2, 1)", total, exhausted)
	}
	claims := m.Claims(testPool)
	if len(claims) != 1 || claims[0] < time.Second || claims[0] > claimedBy {
		t.Fatalf("Claims = %v, want one in [1s, %v]", claims, claimedBy)
	}
	onlines := m.Onlines(testPool)
	if len(onlines) != 1 || onlines[0] < claims[0] || onlines[0] > onlineBy {
		t.Fatalf("Onlines = %v, want one in [%v, %v]", onlines, claims[0], onlineBy)
	}
}

func TestMetricsFinishedEnded(t *testing.T) {
	m := &session.Counters{}
	e := newEnv(t, withCounters(m))
	c := e.capture()
	e.start()
	e.running(c)
	c.exit(0)
	wantEnd(t, e.endedInfo(), session.Ended, session.ReasonExecExit)
	if n := m.Finished(testPool, session.Ended, session.ReasonExecExit); n != 1 {
		t.Fatalf("Finished = %d, want 1", n)
	}
	if got := m.Durations(testPool, session.Ended); !slices.Equal(got, []time.Duration{e.elapsed()}) {
		t.Fatalf("Durations = %v, want [%v]", got, e.elapsed())
	}
	if ok, failed := m.Releases(testPool, session.ReasonExecExit); ok != 1 || failed != 0 {
		t.Fatalf("Releases = (%d, %d), want (1, 0)", ok, failed)
	}
}

func TestMetricsFinishedFailedNoDemand(t *testing.T) {
	m := &session.Counters{}
	e := newEnv(t, withCapacity(0), withCounters(m))
	e.start()
	e.create(testPool)
	waitFor(t, func() bool { return e.Pool.CallCount("ClaimVM") >= 1 })
	e.Demand.set(testPool, 0)
	wantEnd(t, e.driveEnd(time.Second), session.Failed, session.ReasonNoDemand)
	if n := m.Finished(testPool, session.Failed, session.ReasonNoDemand); n != 1 {
		t.Fatalf("Finished = %d, want 1", n)
	}
	if got := m.Onlines(testPool); len(got) != 0 {
		t.Fatalf("Onlines = %v, want none", got)
	}
	if ok, failed := m.Releases(testPool, session.ReasonNoDemand); ok+failed != 0 {
		t.Fatalf("Releases = (%d, %d), want none without a VM", ok, failed)
	}
}

func TestMetricsReleaseRetry(t *testing.T) {
	m := &session.Counters{}
	e := newEnv(t, withCounters(m))
	c := e.capture()
	e.start()
	e.running(c)
	for range 3 {
		e.Pool.FailNext("ReleaseVM", codes.Unavailable)
	}
	c.exit(0)
	wantEnd(t, e.driveEnd(time.Second), session.Ended, session.ReasonExecExit)
	if ok, failed := m.Releases(testPool, session.ReasonExecExit); ok != 1 || failed != 1 {
		t.Fatalf("Releases = (%d, %d), want (1, 1)", ok, failed)
	}
}

func TestMetricsAdoptedNotObservedInHistograms(t *testing.T) {
	m := &session.Counters{}
	e := newEnv(t, withCounters(m))
	e.start()
	runner := e.adoptBusy()
	e.Registry.JobCompleted(runner)
	wantEnd(t, e.driveEnd(5*time.Second), session.Ended, session.ReasonJobCompleted)
	if n := m.Finished(testPool, session.Ended, session.ReasonJobCompleted); n != 1 {
		t.Fatalf("Finished = %d, want 1", n)
	}
	if n := m.Adopted(testPool); n != 1 {
		t.Fatalf("Adopted = %d, want 1", n)
	}
	if got := m.Durations(testPool, session.Ended); len(got) != 0 {
		t.Fatalf("Durations = %v, want none for an adopted session", got)
	}
	if total, _ := m.Attempts(testPool); total != 0 {
		t.Fatalf("Attempts = %d, want 0", total)
	}
	if got := m.Claims(testPool); len(got) != 0 {
		t.Fatalf("Claims = %v, want none", got)
	}
}
