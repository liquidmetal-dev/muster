package fakegithub_test

import (
	"fmt"
	"net/http"
	"runtime"
	"testing"
	"time"

	"github.com/liquidmetal-dev/muster/internal/clock"
	"github.com/liquidmetal-dev/muster/internal/fakegithub"
)

const waitTimeout = 2 * time.Second

func jobByID(e env, id int64) fakegithub.Job {
	for _, j := range e.fake.Jobs() {
		if j.ID == id {
			return j
		}
	}
	return fakegithub.Job{}
}

func runnerByID(e env, id int64) fakegithub.Runner {
	for _, r := range e.fake.Runners() {
		if r.ID == id {
			return r
		}
	}
	return fakegithub.Runner{}
}

func waitJobStatus(t *testing.T, e env, id int64, status string) fakegithub.Job {
	t.Helper()
	e.fake.WaitFor(t, waitTimeout, func() bool { return jobByID(e, id).Status == status })
	return jobByID(e, id)
}

func waitRunnerStatus(t *testing.T, e env, id int64, status string) fakegithub.Runner {
	t.Helper()
	e.fake.WaitFor(t, waitTimeout, func() bool { return runnerByID(e, id).Status == status })
	return runnerByID(e, id)
}

// waitActions waits until the recorded delivery actions equal want, because
// the fake changes state under its mutex and delivers the webhook afterwards.
func waitActions(t *testing.T, e env, want string) {
	t.Helper()
	e.fake.WaitFor(t, waitTimeout, func() bool { return fmt.Sprint(actions(e)) == want })
}

func actions(e env) []string {
	var out []string
	for _, d := range e.fake.Deliveries() {
		out = append(out, d.Action)
	}
	return out
}

func TestRunnerComesOnlineAfterDelay(t *testing.T) {
	e := start(t)
	seed(e)
	id := orgJIT(t, e, "muster-a", "x").GetRunner().GetID()
	if got := runnerByID(e, id).Status; got != "offline" {
		t.Fatalf("status right after JIT = %q", got)
	}
	e.clock.Advance(time.Second)
	waitRunnerStatus(t, e, id, "online")
}

func TestNeverRegisterStaysOffline(t *testing.T) {
	e := start(t)
	seed(e)
	e.fake.NextRunner(fakegithub.Behavior{NeverRegister: true})
	id := orgJIT(t, e, "muster-a", "x").GetRunner().GetID()
	queue(t, e, orgRepo, "x")
	e.clock.Advance(time.Hour)
	time.Sleep(50 * time.Millisecond)
	if got := runnerByID(e, id).Status; got != "offline" {
		t.Fatalf("status = %q, want offline", got)
	}
	if got := actions(e); len(got) != 1 || got[0] != "queued" {
		t.Fatalf("deliveries %v", got)
	}
}

func TestOnlineRunnerPicksOldestMatchingJob(t *testing.T) {
	url, _ := hook(t)
	e := start(t, withHook(url))
	seed(e)
	a := queue(t, e, orgRepo, "x", "y")
	b := queue(t, e, orgRepo, "x")
	orgJIT(t, e, "muster-a", "x", "y", "z")
	e.clock.Advance(time.Second)
	got := waitJobStatus(t, e, a.ID, "in_progress")
	if got.RunnerName != "muster-a" || got.RunnerID == 0 {
		t.Fatalf("job a %+v", got)
	}
	if jobByID(e, b.ID).Status != "queued" {
		t.Fatalf("job b %+v", jobByID(e, b.ID))
	}
	waitActions(t, e, "[queued queued in_progress]")
}

func TestJobQueuedAfterRunnerOnlineIsPickedUp(t *testing.T) {
	url, _ := hook(t)
	e := start(t, withHook(url))
	seed(e)
	id := orgJIT(t, e, "muster-a", "x").GetRunner().GetID()
	e.clock.Advance(time.Second)
	waitRunnerStatus(t, e, id, "online")
	job := queue(t, e, orgRepo, "x")
	waitJobStatus(t, e, job.ID, "in_progress")
	waitActions(t, e, "[queued in_progress]")
}

func TestLabelsMustBeSubset(t *testing.T) {
	e := start(t)
	seed(e)
	job := queue(t, e, orgRepo, "x", "q")
	id := orgJIT(t, e, "muster-a", "x", "y").GetRunner().GetID()
	e.clock.Advance(time.Second)
	waitRunnerStatus(t, e, id, "online")
	if got := jobByID(e, job.ID).Status; got != "queued" {
		t.Fatalf("job status %q, want queued", got)
	}
}

