package poller_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/liquidmetal-dev/muster/internal/clock"
	"github.com/liquidmetal-dev/muster/internal/config"
	"github.com/liquidmetal-dev/muster/internal/demand"
	"github.com/liquidmetal-dev/muster/internal/github"
	"github.com/liquidmetal-dev/muster/internal/intake/poller"
)

var t0 = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

type listResult struct {
	jobs []github.QueuedJob
	err  error
}

type stubLister struct {
	mu      sync.Mutex
	results map[string]listResult
	onList  func(repo string)
	calls   []string
}

func (l *stubLister) ListQueuedJobs(ctx context.Context, repo string) ([]github.QueuedJob, error) {
	l.mu.Lock()
	l.calls = append(l.calls, repo)
	r, fn := l.results[repo], l.onList
	l.mu.Unlock()
	if fn != nil {
		fn(repo)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return r.jobs, r.err
}

func (l *stubLister) Calls() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.calls)
}

type polledCall struct {
	repos    []string
	listedAt time.Time
	jobs     []demand.Job
}

type recDemand struct{ ch chan polledCall }

func newRecDemand() *recDemand { return &recDemand{ch: make(chan polledCall, 64)} }

func (d *recDemand) Polled(repos []string, listedAt time.Time, jobs []demand.Job) {
	d.ch <- polledCall{repos: repos, listedAt: listedAt, jobs: jobs}
}

func (d *recDemand) next(t *testing.T) polledCall {
	t.Helper()
	select {
	case c := <-d.ch:
		return c
	case <-time.After(2 * time.Second):
		t.Fatal("no Polled call within 2s")
		return polledCall{}
	}
}

func (d *recDemand) none(t *testing.T) {
	t.Helper()
	select {
	case c := <-d.ch:
		t.Fatalf("unexpected Polled call %+v", c)
	default:
	}
}

type fixture struct {
	clock   *clock.Manual
	lister  *stubLister
	demand  *recDemand
	metrics *poller.Counters
	cancel  context.CancelFunc
	done    chan struct{}
}

func pollConfig(repos ...string) config.Poll {
	return config.Poll{Enabled: true, Interval: config.Duration(30 * time.Second), Repos: repos}
}

