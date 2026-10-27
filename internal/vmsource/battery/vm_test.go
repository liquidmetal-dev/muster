package battery

import (
	"context"
	"errors"
	"testing"
	"time"

	poolmgrv1alpha1 "github.com/liquidmetal-dev/battery/api/proto/poolmgr/v1alpha1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/liquidmetal-dev/muster/internal/vmsource"
	"github.com/liquidmetal-dev/muster/internal/vmsource/battery/fakepoolmgr"
)

func TestKeepAliveCallsHeartbeat(t *testing.T) {
	fake := fakepoolmgr.New()
	seedPool(fake, "a", 1)
	src := newTestSource(t, fake, nil)
	vm := acquire(t, src, "a", "req-1")
	before := fake.Leases()[0].GetExpiresAt().AsTime()
	time.Sleep(5 * time.Millisecond)
	if err := vm.KeepAlive(context.Background()); err != nil {
		t.Fatalf("KeepAlive: %v", err)
	}
	if n := fake.CallCount("Heartbeat"); n != 1 {
		t.Fatalf("CallCount(Heartbeat) = %d, want 1", n)
	}
	if after := fake.Leases()[0].GetExpiresAt().AsTime(); !after.After(before) {
		t.Fatalf("expires_at %v not after %v", after, before)
	}
}

func TestKeepAliveLeaseGone(t *testing.T) {
	fake := fakepoolmgr.New()
	seedPool(fake, "a", 1)
	src := newTestSource(t, fake, nil)
	vm := acquire(t, src, "a", "req-1")
	fake.DeleteVM("muster", "a", vm.ID(), poolmgrv1alpha1.EventType_VM_DELETED_DUE_TO_EXPIRY)
	err := vm.KeepAlive(context.Background())
	if !errors.Is(err, vmsource.ErrLeaseNotFound) {
		t.Fatalf("KeepAlive error = %v, want ErrLeaseNotFound", err)
	}
	if n := fake.CallCount("Heartbeat"); n != 1 {
		t.Fatalf("CallCount(Heartbeat) = %d, want 1 (no retry)", n)
	}
}

func TestKeepAliveIntervalHalfOfPool(t *testing.T) {
	fake := fakepoolmgr.New()
	seedPool(fake, "a", 1) // 20 s heartbeat interval
	src := newTestSource(t, fake, nil)
	vm := acquire(t, src, "a", "req-1")
	if got := vm.KeepAliveInterval(); got != 10*time.Second {
		t.Fatalf("KeepAliveInterval() = %v, want 10s", got)
	}
	if n := fake.CallCount("GetPool"); n != 1 {
		t.Fatalf("CallCount(GetPool) = %d, want 1", n)
	}
}

func TestKeepAliveIntervalReadPerAcquire(t *testing.T) {
	fake := fakepoolmgr.New()
	seedPool(fake, "a", 2)
	src := newTestSource(t, fake, nil)
	first := acquire(t, src, "a", "req-1")
	fake.AddPool(fakepoolmgr.Pool{Name: "a", Namespace: "muster", Capacity: 2,
		HeartbeatInterval: 40 * time.Second, HeartbeatExpiryThreshold: 60 * time.Second})
	second := acquire(t, src, "a", "req-2")
	if first.KeepAliveInterval() != 10*time.Second || second.KeepAliveInterval() != 20*time.Second {
		t.Fatalf("intervals = %v, %v; want 10s, 20s", first.KeepAliveInterval(), second.KeepAliveInterval())
	}
	if n := fake.CallCount("GetPool"); n != 2 {
		t.Fatalf("CallCount(GetPool) = %d, want 2", n)
	}
}

func TestAcquireFailsWhenGetPoolFails(t *testing.T) {
	fake := fakepoolmgr.New()
	seedPool(fake, "a", 1)
	src := newTestSource(t, fake, nil)
	fake.FailNext("GetPool", codes.Internal)
	_, err := src.Acquire(context.Background(), "a", "req-1")
	if status.Code(err) != codes.Internal {
		t.Fatalf("Acquire error = %v, want Internal", err)
	}
	if n := fake.CallCount("ClaimVM"); n != 0 {
		t.Fatalf("CallCount(ClaimVM) = %d, want 0 (nothing claimed)", n)
	}
	acquire(t, src, "a", "req-1")
}

func TestAcquireFailsOnZeroInterval(t *testing.T) {
	fake := fakepoolmgr.New()
	fake.AddPool(fakepoolmgr.Pool{Name: "a", Namespace: "muster", Capacity: 1, HeartbeatExpiryThreshold: 60 * time.Second})
	src := newTestSource(t, fake, nil)
	_, err := src.Acquire(context.Background(), "a", "req-1")
	if !errors.Is(err, ErrNoHeartbeatInterval) {
		t.Fatalf("Acquire error = %v, want ErrNoHeartbeatInterval", err)
	}
	if n := fake.CallCount("ClaimVM"); n != 0 {
		t.Fatalf("CallCount(ClaimVM) = %d, want 0", n)
	}
}

func TestAcquireUnknownPoolViaGetPool(t *testing.T) {
	src := newTestSource(t, fakepoolmgr.New(), nil)
	_, err := src.Acquire(context.Background(), "nope", "req-1")
	if !errors.Is(err, vmsource.ErrPoolNotFound) {
		t.Fatalf("Acquire error = %v, want ErrPoolNotFound", err)
	}
}

type recordingExecer struct {
	host vmsource.HostInfo
	vmID string
	spec vmsource.ExecSpec
}

func (r *recordingExecer) Exec(_ context.Context, host vmsource.HostInfo, vmID string, spec vmsource.ExecSpec) (vmsource.ExecSession, error) {
	r.host, r.vmID, r.spec = host, vmID, spec
	return nil, nil
}

func TestExecDelegatesToExecer(t *testing.T) {
	fake := fakepoolmgr.New()
	seedPool(fake, "a", 1)
	rec := &recordingExecer{}
	src := newTestSource(t, fake, func(c *Config) { c.Execer = rec })
	vm := acquire(t, src, "a", "req-1")
	spec := vmsource.ExecSpec{Cmd: "/bin/sh", Args: []string{"-c", "true"}, User: "runner", HasStdin: true}
	if _, err := vm.Exec(context.Background(), spec); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if rec.host != (vmsource.HostInfo{Name: "host-a", Address: "127.0.0.1:9090"}) || rec.vmID != vm.ID() {
		t.Fatalf("execer got host %+v vm %q, want host-a/127.0.0.1:9090 and %q", rec.host, rec.vmID, vm.ID())
	}
	if rec.spec.Cmd != spec.Cmd || rec.spec.User != spec.User || !rec.spec.HasStdin || len(rec.spec.Args) != 2 {
		t.Fatalf("execer got spec %+v, want %+v", rec.spec, spec)
	}
}

func TestExecDefaultUnavailable(t *testing.T) {
	fake := fakepoolmgr.New()
	seedPool(fake, "a", 1)
	src := newTestSource(t, fake, nil)
	vm := acquire(t, src, "a", "req-1")
	_, err := vm.Exec(context.Background(), vmsource.ExecSpec{Cmd: "true"})
	if !errors.Is(err, vmsource.ErrExecUnavailable) {
		t.Fatalf("Exec error = %v, want ErrExecUnavailable", err)
	}
}
