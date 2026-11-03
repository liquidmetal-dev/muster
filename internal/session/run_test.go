package session_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	mvmexecv1 "github.com/liquidmetal-dev/flintlock/api/services/microvmexec/v1alpha1"

	"github.com/liquidmetal-dev/muster/internal/config"
	"github.com/liquidmetal-dev/muster/internal/fakegithub"
	"github.com/liquidmetal-dev/muster/internal/session"
	"github.com/liquidmetal-dev/muster/internal/vmsource/flintlock/fakeexec"
)

// running starts one session and brings it to RUNNING through a poll. It
// returns the session id, its runner name and the manual time it entered
// RUNNING.
func (e *env) running(c *capture) (id, runner string, at time.Duration) {
	e.t.Helper()
	id = e.create(testPool)
	if c != nil {
		c.waitStdin(e.t)
	} else {
		waitFor(e.t, func() bool { return len(e.GitHub.Runners()) == 1 })
	}
	e.Clock.Advance(2 * time.Second)
	e.pollOnce()
	e.waitState(id, session.Running)
	i, _ := e.info(id)
	return id, i.RunnerName, e.elapsed()
}

func (e *env) waitLog(fragment string) {
	e.t.Helper()
	waitFor(e.t, func() bool { return strings.Contains(e.Logs.String(), fragment) })
}

func wantEnd(t *testing.T, got session.Info, state session.State, reason session.Reason) {
	t.Helper()
	if got.State != state || got.Reason != reason {
		t.Fatalf("ended = %v %v, want %v %v", got.State, got.Reason, state, reason)
	}
}

func TestHeartbeat(t *testing.T) {
	e := newEnv(t)
	c := e.capture()
	e.start()
	_, _, at := e.running(c)
	before := e.Pool.CallCount("Heartbeat")
	e.drive(time.Second, func() bool { return e.elapsed() >= at+60*time.Second })
	waitFor(t, func() bool { return e.Pool.CallCount("Heartbeat")-before >= 5 })
	c.exit(0)
	e.endedInfo()
	after := e.Pool.CallCount("Heartbeat")
	e.Clock.Advance(time.Minute)
	time.Sleep(100 * time.Millisecond)
	if n := e.Pool.CallCount("Heartbeat"); n != after {
		t.Fatalf("heartbeats after release: %d, want %d", n, after)
	}
}

func TestEndOnExecExit(t *testing.T) {
	for _, code := range []int32{0, 3} {
		t.Run(string(rune('0'+code)), func(t *testing.T) {
			e := newEnv(t)
			c := e.capture()
			e.start()
			e.running(c)
			c.exit(code)
			wantEnd(t, e.endedInfo(), session.Ended, session.ReasonExecExit)
		})
	}
}

func TestEndOnJobCompletedAfterGrace(t *testing.T) {
	e := newEnv(t)
	c := e.capture()
	e.start()
	id, runner, _ := e.running(c)
	e.Registry.JobCompleted(runner)
	e.waitLog(`"msg":"job completed"`)
	e.Clock.Advance(29 * time.Second)
	time.Sleep(50 * time.Millisecond)
	if st := e.state(id); st != session.Running {
		t.Fatalf("state 29s after completed = %v, want RUNNING", st)
	}
	e.Clock.Advance(time.Second)
	wantEnd(t, e.endedInfo(), session.Ended, session.ReasonJobCompleted)
}

func TestEndOnRunnerAbsent(t *testing.T) {
	e := newEnv(t)
	c := e.capture()
	e.start()
	_, runner, _ := e.running(c)
	e.Rec.hide(runner, true)
	e.pollOnce()
	wantEnd(t, e.endedInfo(), session.Ended, session.ReasonRunnerAbsent)
}

func TestEndOnRunnerDeregistered(t *testing.T) {
	e := newEnv(t)
	e.GitHub.NextRunner(fakegithub.Behavior{RunFor: 40 * time.Second})
	if _, err := e.GitHub.QueueJob(testRepo, []string{"self-hosted", "linux"}); err != nil {
		t.Fatalf("QueueJob: %v", err)
	}
	c := e.capture()
	e.start()
	e.running(c)
	e.pollOnce()
	wantEnd(t, e.endedInfo(), session.Ended, session.ReasonRunnerAbsent)
}

func TestAbsentBeforeSeenIgnored(t *testing.T) {
	e := newEnv(t)
	c := e.capture()
	e.start()
	id := e.create(testPool)
	c.waitStdin(t)
	i, _ := e.info(id)
	e.Rec.hide(i.RunnerName, true)
	e.Registry.JobStarted(i.RunnerName)
	e.waitState(id, session.Running)
	e.pollOnce()
	time.Sleep(50 * time.Millisecond)
	if st := e.state(id); st != session.Running {
		t.Fatalf("state after absent before seen = %v, want RUNNING", st)
	}
	e.Rec.hide(i.RunnerName, false)
	e.pollOnce()
	e.Rec.hide(i.RunnerName, true)
	e.pollOnce()
	wantEnd(t, e.endedInfo(), session.Ended, session.ReasonRunnerAbsent)
}

