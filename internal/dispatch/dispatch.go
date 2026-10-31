package dispatch

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/liquidmetal-dev/muster/internal/clock"
	"github.com/liquidmetal-dev/muster/internal/config"
)

// DefaultInterval is the time between timer passes when Config.Interval is
// zero.
const DefaultInterval = 10 * time.Second

// Demand reports the queued jobs that match a pool.
type Demand interface {
	Desired(pool string) int
}

// Sessions is the session store the dispatcher sizes.
type Sessions interface {
	// Counts returns the sessions in PENDING, CLAIMING, STARTING or RUNNING
	// (active), and how many of them have a runner not yet seen busy.
	Counts(pool string) (active, activeNotBusy int)
	// Create records a PENDING session and returns without waiting for a
	// VM; Counts includes the new session on return. Create must not call
	// Dispatcher.Drain or Undrain.
	Create(ctx context.Context, pool string) error
}

// Config configures a Dispatcher.
type Config struct {
	// Pools in configuration order.
	Pools    []config.Pool
	Demand   Demand
	Sessions Sessions
	// Clock is nil for clock.Real().
	Clock clock.Clock
	// Logger is nil for slog.Default().
	Logger *slog.Logger
	// Interval is zero for DefaultInterval.
	Interval time.Duration
}

// Dispatcher creates sessions to match demand.
type Dispatcher struct {
	pools    []config.Pool
	demand   Demand
	sessions Sessions
	clock    clock.Clock
	logger   *slog.Logger
	interval time.Duration
	wakeCh   chan struct{}

	// createMu guards draining and is held across each Create, so that
	// once Drain returns no Create is in flight or can start.
	createMu sync.Mutex
	draining bool
}

// New returns a Dispatcher that is not drained.
func New(cfg Config) *Dispatcher {
	if cfg.Clock == nil {
		cfg.Clock = clock.Real()
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Interval == 0 {
		cfg.Interval = DefaultInterval
	}
	return &Dispatcher{
		pools:    cfg.Pools,
		demand:   cfg.Demand,
		sessions: cfg.Sessions,
		clock:    cfg.Clock,
		logger:   cfg.Logger,
		interval: cfg.Interval,
		wakeCh:   make(chan struct{}, 1),
	}
}

// Drain stops session creation. It returns after any Create in flight.
func (d *Dispatcher) Drain() {
	d.createMu.Lock()
	d.draining = true
	d.createMu.Unlock()
}

// Undrain resumes session creation and wakes the loop.
func (d *Dispatcher) Undrain() {
	d.createMu.Lock()
	d.draining = false
	d.createMu.Unlock()
	d.Wake()
}

// Draining reports whether session creation is stopped.
func (d *Dispatcher) Draining() bool {
	d.createMu.Lock()
	defer d.createMu.Unlock()
	return d.draining
}

// Wake asks the loop for a pass. It never blocks.
func (d *Dispatcher) Wake() {
	select {
	case d.wakeCh <- struct{}{}:
	default:
	}
}

// Run makes one pass at once and then one on each Wake and each Interval,
// until ctx is done. Passes never overlap.
func (d *Dispatcher) Run(ctx context.Context) {
	for {
		// Arm the timer before the pass, so a caller that has seen the
		// pass's effect knows the next tick is already scheduled.
		t := d.clock.NewTimer(d.interval)
		d.pass(ctx)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-d.wakeCh:
		case <-t.C():
		}
		t.Stop()
	}
}

func (d *Dispatcher) pass(ctx context.Context) {
	for _, p := range d.pools {
		desired := d.demand.Desired(p.Name)
		active, notBusy := d.sessions.Counts(p.Name)
		need, room := desired-notBusy, p.MaxConcurrent-active
		if need > room {
			d.logger.Debug("dispatch: max_concurrent reached", "pool", p.Name, "desired", desired, "active", active)
		}
		for range min(need, room) {
			attempted, err := d.create(ctx, p.Name)
			if !attempted {
				return
			}
			if err != nil {
				d.logger.Warn("dispatch: create session failed", "pool", p.Name, "err", err)
				break
			}
		}
	}
}

// create calls Sessions.Create unless the dispatcher is drained. It
// reports false when drained.
func (d *Dispatcher) create(ctx context.Context, pool string) (bool, error) {
	d.createMu.Lock()
	defer d.createMu.Unlock()
	if d.draining {
		return false, nil
	}
	return true, d.sessions.Create(ctx, pool)
}
