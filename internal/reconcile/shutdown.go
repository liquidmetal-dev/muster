package reconcile

import (
	"context"
	"log/slog"
	"time"

	"github.com/liquidmetal-dev/muster/internal/clock"
)

// Drainer stops session creation. *dispatch.Dispatcher satisfies it.
type Drainer interface {
	Drain()
}

// BusyWaiter waits until no session is busy. *session.Registry satisfies
// it.
type BusyWaiter interface {
	WaitNotBusy(ctx context.Context) error
}

// ShutdownConfig configures Shutdown.
type ShutdownConfig struct {
	Dispatcher Drainer
	Sessions   BusyWaiter
	// Stop ends Registry.Run and returns once Run has returned.
	Stop func()
	Grace time.Duration
	// Clock is nil for clock.Real().
	Clock clock.Clock
	// Logger is nil for slog.Default().
	Logger *slog.Logger
}

// Shutdown drains the dispatcher, waits up to Grace for busy sessions to
// end, then calls Stop. Sessions still active are left as they are, for
// the next Run to adopt. It reports whether every busy session ended
// within the grace.
func Shutdown(ctx context.Context, cfg ShutdownConfig) (finished bool) {
	if cfg.Clock == nil {
		cfg.Clock = clock.Real()
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	cfg.Dispatcher.Drain()

	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	t := cfg.Clock.NewTimer(cfg.Grace)
	defer t.Stop()
	go func() {
		select {
		case <-t.C():
			cancel()
		case <-waitCtx.Done():
		}
	}()
	finished = cfg.Sessions.WaitNotBusy(waitCtx) == nil
	if finished {
		cfg.Logger.Info("shutdown: busy sessions finished", "grace", cfg.Grace)
	} else {
		cfg.Logger.Warn("shutdown: grace expired; leaving sessions for adoption", "grace", cfg.Grace)
	}
	cfg.Stop()
	return finished
}
