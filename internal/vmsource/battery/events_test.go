package battery

import (
	"context"
	"fmt"
	"testing"
	"time"

	poolmgrv1alpha1 "github.com/liquidmetal-dev/battery/api/proto/poolmgr/v1alpha1"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/liquidmetal-dev/muster/internal/vmsource"
	"github.com/liquidmetal-dev/muster/internal/vmsource/battery/fakepoolmgr"
)

func TestMapEvent(t *testing.T) {
	at := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		typ  poolmgrv1alpha1.EventType
		want vmsource.EventType
		keep bool
	}{
		{poolmgrv1alpha1.EventType_VM_DELETED_DUE_TO_EXPIRY, vmsource.EventVMLost, true},
		{poolmgrv1alpha1.EventType_VM_DELETED_ON_POOL_DELETE, vmsource.EventVMLost, true},
		{poolmgrv1alpha1.EventType(12), vmsource.EventVMLost, true}, // VM_DELETED_ON_HOST_DRAIN, after v0.5.0
		{poolmgrv1alpha1.EventType_VM_EXPIRING_SOON, vmsource.EventVMExpiring, true},
		{poolmgrv1alpha1.EventType_VM_PROVISIONED, 0, false},
		{poolmgrv1alpha1.EventType_VM_AVAILABLE, 0, false},
		{poolmgrv1alpha1.EventType_VM_CLAIMED, 0, false},
		{poolmgrv1alpha1.EventType_VM_RELEASED, 0, false},
		{poolmgrv1alpha1.EventType_VM_DELETED_ON_RELEASE, 0, false},
		{poolmgrv1alpha1.EventType_VM_HOOK_FAILED, 0, false},
		{poolmgrv1alpha1.EventType_POOL_REPLENISHING, 0, false},
		{poolmgrv1alpha1.EventType_POOL_SIZE_BELOW_TARGET, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.typ.String(), func(t *testing.T) {
			ev, keep := mapEvent(&poolmgrv1alpha1.Event{Id: 7, PoolName: "a", PoolNamespace: "muster", VmUid: "vm-1", Type: tt.typ, CreatedAt: timestamppb.New(at)})
			if keep != tt.keep {
				t.Fatalf("keep = %v, want %v", keep, tt.keep)
			}
			if !keep {
				return
			}
			want := vmsource.Event{Type: tt.want, Pool: "a", VMID: "vm-1", At: at}
			if ev != want {
				t.Fatalf("mapEvent = %+v, want %+v", ev, want)
			}
		})
	}
}

func events(t *testing.T, src *Source, fake *fakepoolmgr.Server, pool string) (<-chan vmsource.Event, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch, err := src.Events(ctx, pool)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	waitFor(t, func() bool { return fake.SubscriberCount() >= 1 }, "subscriber registered")
	return ch, cancel
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func recv(t *testing.T, ch <-chan vmsource.Event, d time.Duration) (vmsource.Event, bool) {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("channel closed")
		}
		return ev, true
	case <-time.After(d):
		return vmsource.Event{}, false
	}
}

func TestEventsDeletionReachesConsumer(t *testing.T) {
	fake := fakepoolmgr.New()
	seedPool(fake, "a", 1)
	src := newTestSource(t, fake, nil)
	vm := acquire(t, src, "a", "req-1")
	ch, _ := events(t, src, fake, "a")
	fake.DeleteVM("muster", "a", vm.ID(), poolmgrv1alpha1.EventType_VM_DELETED_DUE_TO_EXPIRY)
	ev, ok := recv(t, ch, 2*time.Second)
	if !ok {
		t.Fatal("no event within 2s")
	}
	if ev.Type != vmsource.EventVMLost || ev.Pool != "a" || ev.VMID != vm.ID() || ev.At.IsZero() {
		t.Fatalf("event = %+v, want vm-lost for %s in pool a", ev, vm.ID())
	}
}

func TestEventsIgnoresRelease(t *testing.T) {
	fake := fakepoolmgr.New()
	seedPool(fake, "a", 1)
	src := newTestSource(t, fake, nil)
	vm := acquire(t, src, "a", "req-1")
	ch, _ := events(t, src, fake, "a")
	if err := src.Release(context.Background(), vm); err != nil {
		t.Fatalf("Release: %v", err)
	}
	fake.Emit(&poolmgrv1alpha1.Event{PoolName: "a", PoolNamespace: "muster", VmUid: vm.ID(), Type: poolmgrv1alpha1.EventType_VM_EXPIRING_SOON})
	ev, ok := recv(t, ch, 2*time.Second)
	if !ok || ev.Type != vmsource.EventVMExpiring {
		t.Fatalf("first event = %+v (ok=%v), want vm-expiring (release deletion dropped)", ev, ok)
	}
}

