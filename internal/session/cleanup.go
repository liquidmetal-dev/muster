package session

import (
	"context"
	"errors"
	"time"

	"github.com/liquidmetal-dev/muster/internal/vmsource"
)

const (
	cleanupBackoffBase  = time.Second
	cleanupBackoffLimit = time.Minute
)

// cleanup releases the VM, then deletes the runner. Each step is retried
// until it succeeds or ctx ends; it reports false when ctx ended first,
// and leaves the rest to startup reconciliation.
func (r *Registry) cleanup(ctx context.Context, s *session) bool {
	if s.vm != nil {
		ok := r.retry(ctx, s, "release VM", func() error {
			err := r.cfg.Source.Release(ctx, s.vm)
			if errors.Is(err, vmsource.ErrLeaseNotFound) {
				return nil
			}
			return err
		})
		if !ok {
			return false
		}
	}
	if s.runnerID != 0 {
		return r.retry(ctx, s, "delete runner", func() error {
			return r.cfg.GitHub.DeleteRunner(ctx, s.pool.scope, s.runnerID)
		})
	}
	return true
}

func (r *Registry) retry(ctx context.Context, s *session, what string, f func() error) bool {
	bo := newBackoff(cleanupBackoffBase, cleanupBackoffLimit, nil)
	for {
		err := f()
		if err == nil {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		s.logger().Warn(what, "err", err)
		if !r.sleep(ctx, bo.next()) {
			return false
		}
	}
}
