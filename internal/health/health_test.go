package health_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liquidmetal-dev/muster/internal/clock"
	"github.com/liquidmetal-dev/muster/internal/health"
)

var t0 = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

// switchProbe fails while err is set.
type switchProbe struct {
	mu    sync.Mutex
	err   error
	calls atomic.Int32
}

func (p *switchProbe) set(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = err
}

func (p *switchProbe) check(context.Context) error {
	p.calls.Add(1)
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

type readyBody struct {
	Ready  bool `json:"ready"`
	Checks []struct {
		Name  string `json:"name"`
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	} `json:"checks"`
}

func readyz(t *testing.T, r *health.Readiness) (int, readyBody) {
	t.Helper()
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	var b readyBody
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	return rec.Code, b
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(time.Millisecond)
	}
}

// probedAt waits until every probe has a result from time at.
func probedAt(t *testing.T, r *health.Readiness, at time.Time) {
	t.Helper()
	waitFor(t, func() bool {
		_, sts := r.Ready()
		for _, s := range sts {
			if s.Name != health.CheckStarted && s.Name != health.CheckStopping && !s.At.Equal(at) {
				return false
			}
		}
		return true
	})
}

type recorder struct {
	mu  sync.Mutex
	set map[string][]bool
}

func (m *recorder) SetReady(check string, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.set == nil {
		m.set = map[string][]bool{}
	}
	m.set[check] = append(m.set[check], ok)
}

func (m *recorder) get(check string) []bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]bool(nil), m.set[check]...)
}

func run(t *testing.T, cfg health.Config) *health.Readiness {
	t.Helper()
	r := health.New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return r
}

func TestNotProbedYet(t *testing.T) {
	p := &switchProbe{}
	r := health.New(health.Config{Probes: []health.Probe{{Name: "battery", Check: p.check}}})
	r.MarkStarted()
	code, b := readyz(t, r)
	if code != http.StatusServiceUnavailable || b.Ready {
		t.Fatalf("readyz = %d %+v, want 503", code, b)
	}
	if b.Checks[2].Name != "battery" || b.Checks[2].Error != "not probed yet" {
		t.Fatalf("checks = %+v", b.Checks)
	}
}

func TestNotReadyUntilStarted(t *testing.T) {
	clk := clock.NewManual(t0)
	b, g := &switchProbe{}, &switchProbe{}
	r := run(t, health.Config{Clock: clk, Probes: []health.Probe{{Name: "battery", Check: b.check}, {Name: "github", Check: g.check}}})
	probedAt(t, r, t0)
	code, body := readyz(t, r)
	if code != http.StatusServiceUnavailable || body.Checks[0].Name != health.CheckStarted || body.Checks[0].OK {
		t.Fatalf("before MarkStarted: %d %+v, want 503 with started not ok", code, body)
	}
	r.MarkStarted()
	if code, body := readyz(t, r); code != http.StatusOK || !body.Ready {
		t.Fatalf("after MarkStarted: %d %+v, want 200", code, body)
	}
}

func TestFlipsOnFirstFailureAndBack(t *testing.T) {
	clk := clock.NewManual(t0)
	p := &switchProbe{}
	r := run(t, health.Config{Clock: clk, Probes: []health.Probe{{Name: "battery", Check: p.check}}})
	r.MarkStarted()
	probedAt(t, r, t0)
	if code, _ := readyz(t, r); code != http.StatusOK {
		t.Fatalf("readyz = %d, want 200", code)
	}
	p.set(errors.New("battery down"))
	clk.Advance(health.DefaultInterval)
	probedAt(t, r, t0.Add(health.DefaultInterval))
	code, body := readyz(t, r)
	if code != http.StatusServiceUnavailable || body.Checks[2].Error != "battery down" {
		t.Fatalf("after failure: %d %+v, want 503 with the error", code, body)
	}
	p.set(nil)
	clk.Advance(health.DefaultInterval)
	probedAt(t, r, t0.Add(2*health.DefaultInterval))
	if code, _ := readyz(t, r); code != http.StatusOK {
		t.Fatalf("after recovery: %d, want 200", code)
	}
}

func TestProbeTimeoutCountsAsFailure(t *testing.T) {
	clk := clock.NewManual(t0)
	block := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	r := run(t, health.Config{Clock: clk, Timeout: 50 * time.Millisecond, Probes: []health.Probe{{Name: "battery", Check: block}}})
	probedAt(t, r, t0)
	_, sts := r.Ready()
	if sts[2].OK || !strings.Contains(sts[2].Err, "context deadline exceeded") {
		t.Fatalf("status = %+v, want a deadline failure", sts[2])
	}
}

func TestSetReadyCalledOnChange(t *testing.T) {
	clk := clock.NewManual(t0)
	p := &switchProbe{}
	m := &recorder{}
	r := run(t, health.Config{Clock: clk, Metrics: m, Probes: []health.Probe{{Name: "battery", Check: p.check}}})
	probedAt(t, r, t0)
	r.MarkStarted()
	clk.Advance(health.DefaultInterval)
	probedAt(t, r, t0.Add(health.DefaultInterval))
	p.set(errors.New("x"))
	clk.Advance(health.DefaultInterval)
	probedAt(t, r, t0.Add(2*health.DefaultInterval))
	if got := m.get("battery"); len(got) != 3 || got[0] || !got[1] || got[2] {
		t.Fatalf("battery SetReady = %v, want [false true false]", got)
	}
	if got := m.get(health.CheckStarted); len(got) != 2 || got[0] || !got[1] {
		t.Fatalf("started SetReady = %v, want [false true]", got)
	}
}

func TestMarkStoppingMakesNotReady(t *testing.T) {
	clk := clock.NewManual(t0)
	p := &switchProbe{}
	m := &recorder{}
	r := run(t, health.Config{Clock: clk, Metrics: m, Probes: []health.Probe{{Name: "battery", Check: p.check}}})
	probedAt(t, r, t0)
	r.MarkStarted()
	ready, sts := r.Ready()
	if !ready || sts[1] != (health.Status{Name: health.CheckStopping, OK: true}) {
		t.Fatalf("before MarkStopping: ready=%v statuses=%+v, want ready with stopping ok", ready, sts)
	}
	r.MarkStopping()
	ready, sts = r.Ready()
	if ready || sts[1] != (health.Status{Name: health.CheckStopping, Err: "shutdown has begun"}) {
		t.Fatalf("after MarkStopping: ready=%v statuses=%+v, want not ready with stopping failed", ready, sts)
	}
	if code, _ := readyz(t, r); code != http.StatusServiceUnavailable {
		t.Fatalf("readyz = %d, want 503", code)
	}
	r.MarkStopping()
	if got := m.get(health.CheckStopping); len(got) != 2 || !got[0] || got[1] {
		t.Fatalf("stopping SetReady = %v, want [true false]", got)
	}
}