func TestIdleRunnerNeverBusy(t *testing.T) {
	e := newEnv(t)
	e.GitHub.NextRunner(fakegithub.Behavior{HoldIdle: true})
	c := e.capture()
	e.start()
	_, _, at := e.running(c)
	got := e.driveEnd(10 * time.Second)
	wantEnd(t, got, session.Ended, session.ReasonIdleTimeout)
	if d := e.elapsed() - at; d < 5*time.Minute {
		t.Fatalf("idle timeout after %v, want at least 5m", d)
	}
}

func TestBusyDisarmsIdle(t *testing.T) {
	e := newEnv(t)
	e.GitHub.NextRunner(fakegithub.Behavior{HoldIdle: true})
	c := e.capture()
	e.start()
	id, runner, at := e.running(c)
	e.drive(10*time.Second, func() bool { return e.elapsed() >= at+4*time.Minute })
	e.Registry.JobStarted(runner)
	e.waitLog(`"msg":"runner busy"`)
	e.drive(10*time.Second, func() bool { return e.elapsed() >= at+6*time.Minute })
	time.Sleep(50 * time.Millisecond)
	if st := e.state(id); st != session.Running {
		t.Fatalf("state at 6m after busy at 4m = %v, want RUNNING", st)
	}
}

func TestMaxDuration(t *testing.T) {
	p := testPoolConfig(testPool)
	p.MaxSessionDuration = config.Duration(time.Hour)
	e := newEnv(t, withPools(p))
	e.GitHub.NextRunner(fakegithub.Behavior{RunFor: 2 * time.Hour})
	if _, err := e.GitHub.QueueJob(testRepo, []string{"self-hosted", "linux"}); err != nil {
		t.Fatalf("QueueJob: %v", err)
	}
	c := e.capture()
	e.start()
	e.running(c)
	got := e.driveEnd(time.Minute)
	wantEnd(t, got, session.Ended, session.ReasonMaxDuration)
	if el := e.elapsed(); el < time.Hour {
		t.Fatalf("max duration after %v, want at least 1h from the claim", el)
	}
}

func TestRunnerTakesOtherJob(t *testing.T) {
	e := newEnv(t)
	e.GitHub.NextRunner(fakegithub.Behavior{RunFor: time.Hour})
	first, err := e.GitHub.QueueJob(testRepo, []string{"self-hosted", "linux"})
	if err != nil {
		t.Fatalf("QueueJob: %v", err)
	}
	if _, err := e.GitHub.QueueJob(testRepo, []string{"self-hosted"}); err != nil {
		t.Fatalf("QueueJob: %v", err)
	}
	c := e.capture()
	e.start()
	id, runner, _ := e.running(c)
	waitFor(t, func() bool { i, _ := e.info(id); return i.Busy })
	for _, j := range e.GitHub.Jobs() {
		if j.ID == first.ID && j.RunnerName != runner {
			t.Fatalf("oldest job ran on %q, want %q", j.RunnerName, runner)
		}
	}
	if a, nb := e.Registry.Counts(testPool); a != 1 || nb != 0 {
		t.Fatalf("Counts = (%d, %d), want (1, 0)", a, nb)
	}
	c.exit(0)
	wantEnd(t, e.endedInfo(), session.Ended, session.ReasonExecExit)
}

func TestExecBrokenIsNotEnd(t *testing.T) {
	e := newEnv(t)
	breakNow := make(chan struct{})
	read := make(chan struct{})
	e.setHandler(func(ctx context.Context, _ *mvmexecv1.ExecStart, stdin io.Reader, _, _ io.Writer) fakeexec.Result {
		_, _ = io.Copy(io.Discard, stdin)
		close(read)
		select {
		case <-breakNow:
			return fakeexec.Result{Abort: errors.New("host lost")}
		case <-ctx.Done():
			return fakeexec.Result{}
		}
	})
	e.start()
	id := e.create(testPool)
	<-read
	e.Clock.Advance(2 * time.Second)
	e.pollOnce()
	e.waitState(id, session.Running)
	close(breakNow)
	e.waitLog(`"msg":"exec stream broke; watching the runner through GitHub"`)
	e.pollOnce()
	time.Sleep(50 * time.Millisecond)
	if st := e.state(id); st != session.Running {
		t.Fatalf("state after the exec stream broke = %v, want RUNNING", st)
	}
	i, _ := e.info(id)
	e.Rec.hide(i.RunnerName, true)
	e.pollOnce()
	wantEnd(t, e.endedInfo(), session.Ended, session.ReasonRunnerAbsent)
}

func TestFirstEndSignalWins(t *testing.T) {
	e := newEnv(t)
	c := e.capture()
	e.start()
	_, runner, _ := e.running(c)
	e.Registry.JobCompleted(runner)
	e.waitLog(`"msg":"job completed"`)
	c.exit(0)
	e.Clock.Advance(30 * time.Second)
	e.endedInfo()
	select {
	case extra := <-e.ended:
		t.Fatalf("second end: %+v", extra)
	case <-time.After(200 * time.Millisecond):
	}
	if n := e.Pool.CallCount("ReleaseVM"); n != 1 {
		t.Fatalf("ReleaseVM calls = %d, want 1", n)
	}
}
