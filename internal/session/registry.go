package session

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/liquidmetal-dev/muster/internal/clock"
	"github.com/liquidmetal-dev/muster/internal/config"
	"github.com/liquidmetal-dev/muster/internal/dispatch"
	"github.com/liquidmetal-dev/muster/internal/github"
	"github.com/liquidmetal-dev/muster/internal/vmsource"
)

// DefaultGrace is how long a session waits after the job on its runner
// completes before it ends.
const DefaultGrace = 30 * time.Second

var (
	// ErrNotRunning is returned before Run starts and after it stops.
	ErrNotRunning = errors.New("session: registry is not running")
	// ErrUnknownPool is returned for a pool that is not configured.
	ErrUnknownPool = errors.New("session: unknown pool")
)

// GitHub is the part of the GitHub client that sessions use.
// *github.Client satisfies it.
type GitHub interface {
	GenerateJITConfig(ctx context.Context, s github.Scope, r github.JITRequest) (github.JITConfig, error)
	ListRunners(ctx context.Context, s github.Scope, prefix string) ([]github.Runner, error)
	DeleteRunner(ctx context.Context, s github.Scope, id int64) error
	RunnerGroupID(ctx context.Context, s github.Scope, name string) (int64, error)
}

// Demand reports the queued jobs for a pool.
type Demand interface {
	Desired(pool string) int
}

// Config configures a Registry.
type Config struct {
	// Pools come from config.Parse, so MaxSessionDuration and WorkFolder
	// are set.
	Pools     []config.Pool
	Namespace string
	// Defaults come from config.Parse.
	Defaults config.Defaults
	// PollInterval is the runner poll interval. Zero means
	// config.DefaultPollInterval.
	PollInterval time.Duration
	// Grace is the wait after a job completes. Zero means DefaultGrace.
	Grace  time.Duration
	Source vmsource.Source
	GitHub GitHub
	Demand Demand
	// Clock is nil for clock.Real().
	Clock clock.Clock
	// Logger is nil for slog.Default().
	Logger *slog.Logger
	// Metrics is nil for NopMetrics{}.
	Metrics Metrics
	// OnEnd, when set, is called once per session after cleanup and
	// before the session leaves the registry. Info.State is the outcome,
	// Ended or Failed, never Released.
	OnEnd func(Info)
}

// Info is a snapshot of one session.
type Info struct {
	ID         string
	Pool       string
	RunnerName string
	VMID       string
	State      State
	Reason     Reason
	Busy       bool
	ClaimedAt  time.Time
}

type pool struct {
	cfg   config.Pool
	scope github.Scope
}

// Registry holds the live sessions and runs them.
type Registry struct {
	cfg    Config
	clock  clock.Clock
	log    *slog.Logger
	pools  map[string]*pool
	random io.Reader

	mu       sync.Mutex
	sessions map[string]*session
	groupIDs map[string]int64
	runCtx   context.Context
	stopped  bool
	changed  chan struct{}
	wg       sync.WaitGroup
}

var _ dispatch.Sessions = (*Registry)(nil)

// New validates cfg and returns a Registry. Call Run before Create.
func New(cfg Config) (*Registry, error) {
	if cfg.Source == nil || cfg.GitHub == nil || cfg.Demand == nil {
		return nil, errors.New("session: Source, GitHub and Demand are required")
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = config.DefaultPollInterval
	}
	if cfg.Grace == 0 {
		cfg.Grace = DefaultGrace
	}
	if cfg.Clock == nil {
		cfg.Clock = clock.Real()
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Metrics == nil {
		cfg.Metrics = NopMetrics{}
	}
	r := &Registry{
		cfg:      cfg,
		clock:    cfg.Clock,
		log:      cfg.Logger,
		pools:    make(map[string]*pool, len(cfg.Pools)),
		random:   rand.Reader,
		sessions: map[string]*session{},
		groupIDs: map[string]int64{},
		changed:  make(chan struct{}),
	}
	for _, p := range cfg.Pools {
		scope, err := github.ScopeFromConfig(p.Scope)
		if err != nil {
			return nil, fmt.Errorf("session: pool %s: %w", p.Name, err)
		}
		r.pools[p.Name] = &pool{cfg: p, scope: scope}
	}
	return r, nil
}

// Run serves sessions until ctx ends, then waits for every session
// goroutine to return. Sessions stop without releasing their VM or
// deleting their runner, so the next start can adopt them.
func (r *Registry) Run(ctx context.Context) error {
	r.mu.Lock()
	if r.runCtx != nil {
		r.mu.Unlock()
		return errors.New("session: Run called twice")
	}
	r.runCtx = ctx
	r.wg.Add(1 + len(r.pools))
	r.mu.Unlock()
	for name := range r.pools {
		go func() {
			defer r.wg.Done()
			r.watchEvents(ctx, name)
		}()
	}
	go func() {
		defer r.wg.Done()
		r.pollRunners(ctx)
	}()

	<-ctx.Done()
	r.mu.Lock()
	r.stopped = true
	r.mu.Unlock()
	r.wg.Wait()
	return nil
}

// Create records a PENDING session for pool and starts it. It does not
// wait for a VM.
func (r *Registry) Create(_ context.Context, poolName string) error {
	p, ok := r.pools[poolName]
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runCtx == nil || r.stopped {
		return ErrNotRunning
	}
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownPool, poolName)
	}
	id, err := newID(r.random)
	if err != nil {
		return err
	}
	s := newSession(id, p, runnerName(r.cfg.Namespace, p.cfg.Name, id))
	s.createdAt = r.clock.Now()
	r.sessions[id] = s
	r.wg.Add(1)
	go r.runSession(r.runCtx, s)
	return nil
}

// WaitNotBusy returns once no busy session is left in the registry, or
// with ctx.Err() when ctx ends first. A busy session counts until its
// cleanup has finished.
func (r *Registry) WaitNotBusy(ctx context.Context) error {
	for {
		r.mu.Lock()
		busy := 0
		for _, s := range r.sessions {
			if s.busy {
				busy++
			}
		}
		ch := r.changed
		r.mu.Unlock()
		if busy == 0 {
			return nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (r *Registry) signalLocked() {
	close(r.changed)
	r.changed = make(chan struct{})
}

// Counts implements dispatch.Sessions.
func (r *Registry) Counts(poolName string) (active, activeNotBusy int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.countsLocked(poolName)
}

func (r *Registry) countsLocked(poolName string) (active, activeNotBusy int) {
	for _, s := range r.sessions {
		if s.pool.cfg.Name != poolName || !s.state.Active() {
			continue
		}
		active++
		if !s.busy {
			activeNotBusy++
		}
	}
	return active, activeNotBusy
}

// Sessions returns a snapshot of the live sessions, sorted by ID.
func (r *Registry) Sessions() []Info {
	r.mu.Lock()
	out := make([]Info, 0, len(r.sessions))
	for _, s := range r.sessions {
		out = append(out, s.infoLocked())
	}
	r.mu.Unlock()
	slices.SortFunc(out, func(a, b Info) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

func (r *Registry) setState(s *session, st State) {
	r.mu.Lock()
	s.state = st
	r.mu.Unlock()
}

// sleep waits d on the registry clock and reports false when ctx ends
// first.
func (r *Registry) sleep(ctx context.Context, d time.Duration) bool {
	t := r.clock.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C():
		return true
	}
}
