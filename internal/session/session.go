package session

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/liquidmetal-dev/muster/internal/vmsource"
)

// session is one session. Fields under "registry mu" change only through
// the Registry; the rest belong to the session goroutine.
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

	mbmu   sync.Mutex
	mb     mailbox
	notify chan struct{}
}

// mailbox carries what the registry observed for a session to its
// goroutine. Every fact except absent is sticky until the session drains
// it, so no observation is lost while the goroutine is busy.
type mailbox struct {
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
	s.mb.absent = m.absent
	s.mbmu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

// repost puts back the facts that the next phase still needs.
func (s *session) repost(m mailbox) {
	m.online, m.listed = false, false
	if m.busy || m.completed || m.absent {
		s.post(m)
	}
}

// drain returns the mailbox and empties it.
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
	outcome, reason := r.drive(ctx, s)
	if ctx.Err() != nil {
		return
	}
	r.finish(ctx, s, outcome, reason)
}

// drive runs the session through its active states and returns how it
// ended.
func (r *Registry) drive(ctx context.Context, s *session) (State, Reason) {
	if reason, ok := r.claim(ctx, s); !ok {
		return Failed, reason
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

	run, reason, ok := r.start(ctx, s)
	if !ok {
		return Failed, reason
	}
	defer run.close()
	return r.running(ctx, s, run)
}

// finish cleans up after the session and removes it.
func (r *Registry) finish(ctx context.Context, s *session, outcome State, reason Reason) {
	r.mu.Lock()
	s.state, s.reason = outcome, reason
	r.mu.Unlock()
	if s.vm != nil {
		if err := r.cfg.Source.Release(ctx, s.vm); err != nil {
			s.log.Warn("release VM", "err", err)
		}
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
