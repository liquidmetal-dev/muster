package session_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	mvmexecv1 "github.com/liquidmetal-dev/flintlock/api/services/microvmexec/v1alpha1"
	"google.golang.org/grpc/codes"

	"github.com/liquidmetal-dev/muster/internal/config"
	"github.com/liquidmetal-dev/muster/internal/fakegithub"
	"github.com/liquidmetal-dev/muster/internal/session"
	"github.com/liquidmetal-dev/muster/internal/vmsource/flintlock/fakeexec"
)

func TestJITRequestShape(t *testing.T) {
	e := newEnv(t)
	c := e.capture()
	e.start()
	id := e.create(testPool)
	c.waitStdin(t)
	info, _ := e.info(id)
	runners := e.GitHub.Runners()
	if len(runners) != 1 {
		t.Fatalf("runners = %d, want 1", len(runners))
	}
	r := runners[0]
	if r.Name != info.RunnerName || r.Name != "muster-muster-linux-"+id {
		t.Errorf("runner name = %q, want muster-muster-linux-%s", r.Name, id)
	}
	if !slices.Equal(r.Labels, []string{"self-hosted", "linux"}) {
		t.Errorf("labels = %v", r.Labels)
	}
	if r.RunnerGroupID != 1 {
		t.Errorf("runner group = %d, want 1", r.RunnerGroupID)
	}
	_, stdin := c.got()
	dec, err := fakegithub.DecodeJITConfig(string(stdin))
	if err != nil {
		t.Fatalf("decode stdin: %v", err)
	}
	if dec.RunnerName != r.Name || dec.RunnerID != r.ID {
		t.Errorf("stdin config names %s/%d, want %s/%d", dec.RunnerName, dec.RunnerID, r.Name, r.ID)
	}
}

func TestExecSpec(t *testing.T) {
	e := newEnv(t)
	c := e.capture()
	e.start()
	e.create(testPool)
	c.waitStdin(t)
	start, stdin := c.got()
	if start.GetCmd() != "/usr/local/bin/muster-runner" || start.GetUser() != "runner" {
		t.Errorf("cmd/user = %q/%q", start.GetCmd(), start.GetUser())
	}
	if len(start.GetArgs()) != 0 || len(start.GetEnv()) != 0 {
		t.Errorf("args/env = %v/%v, want none", start.GetArgs(), start.GetEnv())
	}
	if !start.GetHasStdin() {
		t.Error("has_stdin = false")
	}
	if issued := e.Rec.issued(); len(issued) != 1 || string(stdin) != issued[0] {
		t.Errorf("stdin is not the issued JIT config")
	}
}

func TestJITAfterClaim(t *testing.T) {
	e := newEnv(t, withCapacity(0))
	e.start()
	e.create(testPool)
	e.drive(time.Second, func() bool { return e.Pool.CallCount("ClaimVM") >= 3 })
	if n := e.GitHub.RequestCount(fakegithub.PatternRepoJITConfig); n != 0 {
		t.Fatalf("JIT requests before a claim = %d, want 0", n)
	}
}

func TestStartingToRunningOnJobStarted(t *testing.T) {
	e := newEnv(t)
	c := e.capture()
	e.start()
	id := e.create(testPool)
	c.waitStdin(t)
	info, _ := e.info(id)
	e.Registry.JobStarted(info.RunnerName)
	e.waitState(id, session.Running)
	if info, _ = e.info(id); !info.Busy {
		t.Error("busy = false after JobStarted")
	}
	if a, nb := e.Registry.Counts(testPool); a != 1 || nb != 0 {
		t.Errorf("Counts = (%d, %d), want (1, 0)", a, nb)
	}
}

func TestJobStartedUnknownRunnerIgnored(t *testing.T) {
	e := newEnv(t)
	e.start()
	e.Registry.JobStarted("muster-muster-linux-zzzzzzzzzzzz")
	e.Registry.JobCompleted("someone-else")
}

