package pools

import (
	poolmgrv1alpha1 "github.com/liquidmetal-dev/battery/api/proto/poolmgr/v1alpha1"
	"github.com/liquidmetal-dev/flintlock/api/types"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/liquidmetal-dev/muster/internal/config"
)

// Spec returns the battery pool spec that the configuration declares for
// p. The template is copied, so p.Template is not changed.
func Spec(namespace string, p config.Pool, d config.Defaults) *poolmgrv1alpha1.PoolSpec {
	var tpl *types.MicroVMSpec
	if p.Template != nil {
		tpl = proto.Clone(p.Template).(*types.MicroVMSpec)
		tpl.AllowGuestAgent = true
	}
	return &poolmgrv1alpha1.PoolSpec{
		Name:                     p.Name,
		Namespace:                namespace,
		MicrovmTemplate:          tpl,
		Size:                     int32(p.Size), //nolint:gosec // validated small positive
		FlintlockHosts:           p.FlintlockHosts,
		ReplenishmentStrategy:    strategy(p.Strategy()),
		CreateCommands:           p.CreateCommands,
		HookFailurePolicy:        poolmgrv1alpha1.HookFailurePolicy_DELETE_AND_REPLACE,
		HeartbeatInterval:        durationpb.New(d.HeartbeatInterval.Duration()),
		HeartbeatExpiryThreshold: durationpb.New(d.HeartbeatExpiryThreshold.Duration()),
	}
}

func strategy(s config.ReplenishmentStrategy) *poolmgrv1alpha1.ReplenishmentStrategy {
	switch s.Type {
	case config.StrategyMinSizeThreshold:
		return &poolmgrv1alpha1.ReplenishmentStrategy{
			Type:    poolmgrv1alpha1.ReplenishmentStrategyType_MIN_SIZE_THRESHOLD,
			MinSize: proto.Int32(int32(s.MinSize)), //nolint:gosec // validated to be at most size
		}
	case config.StrategyReplaceOnDelete:
		return &poolmgrv1alpha1.ReplenishmentStrategy{Type: poolmgrv1alpha1.ReplenishmentStrategyType_REPLACE_ON_DELETE}
	default:
		return &poolmgrv1alpha1.ReplenishmentStrategy{Type: poolmgrv1alpha1.ReplenishmentStrategyType_IMMEDIATE_ON_LEASE}
	}
}
