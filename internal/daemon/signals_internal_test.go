package daemon

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"
)

func done(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return true
	case <-time.After(d):
		return false
	}
}

func TestStopContextsFirstSignalStopsOnly(t *testing.T) {
	sig := make(chan os.Signal, 2)
	stop, force, cancel := stopContexts(context.Background(), sig)
	defer cancel()
	sig <- syscall.SIGTERM
	if !done(stop, time.Second) {
		t.Fatal("stop is not done after the first signal")
	}
	if done(force, 50*time.Millisecond) {
		t.Fatal("force is done after one signal")
	}
}

func TestSecondSignalCancelsForce(t *testing.T) {
	sig := make(chan os.Signal, 2)
	stop, force, cancel := stopContexts(context.Background(), sig)
	defer cancel()
	sig <- syscall.SIGTERM
	sig <- os.Interrupt
	if !done(stop, time.Second) || !done(force, time.Second) {
		t.Fatal("stop and force are not both done after two signals")
	}
}

func TestStopContextsParentCancel(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	stop, force, cancel := stopContexts(parent, make(chan os.Signal))
	defer cancel()
	cancelParent()
	if !done(stop, time.Second) || !done(force, time.Second) {
		t.Fatal("stop and force are not both done after the parent ends")
	}
}