func TestNoRunningBeforeConfigSent(t *testing.T) {
	e := newEnv(t)
	c := e.capture()
	release := e.Exec.HoldServerInfo()
	e.start()
	id := e.create(testPool)
	waitFor(t, func() bool { return e.GitHub.RequestCount(fakegithub.PatternRepoJITConfig) == 1 })
	info, _ := e.info(id)
	e.Registry.JobStarted(info.RunnerName)
	e.drive(time.Second, func() bool { return e.elapsed() >= 5*time.Second })
	if st := e.state(id); st != session.Starting {
		t.Fatalf("state with exec held = %v, want STARTING", st)
	}
	release()
	e.waitState(id, session.Running)
	select {
	case <-c.stdinDone:
	default:
		t.Fatal("RUNNING before the runner read its config")
	}
}

func TestStartTimeout(t *testing.T) {
	e := newEnv(t)
	e.GitHub.NextRunner(fakegithub.Behavior{NeverRegister: true})
	e.start()
	e.create(testPool)
	got := e.driveEnd(10 * time.Second)
	if got.State != session.Failed || got.Reason != session.ReasonStartTimeout {
		t.Fatalf("ended = %v %v, want FAILED start_timeout", got.State, got.Reason)
	}
	if el := e.elapsed(); el < 2*time.Minute {
		t.Fatalf("failed after %v, want at least 2m", el)
	}
	if n := e.Pool.CallCount("ReleaseVM"); n != 1 {
		t.Fatalf("ReleaseVM calls = %d, want 1", n)
	}
}

func TestHeartbeatWhileStarting(t *testing.T) {
	e := newEnv(t)
	e.start()
	id := e.create(testPool)
	e.waitState(id, session.Starting)
	e.drive(time.Second, func() bool { return e.elapsed() >= 90*time.Second })
	waitFor(t, func() bool { return e.Pool.CallCount("Heartbeat") >= 8 })
	if n := len(e.Pool.Leases()); n != 1 {
		t.Fatalf("leases = %d, want 1", n)
	}
}

func TestExecExitWhileStarting(t *testing.T) {
	e := newEnv(t)
	e.setHandler(func(_ context.Context, _ *mvmexecv1.ExecStart, stdin io.Reader, _, _ io.Writer) fakeexec.Result {
		_, _ = io.Copy(io.Discard, stdin)
		return fakeexec.Result{ExitCode: 1}
	})
	e.start()
	e.create(testPool)
	got := e.endedInfo()
	if got.State != session.Failed || got.Reason != session.ReasonExecError {
		t.Fatalf("ended = %v %v, want FAILED exec_error", got.State, got.Reason)
	}
}

func TestJITError(t *testing.T) {
	e := newEnv(t)
	e.GitHub.FailNext(fakegithub.PatternRepoJITConfig, fakegithub.Fault{Status: http.StatusInternalServerError})
	e.start()
	e.create(testPool)
	got := e.endedInfo()
	if got.State != session.Failed || got.Reason != session.ReasonJITError {
		t.Fatalf("ended = %v %v, want FAILED jit_error", got.State, got.Reason)
	}
	if n := e.Pool.CallCount("ReleaseVM"); n != 1 {
		t.Fatalf("ReleaseVM calls = %d, want 1", n)
	}
}

func TestJITConfigNeverLogged(t *testing.T) {
	e := newEnv(t)
	e.setHandler(func(_ context.Context, _ *mvmexecv1.ExecStart, stdin io.Reader, _, _ io.Writer) fakeexec.Result {
		_, _ = stdin.Read(make([]byte, 1))
		return fakeexec.Result{Abort: errors.New("boom")}
	})
	e.start()
	e.create(testPool)
	got := e.endedInfo()
	if got.State != session.Failed || got.Reason != session.ReasonExecError {
		t.Fatalf("ended = %v %v, want FAILED exec_error", got.State, got.Reason)
	}
	e.assertNoSecret(string(got.Reason))
}

