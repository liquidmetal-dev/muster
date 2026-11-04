package poller

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/liquidmetal-dev/muster/internal/clock"
	"github.com/liquidmetal-dev/muster/internal/config"
	"github.com/liquidmetal-dev/muster/internal/demand"
	"github.com/liquidmetal-dev/muster/internal/github"
)

// Lister lists the queued jobs of one owner/name repository.
type Lister interface {
	ListQueuedJobs(ctx context.Context, repo string) ([]github.QueuedJob, error)
}

// Demand is the part of the demand registry the poller feeds.
type Demand interface {
	Polled(repos []string, listedAt time.Time, jobs []demand.Job)
}

// Config configures a Poller.
type Config struct {
	Poll config.Poll
	// Lister is required.
	Lister Lister
	// Demand is required.
	Demand Demand
	// Clock is nil for clock.Real().
	Clock clock.Clock
	// Logger is nil for slog.Default().
	Logger *slog.Logger
	// Metrics is nil for NopMetrics{}.
	Metrics Metrics
}

// Poller is the polling intake.
type Poller struct {
	cfg      Config
	interval time.Duration
}

// New returns a Poller for cfg.
func New(cfg Config) (*Poller, error) {
	if cfg.Lister == nil {
		return nil, errors.New("poller: lister is required")
	}
	if cfg.Demand == nil {
		return nil, errors.New("poller: demand is required")
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
	interval := time.Duration(cfg.Poll.Interval)
	if interval == 0 {
		interval = config.DefaultPollInterval
	}
	return &Poller{cfg: cfg, interval: interval}, nil
}

// Run makes one poll pass at once and then one each interval, until ctx is
// done. Passes never overlap. Run returns at once when polling is disabled.
func (p *Poller) Run(ctx context.Context) {
	if !p.cfg.Poll.Enabled {
		return
	}
	for {
		// Arm the timer before the pass, so a caller that has seen the
		// pass's effect knows the next tick is already scheduled.
		t := p.cfg.Clock.NewTimer(p.interval)
		p.pass(ctx)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C():
		}
	}
}

func (p *Poller) pass(ctx context.Context) {
	for _, repo := range p.cfg.Poll.Repos {
		listedAt := p.cfg.Clock.Now()
		jobs, err := p.cfg.Lister.ListQueuedJobs(ctx, repo)
		d := p.cfg.Clock.Now().Sub(listedAt)
		switch {
		case ctx.Err() != nil:
			return
		case errors.Is(err, github.ErrRateLimited):
			p.cfg.Metrics.ObservePoll(repo, OutcomeRateLimited, d)
			p.cfg.Logger.Warn("poll pass stopped: rate limited", "repo", repo, "err", err)
			return
		case err != nil:
			p.cfg.Metrics.ObservePoll(repo, OutcomeError, d)
			p.cfg.Logger.Warn("poll failed", "repo", repo, "err", err)
			continue
		}
		p.cfg.Demand.Polled([]string{repo}, listedAt, toDemand(repo, jobs))
		p.cfg.Metrics.ObservePoll(repo, OutcomeOK, d)
		p.cfg.Logger.Debug("poll listed", "repo", repo, "jobs", len(jobs))
	}
}

func toDemand(repo string, jobs []github.QueuedJob) []demand.Job {
	out := make([]demand.Job, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, demand.Job{ID: j.JobID, Labels: j.Labels, Repo: repo, QueuedAt: j.CreatedAt})
	}
	return out
}
