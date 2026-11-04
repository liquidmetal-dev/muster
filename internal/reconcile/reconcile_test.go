package reconcile_test

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/liquidmetal-dev/muster/internal/fakegithub"
	"github.com/liquidmetal-dev/muster/internal/reconcile"
	"github.com/liquidmetal-dev/muster/internal/session"
	"github.com/liquidmetal-dev/muster/internal/vmsource"
)

var holdIdle = fakegithub.Behavior{HoldIdle: true}

func wantResult(t *testing.T, got, want reconcile.Result, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != want {
		t.Fatalf("Result = %+v, want %+v", got, want)
	}
}

func (e *env) wantDecisions(action, outcome string, n int) {
	e.t.Helper()
	if got := e.Counters.Decisions(action, outcome); got != n {
		e.t.Fatalf("Decisions(%s, %s) = %d, want %d", action, outcome, got, n)
	}
}

func (e *env) wantLeases(want ...string) {
	e.t.Helper()
	got := e.leaseIDs()
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		e.t.Fatalf("leases = %v, want %v", got, want)
	}
}

func (e *env) wantRunner(name string, present bool) {
	e.t.Helper()
	if _, ok := e.runner(name); ok != present {
		e.t.Fatalf("runner %s present = %v, want %v", name, ok, present)
	}
}

func TestRunAdopts(t *testing.T) {
	e := newEnv(t)
	name := e.prior(poolLinux, idA, holdIdle)
	res, err := e.reconcile()
	wantResult(t, res, reconcile.Result{Adopted: 1}, err)
	if st, ok := e.sessionState(idA); !ok || st != session.Running {
		t.Fatalf("session %s state = %v (live %v), want RUNNING", idA, st, ok)
	}
	e.wantDecisions(reconcile.ActionAdopt, reconcile.OutcomeOK, 1)
	e.wantLeases(poolLinux + "/" + idA)
	e.wantRunner(name, true)
}

func TestRunAdoptsOfflineRunner(t *testing.T) {
	e := newEnv(t)
	e.priorLease(poolLinux, idA)
	scopeRunner := runnerName(poolLinux, idA)
	e.GitHub.NextRunner(fakegithub.Behavior{NeverRegister: true})
	e.jit(repoA, scopeRunner)
	res, err := e.reconcile()
	wantResult(t, res, reconcile.Result{Adopted: 1}, err)
}

