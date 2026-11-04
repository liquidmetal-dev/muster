package fakepoolmgr

import (
	"context"
	"slices"
	"strings"

	poolmgrv1alpha1 "github.com/liquidmetal-dev/battery/api/proto/poolmgr/v1alpha1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
)

// PoolSpec returns the stored spec of a pool, as GetPool would.
func (s *Server) PoolSpec(namespace, name string) (*poolmgrv1alpha1.PoolSpec, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pools[poolKey{namespace, name}]
	if !ok {
		return nil, false
	}
	return specOf(p), true
}

func specOf(p Pool) *poolmgrv1alpha1.PoolSpec {
	if p.Spec != nil {
		return proto.Clone(p.Spec).(*poolmgrv1alpha1.PoolSpec)
	}
	return &poolmgrv1alpha1.PoolSpec{
		Name: p.Name, Namespace: p.Namespace,
		HeartbeatInterval:        durationpb.New(p.HeartbeatInterval),
		HeartbeatExpiryThreshold: durationpb.New(p.HeartbeatExpiryThreshold),
	}
}

// GetPool implements poolmgrv1alpha1.PoolAdminServer.
func (s *Server) GetPool(_ context.Context, req *poolmgrv1alpha1.GetPoolRequest) (*poolmgrv1alpha1.Pool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, key, ok := s.poolOf(req.GetRef())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "pool %s/%s not found", key.ns, key.name)
	}
	return &poolmgrv1alpha1.Pool{Spec: specOf(p)}, nil
}

// ListPools implements poolmgrv1alpha1.PoolAdminServer. Pools are ordered
// by namespace, then name.
func (s *Server) ListPools(_ context.Context, req *poolmgrv1alpha1.ListPoolsRequest) (*poolmgrv1alpha1.ListPoolsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]poolKey, 0, len(s.pools))
	for k := range s.pools {
		if req.Namespace == nil || k.ns == req.GetNamespace() {
			keys = append(keys, k)
		}
	}
	slices.SortFunc(keys, func(a, b poolKey) int {
		if c := strings.Compare(a.ns, b.ns); c != 0 {
			return c
		}
		return strings.Compare(a.name, b.name)
	})
	resp := &poolmgrv1alpha1.ListPoolsResponse{}
	for _, k := range keys {
		resp.Pools = append(resp.Pools, &poolmgrv1alpha1.Pool{Spec: specOf(s.pools[k])})
	}
	return resp, nil
}

// CreatePool implements poolmgrv1alpha1.PoolAdminServer.
func (s *Server) CreatePool(_ context.Context, req *poolmgrv1alpha1.CreatePoolRequest) (*poolmgrv1alpha1.Pool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	spec, err := s.checkSpec(req.GetSpec())
	if err != nil {
		return nil, err
	}
	key := poolKey{spec.GetNamespace(), spec.GetName()}
	if _, exists := s.pools[key]; exists {
		return nil, status.Errorf(codes.AlreadyExists, "pool %s/%s already exists", key.ns, key.name)
	}
	s.pools[key] = poolFromSpec(spec, nil)
	return &poolmgrv1alpha1.Pool{Spec: specOf(s.pools[key])}, nil
}

// UpdatePool implements poolmgrv1alpha1.PoolAdminServer. VMs and leases
// are kept.
func (s *Server) UpdatePool(_ context.Context, req *poolmgrv1alpha1.UpdatePoolRequest) (*poolmgrv1alpha1.Pool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	spec, err := s.checkSpec(req.GetSpec())
	if err != nil {
		return nil, err
	}
	key := poolKey{spec.GetNamespace(), spec.GetName()}
	old, exists := s.pools[key]
	if !exists {
		return nil, status.Errorf(codes.NotFound, "pool %s/%s not found", key.ns, key.name)
	}
	s.pools[key] = poolFromSpec(spec, old.Host)
	return &poolmgrv1alpha1.Pool{Spec: specOf(s.pools[key])}, nil
}

