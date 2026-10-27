// Package vmsource defines the seam between muster's session logic and the
// system that supplies virtual machines (design section 3, requirement
// FUT-1). Session code depends only on the types in this package; the
// battery implementation lives in the battery subpackage and a future
// flintlock snapshot implementation can replace it without touching
// GitHub-facing code.
package vmsource

import (
	"context"
	"fmt"
	"time"
)

// Source supplies booted VMs for pools.
type Source interface {
	// Acquire returns a booted VM for the pool. requestID makes retries
	// idempotent: a repeated call with the same requestID returns the same
	// VM while its lease exists. It returns ErrExhausted when nothing is
	// available right now.
	Acquire(ctx context.Context, pool, requestID string) (VM, error)
	// Release gives the VM back. The source deletes it.
	Release(ctx context.Context, vm VM) error
	// Leases lists what the source currently holds for this muster
	// instance, for startup reconciliation.
	Leases(ctx context.Context) ([]LeaseInfo, error)
	// Events reports VM loss or impending expiry for a pool. The channel
	// closes when ctx ends or when the subscription fails permanently; a
	// consumer that sees it close while ctx is live must treat its VMs as
	// unobserved.
	Events(ctx context.Context, pool string) (<-chan Event, error)
}

// VM is one acquired virtual machine.
type VM interface {
	// ID identifies the VM in the source.
	ID() string
	// Exec starts a command in the VM.
	Exec(ctx context.Context, spec ExecSpec) (ExecSession, error)
	// KeepAlive extends the lease. ErrLeaseNotFound means the VM is gone.
	KeepAlive(ctx context.Context) error
	// KeepAliveInterval is how often the holder must call KeepAlive.
	KeepAliveInterval() time.Duration
}

// HostInfo identifies the host that runs a VM.
type HostInfo struct {
	// Name is the host's configured name in the source; TLS overrides are
	// keyed by it.
	Name string
	// Address is the host's gRPC endpoint.
	Address string
}

// LeaseInfo describes one lease the source holds for this instance.
type LeaseInfo struct {
	LeaseID   string
	VMID      string
	Pool      string
	RequestID string
	ClaimedAt time.Time
	ExpiresAt time.Time
}

// EventType classifies an Event.
type EventType int

const (
	// EventVMLost means the VM is gone; the session must fail.
	EventVMLost EventType = iota + 1
	// EventVMExpiring means the lease expires soon unless kept alive.
	EventVMExpiring
)

// String returns a stable name for logs.
func (t EventType) String() string {
	switch t {
	case EventVMLost:
		return "vm-lost"
	case EventVMExpiring:
		return "vm-expiring"
	default:
		return fmt.Sprintf("unknown(%d)", int(t))
	}
}

// Event is one change reported by Source.Events.
type Event struct {
	Type EventType
	Pool string
	VMID string
	At   time.Time
}