func TestRunAdoptedEndsOnCompleted(t *testing.T) {
	e := newEnv(t)
	name := e.prior(poolLinux, idA, holdIdle)
	if _, err := e.reconcile(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	e.Registry.JobCompleted(name)
	var got session.Info
	e.drive(5*time.Second, func() bool {
		select {
		case got = <-e.ended:
			return true
		default:
			return false
		}
	})
	if got.ID != idA || got.Reason != session.ReasonJobCompleted {
		t.Fatalf("ended %s with %v, want %s with job_completed", got.ID, got.Reason, idA)
	}
	e.wantLeases()
	e.wantRunner(name, false)
}

func TestRunAdoptedEndsWhenRunnerDisappears(t *testing.T) {
	e := newEnv(t)
	name := e.prior(poolLinux, idA, fakegithub.Behavior{})
	if _, err := e.reconcile(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := e.GitHub.QueueJob(repoA, labels); err != nil {
		t.Fatalf("QueueJob: %v", err)
	}
	var got session.Info
	e.drive(time.Second, func() bool {
		select {
		case got = <-e.ended:
			return true
		default:
			return false
		}
	})
	if got.ID != idA || got.Reason != session.ReasonRunnerAbsent {
		t.Fatalf("ended %s with %v, want %s with runner_absent", got.ID, got.Reason, idA)
	}
	e.wantLeases()
	e.wantRunner(name, false)
}

func TestRunReleasesOrphanLease(t *testing.T) {
	e := newEnv(t)
	e.priorLease(poolLinux, idA)
	res, err := e.reconcile()
	wantResult(t, res, reconcile.Result{LeasesReleased: 1}, err)
	e.wantLeases()
	if n := len(e.Registry.Sessions()); n != 0 {
		t.Fatalf("sessions = %d, want 0", n)
	}
	e.wantDecisions(reconcile.ActionReleaseLease, reconcile.OutcomeOK, 1)
}

func TestRunReleasesLeaseOfUnknownPool(t *testing.T) {
	e := newEnv(t)
	e.priorLease(poolOld, idA)
	name := e.priorRunner(repoA, poolOld, idA, holdIdle)
	res, err := e.reconcile()
	wantResult(t, res, reconcile.Result{LeasesReleased: 1, RunnersDeleted: 1}, err)
	e.wantLeases()
	e.wantRunner(name, false)
}

func TestRunReleasesLeaseWithBadID(t *testing.T) {
	e := newEnv(t)
	e.priorLease(poolLinux, "short")
	res, err := e.reconcile()
	wantResult(t, res, reconcile.Result{LeasesReleased: 1}, err)
	e.wantLeases()
}

func TestRunDeletesOrphanRunner(t *testing.T) {
	e := newEnv(t)
	name := e.priorRunner(repoA, poolLinux, idA, holdIdle)
	res, err := e.reconcile()
	wantResult(t, res, reconcile.Result{RunnersDeleted: 1}, err)
	e.wantRunner(name, false)
	e.wantDecisions(reconcile.ActionDeleteRunner, reconcile.OutcomeOK, 1)
}

func TestRunHyphenatedPool(t *testing.T) {
	e := newEnv(t)
	e.prior(poolArm, idA, holdIdle)
	res, err := e.reconcile()
	wantResult(t, res, reconcile.Result{Adopted: 1}, err)
	if st, ok := e.sessionState(idA); !ok || st != session.Running {
		t.Fatalf("session state = %v (live %v), want RUNNING", st, ok)
	}
}

func TestRunIDInOtherPool(t *testing.T) {
	e := newEnv(t)
	e.priorLease(poolLinux, idA)
	name := e.priorRunner(repoA, poolArm, idA, holdIdle)
	res, err := e.reconcile()
	wantResult(t, res, reconcile.Result{LeasesReleased: 1, RunnersDeleted: 1}, err)
	e.wantLeases()
	e.wantRunner(name, false)
}

func TestRunSharedScopeListedOnce(t *testing.T) {
	e := newEnv(t)
	before := e.GitHub.RequestCount(fakegithub.PatternRepoRunners)
	if _, err := e.reconcile(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := e.GitHub.RequestCount(fakegithub.PatternRepoRunners) - before; n != 1 {
		t.Fatalf("runner list requests = %d, want 1", n)
	}
}

func TestRunPoolScopeChanged(t *testing.T) {
	e := newEnv(t, withPools(poolConfig(poolLinux, repoB), poolConfig(poolArm, repoA)))
	e.priorLease(poolLinux, idA)
	name := e.priorRunner(repoA, poolLinux, idA, holdIdle)
	res, err := e.reconcile()
	wantResult(t, res, reconcile.Result{LeasesReleased: 1, RunnersDeleted: 1}, err)
	e.wantLeases()
	e.wantRunner(name, false)
	if n := len(e.Registry.Sessions()); n != 0 {
		t.Fatalf("sessions = %d, want 0", n)
	}
}

func TestRunBusyOrphanRunner(t *testing.T) {
	e := newEnv(t)
	name := e.priorRunner(repoA, poolLinux, idA, holdIdle)
	e.GitHub.FailNext(fakegithub.PatternRepoRunnerDel, fakegithub.Fault{Status: http.StatusUnprocessableEntity, Message: "Bad request - Runner is still running a job"})
	res, err := e.reconcile()
	wantResult(t, res, reconcile.Result{Failed: 1}, err)
	e.wantDecisions(reconcile.ActionDeleteRunner, reconcile.OutcomeFailed, 1)
	if n := e.GitHub.RequestCount(fakegithub.PatternRepoRunnerDel); n != 1 {
		t.Fatalf("delete requests = %d, want 1", n)
	}
	e.wantRunner(name, true)
}

func TestRunAdoptMismatchReleasesReplacement(t *testing.T) {
	var src *vanishingSource
	e := newEnv(t, withSource(func(s vmsource.Source) vmsource.Source {
		src = &vanishingSource{Source: s, requestID: idA}
		return src
	}))
	src.pool = e.Pool
	name := e.prior(poolLinux, idA, holdIdle)
	res, err := e.reconcileDriven(e.config())
	wantResult(t, res, reconcile.Result{LeasesReleased: 1, RunnersDeleted: 1, Failed: 1}, err)
	e.wantDecisions(reconcile.ActionAdopt, reconcile.OutcomeFailed, 1)
	e.wantLeases()
	e.wantRunner(name, false)
	if n := len(e.Registry.Sessions()); n != 0 {
		t.Fatalf("sessions = %d, want 0", n)
	}
}

func TestRunEmptyRequestIDLease(t *testing.T) {
	e := newEnv(t)
	e.claimRaw(poolLinux)
	claims := e.Pool.CallCount("ClaimVM")
	res, err := e.reconcile()
	wantResult(t, res, reconcile.Result{Failed: 1}, err)
	e.wantDecisions(reconcile.ActionReleaseLease, reconcile.OutcomeFailed, 1)
	if n := e.Pool.CallCount("ClaimVM"); n != claims {
		t.Fatalf("ClaimVM calls = %d, want %d", n, claims)
	}
}

func TestRunListRetriesThenSucceeds(t *testing.T) {
	e := newEnv(t)
	e.priorLease(poolLinux, idA)
	e.GitHub.FailNext(fakegithub.PatternRepoRunners, fakegithub.Fault{Status: http.StatusInternalServerError})
	res, err := e.reconcileDriven(e.config())
	wantResult(t, res, reconcile.Result{LeasesReleased: 1}, err)
}

func TestRunListGivesUp(t *testing.T) {
	e := newEnv(t)
	for range 50 {
		e.GitHub.FailNext(fakegithub.PatternRepoRunners, fakegithub.Fault{Status: http.StatusInternalServerError})
	}
	cfg := e.config()
	cfg.ListWindow = 5 * time.Second
	start := e.Clock.Now()
	_, err := e.reconcileDriven(cfg)
	if err == nil || !strings.Contains(err.Error(), "reconcile: list") {
		t.Fatalf("Run = %v, want a reconcile: list error", err)
	}
	if d := e.Clock.Now().Sub(start); d < 5*time.Second {
		t.Fatalf("gave up after %v, want at least 5s", d)
	}
}

func TestRunContextCancelled(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := reconcile.Run(ctx, e.config()); err == nil {
		t.Fatal("Run with a cancelled context succeeded")
	}
}
