package session

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/liquidmetal-dev/muster/internal/vmsource"
)

// Fields under "registry mu" are read by other goroutines through the
// Registry; the rest belong to the session goroutine.
type session struct {
	id     string
	pool   *pool
	runner string
	log    *slog.Logger

	// registry mu
	state     State
	reason    Reason
	busy      bool
	vmID      string
	claimedAt time.Time

	vm       vmsource.VM
	runnerID int64
	// seen is set once GitHub has listed the runner.
	seen bool
	// adopted sessions start in RUNNING with no exec stream.
	adopted bool

	mbmu   sync.Mutex
	mb     mailbox
	notify chan struct{}
}

// mailbox carries what the registry observed for a session to its
// goroutine. Every fact except absent is sticky until the session drains
// it, so no observation is lost while the goroutine is busy.
type mailbox struct {
	polled    bool
	listed    bool
	online    bool
	busy      bool
	absent    bool
	completed bool
	lost      bool
}

func newSession(id string, p *pool, runner string) *session {
	return &session{
		id:     id,
		pool:   p,
		runner: runner,
		state:  Pending,
		notify: make(chan struct{}, 1),
	}
}

// observe records that a poll listed the runner; only then can a later
// absence end the session.
func (s *session) observe(m mailbox) {
	s.seen = s.seen || m.listed || m.online
}

func (s *session) logBrokenStream(err error) {
	s.log.Warn("exec stream broke; watching the runner through GitHub", "err", err)
}

func (s *session) infoLocked() Info {
	return Info{
		ID:         s.id,
		Pool:       s.pool.cfg.Name,
		RunnerName: s.runner,
		VMID:       s.vmID,
		State:      s.state,
		Reason:     s.reason,
		Busy:       s.busy,
		ClaimedAt:  s.claimedAt,
	}
}

// post merges m into the mailbox and wakes the session without blocking.
func (s *session) post(m mailbox) {
	s.mbmu.Lock()
	s.mb.listed = s.mb.listed || m.listed
	s.mb.online = s.mb.online || m.online
	s.mb.busy = s.mb.busy || m.busy
	s.mb.completed = s.mb.completed || m.completed
	s.mb.lost = s.mb.lost || m.lost
	if m.polled {
		s.mb.absent = m.absent
	}
	s.mbmu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (s *session) drain() mailbox {
	s.mbmu.Lock()
	defer s.mbmu.Unlock()
	m := s.mb
	s.mb = mailbox{}
	return m
}

func (r *Registry) runSession(ctx context.Context, s *session) {
	defer r.wg.Done()
	s.log = r.log.With("session", s.id, "pool", s.pool.cfg.Name, "runner", s.runner)
	if s.adopted {
		s.log = s.log.With("vm", s.vmID)
	}
	outcome, reason := r.drive(ctx, s)
	if ctx.Err() != nil {
		return
	}
	r.finish(ctx, s, outcome, reason)
}

// drive runs the session through its active states and returns how it
// ended.
func (r *Registry) drive(ctx context.Context, s *session) (State, Reason) {
	if !s.adopted {
		if reason, ok := r.claim(ctx, s); !ok {
			return Failed, reason
		}
	}
	hbCtx, stopHeartbeat := context.WithCancel(ctx)
	hbDone := make(chan struct{})
	go func() {
		defer close(hbDone)
		r.heartbeat(hbCtx, s)
	}()
	defer func() {
		stopHeartbeat()
		<-hbDone
	}()

	if s.adopted {
		s.log.Info("adopted session")
		return r.running(ctx, s, nil, mailbox{})
	}
	run, carry, reason := r.start(ctx, s)
	if run == nil {
		return reason.outcome(), reason
	}
	defer run.close()
	return r.running(ctx, s, run, carry)
}

// When ctx ends during cleanup, OnEnd does not run and startup
// reconciliation finishes the cleanup.
func (r *Registry) finish(ctx context.Context, s *session, outcome State, reason Reason) {
	r.mu.Lock()
	s.state, s.reason = outcome, reason
	r.mu.Unlock()
	if !r.cleanup(ctx, s) {
		return
	}
	r.mu.Lock()
	info := s.infoLocked()
	s.state = Released
	delete(r.sessions, s.id)
	r.mu.Unlock()
	if outcome == Failed {
		s.log.Warn("session failed", "reason", reason)
	} else {
		s.log.Info("session ended", "reason", reason)
	}
	if r.cfg.OnEnd != nil {
		r.cfg.OnEnd(info)
	}
}
