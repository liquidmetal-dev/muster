package github_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
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
	if n := e.Fake.RequestCount(fakegithub.PatternRuns); n != 2 {
		t.Fatalf("run list requests = %d, want 2", n)
	}
}

// TestListQueuedJobsJobPages uses an httptest stub because the fake returns
// one job per run without a Link header.
func TestListQueuedJobsJobPages(t *testing.T) {
	var jobPages atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v3/repos/acme/app/actions/runs", func(w http.ResponseWriter, _ *http.Request) {
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
