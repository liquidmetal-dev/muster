package vmsource

import "errors"

var (
	// ErrExhausted means the pool has no VM available right now.
	ErrExhausted = errors.New("vmsource: pool exhausted")
	// ErrPoolNotFound means the source does not know the pool.
	ErrPoolNotFound = errors.New("vmsource: pool not found")
	// ErrLeaseNotFound means the lease, and so the VM, is gone.
	ErrLeaseNotFound = errors.New("vmsource: lease not found")
	// ErrExecUnavailable means the source has no Execer.
	ErrExecUnavailable = errors.New("vmsource: exec is not available for this source")
)
