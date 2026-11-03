package flintlock

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	mvmexecv1 "github.com/liquidmetal-dev/flintlock/api/services/microvmexec/v1alpha1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/liquidmetal-dev/muster/internal/vmsource"
)

var _ vmsource.Execer = (*Execer)(nil)

// Exec starts spec in the microvm vmID on host. Cancelling ctx ends the
// command's stream; flintlock has no other way to stop it.
func (e *Execer) Exec(ctx context.Context, host vmsource.HostInfo, vmID string, spec vmsource.ExecSpec) (vmsource.ExecSession, error) {
	env, err := envMap(spec.Env)
	if err != nil {
		return nil, err
	}
	timeout, err := timeoutSeconds(spec.Timeout)
	if err != nil {
		return nil, err
	}
	if err := e.ensureChecked(ctx, host); err != nil {
		return nil, err
	}
	c, err := e.conn(host)
	if err != nil {
		return nil, err
	}

	sctx, cancel := context.WithCancel(ctx)
	stream, err := mvmexecv1.NewMicroVMExecClient(c).ExecCommand(sctx)
	if err != nil {
		cancel()
		return nil, wrapStatus(fmt.Errorf("flintlock: host %s: exec: %w", host.Name, err))
	}
	start := &mvmexecv1.ExecStart{
		Uid:            vmID,
		Cmd:            spec.Cmd,
		Args:           spec.Args,
		Cwd:            spec.Cwd,
		Env:            env,
		User:           spec.User,
		TimeoutSeconds: timeout,
		HasStdin:       spec.HasStdin,
	}
	if err := stream.Send(&mvmexecv1.ExecCommandRequest{Payload: &mvmexecv1.ExecCommandRequest_Start{Start: start}}); err != nil {
		if errors.Is(err, io.EOF) {
			// The status of a stream that the server ended is only
			// available from Recv.
			_, err = stream.Recv()
		}
		cancel()
		return nil, wrapStatus(fmt.Errorf("flintlock: host %s: exec: %w", host.Name, err))
	}
	return newSession(ctx, cancel, stream, spec.HasStdin), nil
}

func envMap(env []string) (map[string]string, error) {
	if len(env) == 0 {
		return nil, nil
	}
	m := make(map[string]string, len(env))
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("flintlock: env entry %q is not KEY=VALUE", kv)
		}
		if _, dup := m[k]; dup {
			return nil, fmt.Errorf("flintlock: env key %q is set twice", k)
		}
		m[k] = v
	}
	return m, nil
}

// timeoutSeconds rounds d up to whole seconds, because flintlock takes the
// timeout in seconds and a shorter limit than asked would kill early.
func timeoutSeconds(d time.Duration) (int32, error) {
	if d < 0 {
		return 0, fmt.Errorf("flintlock: timeout %v is negative", d)
	}
	secs := d / time.Second
	if d%time.Second != 0 {
		secs++
	}
	if secs > math.MaxInt32 {
		return 0, fmt.Errorf("flintlock: timeout %v is too long", d)
	}
	return int32(secs), nil
}

// wrapStatus marks the statuses that mean exec cannot run on this VM or
// host as vmsource.ErrExecUnavailable. The gRPC status stays reachable.
func wrapStatus(err error) error {
	switch status.Code(err) {
	case codes.FailedPrecondition, codes.Unimplemented:
		return fmt.Errorf("%w: %w", vmsource.ErrExecUnavailable, err)
	}
	return err
}
