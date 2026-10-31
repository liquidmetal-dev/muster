package dispatch

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/liquidmetal-dev/muster/internal/config"
)

type fakeDemand map[string]int

func (f fakeDemand) Desired(pool string) int { return f[pool] }

type fakeSessions struct {
	mu      sync.Mutex
	active  map[string]int
	notBusy map[string]int
	failOn  map[string]int
	calls   map[string]int
	created []string
	before  func()
}

func newFakeSessions() *fakeSessions {
	return &fakeSessions{
		active:  map[string]int{},
		notBusy: map[string]int{},
		failOn:  map[string]int{},
		calls:   map[string]int{},
	}
}

func (f *fakeSessions) Counts(pool string) (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active[pool], f.notBusy[pool]
}

func (f *fakeSessions) Create(_ context.Context, pool string) error {
	if f.before != nil {
		f.before()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[pool]++
	if f.calls[pool] == f.failOn[pool] {
		return errors.New("no capacity")
	}
	f.active[pool]++
	f.notBusy[pool]++
	f.created = append(f.created, pool)
	return nil
}

func (f *fakeSessions) createdCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.created)
}

func newTestDispatcher(pools []config.Pool, d fakeDemand, s *fakeSessions) *Dispatcher {
	return New(Config{Pools: pools, Demand: d, Sessions: s})
}

func TestPassSizing(t *testing.T) {
	tests := []struct {
		name                         string
		desired, active, notBusy, mx int
		want                         int
	}{
		{"empty", 0, 0, 0, 4, 0},
		{"fill", 3, 0, 0, 4, 3},
		{"busy excluded", 3, 4, 1, 10, 2},
		{"max_concurrent cap", 5, 0, 0, 2, 2},
		{"cap counts busy", 5, 1, 0, 2, 1},
		{"busy fill the cap", 5, 3, 0, 2, 0},
		{"surplus not-busy", 1, 3, 3, 10, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newFakeSessions()
			s.active["p"], s.notBusy["p"] = tt.active, tt.notBusy
			d := newTestDispatcher([]config.Pool{{Name: "p", MaxConcurrent: tt.mx}}, fakeDemand{"p": tt.desired}, s)
			d.pass(context.Background())
			if got := s.createdCount(); got != tt.want {
				t.Fatalf("created %d sessions, want %d", got, tt.want)
			}
		})
	}
}

func TestPassWalksPoolsInOrder(t *testing.T) {
	s := newFakeSessions()
	pools := []config.Pool{{Name: "x", MaxConcurrent: 2}, {Name: "y", MaxConcurrent: 2}}
	newTestDispatcher(pools, fakeDemand{"x": 1, "y": 1}, s).pass(context.Background())
	if want := []string{"x", "y"}; !slices.Equal(s.created, want) {
		t.Fatalf("created %v, want %v", s.created, want)
	}
}

func TestPassDrainHoldsCreation(t *testing.T) {
	s := newFakeSessions()
	d := newTestDispatcher([]config.Pool{{Name: "p", MaxConcurrent: 4}}, fakeDemand{"p": 2}, s)
	d.Drain()
	d.pass(context.Background())
	if got := s.createdCount(); got != 0 {
		t.Fatalf("created %d sessions while drained, want 0", got)
	}
	if !d.Draining() {
		t.Fatal("Draining() = false after Drain")
	}
	d.Undrain()
	d.pass(context.Background())
	if got := s.createdCount(); got != 2 {
		t.Fatalf("created %d sessions after Undrain, want 2", got)
	}
}

func TestDrainWaitsForInFlightCreate(t *testing.T) {
	s := newFakeSessions()
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	s.before = func() {
		once.Do(func() {
			close(started)
			<-release
		})
	}
	d := newTestDispatcher([]config.Pool{{Name: "p", MaxConcurrent: 4}}, fakeDemand{"p": 2}, s)

	passDone := make(chan struct{})
	go func() {
		d.pass(context.Background())
		close(passDone)
	}()
	<-started
	drained := make(chan struct{})
	go func() {
		d.Drain()
		close(drained)
	}()
	select {
	case <-drained:
		t.Fatal("Drain returned while a Create was in flight")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	<-drained
	n := s.createdCount()
	<-passDone
	if got := s.createdCount(); got != n || n > 2 {
		t.Fatalf("created %d sessions when Drain returned and %d after the pass; want no change and at most 2", n, got)
	}
}

func TestPassCreateErrorSkipsRestOfPool(t *testing.T) {
	s := newFakeSessions()
	s.failOn["x"] = 2
	pools := []config.Pool{{Name: "x", MaxConcurrent: 4}, {Name: "y", MaxConcurrent: 4}}
	newTestDispatcher(pools, fakeDemand{"x": 3, "y": 1}, s).pass(context.Background())
	if s.calls["x"] != 2 || s.calls["y"] != 1 {
		t.Fatalf("Create calls x=%d y=%d, want x=2 y=1", s.calls["x"], s.calls["y"])
	}
	if want := []string{"x", "y"}; !slices.Equal(s.created, want) {
		t.Fatalf("created %v, want %v", s.created, want)
	}
}

func TestNewDefaults(t *testing.T) {
	d := New(Config{})
	if d.interval != DefaultInterval {
		t.Fatalf("interval = %s, want %s", d.interval, DefaultInterval)
	}
	if d.clock == nil || d.logger == nil {
		t.Fatal("New left the clock or logger nil")
	}
}