func TestJITConfigNotLoggedWhenExecOpenFails(t *testing.T) {
	e := newEnv(t)
	e.Exec.FailNext("ExecCommand", codes.Internal)
	e.start()
	e.create(testPool)
	got := e.endedInfo()
	if got.State != session.Failed || got.Reason != session.ReasonExecError {
		t.Fatalf("ended = %v %v, want FAILED exec_error", got.State, got.Reason)
	}
	e.assertNoSecret(string(got.Reason))
}

func TestRunnerOutputLogged(t *testing.T) {
	e := newEnv(t)
	e.setHandler(func(ctx context.Context, _ *mvmexecv1.ExecStart, stdin io.Reader, stdout, stderr io.Writer) fakeexec.Result {
		_, _ = io.Copy(io.Discard, stdin)
		_, _ = io.WriteString(stdout, "hello\n")
		_, _ = io.WriteString(stderr, "oops\n")
		line := strings.Repeat("x", 1023) + "\n"
		for range 10 * 1024 {
			if _, err := io.WriteString(stdout, line); err != nil {
				break
			}
		}
		<-ctx.Done()
		return fakeexec.Result{}
	})
	e.start()
	id := e.create(testPool)
	waitFor(t, func() bool {
		l := e.Logs.String()
		return strings.Contains(l, `"stream":"stdout","line":"hello"`) && strings.Contains(l, `"stream":"stderr","line":"oops"`)
	})
	info, _ := e.info(id)
	e.Registry.JobStarted(info.RunnerName)
	e.waitState(id, session.Running)
}

func TestCompletedWhileStarting(t *testing.T) {
	e := newEnv(t)
	c := e.capture()
	release := e.Exec.HoldServerInfo()
	e.start()
	id := e.create(testPool)
	waitFor(t, func() bool { return e.GitHub.RequestCount(fakegithub.PatternRepoJITConfig) == 1 })
	info, _ := e.info(id)
	e.Registry.JobCompleted(info.RunnerName)
	release()
	c.waitStdin(t)
	waitFor(t, func() bool { i, _ := e.info(id); return i.State == session.Running && i.Busy })
	e.waitLog(`"msg":"job completed"`)
	e.Clock.Advance(30 * time.Second)
	got := e.endedInfo()
	if got.State != session.Ended || got.Reason != session.ReasonJobCompleted {
		t.Fatalf("ended = %v %v, want ENDED job_completed", got.State, got.Reason)
	}
}

func TestStartTimeoutBoundsLaunch(t *testing.T) {
	e := newEnv(t)
	release := e.Exec.HoldServerInfo()
	defer release()
	e.start()
	id := e.create(testPool)
	waitFor(t, func() bool { return e.GitHub.RequestCount(fakegithub.PatternRepoJITConfig) == 1 })
	e.waitState(id, session.Starting)
	got := e.driveEnd(10 * time.Second)
	if got.State != session.Failed || got.Reason != session.ReasonStartTimeout {
		t.Fatalf("ended = %v %v, want FAILED start_timeout", got.State, got.Reason)
	}
	if n := len(e.GitHub.Runners()); n != 0 {
		t.Fatalf("runners after start timeout = %d, want 0", n)
	}
}

func TestMaxDurationWhileStarting(t *testing.T) {
	p := testPoolConfig(testPool)
	p.MaxSessionDuration = config.Duration(time.Minute)
	e := newEnv(t, withPools(p))
	e.GitHub.NextRunner(fakegithub.Behavior{NeverRegister: true})
	e.start()
	e.create(testPool)
	got := e.driveEnd(10 * time.Second)
	if got.State != session.Ended || got.Reason != session.ReasonMaxDuration {
		t.Fatalf("ended = %v %v, want ENDED max_duration", got.State, got.Reason)
	}
	if el := e.elapsed(); el >= 2*time.Minute {
		t.Fatalf("ended after %v, want before the 2m start timeout", el)
	}
}
