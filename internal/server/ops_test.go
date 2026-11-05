package server_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liquidmetal-dev/muster/internal/clock"
	"github.com/liquidmetal-dev/muster/internal/health"
	"github.com/liquidmetal-dev/muster/internal/server"
)

type flipProbe struct {
	mu  sync.Mutex
	err error
}

func (p *flipProbe) set(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = err
}

func (p *flipProbe) check(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func newOps(t *testing.T, probe *flipProbe, clk clock.Clock) (string, *health.Readiness) {
	t.Helper()
	ready := health.New(health.Config{Clock: clk, Probes: []health.Probe{{Name: "battery", Check: probe.check}}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); ready.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	s, err := server.NewOps(server.OpsConfig{Metrics: stub, Ready: ready.Handler()})
	if err != nil {
		t.Fatalf("NewOps: %v", err)
	}
	return serve(t, s), ready
}

func waitProbed(t *testing.T, r *health.Readiness, at time.Time) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, sts := r.Ready()
		if sts[2].At.Equal(at) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("probe did not run")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestOpsRoutes(t *testing.T) {
	addr, _ := newOps(t, &flipProbe{}, nil)
	base := "http://" + addr
	if code, body := do(t, http.DefaultClient, http.MethodGet, base+"/healthz"); code != http.StatusOK || body != "ok\n" {
		t.Fatalf("GET /healthz = %d %q, want 200 ok", code, body)
	}
	if code, body := do(t, http.DefaultClient, http.MethodGet, base+"/metrics"); code != http.StatusOK || body != "ok" {
		t.Fatalf("GET /metrics = %d %q, want the metrics handler", code, body)
	}
	if code, body := do(t, http.DefaultClient, http.MethodGet, base+"/readyz"); code != http.StatusServiceUnavailable || !strings.Contains(body, `"started"`) {
		t.Fatalf("GET /readyz = %d %q, want 503 naming started", code, body)
	}
	if code, _ := do(t, http.DefaultClient, http.MethodPost, base+"/webhook"); code != http.StatusNotFound {
		t.Fatalf("POST /webhook = %d, want 404", code)
	}
	if code, _ := do(t, http.DefaultClient, http.MethodPost, base+"/healthz"); code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /healthz = %d, want 405", code)
	}
}

func TestOpsReadyzFlips(t *testing.T) {
	t0 := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	clk := clock.NewManual(t0)
	p := &flipProbe{}
	addr, ready := newOps(t, p, clk)
	ready.MarkStarted()
	waitProbed(t, ready, t0)
	if code, _ := do(t, http.DefaultClient, http.MethodGet, "http://"+addr+"/readyz"); code != http.StatusOK {
		t.Fatalf("GET /readyz = %d, want 200", code)
	}
	p.set(errors.New("down"))
	clk.Advance(health.DefaultInterval)
	waitProbed(t, ready, t0.Add(health.DefaultInterval))
	if code, body := do(t, http.DefaultClient, http.MethodGet, "http://"+addr+"/readyz"); code != http.StatusServiceUnavailable || !strings.Contains(body, "down") {
		t.Fatalf("GET /readyz = %d %q, want 503 with the error", code, body)
	}
}

func TestNewOpsRequiresHandlers(t *testing.T) {
	if _, err := server.NewOps(server.OpsConfig{}); err == nil {
		t.Fatal("NewOps without handlers: got nil error")
	}
}
