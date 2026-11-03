package session

import (
	"context"
	"errors"
	"fmt"

	"github.com/liquidmetal-dev/muster/internal/config"
	"github.com/liquidmetal-dev/muster/internal/github"
	"github.com/liquidmetal-dev/muster/internal/vmsource"
)

// ErrAdoptMismatch is returned when a lease and a runner do not belong to
// the same session.
var ErrAdoptMismatch = errors.New("session: lease and runner do not match")

// Adoption is a session found in flight at startup: a lease and the
// runner that carries the same session id.
type Adoption struct {
	// Lease.RequestID is the session id.
	Lease  vmsource.LeaseInfo
	Runner github.Runner
}

// Adopt resumes a session in RUNNING without an exec stream. It gets the
// VM again with Acquire, which returns the existing lease for the same
// request id.
func (r *Registry) Adopt(ctx context.Context, a Adoption) error {
	id := a.Lease.RequestID
	p, ok := r.pools[a.Lease.Pool]
	r.mu.Lock()
	running := r.runCtx != nil && !r.stopped
	r.mu.Unlock()
	if !running {
		return ErrNotRunning
	}
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownPool, a.Lease.Pool)
	}
	name := runnerName(r.cfg.Namespace, p.cfg.Name, id)
	if len(id) != config.SessionIDLen || a.Runner.Name != name {
		return fmt.Errorf("%w: lease %s, runner %q", ErrAdoptMismatch, id, a.Runner.Name)
	}
	vm, err := r.cfg.Source.Acquire(ctx, p.cfg.Name, id)
	if err != nil {
		return fmt.Errorf("session: adopt %s: %w", id, err)
	}
	if vm.ID() != a.Lease.VMID {
		// The lease went away after it was listed, so Acquire claimed a
		// new VM that the runner never ran on.
		if err := r.cfg.Source.Release(ctx, vm); err != nil {
			r.log.Warn("release replacement VM", "session", id, "vm", vm.ID(), "err", err)
		}
		return fmt.Errorf("%w: lease %s has VM %s, Acquire returned %s", ErrAdoptMismatch, id, a.Lease.VMID, vm.ID())
	}

	s := newSession(id, p, name)
	s.adopted = true
	s.vm = vm
	s.vmID = vm.ID()
	s.claimedAt = a.Lease.ClaimedAt
	s.busy = a.Runner.Busy
	s.runnerID = a.Runner.ID
	s.seen = true
	s.state = Running

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return ErrNotRunning
	}
	if _, dup := r.sessions[id]; dup {
		return fmt.Errorf("session: %s is already live", id)
	}
	r.sessions[id] = s
	r.wg.Add(1)
	go r.runSession(r.runCtx, s)
	return nil
}
