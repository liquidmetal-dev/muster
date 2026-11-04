package fakepoolmgr_test

import (
	"context"
	"strings"
	"testing"
	"time"

	poolmgrv1alpha1 "github.com/liquidmetal-dev/battery/api/proto/poolmgr/v1alpha1"
	"github.com/liquidmetal-dev/flintlock/api/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/liquidmetal-dev/muster/internal/vmsource/battery/fakepoolmgr"
)

func spec(name, ns string, size int32, hosts ...string) *poolmgrv1alpha1.PoolSpec {
	return &poolmgrv1alpha1.PoolSpec{
		Name: name, Namespace: ns, Size: size, FlintlockHosts: hosts,
		MicrovmTemplate:          &types.MicroVMSpec{Vcpu: 2, MemoryInMb: 1024},
		ReplenishmentStrategy:    &poolmgrv1alpha1.ReplenishmentStrategy{Type: poolmgrv1alpha1.ReplenishmentStrategyType_IMMEDIATE_ON_LEASE},
		HookFailurePolicy:        poolmgrv1alpha1.HookFailurePolicy_DELETE_AND_REPLACE,
		HeartbeatInterval:        durationpb.New(30 * time.Second),
		HeartbeatExpiryThreshold: durationpb.New(90 * time.Second),
		CreateCommands:           []string{"docker info"},
	}
}

func TestListHosts(t *testing.T) {
	fake, c := start(t)
	fake.SeedHost(fakepoolmgr.Host{Name: "h1", Address: "10.0.0.1:9090"})
	fake.SeedHost(fakepoolmgr.Host{Name: "h2", Address: "10.0.0.2:9090"})
	resp, err := c.hosts.ListHosts(context.Background(), &poolmgrv1alpha1.ListHostsRequest{})
	if err != nil {
		t.Fatalf("ListHosts: %v", err)
	}
	got := map[string]string{}
	for _, h := range resp.GetHosts() {
		got[h.GetHost().GetName()] = h.GetHost().GetAddress()
	}
	if len(got) != 2 || got["h1"] != "10.0.0.1:9090" || got["h2"] != "10.0.0.2:9090" {
		t.Fatalf("hosts = %v", got)
	}
	if fake.CallCount("ListHosts") != 1 {
		t.Fatalf("ListHosts not recorded")
	}
}

func TestCreatePool(t *testing.T) {
	ctx := context.Background()
	fake, c := start(t)
	fake.SeedHost(fakepoolmgr.Host{Name: "h1", Address: "10.0.0.1:9090"})
	in := spec("a", "ns", 2, "h1")
	if _, err := c.admin.CreatePool(ctx, &poolmgrv1alpha1.CreatePoolRequest{Spec: in}); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	got, err := c.admin.GetPool(ctx, &poolmgrv1alpha1.GetPoolRequest{Ref: ref("a", "ns")})
	if err != nil {
		t.Fatalf("GetPool: %v", err)
	}
	want := proto.Clone(in).(*poolmgrv1alpha1.PoolSpec)
	want.MicrovmTemplate.AllowGuestAgent = true
	if !proto.Equal(got.GetSpec(), want) {
		t.Fatalf("stored spec = %v, want %v", got.GetSpec(), want)
	}
	if stored, ok := fake.PoolSpec("ns", "a"); !ok || !proto.Equal(stored, want) {
		t.Fatalf("PoolSpec = %v, %v", stored, ok)
	}

	_, err = c.admin.CreatePool(ctx, &poolmgrv1alpha1.CreatePoolRequest{Spec: in})
	wantCode(t, err, codes.AlreadyExists)

	_, err = c.lease.ClaimVM(ctx, &poolmgrv1alpha1.ClaimVMRequest{Pool: ref("a", "ns"), RequestId: "r1"})
	wantCode(t, err, codes.ResourceExhausted)

	_, err = c.admin.CreatePool(ctx, &poolmgrv1alpha1.CreatePoolRequest{Spec: spec("b", "ns", 1, "h1", "host-x")})
	wantCode(t, err, codes.InvalidArgument)
	if !strings.Contains(err.Error(), "unknown hosts host-x") {
		t.Fatalf("error = %v, want unknown hosts host-x", err)
	}
}

