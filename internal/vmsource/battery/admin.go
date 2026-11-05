package battery

import (
	"context"
	"fmt"

	poolmgrv1alpha1 "github.com/liquidmetal-dev/battery/api/proto/poolmgr/v1alpha1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/liquidmetal-dev/muster/internal/vmsource"
)

// Admin manages the pools in the source's namespace through PoolAdmin and
// lists hosts through HostAdmin. It never calls a HostAdmin mutation
// (POOL-3). Calls share the Source's connection, timeout and retries.
type Admin struct {
	s *Source
}

// Admin returns the pool and host administration client.
func (s *Source) Admin() *Admin { return &Admin{s: s} }

// ListHosts returns every host registered in battery.
func (a *Admin) ListHosts(ctx context.Context) ([]*poolmgrv1alpha1.Host, error) {
	var resp *poolmgrv1alpha1.ListHostsResponse
	err := a.s.call(ctx, "ListHosts", retryDefault, func(ctx context.Context) error {
		var cerr error
		resp, cerr = a.s.hosts.ListHosts(ctx, &poolmgrv1alpha1.ListHostsRequest{})
		return cerr
	})
	if err != nil {
		return nil, mapError("ListHosts", nil, err)
	}
	hosts := make([]*poolmgrv1alpha1.Host, 0, len(resp.GetHosts()))
	for _, h := range resp.GetHosts() {
		hosts = append(hosts, h.GetHost())
	}
	return hosts, nil
}

// ListPools returns the spec of every pool in the namespace.
func (a *Admin) ListPools(ctx context.Context) ([]*poolmgrv1alpha1.PoolSpec, error) {
	var resp *poolmgrv1alpha1.ListPoolsResponse
	err := a.s.call(ctx, "ListPools", retryDefault, func(ctx context.Context) error {
		var cerr error
		resp, cerr = a.s.admin.ListPools(ctx, &poolmgrv1alpha1.ListPoolsRequest{Namespace: proto.String(a.s.cfg.Namespace)})
		return cerr
	})
	if err != nil {
		return nil, mapError("ListPools", nil, err)
	}
	specs := make([]*poolmgrv1alpha1.PoolSpec, 0, len(resp.GetPools()))
	for _, p := range resp.GetPools() {
		if p.GetSpec().GetNamespace() == a.s.cfg.Namespace {
			specs = append(specs, p.GetSpec())
		}
	}
	return specs, nil
}

// GetPool returns the spec of a pool. A missing pool is
// vmsource.ErrPoolNotFound.
func (a *Admin) GetPool(ctx context.Context, name string) (*poolmgrv1alpha1.PoolSpec, error) {
	var resp *poolmgrv1alpha1.Pool
	err := a.s.call(ctx, "GetPool", retryDefault, func(ctx context.Context) error {
		var cerr error
		resp, cerr = a.s.admin.GetPool(ctx, &poolmgrv1alpha1.GetPoolRequest{Ref: a.s.poolRef(name)})
		return cerr
	})
	if err != nil {
		return nil, mapError("GetPool", vmsource.ErrPoolNotFound, err)
	}
	return resp.GetSpec(), nil
}

// CreatePool creates a pool. An existing pool is vmsource.ErrPoolExists,
// which is also what a retry after a lost response returns.
func (a *Admin) CreatePool(ctx context.Context, spec *poolmgrv1alpha1.PoolSpec) error {
	err := a.s.call(ctx, "CreatePool", retryDefault, func(ctx context.Context) error {
		_, cerr := a.s.admin.CreatePool(ctx, &poolmgrv1alpha1.CreatePoolRequest{Spec: spec})
		return cerr
	})
	if status.Code(err) == codes.AlreadyExists {
		return fmt.Errorf("%w: %w", vmsource.ErrPoolExists, err)
	}
	if err != nil {
		return mapError("CreatePool", nil, err)
	}
	return nil
}

// UpdatePool replaces the spec of a pool. A missing pool is
// vmsource.ErrPoolNotFound.
func (a *Admin) UpdatePool(ctx context.Context, spec *poolmgrv1alpha1.PoolSpec) error {
	err := a.s.call(ctx, "UpdatePool", retryDefault, func(ctx context.Context) error {
		_, cerr := a.s.admin.UpdatePool(ctx, &poolmgrv1alpha1.UpdatePoolRequest{Spec: spec})
		return cerr
	})
	if err != nil {
		return mapError("UpdatePool", vmsource.ErrPoolNotFound, err)
	}
	return nil
}

// DeletePool deletes a pool without force, so a pool with leased VMs is
// kept and the error is vmsource.ErrPoolInUse. A missing pool is not an
// error: a retry after a lost response finds it gone.
func (a *Admin) DeletePool(ctx context.Context, name string) error {
	err := a.s.call(ctx, "DeletePool", retryDefault, func(ctx context.Context) error {
		_, cerr := a.s.admin.DeletePool(ctx, &poolmgrv1alpha1.DeletePoolRequest{Ref: a.s.poolRef(name)})
		return cerr
	})
	switch status.Code(err) {
	case codes.OK, codes.NotFound:
		return nil
	case codes.FailedPrecondition:
		return fmt.Errorf("%w: %w", vmsource.ErrPoolInUse, err)
	}
	return mapError("DeletePool", nil, err)
}
