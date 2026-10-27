package battery

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/liquidmetal-dev/muster/internal/tlsconfig"
	"github.com/liquidmetal-dev/muster/internal/vmsource"
	"github.com/liquidmetal-dev/muster/internal/vmsource/battery/fakepoolmgr"
)

// newTestSource builds a Source over fake with test-friendly defaults.
// mutate may change the config before New runs.
func newTestSource(t testing.TB, fake *fakepoolmgr.Server, mutate func(*Config)) *Source {
	t.Helper()
	cfg := Config{
		Addr:        "passthrough:///fakepoolmgr",
		Namespace:   "muster",
		TLS:         tlsconfig.Client{Insecure: true},
		CallTimeout: 2 * time.Second,
		dialOptions: []grpc.DialOption{fake.ServeBufconn(t)},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	src, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = src.Close() })
	return src
}

// seedPool adds a pool in the muster namespace with a 20 s heartbeat
// interval and a 60 s expiry threshold.
func seedPool(fake *fakepoolmgr.Server, name string, capacity int) {
	fake.AddPool(fakepoolmgr.Pool{Name: name, Namespace: "muster", Capacity: capacity,
		HeartbeatInterval: 20 * time.Second, HeartbeatExpiryThreshold: 60 * time.Second})
}

func acquire(t testing.TB, src *Source, pool, requestID string) *VM {
	t.Helper()
	vm, err := src.Acquire(context.Background(), pool, requestID)
	if err != nil {
		t.Fatalf("Acquire(%s, %s): %v", pool, requestID, err)
	}
	return vm.(*VM)
}

// stubVM is a vmsource.VM that did not come from this source.
type stubVM struct{ vmsource.VM }

func (stubVM) ID() string { return "stub" }
