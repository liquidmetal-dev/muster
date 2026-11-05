package reconcile

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/liquidmetal-dev/muster/internal/clock"
	"github.com/liquidmetal-dev/muster/internal/config"
	"github.com/liquidmetal-dev/muster/internal/github"
	"github.com/liquidmetal-dev/muster/internal/session"
	"github.com/liquidmetal-dev/muster/internal/vmsource"
)

const (
	// DefaultListWindow is how long Run retries the lease and runner lists
	// before it gives up.
	DefaultListWindow = 2 * time.Minute
	// DefaultItemAttempts is how many times Run tries one decision.
	DefaultItemAttempts = 3

	backoffBase  = time.Second
	backoffLimit = 30 * time.Second
)

var errEmptyRequestID = errors.New("reconcile: lease has no request id")

// Adopter resumes in-flight sessions. *session.Registry satisfies it.
type Adopter interface {
	Adopt(ctx context.Context, a session.Adoption) error
}

// GitHub is the part of the GitHub client that Run uses. *github.Client
// satisfies it.
type GitHub interface {
	ListRunners(ctx context.Context, s github.Scope, prefix string) ([]github.Runner, error)
	DeleteRunner(ctx context.Context, s github.Scope, id int64) error
}

// Config configures Run.
type Config struct {
	Namespace string
	Pools     []config.Pool
	Source    vmsource.Source
	GitHub    GitHub
	Sessions  Adopter
	// Metrics is nil for NopMetrics.
	Metrics Metrics
	// Clock is nil for clock.Real().
	Clock clock.Clock
	// Logger is nil for slog.Default().
	Logger *slog.Logger
	// ListWindow is zero for DefaultListWindow.
	ListWindow time.Duration
	// ItemAttempts is zero for DefaultItemAttempts.
	ItemAttempts int
}

// Result counts the decisions of one Run.
type Result struct {
	Adopted        int
	LeasesReleased int
	RunnersDeleted int
	Failed         int
}

type runnerKey struct {
	scope github.Scope
	name  string
}

type reconciler struct {
	cfg    Config
	clock  clock.Clock
	log    *slog.Logger
	scopes map[string]github.Scope
	res    Result
}

