package battery

import (
	"context"
	"time"
)

// poolInterval returns half of the pool's heartbeat interval. Task 5 reads
// it from PoolAdmin.GetPool; until then it is a fixed value.
func (s *Source) poolInterval(context.Context, string) (time.Duration, error) {
	return 10 * time.Second, nil
}
