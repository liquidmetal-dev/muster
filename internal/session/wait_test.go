package session_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	"github.com/liquidmetal-dev/muster/internal/session"
)

// adoptBusy adopts an in-flight session whose runner GitHub reports busy.
func (e *env) adoptBusy() string {
	e.t.Helper()
	a := e.inFlight("muster-muster-linux-" + adoptID)
	a.Runner.Busy = true
	if err := e.Registry.Adopt(context.Background(), a); err != nil {
		e.t.Fatalf("Adopt: %v", err)
	}
	return a.Runner.Name
}

// waitAsync runs WaitNotBusy and delivers its result.
func (e *env) waitAsync(ctx context.Context) <-chan error {
	done := make(chan error, 1)
	go func() { done <- e.Registry.WaitNotBusy(ctx) }()
	return done
}

func notReturned(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("WaitNotBusy returned %v while a busy session is live", err)
	case <-time.After(50 * time.Millisecond):
	}
}

func returned(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("WaitNotBusy did not return")
		return nil
	}
}

func TestWaitNotBusyNoSessions(t *testing.T) {
	e := newEnv(t)
	e.start()
	if err := returned(t, e.waitAsync(context.Background())); err != nil {
		t.Fatalf("WaitNotBusy = %v, want nil", err)
	}
}

func TestWaitNotBusyIdleSession(t *testing.T) {
	e := newEnv(t)
	e.start()
	a := e.inFlight("muster-muster-linux-" + adoptID)
	if err := e.Registry.Adopt(context.Background(), a); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if err := returned(t, e.waitAsync(context.Background())); err != nil {
		t.Fatalf("WaitNotBusy = %v, want nil", err)
	}
	if st := e.state(adoptID); st != session.Running {
		t.Fatalf("idle session state = %v, want RUNNING", st)
	}
}

func TestWaitNotBusyReturnsWhenBusyEnds(t *testing.T) {
	e := newEnv(t)
	e.start()
	name := e.adoptBusy()
	done := e.waitAsync(context.Background())
	notReturned(t, done)
	e.Registry.JobCompleted(name)
	e.drive(5*time.Second, func() bool { return e.state(adoptID) == session.Released })
	if err := returned(t, done); err != nil {
		t.Fatalf("WaitNotBusy = %v, want nil", err)
	}
}

func TestWaitNotBusyCtxEnds(t *testing.T) {
	e := newEnv(t)
	e.start()
	e.adoptBusy()
	ctx, cancel := context.WithCancel(context.Background())
	done := e.waitAsync(ctx)
	notReturned(t, done)
	cancel()
	if err := returned(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitNotBusy = %v, want context.Canceled", err)
	}
}

func TestWaitNotBusyCleanupSlow(t *testing.T) {
	e := newEnv(t)
	e.start()
	name := e.adoptBusy()
	done := e.waitAsync(context.Background())
	e.Pool.FailNext("ReleaseVM", codes.PermissionDenied)
	e.Registry.JobCompleted(name)
	e.drive(time.Second, func() bool { return e.Pool.CallCount("ReleaseVM") == 1 })
	notReturned(t, done)
	if st := e.state(adoptID); st != session.Ended {
		t.Fatalf("state during cleanup = %v, want ENDED", st)
	}
	e.drive(time.Second, func() bool { return e.state(adoptID) == session.Released })
	if err := returned(t, done); err != nil {
		t.Fatalf("WaitNotBusy = %v, want nil", err)
	}
	if n := e.Pool.CallCount("ReleaseVM"); n != 2 {
		t.Fatalf("ReleaseVM calls = %d, want 2", n)
	}
}
