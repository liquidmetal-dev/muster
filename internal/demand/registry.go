package demand

import (
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/liquidmetal-dev/muster/internal/clock"
	"github.com/liquidmetal-dev/muster/internal/config"
)

// MaxAge is how long GitHub keeps a job queued, and so how long a record
// stays open without a closing event. Tombstones are kept as long.
const MaxAge = 24 * time.Hour

// Job is one queued workflow job as an intake reports it.
type Job struct {
	ID     int64
	Labels []string
	// Repo is "owner/name".
	Repo string
	// QueuedAt is when GitHub queued the job. Zero means the intake does
	// not know it, and expiry counts from when muster first saw the job.
	QueuedAt time.Time
}

// Config configures a Registry.
type Config struct {
	// Pools in configuration order; the first match wins.
	Pools []config.Pool
	// Clock is nil for clock.Real().
	Clock clock.Clock
	// Logger is nil for slog.Default().
	Logger *slog.Logger
}

type record struct {
	id        int64
	labels    []string
	pool      string
	repo      string
	firstSeen time.Time
	lastSeen  time.Time
	queuedAt  time.Time
}

func (r *record) expiresAt() time.Time {
	if !r.queuedAt.IsZero() {
		return r.queuedAt.Add(MaxAge)
	}
	return r.firstSeen.Add(MaxAge)
}

// Registry holds the open queued-job records and the tombstones of closed
// ones. It is safe for concurrent use.
type Registry struct {
	pools  []config.Pool
	clock  clock.Clock
	logger *slog.Logger

	mu       sync.Mutex
	onChange func()
	open     map[int64]*record
	closed   map[int64]time.Time
	// listedAt is the newest poll listing time per lower-case repository.
	listedAt map[string]time.Time
}

// New returns an empty Registry.
func New(cfg Config) *Registry {
	if cfg.Clock == nil {
		cfg.Clock = clock.Real()
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Registry{
		pools:    cfg.Pools,
		clock:    cfg.Clock,
		logger:   cfg.Logger,
		open:     map[int64]*record{},
		closed:   map[int64]time.Time{},
		listedAt: map[string]time.Time{},
	}
}

// SetOnChange sets the function called after an intake call opens or
// closes a record. It runs outside the registry lock. Set it before the
// intakes start.
func (r *Registry) SetOnChange(fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onChange = fn
}

// Queued records a queued job. A job already open, or closed within
// MaxAge, is not counted again.
func (r *Registry) Queued(j Job) {
	r.mu.Lock()
	now := r.pruneLocked()
	changed := r.queuedLocked(j, now, now)
	r.unlockAndNotify(changed)
}

// Started closes the record of a job that is in progress.
func (r *Registry) Started(id int64) { r.finish(id) }

// Completed closes the record of a job that has completed.
func (r *Registry) Completed(id int64) { r.finish(id) }

// Polled merges one poll of repos, listed at listedAt, into the registry.
// Listed jobs are recorded as by Queued. An open record in one of repos
// that is not listed closes, unless muster saw the job at or after
// listedAt, because a newer sighting outranks an older listing. A repo
// already merged from a newer listing is skipped, so polls that finish out
// of order cannot reopen stale demand.
func (r *Registry) Polled(repos []string, listedAt time.Time, jobs []Job) {
	r.mu.Lock()
	now := r.pruneLocked()
	var fresh, stale []string
	for _, repo := range repos {
		key := strings.ToLower(repo)
		if listedAt.Before(r.listedAt[key]) {
			stale = append(stale, repo)
			continue
		}
		r.listedAt[key] = listedAt
		fresh = append(fresh, repo)
	}
	changed := false
	listed := make(map[int64]bool, len(jobs))
	for _, j := range jobs {
		if containsFold(stale, j.Repo) {
			continue
		}
		listed[j.ID] = true
		if r.queuedLocked(j, now, listedAt) {
			changed = true
		}
	}
	for id, rec := range r.open {
		if listed[id] || !rec.lastSeen.Before(listedAt) || !containsFold(fresh, rec.repo) {
			continue
		}
		r.closeLocked(id, now)
		changed = true
	}
	r.unlockAndNotify(changed)
}

// Desired returns the number of open records for pool.
func (r *Registry) Desired(pool string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked()
	n := 0
	for _, rec := range r.open {
		if rec.pool == pool {
			n++
		}
	}
	return n
}

// Total returns the number of open records across all pools.
func (r *Registry) Total() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked()
	return len(r.open)
}

func (r *Registry) finish(id int64) {
	r.mu.Lock()
	now := r.pruneLocked()
	_, wasOpen := r.open[id]
	r.closeLocked(id, now)
	r.unlockAndNotify(wasOpen)
}

// queuedLocked records a sighting of j at seenAt. A poll's sighting is its
// listing time, not the time it is merged, so a late older poll cannot
// shield a job from a newer poll that no longer lists it.
func (r *Registry) queuedLocked(j Job, now, seenAt time.Time) bool {
	if _, ok := r.closed[j.ID]; ok {
		return false
	}
	if rec, ok := r.open[j.ID]; ok {
		if seenAt.After(rec.lastSeen) {
			rec.lastSeen = seenAt
		}
		if rec.queuedAt.IsZero() {
			rec.queuedAt = j.QueuedAt
		}
		return false
	}
	pool, ok := Match(r.pools, j.Labels, j.Repo)
	if !ok {
		r.logger.Debug("demand: job matches no pool", "job_id", j.ID, "repo", j.Repo, "labels", j.Labels)
		return false
	}
	r.open[j.ID] = &record{
		id:        j.ID,
		labels:    slices.Clone(j.Labels),
		pool:      pool,
		repo:      j.Repo,
		firstSeen: now,
		lastSeen:  seenAt,
		queuedAt:  j.QueuedAt,
	}
	return true
}

func (r *Registry) closeLocked(id int64, now time.Time) {
	delete(r.open, id)
	r.closed[id] = now
}

// pruneLocked closes expired records and drops expired tombstones. It does
// not report a change: a drop in demand never needs a dispatcher pass.
func (r *Registry) pruneLocked() time.Time {
	now := r.clock.Now()
	for id, rec := range r.open {
		if !now.Before(rec.expiresAt()) {
			r.closeLocked(id, now)
		}
	}
	for id, at := range r.closed {
		if !now.Before(at.Add(MaxAge)) {
			delete(r.closed, id)
		}
	}
	return now
}

func (r *Registry) unlockAndNotify(changed bool) {
	fn := r.onChange
	r.mu.Unlock()
	if changed && fn != nil {
		fn()
	}
}

func containsFold(list []string, s string) bool {
	return slices.ContainsFunc(list, func(v string) bool { return strings.EqualFold(v, s) })
}