func TestEventsOtherPoolFiltered(t *testing.T) {
	fake := fakepoolmgr.New()
	seedPool(fake, "a", 1)
	seedPool(fake, "b", 1)
	src := newTestSource(t, fake, nil)
	vm := acquire(t, src, "b", "req-b")
	ch, _ := events(t, src, fake, "a")
	fake.DeleteVM("muster", "b", vm.ID(), poolmgrv1alpha1.EventType_VM_DELETED_DUE_TO_EXPIRY)
	if ev, ok := recv(t, ch, 200*time.Millisecond); ok {
		t.Fatalf("received %+v on pool a's channel, want nothing", ev)
	}
}

func TestEventsReconnectDedupes(t *testing.T) {
	fake := fakepoolmgr.New()
	seedPool(fake, "a", 2)
	src := newTestSource(t, fake, nil)
	first := acquire(t, src, "a", "req-1")
	second := acquire(t, src, "a", "req-2")
	ch, _ := events(t, src, fake, "a")
	fake.DeleteVM("muster", "a", first.ID(), poolmgrv1alpha1.EventType_VM_DELETED_DUE_TO_EXPIRY)
	if ev, ok := recv(t, ch, 2*time.Second); !ok || ev.VMID != first.ID() {
		t.Fatalf("first event = %+v (ok=%v)", ev, ok)
	}
	fake.DropSubscribers()
	waitFor(t, func() bool { return fake.CallCount("Subscribe") >= 2 && fake.SubscriberCount() >= 1 }, "resubscribe")
	fake.DeleteVM("muster", "a", second.ID(), poolmgrv1alpha1.EventType_VM_DELETED_DUE_TO_EXPIRY)
	ev, ok := recv(t, ch, 2*time.Second)
	if !ok || ev.VMID != second.ID() {
		t.Fatalf("event after reconnect = %+v (ok=%v), want %s (no replay of %s)", ev, ok, second.ID(), first.ID())
	}
	if extra, ok := recv(t, ch, 200*time.Millisecond); ok {
		t.Fatalf("unexpected extra event %+v", extra)
	}
}

func TestEventsClosesOnCancel(t *testing.T) {
	fake := fakepoolmgr.New()
	seedPool(fake, "a", 1)
	src := newTestSource(t, fake, nil)
	ch, cancel := events(t, src, fake, "a")
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("got an event, want closed channel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel not closed within 2s")
	}
}

func TestEventsEmptyPool(t *testing.T) {
	src := newTestSource(t, fakepoolmgr.New(), nil)
	if _, err := src.Events(context.Background(), ""); err == nil {
		t.Fatal("Events with empty pool succeeded, want error")
	}
}

func TestEventsFirstSubscribeError(t *testing.T) {
	src := newTestSource(t, fakepoolmgr.New(), nil)
	if err := src.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if ch, err := src.Events(context.Background(), "a"); err == nil || ch != nil {
		t.Fatalf("Events after Close = %v, %v; want nil channel and an error", ch, err)
	}
}

func TestEventsPermanentErrorClosesChannel(t *testing.T) {
	fake := fakepoolmgr.New()
	seedPool(fake, "a", 1)
	src := newTestSource(t, fake, nil)
	fake.FailNext("Subscribe", codes.Unauthenticated)
	ch, err := src.Events(context.Background(), "a")
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("got an event, want closed channel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel not closed within 2s")
	}
	if n := fake.CallCount("Subscribe"); n != 1 {
		t.Fatalf("CallCount(Subscribe) = %d, want 1 (no resubscribe on a permanent error)", n)
	}
}

func TestEventsTransientErrorResubscribes(t *testing.T) {
	fake := fakepoolmgr.New()
	seedPool(fake, "a", 1)
	src := newTestSource(t, fake, nil)
	vm := acquire(t, src, "a", "req-1")
	ch, _ := events(t, src, fake, "a")
	fake.DropSubscribers()
	waitFor(t, func() bool { return fake.CallCount("Subscribe") >= 2 && fake.SubscriberCount() >= 1 }, "resubscribe")
	fake.DeleteVM("muster", "a", vm.ID(), poolmgrv1alpha1.EventType_VM_DELETED_DUE_TO_EXPIRY)
	if ev, ok := recv(t, ch, 2*time.Second); !ok || ev.VMID != vm.ID() {
		t.Fatalf("event after resubscribe = %+v (ok=%v)", ev, ok)
	}
}

func TestEventsSlowConsumerLosesNothing(t *testing.T) {
	fake := fakepoolmgr.New()
	seedPool(fake, "a", 1)
	src := newTestSource(t, fake, nil)
	ch, _ := events(t, src, fake, "a")
	const n = 50
	for i := range n {
		fake.Emit(&poolmgrv1alpha1.Event{PoolName: "a", PoolNamespace: "muster", VmUid: fmt.Sprintf("vm-%02d", i), Type: poolmgrv1alpha1.EventType_VM_DELETED_DUE_TO_EXPIRY})
	}
	time.Sleep(100 * time.Millisecond) // let the buffer fill and the reader block
	for i := range n {
		ev, ok := recv(t, ch, 2*time.Second)
		if !ok {
			t.Fatalf("event %d missing", i)
		}
		if want := fmt.Sprintf("vm-%02d", i); ev.VMID != want {
			t.Fatalf("event %d = %s, want %s (order lost)", i, ev.VMID, want)
		}
	}
}
