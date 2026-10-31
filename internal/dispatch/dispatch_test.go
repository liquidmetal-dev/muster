package dispatch_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/liquidmetal-dev/muster/internal/clock"
	"github.com/liquidmetal-dev/muster/internal/config"
	"github.com/liquidmetal-dev/muster/internal/demand"
	"github.com/liquidmetal-dev/muster/internal/dispatch"
)

type demandStub struct {
	mu sync.Mutex
	n  map[string]int
}

func (d *demandStub) Desired(pool string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.n[pool]
}

func (d *demandStub) set(pool string, n int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.n[pool] = n
}

type chanSessions struct {
	mu      sync.Mutex
	active  map[string]int
	created chan string
	counted chan struct{}
}

func newChanSessions() *chanSessions {
	return &chanSessions{
		active:  map[string]int{},
		created: make(chan string, 16),
		counted: make(chan struct{}, 16),
	}
}

func (s *chanSessions) Counts(pool string) (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case s.counted <- struct{}{}:
	default:
	}
	return s.active[pool], s.active[pool]
}

func (s *chanSessions) Create(_ context.Context, pool string) error {
	s.mu.Lock()
	s.active[pool]++
	s.mu.Unlock()
	s.created <- pool
	return nil
}

func waitCreate(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case pool := <-ch:
		return pool
	case <-time.After(2 * time.Second):
		t.Fatal("no session created within 2s")
		return ""
	}
}

func noCreate(t *testing.T, ch <-chan string) {
	t.Helper()
	select {
	case pool := <-ch:
		t.Fatalf("unexpected session created for pool %q", pool)
	case <-time.After(100 * time.Millisecond):
	}
}

var pool = config.Pool{Name: "p", MaxConcurrent: 4}

func startRun(t *testing.T, cfg dispatch.Config) (*dispatch.Dispatcher, context.CancelFunc, <-chan struct{}) {
	t.Helper()
	d := dispatch.New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		d.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return d, cancel, done
}

func TestRunPassesOnStart(t *testing.T) {
	s := newChanSessions()
	startRun(t, dispatch.Config{Pools: []config.Pool{pool}, Demand: &demandStub{n: map[string]int{"p": 1}}, Sessions: s, Clock: clock.NewManual(time.Now())})
	if got := waitCreate(t, s.created); got != "p" {
		t.Fatalf("created for %q, want p", got)
	}
}

func TestRunWakes(t *testing.T) {
	s := newChanSessions()
	dem := &demandStub{n: map[string]int{}}
	d, _, _ := startRun(t, dispatch.Config{Pools: []config.Pool{pool}, Demand: dem, Sessions: s, Clock: clock.NewManual(time.Now())})
	<-s.counted
	dem.set("p", 1)
	d.Wake()
	waitCreate(t, s.created)
}

func TestRunTimer(t *testing.T) {
	s := newChanSessions()
	dem := &demandStub{n: map[string]int{}}
	clk := clock.NewManual(time.Now())
	startRun(t, dispatch.Config{Pools: []config.Pool{pool}, Demand: dem, Sessions: s, Clock: clk})
	<-s.counted
	dem.set("p", 1)
	clk.Advance(dispatch.DefaultInterval)
	waitCreate(t, s.created)
}

func TestRunStopsOnCancel(t *testing.T) {
	s := newChanSessions()
	_, cancel, done := startRun(t, dispatch.Config{Pools: []config.Pool{pool}, Demand: &demandStub{n: map[string]int{}}, Sessions: s, Clock: clock.NewManual(time.Now())})
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s of cancel")
	}
}

func TestRunWithRegistryScopeExclusion(t *testing.T) {
	labels := []string{"self-hosted", "muster"}
	pools := []config.Pool{
		{Name: "a", Scope: config.Scope{Repo: "acme/a"}, Labels: labels, MaxConcurrent: 4},
		{Name: "b", Scope: config.Scope{Repo: "acme/b"}, Labels: labels, MaxConcurrent: 1},
	}
	clk := clock.NewManual(time.Now())
	reg := demand.New(demand.Config{Pools: pools, Clock: clk})
	s := newChanSessions()
	d, _, _ := startRun(t, dispatch.Config{Pools: pools, Demand: reg, Sessions: s, Clock: clk})
	reg.SetOnChange(d.Wake)

	reg.Queued(demand.Job{ID: 1, Labels: labels, Repo: "acme/b"})
	if got := waitCreate(t, s.created); got != "b" {
		t.Fatalf("created for %q, want b", got)
	}
	d.Wake()
	noCreate(t, s.created)
}
