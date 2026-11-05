package reconcile_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/liquidmetal-dev/muster/internal/dispatch"
	"github.com/liquidmetal-dev/muster/internal/reconcile"
)

// stopRecord is a ShutdownConfig.Stop that notes what was left when it
// was called.
type stopRecord struct {
	e      *env
	runner string

	mu           sync.Mutex
	called       bool
	leasesAtStop int
	runnerAtStop bool
}

func (s *stopRecord) stop() {
	s.mu.Lock()
	s.called = true
	s.leasesAtStop = len(s.e.Pool.Leases())
	_, s.runnerAtStop = s.e.runner(s.runner)
	s.mu.Unlock()
	s.e.stop()
}

func (e *env) dispatcher() *dispatch.Dispatcher {
	return dispatch.New(dispatch.Config{
		Pools: e.Pools, Demand: zeroDemand{}, Sessions: e.Registry,
		Clock: e.Clock, Logger: slog.New(slog.DiscardHandler),
	})
}

// adopted leaves an idle in-flight session for poolLinux and adopts it.
func (e *env) adopted() string {
	e.t.Helper()
	name := e.prior(poolLinux, idA, holdIdle)
	res, err := e.reconcile()
	if err != nil || res.Adopted != 1 {
		e.t.Fatalf("Run = %+v, %v; want one adopted", res, err)
	}
	return name
}

func (e *env) shutdownAsync(d *dispatch.Dispatcher, rec *stopRecord, grace time.Duration) <-chan bool {
	done := make(chan bool, 1)
	go func() {
		done <- reconcile.Shutdown(context.Background(), reconcile.ShutdownConfig{
			Dispatcher: d, Sessions: e.Registry, Stop: rec.stop, Grace: grace,
			Clock: e.Clock, Logger: slog.New(slog.DiscardHandler),
		})
	}()
	return done
}

func (e *env) driveShutdown(done <-chan bool) bool {
	e.t.Helper()
	var finished bool
	e.drive(time.Second, func() bool {
		select {
		case finished = <-done:
			return true
		default:
			return false
		}
	})
	return finished
}

func TestShutdownNoBusy(t *testing.T) {
	e := newEnv(t)
	name := e.adopted()
	d := e.dispatcher()
	rec := &stopRecord{e: e, runner: name}
	var finished bool
	select {
	case finished = <-e.shutdownAsync(d, rec, time.Minute):
	case <-time.After(10 * time.Second):
		t.Fatal("Shutdown did not return without busy sessions")
	}
	if !finished {
		t.Fatal("Shutdown = false, want true")
	}
	if !d.Draining() {
		t.Fatal("dispatcher not drained")
	}
	if !rec.called {
		t.Fatal("Stop not called")
	}
	e.wantLeases(poolLinux + "/" + idA)
	e.wantRunner(name, true)
}

func TestShutdownWaitsForBusy(t *testing.T) {
	e := newEnv(t)
	name := e.adopted()
	e.Registry.JobStarted(name, 1)
	rec := &stopRecord{e: e, runner: name}
	done := e.shutdownAsync(e.dispatcher(), rec, time.Minute)
	select {
	case <-done:
		t.Fatal("Shutdown returned while a session is busy")
	case <-time.After(50 * time.Millisecond):
	}
	e.Registry.JobCompleted(name)
	if !e.driveShutdown(done) {
		t.Fatal("Shutdown = false, want true")
	}
	if !rec.called || rec.leasesAtStop != 0 || rec.runnerAtStop {
		t.Fatalf("at Stop: called %v, leases %d, runner present %v; want called, 0, false",
			rec.called, rec.leasesAtStop, rec.runnerAtStop)
	}
}

func TestShutdownGraceExpires(t *testing.T) {
	e := newEnv(t)
	name := e.adopted()
	e.Registry.JobStarted(name, 1)
	rec := &stopRecord{e: e, runner: name}
	start := e.Clock.Now()
	if e.driveShutdown(e.shutdownAsync(e.dispatcher(), rec, time.Minute)) {
		t.Fatal("Shutdown = true, want false")
	}
	if d := e.Clock.Now().Sub(start); d < time.Minute {
		t.Fatalf("Shutdown returned after %v, want at least the 1m grace", d)
	}
	if !rec.called {
		t.Fatal("Stop not called")
	}
	e.wantLeases(poolLinux + "/" + idA)
	e.wantRunner(name, true)
}
