package session

import (
	"context"
	"time"
)

// running watches a RUNNING session until the first end signal. run is
// nil for a session without an exec stream.
func (r *Registry) running(ctx context.Context, s *session, run *execRun, carry mailbox) (State, Reason) {
	var execDone <-chan execResult
	if run != nil {
		execDone = run.done
	}
	idle := r.clock.NewTimer(r.cfg.Defaults.IdleTimeout.Duration())
	defer idle.Stop()
	idleC := idle.C()
	if r.isBusy(s) {
		idle.Stop()
		idleC = nil
	}
	maxD := r.clock.NewTimer(max(s.claimedAt.Add(s.pool.cfg.MaxSessionDuration.Duration()).Sub(r.clock.Now()), 0))
	defer maxD.Stop()
	var graceC <-chan time.Time

	apply := func(m mailbox) (State, Reason, bool) {
		s.seen = s.seen || m.listed || m.online
		if m.lost {
			return Failed, ReasonVMLost, true
		}
		if m.busy && idleC != nil {
			idle.Stop()
			idleC = nil
			s.log.Info("runner busy")
		}
		if m.completed && graceC == nil {
			s.log.Info("job completed", "grace", r.cfg.Grace)
			graceC = r.clock.After(r.cfg.Grace)
		}
		if m.absent && s.seen {
			return Ended, ReasonRunnerAbsent, true
		}
		return 0, "", false
	}
	if st, reason, done := apply(carry); done {
		return st, reason
	}

	for {
		select {
		case <-ctx.Done():
			return Failed, ""
		case res := <-execDone:
			if res.err != nil {
				s.log.Warn("exec stream broke; watching the runner through GitHub", "err", res.err)
				execDone = nil
				continue
			}
			s.log.Info("runner exited", "exit_code", res.code)
			return Ended, ReasonExecExit
		case <-idleC:
			return Ended, ReasonIdleTimeout
		case <-maxD.C():
			return Ended, ReasonMaxDuration
		case <-graceC:
			return Ended, ReasonJobCompleted
		case <-s.notify:
			if st, reason, done := apply(s.drain()); done {
				return st, reason
			}
		}
	}
}

func (r *Registry) isBusy(s *session) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return s.busy
}
