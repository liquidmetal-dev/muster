package session

import (
	"context"
	"log/slog"
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

	vm vmsource.VM
}

func newSession(id string, p *pool, runner string) *session {
	return &session{
		id:     id,
		pool:   p,
		runner: runner,
		state:  Pending,
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
	r.setState(s, Starting)
	<-ctx.Done()
	return Failed, ""
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