// DeletePool implements poolmgrv1alpha1.PoolAdminServer. Without force, a
// pool with a leased VM is kept.
func (s *Server) DeletePool(_ context.Context, req *poolmgrv1alpha1.DeletePoolRequest) (*emptypb.Empty, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, key, ok := s.poolOf(req.GetRef())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "pool %s/%s not found", key.ns, key.name)
	}
	if !req.GetForce() {
		for _, l := range s.leases {
			if l.pool == key {
				return nil, status.Errorf(codes.FailedPrecondition, "pool %s/%s has leased VMs, release them or delete with force", key.ns, key.name)
			}
		}
	}
	for id, l := range s.leases {
		if l.pool == key {
			delete(s.leases, id)
			delete(s.byRequest, l.requestID)
		}
	}
	for uid, v := range s.vms {
		if v.pool == key {
			delete(s.vms, uid)
		}
	}
	delete(s.pools, key)
	return &emptypb.Empty{}, nil
}

// checkSpec mirrors the real server's validatePoolSpec, checkFlintlockHosts
// and store checks, and returns a copy with allow_guest_agent forced on.
func (s *Server) checkSpec(in *poolmgrv1alpha1.PoolSpec) (*poolmgrv1alpha1.PoolSpec, error) {
	switch {
	case in.GetName() == "":
		return nil, status.Error(codes.InvalidArgument, "spec.name is required")
	case in.GetNamespace() == "":
		return nil, status.Error(codes.InvalidArgument, "spec.namespace is required")
	}
	rs := in.GetReplenishmentStrategy()
	switch rs.GetType() {
	case poolmgrv1alpha1.ReplenishmentStrategyType_IMMEDIATE_ON_LEASE, poolmgrv1alpha1.ReplenishmentStrategyType_REPLACE_ON_DELETE:
	case poolmgrv1alpha1.ReplenishmentStrategyType_MIN_SIZE_THRESHOLD:
		if rs.MinSize == nil || rs.GetMinSize() <= 0 {
			return nil, status.Error(codes.InvalidArgument, "spec.replenishment_strategy: min_size is required")
		}
	default:
		return nil, status.Errorf(codes.InvalidArgument, "spec.replenishment_strategy: unknown strategy %v", rs.GetType())
	}
	switch in.GetHookFailurePolicy() {
	case poolmgrv1alpha1.HookFailurePolicy_DELETE_AND_REPLACE, poolmgrv1alpha1.HookFailurePolicy_QUARANTINE:
	default:
		return nil, status.Errorf(codes.InvalidArgument, "spec.hook_failure_policy: invalid value %v", in.GetHookFailurePolicy())
	}
	if in.GetMicrovmTemplate() == nil {
		return nil, status.Error(codes.InvalidArgument, "spec.microvm_template is required")
	}
	var unknown []string
	for _, h := range in.GetFlintlockHosts() {
		if _, ok := s.hosts[h]; !ok && !slices.Contains(unknown, h) {
			unknown = append(unknown, h)
		}
	}
	if len(unknown) > 0 {
		return nil, status.Errorf(codes.InvalidArgument, "spec.flintlock_hosts: unknown hosts %s; add them with HostAdmin.AddHost first", strings.Join(unknown, ", "))
	}
	if in.GetHeartbeatInterval() == nil || in.GetHeartbeatExpiryThreshold() == nil {
		return nil, status.Error(codes.Internal, "create pool: store: heartbeat_interval and heartbeat_expiry_threshold are required")
	}
	spec := proto.Clone(in).(*poolmgrv1alpha1.PoolSpec)
	spec.MicrovmTemplate.AllowGuestAgent = true
	return spec, nil
}

func poolFromSpec(spec *poolmgrv1alpha1.PoolSpec, host *poolmgrv1alpha1.HostInfo) Pool {
	if host == nil {
		host = &poolmgrv1alpha1.HostInfo{Name: "host-a", Address: "127.0.0.1:9090"}
	}
	return Pool{
		Name: spec.GetName(), Namespace: spec.GetNamespace(),
		HeartbeatInterval:        spec.GetHeartbeatInterval().AsDuration(),
		HeartbeatExpiryThreshold: spec.GetHeartbeatExpiryThreshold().AsDuration(),
		Host:                     host,
		Spec:                     spec,
	}
}