func newPoller(t *testing.T, poll config.Poll, f *fixture) *poller.Poller {
	t.Helper()
	p, err := poller.New(poller.Config{
		Poll:    poll,
		Lister:  f.lister,
		Demand:  f.demand,
		Clock:   f.clock,
		Logger:  slog.New(slog.DiscardHandler),
		Metrics: f.metrics,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func newFixture(results map[string]listResult) *fixture {
	return &fixture{
		clock:   clock.NewManual(t0),
		lister:  &stubLister{results: results},
		demand:  newRecDemand(),
		metrics: &poller.Counters{},
		done:    make(chan struct{}),
	}
}

// start runs the poller in the background and stops it at cleanup.
func (f *fixture) start(t *testing.T, poll config.Poll) {
	t.Helper()
	p := newPoller(t, poll, f)
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	go func() {
		defer close(f.done)
		p.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		f.wait(t)
	})
}

func run(t *testing.T, poll config.Poll, results map[string]listResult) *fixture {
	t.Helper()
	f := newFixture(results)
	f.start(t, poll)
	return f
}

func (f *fixture) wait(t *testing.T) {
	t.Helper()
	select {
	case <-f.done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s")
	}
}

func TestNewRequiresListerAndDemand(t *testing.T) {
	if _, err := poller.New(poller.Config{Demand: newRecDemand()}); err == nil {
		t.Error("New without a lister succeeded")
	}
	if _, err := poller.New(poller.Config{Lister: &stubLister{}}); err == nil {
		t.Error("New without a demand succeeded")
	}
}

func TestDisabledRunReturnsAtOnce(t *testing.T) {
	poll := pollConfig("acme/app")
	poll.Enabled = false
	f := run(t, poll, nil)
	f.wait(t)
	if calls := f.lister.Calls(); len(calls) != 0 {
		t.Fatalf("lister calls = %v, want none", calls)
	}
}

func TestRunPollsAtOnceThenEachInterval(t *testing.T) {
	f := run(t, pollConfig("acme/app"), nil)
	if c := f.demand.next(t); !c.listedAt.Equal(t0) {
		t.Fatalf("first pass listedAt = %v, want %v", c.listedAt, t0)
	}
	f.clock.Advance(29 * time.Second)
	f.clock.Advance(time.Second)
	if c := f.demand.next(t); !c.listedAt.Equal(t0.Add(30 * time.Second)) {
		t.Fatalf("second pass listedAt = %v, want %v", c.listedAt, t0.Add(30*time.Second))
	}
}

func TestSlowPassStartsNextAtOnce(t *testing.T) {
	f := newFixture(nil)
	first := true
	f.lister.onList = func(string) {
		if first {
			first = false
			f.clock.Advance(45 * time.Second)
		}
	}
	f.start(t, pollConfig("acme/app"))
	f.demand.next(t)
	if c := f.demand.next(t); !c.listedAt.Equal(t0.Add(45 * time.Second)) {
		t.Fatalf("pass after the slow one listedAt = %v, want %v (at once)", c.listedAt, t0.Add(45*time.Second))
	}
}

func TestZeroIntervalUsesDefault(t *testing.T) {
	poll := pollConfig("acme/app")
	poll.Interval = 0
	f := run(t, poll, nil)
	f.demand.next(t)
	f.clock.Advance(config.DefaultPollInterval - time.Second)
	f.clock.Advance(time.Second)
	if c := f.demand.next(t); !c.listedAt.Equal(t0.Add(config.DefaultPollInterval)) {
		t.Fatalf("second pass listedAt = %v, want %v", c.listedAt, t0.Add(config.DefaultPollInterval))
	}
}

func TestPassMergesEachRepoWithListedAtBeforeList(t *testing.T) {
	created := t0.Add(-time.Minute)
	f := newFixture(map[string]listResult{
		"acme/a": {jobs: []github.QueuedJob{{JobID: 1, RunID: 9, Labels: []string{"self-hosted"}, Repository: "acme/a", CreatedAt: created}}},
	})
	f.lister.onList = func(string) { f.clock.Advance(2 * time.Second) }
	f.start(t, pollConfig("acme/a", "acme/b"))

	a := f.demand.next(t)
	b := f.demand.next(t)
	f.cancel()
	f.wait(t)
	if !slices.Equal(a.repos, []string{"acme/a"}) || !a.listedAt.Equal(t0) {
		t.Fatalf("first merge = %v at %v, want [acme/a] at %v", a.repos, a.listedAt, t0)
	}
	if !slices.Equal(b.repos, []string{"acme/b"}) || !b.listedAt.Equal(t0.Add(2*time.Second)) {
		t.Fatalf("second merge = %v at %v, want [acme/b] at %v", b.repos, b.listedAt, t0.Add(2*time.Second))
	}
	want := demand.Job{ID: 1, Labels: []string{"self-hosted"}, Repo: "acme/a", QueuedAt: created}
	if len(a.jobs) != 1 || a.jobs[0].ID != want.ID || a.jobs[0].Repo != want.Repo ||
		!slices.Equal(a.jobs[0].Labels, want.Labels) || !a.jobs[0].QueuedAt.Equal(want.QueuedAt) {
		t.Fatalf("jobs = %+v, want [%+v]", a.jobs, want)
	}
	if len(b.jobs) != 0 {
		t.Fatalf("acme/b jobs = %+v, want none", b.jobs)
	}
	if got := f.metrics.Duration("acme/a"); got != 2*time.Second {
		t.Fatalf("duration(acme/a) = %v, want 2s", got)
	}
	if f.metrics.Count("acme/a", poller.OutcomeOK) != 1 || f.metrics.Count("acme/b", poller.OutcomeOK) != 1 {
		t.Fatal("ok outcomes not recorded for both repos")
	}
}

func TestPassSkipsFailedRepo(t *testing.T) {
	f := run(t, pollConfig("acme/a", "acme/b", "acme/c"), map[string]listResult{
		"acme/b": {err: errors.New("boom")},
	})
	a, c := f.demand.next(t), f.demand.next(t)
	if !slices.Equal(a.repos, []string{"acme/a"}) || !slices.Equal(c.repos, []string{"acme/c"}) {
		t.Fatalf("merged %v then %v, want acme/a then acme/c", a.repos, c.repos)
	}
	f.cancel()
	f.wait(t)
	f.demand.none(t)
	if f.metrics.Count("acme/b", poller.OutcomeError) != 1 || f.metrics.Count("acme/b", poller.OutcomeOK) != 0 {
		t.Fatal("acme/b not recorded as one error")
	}
	if f.metrics.Count("acme/a", poller.OutcomeOK) != 1 || f.metrics.Count("acme/c", poller.OutcomeOK) != 1 {
		t.Fatal("acme/a and acme/c not recorded as ok")
	}
}

func TestRateLimitStopsPass(t *testing.T) {
	f := run(t, pollConfig("acme/a", "acme/b", "acme/c"), map[string]listResult{
		"acme/b": {err: fmt.Errorf("list runs: %w", github.ErrRateLimited)},
	})
	if a := f.demand.next(t); !slices.Equal(a.repos, []string{"acme/a"}) {
		t.Fatalf("merged %v, want acme/a", a.repos)
	}
	f.clock.Advance(30 * time.Second)
	if a := f.demand.next(t); !slices.Equal(a.repos, []string{"acme/a"}) || !a.listedAt.Equal(t0.Add(30*time.Second)) {
		t.Fatalf("second pass merged %v at %v, want acme/a at t0+30s", a.repos, a.listedAt)
	}
	f.cancel()
	f.wait(t)
	if calls := f.lister.Calls(); slices.Contains(calls, "acme/c") {
		t.Fatalf("lister calls = %v, acme/c listed after a rate limit", calls)
	}
	if f.metrics.Count("acme/b", poller.OutcomeRateLimited) < 1 {
		t.Fatal("rate limit not recorded for acme/b")
	}
	for _, o := range []string{poller.OutcomeOK, poller.OutcomeError, poller.OutcomeRateLimited} {
		if f.metrics.Count("acme/c", o) != 0 {
			t.Fatalf("acme/c recorded as %s", o)
		}
	}
}

func TestRunStopsWhenCancelledMidPass(t *testing.T) {
	f := newFixture(nil)
	f.lister.onList = func(string) { f.cancel() }
	f.start(t, pollConfig("acme/a", "acme/b"))
	f.wait(t)
	f.demand.none(t)
	if calls := f.lister.Calls(); !slices.Equal(calls, []string{"acme/a"}) {
		t.Fatalf("lister calls = %v, want [acme/a]", calls)
	}
	for _, o := range []string{poller.OutcomeOK, poller.OutcomeError, poller.OutcomeRateLimited} {
		if f.metrics.Count("acme/a", o) != 0 {
			t.Fatalf("cancelled listing recorded as %s", o)
		}
	}
}

func TestPassUsesConfiguredRepoName(t *testing.T) {
	f := run(t, pollConfig("Acme/App"), map[string]listResult{
		"Acme/App": {jobs: []github.QueuedJob{{JobID: 1, Labels: []string{"x"}, Repository: "acme/app"}}},
	})
	c := f.demand.next(t)
	if !slices.Equal(c.repos, []string{"Acme/App"}) || len(c.jobs) != 1 || c.jobs[0].Repo != "Acme/App" {
		t.Fatalf("merge = %v %+v, want repo Acme/App on the call and the job", c.repos, c.jobs)
	}
}
