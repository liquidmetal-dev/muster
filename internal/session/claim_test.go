package session_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	"github.com/liquidmetal-dev/muster/internal/session"
)

func TestCreateBeforeRunFails(t *testing.T) {
	e := newEnv(t)
	if err := e.Registry.Create(context.Background(), testPool); !errors.Is(err, session.ErrNotRunning) {
		t.Fatalf("Create before Run = %v, want ErrNotRunning", err)
	}
}

func TestCreateUnknownPool(t *testing.T) {
	e := newEnv(t)
	e.start()
	if err := e.Registry.Create(context.Background(), "nope"); !errors.Is(err, session.ErrUnknownPool) {
		t.Fatalf("Create(nope) = %v, want ErrUnknownPool", err)
	}
}

func TestCreateCountsAtOnce(t *testing.T) {
	e := newEnv(t, withCapacity(0))
	e.start()
	if err := e.Registry.Create(context.Background(), testPool); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if a, nb := e.Registry.Counts(testPool); a != 1 || nb != 1 {
		t.Fatalf("Counts = (%d, %d), want (1, 1)", a, nb)
	}
}

func TestClaimUsesSessionID(t *testing.T) {
	e := newEnv(t)
	e.start()
	id := e.create(testPool)
	waitFor(t, func() bool { return len(e.Pool.Leases()) == 1 })
	if got := e.Pool.Leases()[0].GetRequestId(); got != id {
		t.Fatalf("lease request id = %q, want session id %q", got, id)
	}
	e.waitState(id, session.Starting)
	if i, _ := e.info(id); i.VMID == "" || !i.ClaimedAt.Equal(startTime) {
		t.Fatalf("info after claim = %+v, want VMID and ClaimedAt %v", i, startTime)
	}
}

func TestExhaustedBacksOffWhileDemand(t *testing.T) {
	e := newEnv(t, withCapacity(1))
	for range 3 {
		e.Pool.FailNext("ClaimVM", codes.ResourceExhausted)
	}
	e.start()
	id := e.create(testPool)
	e.driveState(id, time.Second, session.Starting)
	if n := e.Pool.CallCount("ClaimVM"); n != 4 {
		t.Fatalf("ClaimVM calls = %d, want 4", n)
	}
}

func TestExhaustedGivesUpWithoutDemand(t *testing.T) {
	e := newEnv(t, withCapacity(0))
	e.start()
	e.create(testPool)
	waitFor(t, func() bool { return e.Pool.CallCount("ClaimVM") >= 1 })
	e.Demand.set(testPool, 0)
	got := e.driveEnd(time.Second)
	if got.State != session.Failed || got.Reason != session.ReasonNoDemand {
		t.Fatalf("ended = %v %v, want FAILED no_demand", got.State, got.Reason)
	}
	if n := len(e.Pool.Leases()); n != 0 {
		t.Fatalf("leases = %d, want 0", n)
	}
	if n := len(e.Registry.Sessions()); n != 0 {
		t.Fatalf("sessions = %d, want 0 after release", n)
	}
}

func TestClaimDeadline(t *testing.T) {
	e := newEnv(t, withCapacity(0))
	e.start()
	e.create(testPool)
	got := e.driveEnd(30 * time.Second)
	if got.State != session.Failed || got.Reason != session.ReasonClaimDeadline {
		t.Fatalf("ended = %v %v, want FAILED claim_deadline", got.State, got.Reason)
	}
	if el := e.elapsed(); el < 10*time.Minute {
		t.Fatalf("failed after %v, want at least 10m", el)
	}
}

func TestClaimPoolNotFound(t *testing.T) {
	missing := testPoolConfig("missing")
	e := newEnv(t, withPools(testPoolConfig(testPool)), withConfig(func(c *session.Config) {
		c.Pools = append(c.Pools, missing)
	}))
	e.start()
	e.create("missing")
	got := e.endedInfo()
	if got.State != session.Failed || got.Reason != session.ReasonClaimError {
		t.Fatalf("ended = %v %v, want FAILED claim_error", got.State, got.Reason)
	}
}

func TestClaimFatalErrorFails(t *testing.T) {
	e := newEnv(t)
	e.Pool.FailNext("ClaimVM", codes.PermissionDenied)
	e.start()
	e.create(testPool)
	got := e.endedInfo()
	if got.State != session.Failed || got.Reason != session.ReasonClaimError {
		t.Fatalf("ended = %v %v, want FAILED claim_error", got.State, got.Reason)
	}
}

func TestClaimSucceedsAfterDeadline(t *testing.T) {
	e := newEnv(t)
	e.Pool.Stall("ClaimVM", 500*time.Millisecond)
	e.start()
	id := e.create(testPool)
	e.waitState(id, session.Claiming)
	e.Clock.Advance(11 * time.Minute)
	got := e.endedInfo()
	if got.State != session.Failed || got.Reason != session.ReasonClaimDeadline {
		t.Fatalf("ended = %v %v, want FAILED claim_deadline", got.State, got.Reason)
	}
	if n := e.Pool.CallCount("ReleaseVM"); n != 1 {
		t.Fatalf("ReleaseVM calls = %d, want 1 for the late VM", n)
	}
}
