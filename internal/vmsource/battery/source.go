// Package battery implements vmsource.Source over battery's poolmgrd
// (docs/adr/2026-10-27-battery-as-vm-source.md).
//
// RPC mapping, from design section 3:
//
//	Acquire            PoolAdmin.GetPool (heartbeat interval), then Lease.ClaimVM
//	VM.KeepAlive       Lease.Heartbeat
//	Release            Lease.ReleaseVM
//	Leases             Lease.ListLeases, filtered to the configured namespace
//	Events             Events.Subscribe for one pool
//	VM.Exec            the configured vmsource.Execer (the flintlock client)
//
// Every call has a per-attempt timeout (Config.CallTimeout, default 10 s)
// and at most Config.MaxAttempts attempts (default 3) with full-jitter
// backoff. UNAVAILABLE and a per-attempt DEADLINE_EXCEEDED are retried on
// every call; ABORTED is retried on ClaimVM only. RESOURCE_EXHAUSTED is
// never retried and surfaces as vmsource.ErrExhausted; NOT_FOUND surfaces
// as vmsource.ErrPoolNotFound or vmsource.ErrLeaseNotFound, except on
// ReleaseVM where it means the lease is already gone.
//
// Battery events map as follows: VM_DELETED_DUE_TO_EXPIRY,
// VM_DELETED_ON_POOL_DELETE and VM_DELETED_ON_HOST_DRAIN become
// vmsource.EventVMLost, VM_EXPIRING_SOON becomes vmsource.EventVMExpiring,
// and every other type is dropped.
package battery

import (
	"context"
	"errors"
	"fmt"
	"time"

	poolmgrv1alpha1 "github.com/liquidmetal-dev/battery/api/proto/poolmgr/v1alpha1"
	"google.golang.org/grpc"

	"github.com/liquidmetal-dev/muster/internal/vmsource"
)

// Source is a vmsource.Source backed by one poolmgrd. Use New.
type Source struct {
	cfg    Config
	conn   *grpc.ClientConn
	lease  poolmgrv1alpha1.LeaseClient
	events poolmgrv1alpha1.EventsClient
	admin  poolmgrv1alpha1.PoolAdminClient
	// sleep waits between retry attempts; tests replace it.
	sleep func(ctx context.Context, d time.Duration) error
}

var _ vmsource.Source = (*Source)(nil)

// New validates cfg and prepares the client. The connection is made lazily
// on the first call.
func New(cfg Config) (*Source, error) {
	cfg = cfg.withDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	creds, err := cfg.TLS.Credentials()
	if err != nil {
		return nil, err
	}
	opts := []grpc.DialOption{grpc.WithTransportCredentials(creds)}
	if cfg.BasicAuthToken != "" {
		opts = append(opts, grpc.WithPerRPCCredentials(newBasicAuth(cfg.BasicAuthToken)))
	}
	opts = append(opts, cfg.dialOptions...)
	conn, err := grpc.NewClient(cfg.Addr, opts...)
	if err != nil {
		return nil, fmt.Errorf("battery: dial %s: %w", cfg.Addr, err)
	}
	return &Source{
		cfg:    cfg,
		conn:   conn,
		lease:  poolmgrv1alpha1.NewLeaseClient(conn),
		events: poolmgrv1alpha1.NewEventsClient(conn),
		admin:  poolmgrv1alpha1.NewPoolAdminClient(conn),
		sleep:  sleepCtx,
	}, nil
}

// Close closes the connection.
func (s *Source) Close() error { return s.conn.Close() }

func (s *Source) poolRef(pool string) *poolmgrv1alpha1.PoolRef {
	return &poolmgrv1alpha1.PoolRef{Name: pool, Namespace: s.cfg.Namespace}
}

// Acquire implements vmsource.Source. It reads the pool's heartbeat
// interval first, so nothing is claimed when the interval is unknown, then
// calls ClaimVM with requestID so a retry returns the same lease.
func (s *Source) Acquire(ctx context.Context, pool, requestID string) (vmsource.VM, error) {
	if requestID == "" {
		return nil, errors.New("battery: requestID must not be empty")
	}
	interval, err := s.poolInterval(ctx, pool)
	if err != nil {
		return nil, err
	}
	var resp *poolmgrv1alpha1.ClaimVMResponse
	err = s.call(ctx, "ClaimVM", retryClaim, func(ctx context.Context) error {
		var cerr error
		resp, cerr = s.lease.ClaimVM(ctx, &poolmgrv1alpha1.ClaimVMRequest{Pool: s.poolRef(pool), RequestId: requestID})
		return cerr
	})
	if err != nil {
		return nil, mapError("ClaimVM", vmsource.ErrPoolNotFound, err)
	}
	return &VM{
		source:   s,
		id:       resp.GetVmUid(),
		leaseID:  resp.GetLeaseId(),
		pool:     pool,
		host:     vmsource.HostInfo{Name: resp.GetHost().GetName(), Address: resp.GetHost().GetAddress()},
		interval: interval,
	}, nil
}

// Release implements vmsource.Source. A lease battery no longer knows is
// treated as released, which makes a retry after a lost response succeed.
func (s *Source) Release(ctx context.Context, v vmsource.VM) error {
	bv, ok := v.(*VM)
	if !ok || bv.source != s {
		return ErrForeignVM
	}
	err := s.call(ctx, "ReleaseVM", retryDefault, func(ctx context.Context) error {
		_, rerr := s.lease.ReleaseVM(ctx, &poolmgrv1alpha1.ReleaseVMRequest{LeaseId: bv.leaseID})
		return rerr
	})
	if err == nil {
		return nil
	}
	if isNotFound(err) {
		s.cfg.Logger.Debug("battery: lease already gone on release", "lease", bv.leaseID, "vm", bv.id)
		return nil
	}
	return mapError("ReleaseVM", nil, err)
}

// Leases implements vmsource.Source. battery can filter leases only by
// pool, so it lists everything and keeps the configured namespace.
func (s *Source) Leases(ctx context.Context) ([]vmsource.LeaseInfo, error) {
	var resp *poolmgrv1alpha1.ListLeasesResponse
	err := s.call(ctx, "ListLeases", retryDefault, func(ctx context.Context) error {
		var lerr error
		resp, lerr = s.lease.ListLeases(ctx, &poolmgrv1alpha1.ListLeasesRequest{})
		return lerr
	})
	if err != nil {
		return nil, mapError("ListLeases", nil, err)
	}
	out := []vmsource.LeaseInfo{}
	for _, l := range resp.GetLeases() {
		if l.GetPoolNamespace() != s.cfg.Namespace {
			continue
		}
		out = append(out, vmsource.LeaseInfo{
			LeaseID:   l.GetLeaseId(),
			VMID:      l.GetVmUid(),
			Pool:      l.GetPoolName(),
			RequestID: l.GetRequestId(),
			ClaimedAt: l.GetClaimedAt().AsTime(),
			ExpiresAt: l.GetExpiresAt().AsTime(),
		})
	}
	return out, nil
}