func TestCreatePoolInvalid(t *testing.T) {
	fake, c := start(t)
	fake.SeedHost(fakepoolmgr.Host{Name: "h1", Address: "10.0.0.1:9090"})
	tests := []struct {
		name   string
		mutate func(s *poolmgrv1alpha1.PoolSpec)
		code   codes.Code
	}{
		{"no template", func(s *poolmgrv1alpha1.PoolSpec) { s.MicrovmTemplate = nil }, codes.InvalidArgument},
		{"no strategy", func(s *poolmgrv1alpha1.PoolSpec) { s.ReplenishmentStrategy = nil }, codes.InvalidArgument},
		{"unspecified strategy", func(s *poolmgrv1alpha1.PoolSpec) {
			s.ReplenishmentStrategy = &poolmgrv1alpha1.ReplenishmentStrategy{}
		}, codes.InvalidArgument},
		{"min_size missing", func(s *poolmgrv1alpha1.PoolSpec) {
			s.ReplenishmentStrategy = &poolmgrv1alpha1.ReplenishmentStrategy{Type: poolmgrv1alpha1.ReplenishmentStrategyType_MIN_SIZE_THRESHOLD}
		}, codes.InvalidArgument},
		{"unspecified hook policy", func(s *poolmgrv1alpha1.PoolSpec) {
			s.HookFailurePolicy = poolmgrv1alpha1.HookFailurePolicy_HOOK_FAILURE_POLICY_UNSPECIFIED
		}, codes.InvalidArgument},
		{"no heartbeat interval", func(s *poolmgrv1alpha1.PoolSpec) { s.HeartbeatInterval = nil }, codes.Internal},
		{"no expiry threshold", func(s *poolmgrv1alpha1.PoolSpec) { s.HeartbeatExpiryThreshold = nil }, codes.Internal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := spec("a", "ns", 1, "h1")
			tt.mutate(s)
			_, err := c.admin.CreatePool(context.Background(), &poolmgrv1alpha1.CreatePoolRequest{Spec: s})
			wantCode(t, err, tt.code)
		})
	}
}

func TestUpdatePool(t *testing.T) {
	ctx := context.Background()
	fake, c := start(t)
	fake.SeedHost(fakepoolmgr.Host{Name: "h1", Address: "10.0.0.1:9090"})
	_, err := c.admin.UpdatePool(ctx, &poolmgrv1alpha1.UpdatePoolRequest{Spec: spec("a", "ns", 1, "h1")})
	wantCode(t, err, codes.NotFound)

	fake.AddPool(pool("a", "ns", 1))
	claim(t, c, "a", "ns", "req-1")
	if _, err := c.admin.UpdatePool(ctx, &poolmgrv1alpha1.UpdatePoolRequest{Spec: spec("a", "ns", 3, "h1")}); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	got, _ := fake.PoolSpec("ns", "a")
	if got.GetSize() != 3 {
		t.Fatalf("size = %d, want 3", got.GetSize())
	}
	if n := len(fake.Leases()); n != 1 {
		t.Fatalf("leases = %d, want 1 kept", n)
	}

	_, err = c.admin.UpdatePool(ctx, &poolmgrv1alpha1.UpdatePoolRequest{Spec: spec("a", "ns", 3, "host-x")})
	wantCode(t, err, codes.InvalidArgument)
}

func TestDeletePool(t *testing.T) {
	ctx := context.Background()
	fake, c := start(t)
	_, err := c.admin.DeletePool(ctx, &poolmgrv1alpha1.DeletePoolRequest{Ref: ref("a", "ns")})
	wantCode(t, err, codes.NotFound)

	fake.AddPool(pool("a", "ns", 2))
	resp := claim(t, c, "a", "ns", "req-1")
	_, err = c.admin.DeletePool(ctx, &poolmgrv1alpha1.DeletePoolRequest{Ref: ref("a", "ns")})
	wantCode(t, err, codes.FailedPrecondition)

	if _, err := c.lease.ReleaseVM(ctx, &poolmgrv1alpha1.ReleaseVMRequest{LeaseId: resp.GetLeaseId()}); err != nil {
		t.Fatalf("ReleaseVM: %v", err)
	}
	if _, err := c.admin.DeletePool(ctx, &poolmgrv1alpha1.DeletePoolRequest{Ref: ref("a", "ns")}); err != nil {
		t.Fatalf("DeletePool: %v", err)
	}
	_, err = c.admin.GetPool(ctx, &poolmgrv1alpha1.GetPoolRequest{Ref: ref("a", "ns")})
	wantCode(t, err, codes.NotFound)
}

func TestListPools(t *testing.T) {
	fake, c := start(t)
	fake.AddPool(pool("a", "ns", 0))
	fake.AddPool(pool("b", "ns", 0))
	fake.AddPool(pool("a", "other", 0))
	names := func(req *poolmgrv1alpha1.ListPoolsRequest) []string {
		resp, err := c.admin.ListPools(context.Background(), req)
		if err != nil {
			t.Fatalf("ListPools: %v", err)
		}
		var out []string
		for _, p := range resp.GetPools() {
			out = append(out, p.GetSpec().GetNamespace()+"/"+p.GetSpec().GetName())
		}
		return out
	}
	if got := names(&poolmgrv1alpha1.ListPoolsRequest{Namespace: proto.String("ns")}); strings.Join(got, ",") != "ns/a,ns/b" {
		t.Fatalf("ns pools = %v", got)
	}
	if got := names(&poolmgrv1alpha1.ListPoolsRequest{}); len(got) != 3 {
		t.Fatalf("all pools = %v", got)
	}
}
