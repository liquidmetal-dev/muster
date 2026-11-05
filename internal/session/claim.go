package session

import (
	"context"
	"errors"
	"time"

	"github.com/liquidmetal-dev/muster/internal/vmsource"
)

const (
	claimBackoffBase  = time.Second
	claimBackoffLimit = 30 * time.Second
)

// claim acquires a VM with the session id as the request id. While the
// pool is exhausted it backs off, until demand no longer needs this
// session or the claim deadline passes.
func (r *Registry) claim(ctx context.Context, s *session) (Reason, bool) {
	r.setState(s, Claiming)
	deadline := r.clock.NewTimer(r.cfg.Defaults.ClaimDeadline.Duration())
	defer deadline.Stop()
	bo := newBackoff(claimBackoffBase, claimBackoffLimit, nil)
	began := r.clock.Now()
	for {
		vm, err := r.cfg.Source.Acquire(ctx, s.pool.cfg.Name, s.id)
		r.cfg.Metrics.ObserveClaimAttempt(s.pool.cfg.Name, errors.Is(err, vmsource.ErrExhausted))
		if err == nil {
			s.vm = vm
			r.mu.Lock()
			s.vmID = vm.ID()
			s.claimedAt = r.clock.Now()
			r.mu.Unlock()
			s.setLogger(s.baseLog.With("vm_id", vm.ID()))
			select {
			case <-deadline.C():
				return ReasonClaimDeadline, false
			default:
			}
			r.cfg.Metrics.ObserveClaim(s.pool.cfg.Name, r.clock.Now().Sub(began))
			return "", true
		}
		if ctx.Err() != nil {
			return "", false
		}
		if !errors.Is(err, vmsource.ErrExhausted) {
			s.logger().Warn("claim VM", "err", err)
			return ReasonClaimError, false
		}
		if !r.demandNeeds(s) {
			return ReasonNoDemand, false
		}
		wait := r.clock.NewTimer(bo.next())
		select {
		case <-ctx.Done():
			wait.Stop()
			return "", false
		case <-deadline.C():
			wait.Stop()
			return ReasonClaimDeadline, false
		case <-wait.C():
		}
	}
}

// demandNeeds reports whether queued jobs still need every not-yet-busy
// session of the pool, this one included.
func (r *Registry) demandNeeds(s *session) bool {
	desired := r.cfg.Demand.Desired(s.pool.cfg.Name)
	_, notBusy := r.Counts(s.pool.cfg.Name)
	return desired >= notBusy
}
