package battery

import (
	"context"
	"fmt"
	"time"

	poolmgrv1alpha1 "github.com/liquidmetal-dev/battery/api/proto/poolmgr/v1alpha1"

	"github.com/liquidmetal-dev/muster/internal/vmsource"
)

// poolInterval returns half of the pool's heartbeat_interval, read from
// PoolAdmin.GetPool. Acquire calls it before every ClaimVM and nothing is
// cached, so a pool update in battery takes effect on the next claim
// (requirement SESSION-6). A GetPool error is returned wrapped; a zero or
// unset interval returns ErrNoHeartbeatInterval.
func (s *Source) poolInterval(ctx context.Context, pool string) (time.Duration, error) {
	var resp *poolmgrv1alpha1.Pool
	err := s.call(ctx, "GetPool", retryDefault, func(ctx context.Context) error {
		var gerr error
		resp, gerr = s.admin.GetPool(ctx, &poolmgrv1alpha1.GetPoolRequest{Ref: s.poolRef(pool)})
		return gerr
	})
	if err != nil {
		return 0, mapError("GetPool", vmsource.ErrPoolNotFound, err)
	}
	interval := resp.GetSpec().GetHeartbeatInterval().AsDuration()
	if interval <= 0 {
		return 0, fmt.Errorf("%w: pool %s/%s", ErrNoHeartbeatInterval, s.cfg.Namespace, pool)
	}
	return interval / 2, nil
}
