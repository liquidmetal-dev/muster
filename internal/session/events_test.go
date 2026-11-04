package session_test

import (
	"strings"
	"testing"
	"time"

	poolmgrv1alpha1 "github.com/liquidmetal-dev/battery/api/proto/poolmgr/v1alpha1"
	"google.golang.org/grpc/codes"

	"github.com/liquidmetal-dev/muster/internal/session"
)

func (e *env) waitSubscribed() {
	e.t.Helper()
	waitFor(e.t, func() bool { return e.Pool.SubscriberCount() >= 1 })
}

func TestVMLostEventFails(t *testing.T) {
	e := newEnv(t)
	c := e.capture()
	e.start()
	e.waitSubscribed()
	id, _, _ := e.running(c)
	i, _ := e.info(id)
	e.Pool.DeleteVM(testNS, testPool, i.VMID, poolmgrv1alpha1.EventType_VM_DELETED_DUE_TO_EXPIRY)
	wantEnd(t, e.endedInfo(), session.Failed, session.ReasonVMLost)
}

func TestVMExpiringLogged(t *testing.T) {
	e := newEnv(t)
	c := e.capture()
	e.start()
	e.waitSubscribed()
	id, _, _ := e.running(c)
	i, _ := e.info(id)
	e.Pool.Emit(&poolmgrv1alpha1.Event{PoolName: testPool, PoolNamespace: testNS, VmUid: i.VMID, Type: poolmgrv1alpha1.EventType_VM_EXPIRING_SOON})
	e.waitLog(`"msg":"VM lease expiring"`)
	if st := e.state(id); st != session.Running {
		t.Fatalf("state after expiring event = %v, want RUNNING", st)
	}
}

func TestKeepAliveLeaseNotFound(t *testing.T) {
	e := newEnv(t)
	c := e.capture()
	e.start()
	e.running(c)
	e.Pool.FailNext("Heartbeat", codes.NotFound)
	got := e.driveEnd(time.Second)
	wantEnd(t, got, session.Failed, session.ReasonVMLost)
}

func TestKeepAliveTransientError(t *testing.T) {
	e := newEnv(t)
	c := e.capture()
	e.start()
	id, _, at := e.running(c)
	for range 3 {
		e.Pool.FailNext("Heartbeat", codes.Unavailable)
	}
	e.drive(time.Second, func() bool { return strings.Contains(e.Logs.String(), `"msg":"heartbeat"`) })
	e.drive(time.Second, func() bool { return e.elapsed() >= at+45*time.Second })
	if st := e.state(id); st != session.Running {
		t.Fatalf("state after a failed heartbeat = %v, want RUNNING", st)
	}
}

func TestEventsResubscribe(t *testing.T) {
	e := newEnv(t)
	e.start()
	e.waitSubscribed()
	e.Pool.DropSubscribers()
	e.drive(time.Second, func() bool { return e.Pool.SubscriberCount() == 1 })
}
