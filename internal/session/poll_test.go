package session_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liquidmetal-dev/muster/internal/fakegithub"
	"github.com/liquidmetal-dev/muster/internal/session"
)

// pollOnce advances the clock to the next poll and waits for its request.
func (e *env) pollOnce() {
	e.t.Helper()
	want := e.GitHub.RequestCount(fakegithub.PatternRepoRunners) + 1
	e.Clock.Advance(pollPeriod)
	waitFor(e.t, func() bool { return e.GitHub.RequestCount(fakegithub.PatternRepoRunners) >= want })
}

// pollOnline advances the clock past the fake runner's online delay, waits for the fake to list
// the session's runner online, then polls. The fake changes runner state on its own goroutine,
// so a poll that runs first sees the runner offline, and no other poll follows.
func (e *env) pollOnline(id string) {
	e.t.Helper()
	i, _ := e.info(id)
	e.Clock.Advance(2 * time.Second)
	waitFor(e.t, func() bool { return runnerStatus(e.GitHub.Runners(), i.RunnerName) == "online" })
	e.pollOnce()
}

func runnerStatus(runners []fakegithub.Runner, name string) string {
	for _, r := range runners {
		if r.Name == name {
			return r.Status
		}
	}
	return ""
}

func TestPollOnePerScope(t *testing.T) {
	e := newEnv(t, withPools(testPoolConfig("linux"), testPoolConfig("linux2")))
	e.start()
	a := e.create("linux")
	b := e.create("linux2")
	e.waitState(a, session.Starting)
	e.waitState(b, session.Starting)
	for range 3 {
		e.pollOnce()
	}
	time.Sleep(50 * time.Millisecond)
	if n := e.GitHub.RequestCount(fakegithub.PatternRepoRunners); n != 3 {
		t.Fatalf("runner list requests after 3 polls = %d, want 3", n)
	}
}

func TestPollOnlineMovesToRunning(t *testing.T) {
	e := newEnv(t)
	c := e.capture()
	e.start()
	id := e.create(testPool)
	c.waitStdin(t)
	e.pollOnline(id)
	e.waitState(id, session.Running)
	if i, _ := e.info(id); i.Busy {
		t.Error("busy = true for an idle runner")
	}
	_, stdin := c.got()
	if issued := e.Rec.issued(); len(issued) != 1 || string(stdin) != issued[0] {
		t.Error("runner did not get the issued config before RUNNING")
	}
}

func TestPollBusyMarksBusy(t *testing.T) {
	e := newEnv(t)
	e.GitHub.NextRunner(fakegithub.Behavior{RunFor: time.Hour})
	if _, err := e.GitHub.QueueJob(testRepo, []string{"self-hosted", "linux"}); err != nil {
		t.Fatalf("QueueJob: %v", err)
	}
	c := e.capture()
	e.start()
	id := e.create(testPool)
	c.waitStdin(t)
	e.pollOnline(id)
	waitFor(t, func() bool { i, _ := e.info(id); return i.State == session.Running && i.Busy })
	if a, nb := e.Registry.Counts(testPool); a != 1 || nb != 0 {
		t.Fatalf("Counts = (%d, %d), want (1, 0)", a, nb)
	}
}

func TestPollErrorIsNotAbsence(t *testing.T) {
	e := newEnv(t)
	c := e.capture()
	e.start()
	id := e.create(testPool)
	c.waitStdin(t)
	e.pollOnline(id)
	e.waitState(id, session.Running)
	e.GitHub.FailNext(fakegithub.PatternRepoRunners, fakegithub.Fault{Status: http.StatusInternalServerError})
	e.pollOnce()
	waitFor(t, func() bool { return strings.Contains(e.Logs.String(), `"msg":"list runners"`) })
	time.Sleep(50 * time.Millisecond)
	if st := e.state(id); st != session.Running {
		t.Fatalf("state after a failed poll = %v, want RUNNING", st)
	}
}

func TestPollDoesNotBlockOnSlowSession(t *testing.T) {
	e := newEnv(t, withPools(testPoolConfig("linux"), testPoolConfig("linux2")))
	e.start()
	e.GitHub.Stall(fakegithub.PatternRepoJITConfig, 3*time.Second)
	slow := e.create("linux")
	waitFor(t, func() bool { return e.GitHub.RequestCount(fakegithub.PatternRepoJITConfig) == 1 })
	fast := e.create("linux2")
	waitFor(t, func() bool {
		return e.GitHub.RequestCount(fakegithub.PatternRepoJITConfig) == 2 && len(e.GitHub.Runners()) == 1
	})
	e.pollOnline(fast)
	e.waitState(fast, session.Running)
	if st := e.state(slow); st != session.Starting {
		t.Fatalf("slow session = %v, want STARTING while its JIT request stalls", st)
	}
}
