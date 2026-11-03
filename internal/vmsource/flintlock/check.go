package flintlock

import (
	"context"
	"fmt"
	"strings"

	mvmv1 "github.com/liquidmetal-dev/flintlock/api/services/microvm/v1alpha1"
	"golang.org/x/mod/semver"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/liquidmetal-dev/muster/internal/vmsource"
)

// MinVersion is the oldest flintlock with the MicroVMExec API (POOL-4).
const MinVersion = "v0.15.2"

var (
	// ErrExecDisabled means the host runs without --enable-exec-api.
	ErrExecDisabled = fmt.Errorf("%w: flintlock exec api is disabled", vmsource.ErrExecUnavailable)
	// ErrUnsupportedVersion means the host runs a flintlock older than
	// MinVersion, or reports a version that is not semver.
	ErrUnsupportedVersion = fmt.Errorf("%w: flintlock version is not supported", vmsource.ErrExecUnavailable)
)

// CheckHost asks host for its ServerInfo. It returns ErrExecDisabled or
// ErrUnsupportedVersion when the host cannot run commands, or the RPC
// error, with its gRPC status kept, when the host did not answer.
func (e *Execer) CheckHost(ctx context.Context, host vmsource.HostInfo) error {
	c, err := e.conn(host)
	if err != nil {
		return err
	}
	info, err := mvmv1.NewMicroVMClient(c).ServerInfo(ctx, &emptypb.Empty{})
	if err != nil {
		return fmt.Errorf("flintlock: host %s (%s): server info: %w", host.Name, host.Address, err)
	}
	// The version comes first: an old flintlock has no exec api to enable.
	raw := info.GetVersion().GetVersion()
	v := raw
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if !semver.IsValid(v) {
		return hostError{ErrUnsupportedVersion, fmt.Sprintf("flintlock: host %s (%s): version %q is not semver; need %s or later",
			host.Name, host.Address, raw, MinVersion)}
	}
	if semver.Compare(v, MinVersion) < 0 {
		return hostError{ErrUnsupportedVersion, fmt.Sprintf("flintlock: host %s (%s): version %s is older than %s",
			host.Name, host.Address, raw, MinVersion)}
	}
	if !info.GetExec().GetEnabled() {
		return hostError{ErrExecDisabled, fmt.Sprintf("flintlock: host %s (%s): exec api is disabled; start flintlockd with --enable-exec-api",
			host.Name, host.Address)}
	}
	return nil
}

// hostError carries a message that names the host and unwraps to one of
// the sentinels, so the sentinel text is not repeated in the message.
type hostError struct {
	sentinel error
	msg      string
}

func (e hostError) Error() string { return e.msg }
func (e hostError) Unwrap() error { return e.sentinel }

// checkCall is one ServerInfo check that concurrent callers share.
type checkCall struct {
	done chan struct{}
	err  error
}

// ensureChecked runs CheckHost once per host until it passes. Callers that
// arrive while a check runs wait for its result.
func (e *Execer) ensureChecked(ctx context.Context, host vmsource.HostInfo) error {
	k := keyOf(host)
	e.mu.Lock()
	if e.checked[k] {
		e.mu.Unlock()
		return nil
	}
	call, running := e.inflight[k]
	if !running {
		call = &checkCall{done: make(chan struct{})}
		e.inflight[k] = call
	}
	e.mu.Unlock()

	if !running {
		call.err = e.CheckHost(ctx, host)
		e.mu.Lock()
		if call.err == nil {
			e.checked[k] = true
		}
		delete(e.inflight, k)
		e.mu.Unlock()
		close(call.done)
	}

	select {
	case <-call.done:
		return call.err
	case <-ctx.Done():
		return ctx.Err()
	}
}
