package battery

import (
	"context"
	"time"

	poolmgrv1alpha1 "github.com/liquidmetal-dev/battery/api/proto/poolmgr/v1alpha1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/liquidmetal-dev/muster/internal/vmsource"
)

// VM is one lease held through this source.
type VM struct {
	source   *Source
	id       string
	leaseID  string
	pool     string
	host     vmsource.HostInfo
	interval time.Duration
}

var _ vmsource.VM = (*VM)(nil)

// ID returns battery's vm_uid.
func (v *VM) ID() string { return v.id }

// LeaseID returns the lease token.
func (v *VM) LeaseID() string { return v.leaseID }

// Pool returns the pool name the VM was claimed from.
func (v *VM) Pool() string { return v.pool }

// Host returns the flintlock host that runs the VM.
func (v *VM) Host() vmsource.HostInfo { return v.host }

// Exec implements vmsource.VM through the configured Execer.
func (v *VM) Exec(ctx context.Context, spec vmsource.ExecSpec) (vmsource.ExecSession, error) {
	return v.source.cfg.Execer.Exec(ctx, v.host, v.id, spec)
}

// KeepAlive implements vmsource.VM with Heartbeat. ErrLeaseNotFound means
// the lease, and the VM, are gone.
func (v *VM) KeepAlive(ctx context.Context) error {
	err := v.source.call(ctx, "Heartbeat", retryDefault, func(ctx context.Context) error {
		_, herr := v.source.lease.Heartbeat(ctx, &poolmgrv1alpha1.HeartbeatRequest{LeaseId: v.leaseID})
		return herr
	})
	if err != nil {
		return mapError("Heartbeat", vmsource.ErrLeaseNotFound, err)
	}
	return nil
}

// KeepAliveInterval implements vmsource.VM: half the pool's heartbeat
// interval, read when the VM was acquired.
func (v *VM) KeepAliveInterval() time.Duration { return v.interval }

func isNotFound(err error) bool { return status.Code(err) == codes.NotFound }
