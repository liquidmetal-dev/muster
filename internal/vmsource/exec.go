package vmsource

import (
	"context"
	"io"
	"time"
)

// ExecSpec describes a command to run in a VM.
type ExecSpec struct {
	Cmd  string
	Args []string
	Cwd  string
	// Env entries are KEY=VALUE.
	Env  []string
	User string
	// Timeout bounds the command; zero means no limit.
	Timeout time.Duration
	// HasStdin opens the stdin stream; the caller closes it to send EOF.
	HasStdin bool
}

// ExecSession is a running command.
type ExecSession interface {
	// Stdin is the command's input. Close sends EOF.
	Stdin() io.WriteCloser
	Stdout() io.Reader
	Stderr() io.Reader
	// Wait blocks until the command ends and returns its exit code.
	Wait(ctx context.Context) (exitCode int, err error)
	// Close cancels the stream.
	Close() error
}

// Execer starts commands on a host. The battery source delegates VM.Exec
// to it so the flintlock client can be supplied separately.
type Execer interface {
	Exec(ctx context.Context, host HostInfo, vmID string, spec ExecSpec) (ExecSession, error)
}

// UnavailableExecer is the Execer used when none is configured.
type UnavailableExecer struct{}

// Exec always returns ErrExecUnavailable.
func (UnavailableExecer) Exec(context.Context, HostInfo, string, ExecSpec) (ExecSession, error) {
	return nil, ErrExecUnavailable
}