func TestScopeCoversRepo(t *testing.T) {
	e := start(t)
	seed(e)
	orgJob := queue(t, e, orgRepo, "x")
	privJob := queue(t, e, privateRepo, "x")
	orgRunner := orgJIT(t, e, "muster-org", "x").GetRunner().GetID()
	e.clock.Advance(time.Second)
	waitRunnerStatus(t, e, orgRunner, "online")
	if got := waitJobStatus(t, e, orgJob.ID, "in_progress"); got.RunnerName != "muster-org" {
		t.Fatalf("org job %+v", got)
	}
	if got := jobByID(e, privJob.ID).Status; got != "queued" {
		t.Fatalf("private job status %q, org runner must not take it", got)
	}
	cfg, _, err := e.client.Actions.CreateRepoJITConfig(ctx(), "someone", "private", jitReq("muster-repo", "x"))
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Second)
	waitRunnerStatus(t, e, cfg.GetRunner().GetID(), "online")
	if got := waitJobStatus(t, e, privJob.ID, "in_progress"); got.RunnerName != "muster-repo" {
		t.Fatalf("private job %+v", got)
	}
}

func TestHoldIdleNeverTakesJob(t *testing.T) {
	e := start(t)
	seed(e)
	e.fake.NextRunner(fakegithub.Behavior{HoldIdle: true})
	job := queue(t, e, orgRepo, "x")
	id := orgJIT(t, e, "muster-a", "x").GetRunner().GetID()
	e.clock.Advance(time.Hour)
	r := waitRunnerStatus(t, e, id, "online")
	if r.Busy || jobByID(e, job.ID).Status != "queued" {
		t.Fatalf("runner %+v job %+v", r, jobByID(e, job.ID))
	}
}

func TestCompletedAfterRunFor(t *testing.T) {
	url, ch := hook(t)
	e := start(t, withHook(url))
	seed(e)
	job := queue(t, e, orgRepo, "x")
	id := orgJIT(t, e, "muster-a", "x").GetRunner().GetID()
	e.clock.Advance(time.Second)
	waitJobStatus(t, e, job.ID, "in_progress")
	if r := runnerByID(e, id); !r.Busy || r.JobID != job.ID {
		t.Fatalf("runner %+v", r)
	}
	e.clock.Advance(5 * time.Second)
	got := waitJobStatus(t, e, job.ID, "completed")
	if got.Conclusion != "success" {
		t.Fatalf("job %+v", got)
	}
	if r := runnerByID(e, id); r.Busy || r.JobID != 0 {
		t.Fatalf("runner after completion %+v", r)
	}
	recv(t, ch) // queued
	recv(t, ch) // in_progress
	ev := parseEvent(t, recv(t, ch), hookSecret)
	if ev.GetAction() != "completed" || ev.GetWorkflowJob().GetRunnerName() != "muster-a" || ev.GetWorkflowJob().GetConclusion() != "success" {
		t.Fatalf("completed event %+v", ev.GetWorkflowJob())
	}
}

func TestExitCodeGivesFailure(t *testing.T) {
	e := start(t)
	seed(e)
	e.fake.NextRunner(fakegithub.Behavior{ExitCode: 3})
	job := queue(t, e, orgRepo, "x")
	cfg := orgJIT(t, e, "muster-a", "x")
	blob, err := fakegithub.DecodeJITConfig(cfg.GetEncodedJITConfig())
	if err != nil || blob.ExitCode != 3 || blob.SleepSeconds != 5 {
		t.Fatalf("blob %+v err %v", blob, err)
	}
	e.clock.Advance(time.Second)
	waitJobStatus(t, e, job.ID, "in_progress")
	e.clock.Advance(5 * time.Second)
	if got := waitJobStatus(t, e, job.ID, "completed"); got.Conclusion != "failure" {
		t.Fatalf("job %+v", got)
	}
}

func TestDeregisterAfterCompletion(t *testing.T) {
	e := start(t)
	seed(e)
	job := queue(t, e, orgRepo, "x")
	orgJIT(t, e, "muster-a", "x")
	e.clock.Advance(time.Second)
	waitJobStatus(t, e, job.ID, "in_progress")
	e.clock.Advance(5 * time.Second)
	waitJobStatus(t, e, job.ID, "completed")
	e.clock.Advance(500 * time.Millisecond)
	e.fake.WaitFor(t, waitTimeout, func() bool { return len(e.fake.Runners()) == 0 })
}

