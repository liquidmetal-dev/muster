package battery

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/liquidmetal-dev/muster/internal/vmsource"
	"github.com/liquidmetal-dev/muster/internal/vmsource/battery/fakepoolmgr"
)

func TestRetryOnUnavailable(t *testing.T) {
	fake := fakepoolmgr.New()
	seedPool(fake, "a", 1)
	src := newTestSource(t, fake, nil)
	fake.FailNext("ClaimVM", codes.Unavailable)
	acquire(t, src, "a", "req-1")
	if n := fake.CallCount("ClaimVM"); n != 2 {
		t.Fatalf("CallCount(ClaimVM) = %d, want 2", n)
	}
}

func TestRetryClaimOnAborted(t *testing.T) {
	fake := fakepoolmgr.New()
	seedPool(fake, "a", 1)
	src := newTestSource(t, fake, nil)
	fake.FailNext("ClaimVM", codes.Aborted)
	acquire(t, src, "a", "req-1")
	if n := fake.CallCount("ClaimVM"); n != 2 {
		t.Fatalf("CallCount(ClaimVM) = %d, want 2", n)
	}
}

func TestRetryReleaseOnUnavailable(t *testing.T) {
	fake := fakepoolmgr.New()
	seedPool(fake, "a", 1)
	src := newTestSource(t, fake, nil)
	vm := acquire(t, src, "a", "req-1")
	fake.FailNext("ReleaseVM", codes.Unavailable)
	if err := src.Release(context.Background(), vm); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if n := fake.CallCount("ReleaseVM"); n != 2 {
		t.Fatalf("CallCount(ReleaseVM) = %d, want 2", n)
	}
}

func TestRetryBudgetExhausted(t *testing.T) {
	fake := fakepoolmgr.New()
	seedPool(fake, "a", 1)
	src := newTestSource(t, fake, func(c *Config) { c.MaxAttempts = 2 })
	fake.FailNext("ClaimVM", codes.Unavailable)
	fake.FailNext("ClaimVM", codes.Unavailable)
	_, err := src.Acquire(context.Background(), "a", "req-1")
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("Acquire error = %v, want Unavailable after the budget", err)
	}
	if n := fake.CallCount("ClaimVM"); n != 2 {
		t.Fatalf("CallCount(ClaimVM) = %d, want 2", n)
	}
}

func TestDefaultMaxAttemptsIsThree(t *testing.T) {
	fake := fakepoolmgr.New()
	seedPool(fake, "a", 1)
	src := newTestSource(t, fake, nil) // MaxAttempts left at zero: the default applies
	for range 2 {
		fake.FailNext("ClaimVM", codes.Unavailable)
	}
	acquire(t, src, "a", "req-1") // third attempt succeeds
	if n := fake.CallCount("ClaimVM"); n != 3 {
		t.Fatalf("CallCount(ClaimVM) = %d, want 3", n)
	}
	for range 3 {
		fake.FailNext("ClaimVM", codes.Unavailable)
	}
	if _, err := src.Acquire(context.Background(), "a", "req-2"); status.Code(err) != codes.Unavailable {
		t.Fatalf("Acquire error = %v, want Unavailable after three failed attempts", err)
	}
	if n := fake.CallCount("ClaimVM"); n != 6 {
		t.Fatalf("CallCount(ClaimVM) = %d, want 6 (three more attempts, no fourth)", n)
	}
}

func TestNoRetryOnHeartbeatNotFound(t *testing.T) {
	fake := fakepoolmgr.New()
	seedPool(fake, "a", 1)
	src := newTestSource(t, fake, nil)
	vm := acquire(t, src, "a", "req-1")
	fake.FailNext("Heartbeat", codes.NotFound)
	if err := vm.KeepAlive(context.Background()); !errors.Is(err, vmsource.ErrLeaseNotFound) {
		t.Fatalf("KeepAlive error = %v, want ErrLeaseNotFound", err)
	}
	if n := fake.CallCount("Heartbeat"); n != 1 {
		t.Fatalf("CallCount(Heartbeat) = %d, want 1", n)
	}
}

func TestRetryStopsOnContextCancel(t *testing.T) {
	fake := fakepoolmgr.New()
	seedPool(fake, "a", 1)
	src := newTestSource(t, fake, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src.sleep = func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	}
	fake.FailNext("ClaimVM", codes.Unavailable)
	_, err := src.Acquire(ctx, "a", "req-1")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire error = %v, want context.Canceled", err)
	}
	if n := fake.CallCount("ClaimVM"); n != 1 {
		t.Fatalf("CallCount(ClaimVM) = %d, want 1", n)
	}
}

func TestCallTimeout(t *testing.T) {
	fake := fakepoolmgr.New()
	seedPool(fake, "a", 1)
	src := newTestSource(t, fake, func(c *Config) { c.CallTimeout = 50 * time.Millisecond; c.MaxAttempts = 1 })
	fake.Stall("ClaimVM", 500*time.Millisecond)
	startAt := time.Now()
	_, err := src.Acquire(context.Background(), "a", "req-1")
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("Acquire error = %v, want DeadlineExceeded", err)
	}
	if took := time.Since(startAt); took > 200*time.Millisecond {
		t.Fatalf("took %v, want under 200ms", took)
	}
}
