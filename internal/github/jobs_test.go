package github_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gogithub "github.com/google/go-github/v93/github"

	"github.com/liquidmetal-dev/muster/internal/fakegithub"
	"github.com/liquidmetal-dev/muster/internal/github"
)

func TestListQueuedJobs(t *testing.T) {
	e := newEnv(t)
	e.Fake.AddRepo("acme/app")
	var want []fakegithub.Job
	for range 2 {
		j, err := e.Fake.QueueJob("acme/app", []string{"self-hosted", "linux"})
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, j)
	}
	got, err := e.AppClient(t).ListQueuedJobs(context.Background(), "acme/app")
	if err != nil {
		t.Fatalf("ListQueuedJobs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("jobs = %+v, want 2", got)
	}
	for i, j := range got {
		if j.JobID != want[i].ID || j.RunID != want[i].RunID || j.Repository != "acme/app" || !slices.Equal(j.Labels, want[i].Labels) {
			t.Errorf("job %d = %+v, want %+v", i, j, want[i])
		}
	}
}

func TestListQueuedJobsPaginated(t *testing.T) {
	e := newEnv(t)
	e.Fake.AddRepo("acme/app")
	for range 150 {
		if _, err := e.Fake.QueueJob("acme/app", []string{"self-hosted"}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := e.AppClient(t).ListQueuedJobs(context.Background(), "acme/app")
	if err != nil || len(got) != 150 {
		t.Fatalf("ListQueuedJobs = %d, %v; want 150", len(got), err)
	}
	if n := e.Fake.RequestCount(fakegithub.PatternRuns); n != 3 {
		t.Fatalf("run list requests = %d, want 3 (2 queued pages, 1 in_progress)", n)
	}
}

// TestListQueuedJobsJobPages uses an httptest stub because the fake returns
// one job per run without a Link header.
func TestListQueuedJobsJobPages(t *testing.T) {
	var jobPages atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v3/repos/acme/app/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("status") != "queued" {
			_ = json.NewEncoder(w).Encode(gogithub.WorkflowRuns{TotalCount: new(0)})
			return
		}
		_ = json.NewEncoder(w).Encode(gogithub.WorkflowRuns{TotalCount: new(1), WorkflowRuns: []*gogithub.WorkflowRun{{ID: new(int64(7)), Status: new("queued")}}})
	})
	var base string
	mux.HandleFunc("GET /api/v3/repos/acme/app/actions/runs/7/jobs", func(w http.ResponseWriter, r *http.Request) {
		jobPages.Add(1)
		n, first := 100, int64(1)
		if r.URL.Query().Get("page") == "2" {
			n, first = 5, 101
		} else {
			w.Header().Set("Link", fmt.Sprintf(`<%s/api/v3/repos/acme/app/actions/runs/7/jobs?page=2&per_page=100>; rel="next"`, base))
		}
		jobs := make([]*gogithub.WorkflowJob, n)
		for i := range jobs {
			jobs[i] = &gogithub.WorkflowJob{ID: new(first + int64(i)), RunID: new(int64(7)), Status: new("queued"), Labels: []string{"x"}}
		}
		_ = json.NewEncoder(w).Encode(gogithub.Jobs{TotalCount: new(105), Jobs: jobs})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	base = srv.URL
	c, err := github.New(github.Config{Token: "x", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.ListQueuedJobs(context.Background(), "acme/app")
	if err != nil || len(got) != 105 {
		t.Fatalf("ListQueuedJobs = %d, %v; want 105", len(got), err)
	}
	if jobPages.Load() != 2 {
		t.Fatalf("job pages fetched = %d, want 2", jobPages.Load())
	}
}

func TestListQueuedJobsEmpty(t *testing.T) {
	e := newEnv(t)
	e.Fake.AddRepo("acme/app")
	got, err := e.AppClient(t).ListQueuedJobs(context.Background(), "acme/app")
	if err != nil || len(got) != 0 {
		t.Fatalf("ListQueuedJobs = %+v, %v", got, err)
	}
}

func TestListQueuedJobsUnknownRepo(t *testing.T) {
	e := newEnv(t)
	if _, err := e.AppClient(t).ListQueuedJobs(context.Background(), "acme/missing"); !errors.Is(err, github.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestListQueuedJobsBadRepo(t *testing.T) {
	e := newEnv(t)
	if _, err := e.AppClient(t).ListQueuedJobs(context.Background(), "nope"); !errors.Is(err, github.ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
}

func TestListQueuedJobsSkipsNonQueuedRuns(t *testing.T) {
	e := newEnv(t)
	e.Fake.AddRepo("acme/app")
	c := e.AppClient(t)
	s, _ := github.RepoScope("acme/app")
	if _, err := c.GenerateJITConfig(context.Background(), s, jit("r1", "self-hosted")); err != nil {
		t.Fatal(err)
	}
	taken, err := e.Fake.QueueJob("acme/app", []string{"self-hosted"})
	if err != nil {
		t.Fatal(err)
	}
	e.Clock.Advance(time.Second)
	e.Fake.WaitFor(t, 5*time.Second, func() bool {
		for _, j := range e.Fake.Jobs() {
			if j.ID == taken.ID && j.Status != "queued" {
				return true
			}
		}
		return false
	})
	waiting, err := e.Fake.QueueJob("acme/app", []string{"gpu"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.ListQueuedJobs(context.Background(), "acme/app")
	if err != nil || len(got) != 1 || got[0].JobID != waiting.ID {
		t.Fatalf("ListQueuedJobs = %+v, %v; want only job %d", got, err, waiting.ID)
	}
}

// runListHook wraps the fake so a test can act after the first run list
// returns, and records the status query of each run list.
type runListHook struct {
	next       http.Handler
	mu         sync.Mutex
	statuses   []string
	afterFirst func()
}

func (h *runListHook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.next.ServeHTTP(w, r)
	if !strings.HasSuffix(r.URL.Path, "/actions/runs") {
		return
	}
	h.mu.Lock()
	h.statuses = append(h.statuses, r.URL.Query().Get("status"))
	fn := h.afterFirst
	h.afterFirst = nil
	h.mu.Unlock()
	if fn != nil {
		fn()
	}
}

func hooked(t *testing.T, e env, afterFirst func()) (*github.Client, *runListHook) {
	t.Helper()
	h := &runListHook{next: e.Fake.Handler(), afterFirst: afterFirst}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	e.URL = srv.URL
	return e.PATClient(t), h
}

func jobIDs(jobs []github.QueuedJob) []int64 {
	out := make([]int64, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, j.JobID)
	}
	return out
}

func queueRun(t *testing.T, e env, n int) []fakegithub.Job {
	t.Helper()
	first, err := e.Fake.QueueJob("acme/app", []string{"x"})
	if err != nil {
		t.Fatal(err)
	}
	out := []fakegithub.Job{first}
	for range n - 1 {
		j, err := e.Fake.QueueJobInRun(first.RunID, []string{"x"})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, j)
	}
	return out
}

func TestListQueuedJobsInProgressRun(t *testing.T) {
	e := newEnv(t)
	e.Fake.AddRepo("acme/app")
	run := queueRun(t, e, 2)
	if err := e.Fake.StartJob(run[0].ID); err != nil {
		t.Fatal(err)
	}
	got, err := e.PATClient(t).ListQueuedJobs(context.Background(), "acme/app")
	if err != nil {
		t.Fatalf("ListQueuedJobs: %v", err)
	}
	if len(got) != 1 || got[0].JobID != run[1].ID || got[0].RunID != run[0].RunID || !got[0].CreatedAt.Equal(startTime) {
		t.Fatalf("jobs = %+v, want only job %d created at %v", got, run[1].ID, startTime)
	}
}

func TestListQueuedJobsCreatedAt(t *testing.T) {
	e := newEnv(t)
	e.Fake.AddRepo("acme/app")
	queueRun(t, e, 1)
	e.Clock.Advance(5 * time.Minute)
	queueRun(t, e, 1)
	got, err := e.PATClient(t).ListQueuedJobs(context.Background(), "acme/app")
	if err != nil || len(got) != 2 {
		t.Fatalf("ListQueuedJobs = %+v, %v", got, err)
	}
	if !got[0].CreatedAt.Equal(startTime) || !got[1].CreatedAt.Equal(startTime.Add(5*time.Minute)) {
		t.Fatalf("CreatedAt = %v, %v", got[0].CreatedAt, got[1].CreatedAt)
	}
}

func TestListQueuedJobsListsQueuedRunsFirst(t *testing.T) {
	e := newEnv(t)
	e.Fake.AddRepo("acme/app")
	c, h := hooked(t, e, nil)
	if _, err := c.ListQueuedJobs(context.Background(), "acme/app"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(h.statuses, []string{"queued", "in_progress"}) {
		t.Fatalf("run list statuses = %v, want [queued in_progress]", h.statuses)
	}
}

func TestListQueuedJobsRunStartedBetweenLists(t *testing.T) {
	e := newEnv(t)
	e.Fake.AddRepo("acme/app")
	run := queueRun(t, e, 2)
	c, _ := hooked(t, e, func() {
		if err := e.Fake.StartJob(run[0].ID); err != nil {
			t.Error(err)
		}
	})
	got, err := c.ListQueuedJobs(context.Background(), "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	if ids := jobIDs(got); !slices.Equal(ids, []int64{run[1].ID}) {
		t.Fatalf("jobs = %v, want [%d]", ids, run[1].ID)
	}
}

func TestListQueuedJobsNoDuplicates(t *testing.T) {
	e := newEnv(t)
	e.Fake.AddRepo("acme/app")
	run := queueRun(t, e, 3)
	c, _ := hooked(t, e, func() {
		if err := e.Fake.StartJob(run[0].ID); err != nil {
			t.Error(err)
		}
	})
	got, err := c.ListQueuedJobs(context.Background(), "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	if ids := jobIDs(got); !slices.Equal(ids, []int64{run[1].ID, run[2].ID}) {
		t.Fatalf("jobs = %v, want each queued job of the run once", ids)
	}
}
