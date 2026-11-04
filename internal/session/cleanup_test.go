package session_test

import (
	"net/http"
	"slices"
	"testing"
	"time"

	poolmgrv1alpha1 "github.com/liquidmetal-dev/battery/api/proto/poolmgr/v1alpha1"
	"google.golang.org/grpc/codes"

	"github.com/liquidmetal-dev/muster/internal/fakegithub"
	"github.com/liquidmetal-dev/muster/internal/session"
)

func TestCleanupReleasesThenDeletes(t *testing.T) {
	e := newEnv(t)
	c := e.capture()
	e.start()
	e.running(c)
	c.exit(0)
	wantEnd(t, e.endedInfo(), session.Ended, session.ReasonExecExit)
	if d := e.Rec.deletes(); !slices.Equal(d, []int{1}) {
		t.Fatalf("ReleaseVM count at each runner delete = %v, want [1]", d)
	}
	if n := len(e.GitHub.Runners()); n != 0 {
		t.Fatalf("runners after cleanup = %d, want 0", n)
	}
	if n := len(e.Registry.Sessions()); n != 0 {
		t.Fatalf("sessions after cleanup = %d, want 0", n)
	}
}

func TestCleanupRetriesRelease(t *testing.T) {
	e := newEnv(t)
	c := e.capture()
	e.start()
	e.running(c)
	e.Pool.FailNext("ReleaseVM", codes.Internal)
	e.Pool.FailNext("ReleaseVM", codes.Internal)
	c.exit(0)
	e.driveEnd(time.Second)
	if n := e.Pool.CallCount("ReleaseVM"); n != 3 {
		t.Fatalf("ReleaseVM calls = %d, want 3", n)
	}
	if n := len(e.Pool.Leases()); n != 0 {
		t.Fatalf("leases after cleanup = %d, want 0", n)
	}
}

func TestCleanupRetriesBusyRunner(t *testing.T) {
	e := newEnv(t)
	c := e.capture()
	e.start()
	e.running(c)
	e.GitHub.FailNext(fakegithub.PatternRepoRunnerDel, fakegithub.Fault{Status: http.StatusUnprocessableEntity, Message: "runner is busy"})
	c.exit(0)
	e.driveEnd(time.Second)
	if n := e.GitHub.RequestCount(fakegithub.PatternRepoRunnerDel); n != 2 {
		t.Fatalf("runner delete requests = %d, want 2", n)
	}
	if n := len(e.GitHub.Runners()); n != 0 {
		t.Fatalf("runners after cleanup = %d, want 0", n)
	}
}

func TestReleaseLeaseNotFoundIsSuccess(t *testing.T) {
	e := newEnv(t)
	c := e.capture()
	e.start()
	e.running(c)
	e.Pool.FailNext("ReleaseVM", codes.NotFound)
	c.exit(0)
	e.endedInfo()
	if n := e.Pool.CallCount("ReleaseVM"); n != 1 {
		t.Fatalf("ReleaseVM calls = %d, want 1", n)
	}
	if n := len(e.GitHub.Runners()); n != 0 {
		t.Fatalf("runners after cleanup = %d, want 0", n)
	}
}

func TestVMLostDeletesRunner(t *testing.T) {
	e := newEnv(t)
	c := e.capture()
	e.start()
	e.waitSubscribed()
	id, _, _ := e.running(c)
	i, _ := e.info(id)
	e.Pool.DeleteVM(testNS, testPool, i.VMID, poolmgrv1alpha1.EventType_VM_DELETED_DUE_TO_EXPIRY)
	wantEnd(t, e.endedInfo(), session.Failed, session.ReasonVMLost)
	if n := len(e.GitHub.Runners()); n != 0 {
		t.Fatalf("runners after a lost VM = %d, want 0", n)
	}
}

func TestShutdownLeavesSessions(t *testing.T) {
	e := newEnv(t, withPools(testPoolConfig("linux"), testPoolConfig("linux2")))
	c := e.capture()
	stop := e.startCancelable()
	e.running(c)
	for range 50 {
		e.Pool.FailNext("ClaimVM", codes.ResourceExhausted)
	}
	waiting := e.create("linux2")
	e.waitState(waiting, session.Claiming)
	waitFor(t, func() bool { return e.Pool.CallCount("ClaimVM") >= 2 })
	if !stop(time.Second) {
		t.Fatal("Run did not return within 1s of cancel")
	}
	if n := e.Pool.CallCount("ReleaseVM"); n != 0 {
		t.Fatalf("ReleaseVM calls after shutdown = %d, want 0", n)
	}
	if n := len(e.GitHub.Runners()); n != 1 {
		t.Fatalf("runners after shutdown = %d, want 1", n)
	}
	if n := e.GitHub.RequestCount(fakegithub.PatternRepoRunnerDel); n != 0 {
		t.Fatalf("runner deletes after shutdown = %d, want 0", n)
	}
}

func TestShutdownDuringCleanup(t *testing.T) {
	e := newEnv(t)
	c := e.capture()
	stop := e.startCancelable()
	e.running(c)
	for range 100 {
		e.Pool.FailNext("ReleaseVM", codes.Internal)
	}
	c.exit(0)
	waitFor(t, func() bool { return e.Pool.CallCount("ReleaseVM") >= 1 })
	if !stop(time.Second) {
		t.Fatal("Run did not return within 1s of cancel")
	}
	if n := e.GitHub.RequestCount(fakegithub.PatternRepoRunnerDel); n != 0 {
		t.Fatalf("runner deletes after shutdown = %d, want 0", n)
	}
	select {
	case got := <-e.ended:
		t.Fatalf("OnEnd after shutdown: %+v", got)
	default:
	}
}
