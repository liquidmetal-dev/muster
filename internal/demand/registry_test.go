package demand_test

import (
	"testing"
	"time"

	"github.com/liquidmetal-dev/muster/internal/clock"
	"github.com/liquidmetal-dev/muster/internal/demand"
)

var start = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func newRegistry(t *testing.T) (*demand.Registry, *clock.Manual, *int) {
	t.Helper()
	clk := clock.NewManual(start)
	r := demand.New(demand.Config{Pools: testPools(), Clock: clk})
	changes := new(int)
	r.SetOnChange(func() { *changes++ })
	return r, clk, changes
}

func job(id int64, repo string) demand.Job {
	return demand.Job{ID: id, Labels: []string{"self-hosted", "muster"}, Repo: repo}
}

func wantDesired(t *testing.T, r *demand.Registry, pool string, want int) {
	t.Helper()
	if got := r.Desired(pool); got != want {
		t.Fatalf("Desired(%q) = %d, want %d", pool, got, want)
	}
}

func TestQueuedCountsOncePerPool(t *testing.T) {
	r, clk, _ := newRegistry(t)
	r.Queued(job(1, "acme/a"))
	r.Queued(job(1, "acme/a"))
	r.Polled([]string{"acme/a"}, clk.Now(), []demand.Job{job(1, "acme/a")})
	wantDesired(t, r, "a", 1)
	if got := r.Total(); got != 1 {
		t.Fatalf("Total() = %d, want 1", got)
	}
}

func TestQueuedOutOfScopeIgnored(t *testing.T) {
	r, _, changes := newRegistry(t)
	r.Queued(job(1, "stranger/x"))
	for _, p := range testPools() {
		wantDesired(t, r, p.Name, 0)
	}
	if got := r.Total(); got != 0 {
		t.Fatalf("Total() = %d, want 0", got)
	}
	if *changes != 0 {
		t.Fatalf("OnChange called %d times, want 0", *changes)
	}
}

func TestStartedAndCompletedClose(t *testing.T) {
	r, _, changes := newRegistry(t)
	r.Queued(job(1, "acme/a"))
	r.Queued(job(2, "acme/a"))
	r.Started(1)
	r.Completed(2)
	wantDesired(t, r, "a", 0)
	if *changes != 4 {
		t.Fatalf("OnChange called %d times, want 4", *changes)
	}
}

func TestStartedBeforeQueuedIsIgnored(t *testing.T) {
	r, _, _ := newRegistry(t)
	r.Started(7)
	r.Queued(job(7, "acme/a"))
	wantDesired(t, r, "a", 0)
}

func TestPolledClosesMissing(t *testing.T) {
	r, clk, _ := newRegistry(t)
	r.Queued(job(1, "acme/a"))
	clk.Advance(time.Second)
	r.Polled([]string{"acme/a"}, clk.Now(), nil)
	wantDesired(t, r, "a", 0)
}

func TestPolledClosesOnlyCoveredRepos(t *testing.T) {
	r, clk, _ := newRegistry(t)
	r.Queued(job(1, "acme/a"))
	r.Queued(job(2, "other/b"))
	clk.Advance(time.Second)
	r.Polled([]string{"acme/a"}, clk.Now(), nil)
	wantDesired(t, r, "a", 0)
	wantDesired(t, r, "b", 1)
}

func TestPolledKeepsJobsSeenAfterListing(t *testing.T) {
	r, clk, _ := newRegistry(t)
	listedAt := clk.Now()
	clk.Advance(time.Second)
	r.Queued(job(1, "acme/a"))
	r.Polled([]string{"acme/a"}, listedAt, nil)
	wantDesired(t, r, "a", 1)
}

func TestPolledKeepsJobsReconfirmedAfterListing(t *testing.T) {
	r, clk, _ := newRegistry(t)
	r.Queued(job(1, "acme/a"))
	clk.Advance(time.Second)
	listedAt := clk.Now()
	clk.Advance(time.Second)
	r.Queued(job(1, "acme/a"))
	r.Polled([]string{"acme/a"}, listedAt, nil)
	wantDesired(t, r, "a", 1)
}

func TestPolledDoesNotReopenClosed(t *testing.T) {
	r, clk, _ := newRegistry(t)
	r.Queued(job(1, "acme/a"))
	r.Completed(1)
	r.Polled([]string{"acme/a"}, clk.Now(), []demand.Job{job(1, "acme/a")})
	wantDesired(t, r, "a", 0)
}

func TestExpiresAfterMaxAge(t *testing.T) {
	r, clk, _ := newRegistry(t)
	old := job(1, "acme/a")
	old.QueuedAt = clk.Now().Add(-23 * time.Hour)
	r.Queued(old)
	clk.Advance(59 * time.Minute)
	wantDesired(t, r, "a", 1)
	clk.Advance(time.Minute)
	wantDesired(t, r, "a", 0)

	r.Queued(job(2, "acme/a"))
	clk.Advance(demand.MaxAge - time.Second)
	wantDesired(t, r, "a", 1)
	clk.Advance(time.Second)
	wantDesired(t, r, "a", 0)
}

func TestTombstoneExpires(t *testing.T) {
	r, clk, _ := newRegistry(t)
	r.Completed(1)
	clk.Advance(demand.MaxAge)
	r.Queued(job(1, "acme/a"))
	wantDesired(t, r, "a", 1)
}
