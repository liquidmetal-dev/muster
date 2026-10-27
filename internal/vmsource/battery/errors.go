package battery

import (
	"errors"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/liquidmetal-dev/muster/internal/vmsource"
)

// ErrForeignVM means a VM passed to Release did not come from this source.
var ErrForeignVM = errors.New("battery: VM was not acquired from this source")

// ErrNoHeartbeatInterval means the pool has no heartbeat_interval, so the
// source cannot tell the holder how often to keep the lease alive.
var ErrNoHeartbeatInterval = errors.New("battery: pool has no heartbeat interval")

// mapError wraps a failed RPC's error with the matching vmsource sentinel:
// RESOURCE_EXHAUSTED becomes ErrExhausted and NOT_FOUND becomes notFound
// (ErrPoolNotFound for ClaimVM and GetPool, ErrLeaseNotFound for Heartbeat).
// Both wrap the original error too, so status.Code still works. Other
// errors are returned with the method name prefixed.
func mapError(method string, notFound error, err error) error {
	switch status.Code(err) {
	case codes.ResourceExhausted:
		return fmt.Errorf("%w: %w", vmsource.ErrExhausted, err)
	case codes.NotFound:
		if notFound != nil {
			return fmt.Errorf("%w: %w", notFound, err)
		}
	}
	return fmt.Errorf("battery: %s: %w", method, err)
}
