package pools_test

import (
	"testing"
	"time"

	poolmgrv1alpha1 "github.com/liquidmetal-dev/battery/api/proto/poolmgr/v1alpha1"
	"github.com/liquidmetal-dev/flintlock/api/types"
	"google.golang.org/protobuf/proto"

	"github.com/liquidmetal-dev/muster/internal/config"
	"github.com/liquidmetal-dev/muster/internal/pools"
)

func testDefaults() config.Defaults {
	return config.Defaults{
		HeartbeatInterval:        config.Duration(30 * time.Second),
		HeartbeatExpiryThreshold: config.Duration(90 * time.Second),
	}
}

func testPool(name string, size int, hosts ...string) config.Pool {
	return config.Pool{
		Name: name, Size: size, FlintlockHosts: hosts,
		CreateCommands: []string{"docker info"},
		Template:       &types.MicroVMSpec{Vcpu: 2, MemoryInMb: 4096},
	}
}

func TestSpecDefaults(t *testing.T) {
	p := testPool("a", 3, "h2", "h1")
	got := pools.Spec("muster", p, testDefaults())

	tpl := proto.Clone(p.Template).(*types.MicroVMSpec)
	tpl.AllowGuestAgent = true
	want := &poolmgrv1alpha1.PoolSpec{
		Name: "a", Namespace: "muster", Size: 3,
		FlintlockHosts:           []string{"h2", "h1"},
		CreateCommands:           []string{"docker info"},
		MicrovmTemplate:          tpl,
		ReplenishmentStrategy:    &poolmgrv1alpha1.ReplenishmentStrategy{Type: poolmgrv1alpha1.ReplenishmentStrategyType_IMMEDIATE_ON_LEASE},
		HookFailurePolicy:        poolmgrv1alpha1.HookFailurePolicy_DELETE_AND_REPLACE,
		HeartbeatInterval:        durationOf(30 * time.Second),
		HeartbeatExpiryThreshold: durationOf(90 * time.Second),
	}
	if !proto.Equal(got, want) {
		t.Fatalf("Spec() = %v\nwant %v", got, want)
	}
	if got.GetReplenishmentStrategy().MinSize != nil {
		t.Fatalf("min_size = %v, want nil", got.GetReplenishmentStrategy().GetMinSize())
	}
	if len(got.GetPreLeaseCommands()) != 0 {
		t.Fatalf("pre_lease_commands = %v, want empty", got.GetPreLeaseCommands())
	}
	if p.Template.AllowGuestAgent {
		t.Fatalf("Spec() changed the configured template")
	}
}

func TestSpecStrategies(t *testing.T) {
	tests := []struct {
		in      config.ReplenishmentStrategy
		typ     poolmgrv1alpha1.ReplenishmentStrategyType
		minSize *int32
	}{
		{config.ReplenishmentStrategy{Type: config.StrategyMinSizeThreshold, MinSize: 2}, poolmgrv1alpha1.ReplenishmentStrategyType_MIN_SIZE_THRESHOLD, proto.Int32(2)},
		{config.ReplenishmentStrategy{Type: config.StrategyReplaceOnDelete}, poolmgrv1alpha1.ReplenishmentStrategyType_REPLACE_ON_DELETE, nil},
		{config.ReplenishmentStrategy{Type: config.StrategyImmediateOnLease}, poolmgrv1alpha1.ReplenishmentStrategyType_IMMEDIATE_ON_LEASE, nil},
	}
	for _, tt := range tests {
		t.Run(tt.in.Type, func(t *testing.T) {
			p := testPool("a", 3, "h1")
			p.ReplenishmentStrategy = &tt.in
			got := pools.Spec("muster", p, testDefaults()).GetReplenishmentStrategy()
			want := &poolmgrv1alpha1.ReplenishmentStrategy{Type: tt.typ, MinSize: tt.minSize}
			if !proto.Equal(got, want) {
				t.Fatalf("strategy = %v, want %v", got, want)
			}
		})
	}
}
