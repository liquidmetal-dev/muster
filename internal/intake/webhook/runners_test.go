package webhook_test

import (
	"net/http"
	"slices"
	"sync"
	"testing"

	"github.com/liquidmetal-dev/muster/internal/intake/webhook"
)

type startedCall struct {
	runner string
	job    int64
}

type fakeRunners struct {
	mu      sync.Mutex
	started []startedCall
}

func (f *fakeRunners) JobStarted(runnerName string, jobID int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, startedCall{runnerName, jobID})
}

func (f *fakeRunners) calls() []startedCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.started)
}

func inProgress(id int64, runner string) job {
	j := matching(id)
	j.action, j.runner = "in_progress", runner
	return j
}

func TestInProgressCallsRunners(t *testing.T) {
	r := &fakeRunners{}
	h := newHandler(t, webhook.Config{Runners: r})
	if code := send(t, h, "d1", inProgress(7, "r1")); code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if got := r.calls(); !slices.Equal(got, []startedCall{{"r1", 7}}) {
		t.Fatalf("JobStarted calls = %v, want [{r1 7}]", got)
	}
}

func TestInProgressWithoutRunnerNameSkipsRunners(t *testing.T) {
	r := &fakeRunners{}
	h := newHandler(t, webhook.Config{Runners: r})
	send(t, h, "d1", inProgress(7, ""))
	if got := r.calls(); len(got) != 0 {
		t.Fatalf("JobStarted calls = %v, want none", got)
	}
}

func TestDuplicateDoesNotCallRunners(t *testing.T) {
	r := &fakeRunners{}
	h := newHandler(t, webhook.Config{Runners: r})
	send(t, h, "d1", inProgress(7, "r1"))
	send(t, h, "d1", inProgress(7, "r1"))
	if got := r.calls(); len(got) != 1 {
		t.Fatalf("JobStarted calls = %v, want 1", got)
	}
}

func TestCompletedDoesNotCallRunners(t *testing.T) {
	r := &fakeRunners{}
	h := newHandler(t, webhook.Config{Runners: r})
	j := inProgress(7, "r1")
	j.action, j.conclusion = "completed", "success"
	send(t, h, "d1", j)
	if got := r.calls(); len(got) != 0 {
		t.Fatalf("JobStarted calls = %v, want none", got)
	}
}

func TestNilRunnersIsFine(t *testing.T) {
	h := newHandler(t, webhook.Config{})
	if code := send(t, h, "d1", inProgress(7, "r1")); code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
}
