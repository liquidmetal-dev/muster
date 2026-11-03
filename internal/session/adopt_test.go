package session_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/liquidmetal-dev/muster/internal/github"
	"github.com/liquidmetal-dev/muster/internal/session"
	"github.com/liquidmetal-dev/muster/internal/vmsource"
)

const adoptID = "abcdefghijkl"

// inFlight claims a VM and registers a runner for adoptID outside the
// registry, as a previous musterd process would have, and returns what
// startup reconciliation would pass to Adopt.
func (e *env) inFlight(runnerName string) session.Adoption {
	e.t.Helper()
	ctx := context.Background()
	if _, err := e.Source.Acquire(ctx, testPool, adoptID); err != nil {
		e.t.Fatalf("Acquire: %v", err)
	}
	scope, err := github.RepoScope(testRepo)
	if err != nil {
		e.t.Fatalf("RepoScope: %v", err)
	}
	if _, err := e.Client.GenerateJITConfig(ctx, scope, github.JITRequest{Name: runnerName, Labels: []string{"self-hosted", "linux"}, RunnerGroupID: 1}); err != nil {
		e.t.Fatalf("GenerateJITConfig: %v", err)
	}
	leases, err := e.Source.Leases(ctx)
	if err != nil || len(leases) != 1 {
		e.t.Fatalf("Leases = %v, %v; want one", leases, err)
	}
	e.Clock.Advance(2 * time.Second)
	var runner github.Runner
	waitFor(e.t, func() bool {
		rs, err := e.Client.ListRunners(ctx, scope, "muster-")
		if err != nil || len(rs) != 1 || rs[0].Status != "online" {
			return false
		}
		runner = rs[0]
		return true
	})
	return session.Adoption{Lease: leases[0], Runner: runner}
}

func TestAdoptRunsToEnd(t *testing.T) {
	e := newEnv(t)
	e.start()
	a := e.inFlight("muster-muster-linux-" + adoptID)
	if err := e.Registry.Adopt(context.Background(), a); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if st := e.state(adoptID); st != session.Running {
		t.Fatalf("adopted state = %v, want RUNNING", st)
	}
	e.Rec.hide(a.Runner.Name, true)
	e.pollOnce()
	wantEnd(t, e.endedInfo(), session.Ended, session.ReasonRunnerAbsent)
	if n := e.Pool.CallCount("ReleaseVM"); n != 1 {
		t.Fatalf("ReleaseVM calls = %d, want 1", n)
	}
	if n := len(e.GitHub.Runners()); n != 0 {
		t.Fatalf("runners after cleanup = %d, want 0", n)
	}
}

func TestAdoptMaxDurationFromClaim(t *testing.T) {
	e := newEnv(t)
	e.start()
	a := e.inFlight("muster-muster-linux-" + adoptID)
	a.Lease.ClaimedAt = e.Clock.Now().Add(-(6*time.Hour - time.Minute))
	at := e.elapsed()
	if err := e.Registry.Adopt(context.Background(), a); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	got := e.driveEnd(10 * time.Second)
	wantEnd(t, got, session.Ended, session.ReasonMaxDuration)
	if d := e.elapsed() - at; d > time.Minute+10*time.Second {
		t.Fatalf("max duration after %v of adoption, want about 1m", d)
	}
}

func TestAdoptIdleFromAdoption(t *testing.T) {
	e := newEnv(t)
	e.start()
	a := e.inFlight("muster-muster-linux-" + adoptID)
	at := e.elapsed()
	if err := e.Registry.Adopt(context.Background(), a); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	got := e.driveEnd(10 * time.Second)
	wantEnd(t, got, session.Ended, session.ReasonIdleTimeout)
	if d := e.elapsed() - at; d < 5*time.Minute {
		t.Fatalf("idle timeout after %v, want at least 5m from adoption", d)
	}
}

func TestAdoptMismatch(t *testing.T) {
	e := newEnv(t)
	e.start()
	a := e.inFlight("muster-muster-linux-zzzzzzzzzzzz")
	if err := e.Registry.Adopt(context.Background(), a); !errors.Is(err, session.ErrAdoptMismatch) {
		t.Fatalf("Adopt = %v, want ErrAdoptMismatch", err)
	}
	if n := len(e.Registry.Sessions()); n != 0 {
		t.Fatalf("sessions = %d, want 0", n)
	}
}

func TestAdoptCountsActive(t *testing.T) {
	for _, busy := range []bool{false, true} {
		e := newEnv(t)
		e.start()
		a := e.inFlight("muster-muster-linux-" + adoptID)
		a.Runner.Busy = busy
		if err := e.Registry.Adopt(context.Background(), a); err != nil {
			t.Fatalf("Adopt: %v", err)
		}
		wantNotBusy := 1
		if busy {
			wantNotBusy = 0
		}
		if act, nb := e.Registry.Counts(testPool); act != 1 || nb != wantNotBusy {
			t.Fatalf("busy=%v: Counts = (%d, %d), want (1, %d)", busy, act, nb, wantNotBusy)
		}
	}
}

func TestAdoptBeforeRunFails(t *testing.T) {
	e := newEnv(t)
	err := e.Registry.Adopt(context.Background(), session.Adoption{Lease: vmsource.LeaseInfo{Pool: testPool, RequestID: adoptID}})
	if !errors.Is(err, session.ErrNotRunning) {
		t.Fatalf("Adopt before Run = %v, want ErrNotRunning", err)
	}
}

func TestAdoptLeaseGoneBeforeAcquire(t *testing.T) {
	e := newEnv(t)
	e.start()
	a := e.inFlight("muster-muster-linux-" + adoptID)
	vm, err := e.Source.Acquire(context.Background(), testPool, adoptID)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := e.Source.Release(context.Background(), vm); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := e.Registry.Adopt(context.Background(), a); !errors.Is(err, session.ErrAdoptMismatch) {
		t.Fatalf("Adopt = %v, want ErrAdoptMismatch", err)
	}
	if n := len(e.Registry.Sessions()); n != 0 {
		t.Fatalf("sessions = %d, want 0", n)
	}
	if n := len(e.Pool.Leases()); n != 0 {
		t.Fatalf("leases = %d, want 0: the replacement VM must be released", n)
	}
}
