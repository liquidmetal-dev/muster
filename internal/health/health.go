// Package health reports whether musterd is ready (requirement OBS-3).
// Readiness probes battery and GitHub in the background and serves the
// latest results, so a probe of /readyz never makes a GitHub API call
// (docs/adr/2026-11-05-ops-listener-and-readiness.md).
package health

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/liquidmetal-dev/muster/internal/clock"
)

// Defaults of Config.
const (
	DefaultInterval = 15 * time.Second
	DefaultTimeout  = 5 * time.Second
)

// CheckStarted is the check that passes once musterd marks its startup
// complete.
const CheckStarted = "started"

// CheckStopping is the check that fails once musterd begins shutdown.
const CheckStopping = "stopping"

const notProbed = "not probed yet"

// Probe is one readiness check, such as "battery" or "github".
type Probe struct {
	Name  string
	Check func(ctx context.Context) error
}

// Metrics receives each change of a check. *metrics.Metrics satisfies it.
type Metrics interface {
	SetReady(check string, ok bool)
}

// Config configures Readiness.
type Config struct {
	Probes []Probe
	// Interval is 0 for DefaultInterval.
	Interval time.Duration
	// Timeout bounds each probe. It is 0 for DefaultTimeout.
	Timeout time.Duration
	// Clock is nil for clock.Real().
	Clock clock.Clock
	// Logger is nil for slog.Default().
	Logger *slog.Logger
	// Metrics is optional.
	Metrics Metrics
}

// Status is the latest result of one check.
type Status struct {
	Name string    `json:"name"`
	OK   bool      `json:"ok"`
	Err  string    `json:"error,omitempty"`
	At   time.Time `json:"at,omitzero"`
}

// Readiness holds the latest result of each check.
type Readiness struct {
	cfg Config

	mu       sync.Mutex
	started  bool
	stopping bool
	statuses []Status
}

// New returns a Readiness that is not ready: it is not started and no
// probe has run.
func New(cfg Config) *Readiness {
	if cfg.Interval == 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.Clock == nil {
		cfg.Clock = clock.Real()
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	r := &Readiness{cfg: cfg, statuses: make([]Status, len(cfg.Probes))}
	r.setReady(CheckStarted, false)
	r.setReady(CheckStopping, true)
	for i, p := range cfg.Probes {
		r.statuses[i] = Status{Name: p.Name, Err: notProbed}
		r.setReady(p.Name, false)
	}
	return r
}

// Run probes at once and then each interval, until ctx ends.
func (r *Readiness) Run(ctx context.Context) {
	for {
		// Arm the timer before the probes, so a caller that has seen their
		// results knows the next tick is already scheduled.
		t := r.cfg.Clock.NewTimer(r.cfg.Interval)
		r.probeAll(ctx)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C():
		}
	}
}

func (r *Readiness) probeAll(ctx context.Context) {
	at := r.cfg.Clock.Now()
	var wg sync.WaitGroup
	for i, p := range r.cfg.Probes {
		wg.Go(func() {
			pctx, cancel := context.WithTimeout(ctx, r.cfg.Timeout)
			defer cancel()
			err := p.Check(pctx)
			if ctx.Err() != nil {
				return
			}
			r.record(i, at, err)
		})
	}
	wg.Wait()
}

func (r *Readiness) record(i int, at time.Time, err error) {
	st := Status{Name: r.cfg.Probes[i].Name, OK: err == nil, At: at}
	if err != nil {
		st.Err = err.Error()
	}
	r.mu.Lock()
	prev := r.statuses[i]
	r.statuses[i] = st
	r.mu.Unlock()
	changed := prev.OK != st.OK || prev.Err == notProbed
	if !changed {
		return
	}
	r.setReady(st.Name, st.OK)
	switch {
	case !st.OK:
		r.cfg.Logger.Warn("readiness check failed", "check", st.Name, "err", err)
	case prev.Err != notProbed:
		r.cfg.Logger.Info("readiness check passes again", "check", st.Name)
	}
}

// MarkStarted records that musterd has completed its startup.
func (r *Readiness) MarkStarted() {
	r.mu.Lock()
	was := r.started
	r.started = true
	r.mu.Unlock()
	if !was {
		r.setReady(CheckStarted, true)
	}
}

// MarkStopping records that musterd has begun shutdown. musterd is not
// ready from then on.
func (r *Readiness) MarkStopping() {
	r.mu.Lock()
	was := r.stopping
	r.stopping = true
	r.mu.Unlock()
	if !was {
		r.setReady(CheckStopping, false)
	}
}

// Ready reports whether every check passes, and the status of each check,
// started first, then stopping.
func (r *Readiness) Ready() (bool, []Status) {
	r.mu.Lock()
	defer r.mu.Unlock()
	started := Status{Name: CheckStarted, OK: r.started}
	if !r.started {
		started.Err = "startup is not complete"
	}
	stopping := Status{Name: CheckStopping, OK: !r.stopping}
	if r.stopping {
		stopping.Err = "shutdown has begun"
	}
	out := append([]Status{started, stopping}, r.statuses...)
	ready := true
	for _, s := range out {
		ready = ready && s.OK
	}
	return ready, out
}

// Handler serves /readyz: 200 when ready and 503 when not, with a JSON body
// that names each check.
func (r *Readiness) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		ready, checks := r.Ready()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if !ready {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(struct {
			Ready  bool     `json:"ready"`
			Checks []Status `json:"checks"`
		}{ready, checks})
	})
}

func (r *Readiness) setReady(check string, ok bool) {
	if r.cfg.Metrics != nil {
		r.cfg.Metrics.SetReady(check, ok)
	}
}
