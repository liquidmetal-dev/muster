package battery

import (
	"context"
	"errors"
	"testing"
	"time"

	poolmgrv1alpha1 "github.com/liquidmetal-dev/battery/api/proto/poolmgr/v1alpha1"
	"github.com/liquidmetal-dev/flintlock/api/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/liquidmetal-dev/muster/internal/vmsource"
	"github.com/liquidmetal-dev/muster/internal/vmsource/battery/fakepoolmgr"
)

func adminSpec(name string, size int32) *poolmgrv1alpha1.PoolSpec {
	return &poolmgrv1alpha1.PoolSpec{
		Name: name, Namespace: "muster", Size: size, FlintlockHosts: []string{"h1"},
		MicrovmTemplate:          &types.MicroVMSpec{Vcpu: 2, MemoryInMb: 1024},
		ReplenishmentStrategy:    &poolmgrv1alpha1.ReplenishmentStrategy{Type: poolmgrv1alpha1.ReplenishmentStrategyType_IMMEDIATE_ON_LEASE},
		HookFailurePolicy:        poolmgrv1alpha1.HookFailurePolicy_DELETE_AND_REPLACE,
		HeartbeatInterval:        durationpb.New(30 * time.Second),
		HeartbeatExpiryThreshold: durationpb.New(90 * time.Second),
	}
}

func newTestAdmin(t *testing.T) (*fakepoolmgr.Server, *Admin) {
	t.Helper()
	fake := fakepoolmgr.New()
	fake.SeedHost(fakepoolmgr.Host{Name: "h1", Address: "10.0.0.1:9090"})
	src := newTestSource(t, fake, nil)
	src.sleep = func(context.Context, time.Duration) error { return nil }
	return fake, src.Admin()
}

func TestAdminRoundTrip(t *testing.T) {
	ctx := context.Background()
	fake, a := newTestAdmin(t)
	fake.AddPool(fakepoolmgr.Pool{Name: "x", Namespace: "other"})

	if err := a.CreatePool(ctx, adminSpec("a", 2)); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	got, err := a.GetPool(ctx, "a")
	if err != nil {
		t.Fatalf("GetPool: %v", err)
	}
	want := adminSpec("a", 2)
	want.MicrovmTemplate.AllowGuestAgent = true
	if !proto.Equal(got, want) {
		t.Fatalf("GetPool = %v, want %v", got, want)
	}
	if err := a.UpdatePool(ctx, adminSpec("a", 3)); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	pools, err := a.ListPools(ctx)
	if err != nil {
		t.Fatalf("ListPools: %v", err)
	}
	if len(pools) != 1 || pools[0].GetName() != "a" || pools[0].GetSize() != 3 {
		t.Fatalf("ListPools = %v, want only muster/a with size 3", pools)
	}
	hosts, err := a.ListHosts(ctx)
	if err != nil {
		t.Fatalf("ListHosts: %v", err)
	}
	if len(hosts) != 1 || hosts[0].GetName() != "h1" || hosts[0].GetAddress() != "10.0.0.1:9090" {
		t.Fatalf("ListHosts = %v", hosts)
	}
	if err := a.DeletePool(ctx, "a"); err != nil {
		t.Fatalf("DeletePool: %v", err)
	}
	if _, ok := fake.PoolSpec("muster", "a"); ok {
		t.Fatalf("pool still exists after DeletePool")
	}
}

func TestAdminErrors(t *testing.T) {
	ctx := context.Background()
	fake, a := newTestAdmin(t)
	if err := a.CreatePool(ctx, adminSpec("a", 1)); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	if err := a.CreatePool(ctx, adminSpec("a", 1)); !errors.Is(err, vmsource.ErrPoolExists) {
		t.Fatalf("second CreatePool = %v, want ErrPoolExists", err)
	}
	if _, err := a.GetPool(ctx, "nope"); !errors.Is(err, vmsource.ErrPoolNotFound) {
		t.Fatalf("GetPool missing = %v, want ErrPoolNotFound", err)
	}
	if err := a.UpdatePool(ctx, adminSpec("nope", 1)); !errors.Is(err, vmsource.ErrPoolNotFound) {
		t.Fatalf("UpdatePool missing = %v, want ErrPoolNotFound", err)
	}
	if err := a.DeletePool(ctx, "nope"); err != nil {
		t.Fatalf("DeletePool missing = %v, want nil", err)
	}
	seedPool(fake, "busy", 1)
	acquire(t, a.s, "busy", "req-1")
	if err := a.DeletePool(ctx, "busy"); !errors.Is(err, vmsource.ErrPoolInUse) {
		t.Fatalf("DeletePool busy = %v, want ErrPoolInUse", err)
	}
}

func TestAdminDeleteLostResponse(t *testing.T) {
	ctx := context.Background()
	fake, a := newTestAdmin(t)
	seedPool(fake, "a", 0)
	fake.LoseNext("DeletePool")
	if err := a.DeletePool(ctx, "a"); err != nil {
		t.Fatalf("DeletePool = %v, want nil after a lost response", err)
	}
	if _, ok := fake.PoolSpec("muster", "a"); ok {
		t.Fatalf("pool still exists")
	}
}

func TestAdminRetriesUnavailable(t *testing.T) {
	fake, a := newTestAdmin(t)
	fake.FailNext("ListHosts", codes.Unavailable)
	if _, err := a.ListHosts(context.Background()); err != nil {
		t.Fatalf("ListHosts: %v", err)
	}
	if n := fake.CallCount("ListHosts"); n != 2 {
		t.Fatalf("ListHosts calls = %d, want 2", n)
	}
}

func TestAdminNeverForces(t *testing.T) {
	ctx := context.Background()
	fake, a := newTestAdmin(t)
	seedPool(fake, "a", 0)
	seedPool(fake, "b", 0)
	_ = a.DeletePool(ctx, "a")
	_ = a.DeletePool(ctx, "b")
	n := 0
	for _, c := range fake.Calls() {
		if c.Method != "DeletePool" {
			continue
		}
		n++
		if c.Req.(*poolmgrv1alpha1.DeletePoolRequest).GetForce() {
			t.Fatalf("DeletePool sent force=true")
		}
	}
	if n != 2 {
		t.Fatalf("DeletePool calls = %d, want 2", n)
	}
}
