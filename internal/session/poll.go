package session

import (
	"context"

	"github.com/liquidmetal-dev/muster/internal/github"
)

// pollRunners lists the runners once per GitHub scope every
// PollInterval and posts what it saw to each session in that scope.
// Polls keep a fixed schedule, so a slow poll does not shift the next.
func (r *Registry) pollRunners(ctx context.Context) {
	next := r.clock.Now().Add(r.cfg.PollInterval)
	for {
		t := r.clock.NewTimer(max(next.Sub(r.clock.Now()), 0))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C():
		}
		next = next.Add(r.cfg.PollInterval)
		if now := r.clock.Now(); next.Before(now) {
			next = now.Add(r.cfg.PollInterval)
		}
		r.pollOnce(ctx)
	}
}

func (r *Registry) pollOnce(ctx context.Context) {
	byScope := map[github.Scope][]*session{}
	r.mu.Lock()
	for _, s := range r.sessions {
		if s.state == Starting || s.state == Running {
			byScope[s.pool.scope] = append(byScope[s.pool.scope], s)
		}
	}
	r.mu.Unlock()

	prefix := "muster-" + r.cfg.Namespace + "-"
	for scope, sessions := range byScope {
		runners, err := r.cfg.GitHub.ListRunners(ctx, scope, prefix)
		if err != nil {
			if ctx.Err() == nil {
				r.log.Warn("list runners", "scope", scope.String(), "err", err)
			}
			continue
		}
		byName := make(map[string]github.Runner, len(runners))
		for _, rn := range runners {
			byName[rn.Name] = rn
		}
		for _, s := range sessions {
			rn, ok := byName[s.runner]
			m := mailbox{listed: ok, online: ok && rn.Status == "online", busy: ok && rn.Busy, absent: !ok}
			if m.busy {
				r.mu.Lock()
				s.busy = true
				r.mu.Unlock()
			}
			s.post(m)
		}
	}
}
