package battery

import (
	"context"
	"math/rand/v2"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// retryPolicy names the status codes one call retries.
type retryPolicy struct{ codes map[codes.Code]bool }

var (
	// retryDefault retries transport failures and per-attempt timeouts.
	retryDefault = retryPolicy{codes: map[codes.Code]bool{codes.Unavailable: true, codes.DeadlineExceeded: true}}
	// retryClaim also retries ABORTED, which battery returns while a
	// replayed lease's VM is still being deleted.
	retryClaim = retryPolicy{codes: map[codes.Code]bool{codes.Unavailable: true, codes.DeadlineExceeded: true, codes.Aborted: true}}
)

// backoff produces full-jitter exponential delays that grow from base to limit.
type backoff struct {
	base, limit, next time.Duration
}

func newBackoff(base, limit time.Duration) *backoff {
	return &backoff{base: base, limit: limit, next: base}
}

// delay returns the next sleep and grows the schedule.
func (b *backoff) delay() time.Duration {
	d := time.Duration(rand.Int64N(int64(b.next)) + 1) //nolint:gosec // jitter, not security
	b.next *= 2
	if b.next > b.limit {
		b.next = b.limit
	}
	return d
}

// reset restarts the schedule from base.
func (b *backoff) reset() { b.next = b.base }

// sleepCtx waits d or until ctx ends.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// call runs fn up to cfg.MaxAttempts times, each attempt with a
// cfg.CallTimeout deadline. It retries only the codes in policy, sleeps a
// full-jitter backoff (base 100 ms, doubled, cap 2 s) through s.sleep, and
// stops with ctx.Err() when ctx ends. It returns the last error.
func (s *Source) call(ctx context.Context, method string, policy retryPolicy, fn func(ctx context.Context) error) error {
	bo := newBackoff(100*time.Millisecond, 2*time.Second)
	var err error
	for attempt := 1; ; attempt++ {
		err = s.attempt(ctx, fn)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		code := status.Code(err)
		if !policy.codes[code] || attempt >= s.cfg.MaxAttempts {
			return err
		}
		d := bo.delay()
		s.cfg.Logger.Debug("battery: retrying call", "method", method, "attempt", attempt, "code", code.String(), "after", d)
		if serr := s.sleep(ctx, d); serr != nil {
			return serr
		}
	}
}

func (s *Source) attempt(ctx context.Context, fn func(ctx context.Context) error) error {
	actx, cancel := context.WithTimeout(ctx, s.cfg.CallTimeout)
	defer cancel()
	return fn(actx)
}