// Run pairs leases with runners and acts on each pair and orphan. It
// returns an error only when the lists cannot be read within ListWindow;
// a decision that fails is logged, counted in Result.Failed and skipped.
func Run(ctx context.Context, cfg Config) (Result, error) {
	if cfg.Source == nil || cfg.GitHub == nil || cfg.Sessions == nil {
		return Result{}, errors.New("reconcile: Source, GitHub and Sessions are required")
	}
	if cfg.Metrics == nil {
		cfg.Metrics = NopMetrics{}
	}
	if cfg.Clock == nil {
		cfg.Clock = clock.Real()
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.ListWindow == 0 {
		cfg.ListWindow = DefaultListWindow
	}
	if cfg.ItemAttempts == 0 {
		cfg.ItemAttempts = DefaultItemAttempts
	}
	r := &reconciler{cfg: cfg, clock: cfg.Clock, log: cfg.Logger, scopes: map[string]github.Scope{}}
	var scopes []github.Scope
	for _, p := range cfg.Pools {
		s, err := github.ScopeFromConfig(p.Scope)
		if err != nil {
			return Result{}, fmt.Errorf("reconcile: pool %s: %w", p.Name, err)
		}
		r.scopes[p.Name] = s
		if !slices.Contains(scopes, s) {
			scopes = append(scopes, s)
		}
	}

	leases, runners, err := r.list(ctx, scopes)
	if err != nil {
		return Result{}, err
	}
	pools := map[string]bool{}
	for name := range r.scopes {
		pools[name] = true
	}
	for _, l := range leases {
		pools[l.Pool] = true
		r.lease(ctx, l, runners)
	}
	keys := make([]runnerKey, 0, len(runners))
	for k := range runners {
		if r.ours(k.name, pools) {
			keys = append(keys, k)
		} else {
			r.log.Debug("reconcile: runner left alone", "runner_name", k.name)
		}
	}
	slices.SortFunc(keys, func(a, b runnerKey) int { return cmp.Compare(a.name, b.name) })
	for _, k := range keys {
		r.deleteRunner(ctx, k.scope, runners[k])
	}
	r.log.Info("reconciliation complete", "adopted", r.res.Adopted, "leases_released", r.res.LeasesReleased,
		"runners_deleted", r.res.RunnersDeleted, "failed", r.res.Failed)
	return r.res, nil
}

// ours reports whether name is a runner name of this namespace for one of
// pools. The namespace prefix alone also matches the runners of a longer
// namespace that starts with this one and a hyphen.
func (r *reconciler) ours(name string, pools map[string]bool) bool {
	for p := range pools {
		prefix := config.RunnerNamePrefix(r.cfg.Namespace, p)
		if strings.HasPrefix(name, prefix) && len(name)-len(prefix) == config.SessionIDLen {
			return true
		}
	}
	return false
}

func (r *reconciler) list(ctx context.Context, scopes []github.Scope) ([]vmsource.LeaseInfo, map[runnerKey]github.Runner, error) {
	deadline := r.clock.Now().Add(r.cfg.ListWindow)
	b := newBackoff()
	for {
		leases, runners, err := r.listOnce(ctx, scopes)
		if err == nil {
			return leases, runners, nil
		}
		if ctx.Err() != nil {
			return nil, nil, fmt.Errorf("reconcile: list: %w", ctx.Err())
		}
		remaining := deadline.Sub(r.clock.Now())
		if remaining <= 0 {
			return nil, nil, fmt.Errorf("reconcile: list: %w", err)
		}
		d := min(b.next(), remaining)
		r.log.Warn("reconcile list failed; retrying", "err", err, "retry_in", d)
		if !r.sleep(ctx, d) {
			return nil, nil, fmt.Errorf("reconcile: list: %w", ctx.Err())
		}
	}
}

func (r *reconciler) listOnce(ctx context.Context, scopes []github.Scope) ([]vmsource.LeaseInfo, map[runnerKey]github.Runner, error) {
	leases, err := r.cfg.Source.Leases(ctx)
	if err != nil {
		return nil, nil, err
	}
	prefix := config.RunnerNamespacePrefix(r.cfg.Namespace)
	runners := map[runnerKey]github.Runner{}
	for _, s := range scopes {
		rs, err := r.cfg.GitHub.ListRunners(ctx, s, prefix)
		if err != nil {
			return nil, nil, err
		}
		for _, run := range rs {
			runners[runnerKey{s, run.Name}] = run
		}
	}
	return leases, runners, nil
}

func (r *reconciler) lease(ctx context.Context, l vmsource.LeaseInfo, runners map[runnerKey]github.Runner) {
	scope, known := r.scopes[l.Pool]
	if !known || len(l.RequestID) != config.SessionIDLen {
		r.releaseLease(ctx, l)
		return
	}
	key := runnerKey{scope, config.RunnerNamePrefix(r.cfg.Namespace, l.Pool) + l.RequestID}
	run, ok := runners[key]
	if !ok {
		r.releaseLease(ctx, l)
		return
	}
	delete(runners, key)
	err := r.attempt(ctx, ActionAdopt, leaseAttrs(l, run.Name), func(ctx context.Context) error {
		return r.cfg.Sessions.Adopt(ctx, session.Adoption{Lease: l, Runner: run})
	})
	if !errors.Is(err, session.ErrAdoptMismatch) {
		return
	}
	// The lease vanished after it was listed. Adopt may have failed to
	// release the replacement VM it claimed, so look for it.
	if again, err := r.cfg.Source.Leases(ctx); err != nil {
		r.log.Warn("list leases after adopt mismatch", append(leaseAttrs(l, run.Name), "err", err)...)
	} else {
		for _, l2 := range again {
			if l2.Pool == l.Pool && l2.RequestID == l.RequestID {
				r.releaseLease(ctx, l2)
			}
		}
	}
	r.deleteRunner(ctx, scope, run)
}

func (r *reconciler) releaseLease(ctx context.Context, l vmsource.LeaseInfo) {
	attrs := leaseAttrs(l, "")
	if l.RequestID == "" {
		r.record(ActionReleaseLease, errEmptyRequestID, attrs)
		return
	}
	_ = r.attempt(ctx, ActionReleaseLease, attrs, func(ctx context.Context) error {
		// Acquire with the lease's request id returns the existing lease,
		// or claims a new VM when the lease has gone; both are released.
		vm, err := r.cfg.Source.Acquire(ctx, l.Pool, l.RequestID)
		if errors.Is(err, vmsource.ErrExhausted) {
			return nil
		}
		if err != nil {
			return err
		}
		err = r.cfg.Source.Release(ctx, vm)
		if errors.Is(err, vmsource.ErrLeaseNotFound) {
			return nil
		}
		return err
	})
}

func (r *reconciler) deleteRunner(ctx context.Context, s github.Scope, run github.Runner) {
	_ = r.attempt(ctx, ActionDeleteRunner, []any{"runner_name", run.Name, "runner_id", run.ID}, func(ctx context.Context) error {
		return r.cfg.GitHub.DeleteRunner(ctx, s, run.ID)
	})
}

func leaseAttrs(l vmsource.LeaseInfo, runner string) []any {
	attrs := []any{"session_id", l.RequestID, "pool", l.Pool, "vm_id", l.VMID}
	if runner != "" {
		attrs = append(attrs, "runner_name", runner)
	}
	return attrs
}

// permanent errors are not retried: a further attempt cannot succeed.
func permanent(err error) bool {
	return errors.Is(err, github.ErrRunnerBusy) ||
		errors.Is(err, session.ErrAdoptMismatch) ||
		errors.Is(err, session.ErrNotRunning)
}

// attempt runs fn up to ItemAttempts times and records the outcome.
func (r *reconciler) attempt(ctx context.Context, action string, attrs []any, fn func(context.Context) error) error {
	b := newBackoff()
	var err error
	for i := 1; ; i++ {
		err = fn(ctx)
		if err == nil || permanent(err) || i >= r.cfg.ItemAttempts || ctx.Err() != nil {
			break
		}
		if !r.sleep(ctx, b.next()) {
			err = ctx.Err()
			break
		}
	}
	r.record(action, err, attrs)
	return err
}

func (r *reconciler) record(action string, err error, attrs []any) {
	outcome := OutcomeOK
	if err != nil {
		outcome = OutcomeFailed
	}
	r.cfg.Metrics.ObserveDecision(action, outcome)
	attrs = append([]any{"action", action, "outcome", outcome}, attrs...)
	if err != nil {
		r.res.Failed++
		r.log.Warn("reconcile decision failed", append(attrs, "err", err)...)
		return
	}
	switch action {
	case ActionAdopt:
		r.res.Adopted++
	case ActionReleaseLease:
		r.res.LeasesReleased++
	case ActionDeleteRunner:
		r.res.RunnersDeleted++
	}
	r.log.Info("reconcile decision", attrs...)
}

func (r *reconciler) sleep(ctx context.Context, d time.Duration) bool {
	t := r.clock.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C():
		return true
	case <-ctx.Done():
		return false
	}
}

// backoff yields exponential delays from backoffBase to backoffLimit,
// each jittered into [d/2, d] (REL-4).
type backoff struct{ cur time.Duration }

func newBackoff() *backoff { return &backoff{} }

func (b *backoff) next() time.Duration {
	switch {
	case b.cur == 0:
		b.cur = backoffBase
	case b.cur < backoffLimit:
		b.cur = min(2*b.cur, backoffLimit)
	}
	half := b.cur / 2
	return half + time.Duration(rand.Float64()*float64(b.cur-half)) //nolint:gosec // jitter, not security
}