func TestCompletedRunnerTakesNoSecondJob(t *testing.T) {
	e := start(t)
	seed(e)
	e.fake.NextRunner(fakegithub.Behavior{DeregisterAfter: time.Hour})
	first := queue(t, e, orgRepo, "x")
	second := queue(t, e, orgRepo, "x")
	id := orgJIT(t, e, "muster-a", "x").GetRunner().GetID()
	e.clock.Advance(time.Second)
	waitJobStatus(t, e, first.ID, "in_progress")
	e.clock.Advance(5 * time.Second)
	waitJobStatus(t, e, first.ID, "completed")
	r := runnerByID(e, id)
	if !r.Finished || r.Busy {
		t.Fatalf("runner %+v", r)
	}
	e.clock.Advance(10 * time.Second)
	time.Sleep(50 * time.Millisecond)
	if got := jobByID(e, second.ID).Status; got != "queued" {
		t.Fatalf("second job status %q", got)
	}
	n := 0
	for _, a := range actions(e) {
		if a == "in_progress" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d in_progress deliveries, want 1", n)
	}
}

func TestNextRunnerQueueIsFIFO(t *testing.T) {
	e := start(t)
	seed(e)
	e.fake.NextRunner(fakegithub.Behavior{RunFor: time.Second})
	e.fake.NextRunner(fakegithub.Behavior{RunFor: 2 * time.Second})
	for i, want := range []float64{1, 2, 5} {
		cfg := orgJIT(t, e, fmt.Sprintf("muster-%d", i), "x")
		blob, err := fakegithub.DecodeJITConfig(cfg.GetEncodedJITConfig())
		if err != nil || blob.SleepSeconds != want {
			t.Fatalf("runner %d: sleep %v, want %v (err %v)", i, blob.SleepSeconds, want, err)
		}
	}
}

func TestSetDefaultBehaviorApplies(t *testing.T) {
	e := start(t)
	seed(e)
	e.fake.SetDefaultBehavior(fakegithub.Behavior{OnlineAfter: 10 * time.Second})
	id := orgJIT(t, e, "muster-a", "x").GetRunner().GetID()
	e.clock.Advance(time.Second)
	time.Sleep(50 * time.Millisecond)
	if got := runnerByID(e, id).Status; got != "offline" {
		t.Fatalf("status after 1s = %q, want offline", got)
	}
	e.clock.Advance(9 * time.Second)
	waitRunnerStatus(t, e, id, "online")
}

func TestDeleteBusyRunnerIs422(t *testing.T) {
	e := start(t)
	seed(e)
	job := queue(t, e, orgRepo, "x")
	id := orgJIT(t, e, "muster-a", "x").GetRunner().GetID()
	e.clock.Advance(time.Second)
	waitJobStatus(t, e, job.ID, "in_progress")
	_, err := e.client.Actions.RemoveOrganizationRunner(ctx(), org, id)
	wantStatus(t, err, http.StatusUnprocessableEntity)
	e.clock.Advance(5 * time.Second)
	waitJobStatus(t, e, job.ID, "completed")
	resp, err := e.client.Actions.RemoveOrganizationRunner(ctx(), org, id)
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete after completion: %v", err)
	}
}

func TestCloseStopsScheduler(t *testing.T) {
	before := runtime.NumGoroutine()
	s := fakegithub.New(fakegithub.Config{Clock: clock.NewManual(startTime)})
	s.Close()
	s.Close()
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Fatalf("%d goroutines after Close, %d before", after, before)
	}
}

type fatalRecorder struct {
	testing.TB
	msg string
}

func (f *fatalRecorder) Fatalf(format string, args ...any) { f.msg = fmt.Sprintf(format, args...) }
func (f *fatalRecorder) Helper()                           {}

func TestWaitForTimesOut(t *testing.T) {
	e := start(t)
	rec := &fatalRecorder{}
	e.fake.WaitFor(rec, 20*time.Millisecond, func() bool { return false })
	if rec.msg == "" {
		t.Fatal("WaitFor did not call Fatalf on timeout")
	}
}

func TestClockJumpCompletesJob(t *testing.T) {
	e := start(t)
	seed(e)
	job := queue(t, e, orgRepo, "x")
	id := orgJIT(t, e, "muster-a", "x").GetRunner().GetID()
	// One jump past online (+1s) and completion (+6s): both must apply.
	e.clock.Advance(6 * time.Second)
	got := waitJobStatus(t, e, job.ID, "completed")
	if got.Conclusion != "success" || got.RunnerName != "muster-a" {
		t.Fatalf("job %+v", got)
	}
	if r := runnerByID(e, id); r.Busy || !r.Finished {
		t.Fatalf("runner %+v", r)
	}
	waitActions(t, e, "[queued in_progress completed]")
	e.clock.Advance(500 * time.Millisecond)
	e.fake.WaitFor(t, waitTimeout, func() bool { return len(e.fake.Runners()) == 0 })
}
