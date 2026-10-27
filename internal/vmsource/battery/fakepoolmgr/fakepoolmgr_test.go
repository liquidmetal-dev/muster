package fakepoolmgr_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	poolmgrv1alpha1 "github.com/liquidmetal-dev/battery/api/proto/poolmgr/v1alpha1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/liquidmetal-dev/muster/internal/vmsource/battery/fakepoolmgr"
)

type clients struct {
	lease  poolmgrv1alpha1.LeaseClient
	events poolmgrv1alpha1.EventsClient
	admin  poolmgrv1alpha1.PoolAdminClient
}

func start(t *testing.T) (*fakepoolmgr.Server, clients) {
	t.Helper()
	fake := fakepoolmgr.New()
	conn, err := grpc.NewClient("passthrough:///fakepoolmgr", fake.ServeBufconn(t),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return fake, clients{
		lease:  poolmgrv1alpha1.NewLeaseClient(conn),
		events: poolmgrv1alpha1.NewEventsClient(conn),
		admin:  poolmgrv1alpha1.NewPoolAdminClient(conn),
	}
}

func pool(name, ns string, capacity int) fakepoolmgr.Pool {
	return fakepoolmgr.Pool{Name: name, Namespace: ns, Capacity: capacity,
		HeartbeatInterval: 20 * time.Second, HeartbeatExpiryThreshold: 60 * time.Second}
}

func ref(name, ns string) *poolmgrv1alpha1.PoolRef {
	return &poolmgrv1alpha1.PoolRef{Name: name, Namespace: ns}
}

func claim(t *testing.T, c clients, name, ns, reqID string) *poolmgrv1alpha1.ClaimVMResponse {
	t.Helper()
	resp, err := c.lease.ClaimVM(context.Background(), &poolmgrv1alpha1.ClaimVMRequest{Pool: ref(name, ns), RequestId: reqID})
	if err != nil {
		t.Fatalf("ClaimVM: %v", err)
	}
	return resp
}

func wantCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if got := status.Code(err); got != want {
		t.Fatalf("status code = %v (%v), want %v", got, err, want)
	}
}

func TestClaimReturnsVMAndHost(t *testing.T) {
	fake, c := start(t)
	fake.AddPool(pool("a", "ns", 1))
	resp := claim(t, c, "a", "ns", "req-1")
	if resp.GetLeaseId() == "" || resp.GetVmUid() == "" {
		t.Fatalf("ClaimVM returned empty ids: %v", resp)
	}
	if got := resp.GetHost().GetAddress(); got != "127.0.0.1:9090" {
		t.Fatalf("host address = %q, want 127.0.0.1:9090", got)
	}
	if n := len(fake.Leases()); n != 1 {
		t.Fatalf("Leases() = %d, want 1", n)
	}
}

func TestClaimExhausted(t *testing.T) {
	fake, c := start(t)
	fake.AddPool(pool("a", "ns", 1))
	claim(t, c, "a", "ns", "req-1")
	_, err := c.lease.ClaimVM(context.Background(), &poolmgrv1alpha1.ClaimVMRequest{Pool: ref("a", "ns"), RequestId: "req-2"})
	wantCode(t, err, codes.ResourceExhausted)
}

func TestClaimUnknownPool(t *testing.T) {
	_, c := start(t)
	_, err := c.lease.ClaimVM(context.Background(), &poolmgrv1alpha1.ClaimVMRequest{Pool: ref("nope", "ns"), RequestId: "req-1"})
	wantCode(t, err, codes.NotFound)
}

func TestClaimSameRequestIDReplays(t *testing.T) {
	fake, c := start(t)
	fake.AddPool(pool("a", "ns", 2))
	first := claim(t, c, "a", "ns", "req-1")
	second := claim(t, c, "a", "ns", "req-1")
	if first.GetLeaseId() != second.GetLeaseId() || first.GetVmUid() != second.GetVmUid() {
		t.Fatalf("replay returned a different lease: %v vs %v", first, second)
	}
	if n := fake.CallCount("ClaimVM"); n != 2 {
		t.Fatalf("CallCount(ClaimVM) = %d, want 2", n)
	}
	if n := len(fake.Leases()); n != 1 {
		t.Fatalf("Leases() = %d, want 1", n)
	}
}

func TestClaimRequestIDOtherPool(t *testing.T) {
	fake, c := start(t)
	fake.AddPool(pool("a", "ns", 1))
	fake.AddPool(pool("b", "ns", 1))
	claim(t, c, "a", "ns", "req-1")
	_, err := c.lease.ClaimVM(context.Background(), &poolmgrv1alpha1.ClaimVMRequest{Pool: ref("b", "ns"), RequestId: "req-1"})
	wantCode(t, err, codes.InvalidArgument)
}

func TestHeartbeatExtends(t *testing.T) {
	fake, c := start(t)
	fake.AddPool(pool("a", "ns", 1))
	resp := claim(t, c, "a", "ns", "req-1")
	before := fake.Leases()[0].GetExpiresAt().AsTime()
	time.Sleep(5 * time.Millisecond)
	hb, err := c.lease.Heartbeat(context.Background(), &poolmgrv1alpha1.HeartbeatRequest{LeaseId: resp.GetLeaseId()})
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if !hb.GetExpiresAt().AsTime().After(before) {
		t.Fatalf("expires_at %v not after %v", hb.GetExpiresAt().AsTime(), before)
	}
}

func TestHeartbeatUnknownLease(t *testing.T) {
	_, c := start(t)
	_, err := c.lease.Heartbeat(context.Background(), &poolmgrv1alpha1.HeartbeatRequest{LeaseId: "nope"})
	wantCode(t, err, codes.NotFound)
}

func subscribe(t *testing.T, c clients, p *poolmgrv1alpha1.PoolRef) grpc.ServerStreamingClient[poolmgrv1alpha1.Event] {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stream, err := c.events.Subscribe(ctx, &poolmgrv1alpha1.SubscribeRequest{Pool: p})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	return stream
}

func recvWithin(t *testing.T, stream grpc.ServerStreamingClient[poolmgrv1alpha1.Event], d time.Duration) (*poolmgrv1alpha1.Event, error) {
	t.Helper()
	type res struct {
		ev  *poolmgrv1alpha1.Event
		err error
	}
	ch := make(chan res, 1)
	go func() {
		ev, err := stream.Recv()
		ch <- res{ev, err}
	}()
	select {
	case r := <-ch:
		return r.ev, r.err
	case <-time.After(d):
		return nil, context.DeadlineExceeded
	}
}

func TestReleaseDeletesAndEmits(t *testing.T) {
	fake, c := start(t)
	fake.AddPool(pool("a", "ns", 1))
	stream := subscribe(t, c, ref("a", "ns"))
	resp := claim(t, c, "a", "ns", "req-1")
	if _, err := c.lease.ReleaseVM(context.Background(), &poolmgrv1alpha1.ReleaseVMRequest{LeaseId: resp.GetLeaseId()}); err != nil {
		t.Fatalf("ReleaseVM: %v", err)
	}
	if n := len(fake.Leases()); n != 0 {
		t.Fatalf("Leases() = %d, want 0", n)
	}
	var types []poolmgrv1alpha1.EventType
	for range 2 {
		ev, err := recvWithin(t, stream, 2*time.Second)
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		types = append(types, ev.GetType())
		if ev.GetVmUid() != resp.GetVmUid() {
			t.Fatalf("event vm_uid = %q, want %q", ev.GetVmUid(), resp.GetVmUid())
		}
	}
	if types[0] != poolmgrv1alpha1.EventType_VM_CLAIMED || types[1] != poolmgrv1alpha1.EventType_VM_DELETED_ON_RELEASE {
		t.Fatalf("event types = %v, want [VM_CLAIMED VM_DELETED_ON_RELEASE]", types)
	}
}

func TestReleaseUnknownLease(t *testing.T) {
	_, c := start(t)
	_, err := c.lease.ReleaseVM(context.Background(), &poolmgrv1alpha1.ReleaseVMRequest{LeaseId: "nope"})
	wantCode(t, err, codes.NotFound)
}

func TestListLeasesFilter(t *testing.T) {
	fake, c := start(t)
	fake.AddPool(pool("a", "ns1", 1))
	fake.AddPool(pool("b", "ns2", 1))
	claim(t, c, "a", "ns1", "req-a")
	claim(t, c, "b", "ns2", "req-b")
	all, err := c.lease.ListLeases(context.Background(), &poolmgrv1alpha1.ListLeasesRequest{})
	if err != nil {
		t.Fatalf("ListLeases: %v", err)
	}
	if n := len(all.GetLeases()); n != 2 {
		t.Fatalf("unfiltered leases = %d, want 2", n)
	}
	one, err := c.lease.ListLeases(context.Background(), &poolmgrv1alpha1.ListLeasesRequest{PoolRef: ref("b", "ns2")})
	if err != nil {
		t.Fatalf("ListLeases: %v", err)
	}
	if n := len(one.GetLeases()); n != 1 || one.GetLeases()[0].GetPoolNamespace() != "ns2" || one.GetLeases()[0].GetRequestId() != "req-b" {
		t.Fatalf("filtered leases = %v, want one in ns2 with req-b", one.GetLeases())
	}
}

func TestSubscribeReplaysThenLive(t *testing.T) {
	fake, c := start(t)
	fake.AddPool(pool("a", "ns", 1))
	resp := claim(t, c, "a", "ns", "req-1")
	stream := subscribe(t, c, ref("a", "ns"))
	ev, err := recvWithin(t, stream, 2*time.Second)
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if ev.GetId() != 1 || ev.GetType() != poolmgrv1alpha1.EventType_VM_CLAIMED {
		t.Fatalf("replayed event = %v, want id 1 VM_CLAIMED", ev)
	}
	fake.Emit(&poolmgrv1alpha1.Event{PoolName: "a", PoolNamespace: "ns", VmUid: resp.GetVmUid(), Type: poolmgrv1alpha1.EventType_VM_EXPIRING_SOON})
	ev, err = recvWithin(t, stream, 2*time.Second)
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if ev.GetId() != 2 || ev.GetType() != poolmgrv1alpha1.EventType_VM_EXPIRING_SOON || ev.GetCreatedAt() == nil {
		t.Fatalf("live event = %v, want id 2 VM_EXPIRING_SOON with created_at", ev)
	}
}

func TestSubscribePoolFilter(t *testing.T) {
	fake, c := start(t)
	fake.AddPool(pool("a", "ns", 1))
	fake.AddPool(pool("b", "ns", 1))
	stream := subscribe(t, c, ref("a", "ns"))
	waitSubscribers(t, fake, 1)
	claim(t, c, "b", "ns", "req-b")
	if ev, err := recvWithin(t, stream, 200*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("received %v, %v; want nothing", ev, err)
	}
}

func TestFailNext(t *testing.T) {
	fake, c := start(t)
	fake.AddPool(pool("a", "ns", 1))
	fake.FailNext("ClaimVM", codes.Unavailable)
	_, err := c.lease.ClaimVM(context.Background(), &poolmgrv1alpha1.ClaimVMRequest{Pool: ref("a", "ns"), RequestId: "req-1"})
	wantCode(t, err, codes.Unavailable)
	if n := len(fake.Leases()); n != 0 {
		t.Fatalf("Leases() after injected failure = %d, want 0", n)
	}
	claim(t, c, "a", "ns", "req-1")
	if n := len(fake.Leases()); n != 1 {
		t.Fatalf("Leases() = %d, want 1", n)
	}
}

func TestLoseNext(t *testing.T) {
	fake, c := start(t)
	fake.AddPool(pool("a", "ns", 1))
	resp := claim(t, c, "a", "ns", "req-1")
	fake.LoseNext("ReleaseVM")
	_, err := c.lease.ReleaseVM(context.Background(), &poolmgrv1alpha1.ReleaseVMRequest{LeaseId: resp.GetLeaseId()})
	wantCode(t, err, codes.Unavailable)
	if n := len(fake.Leases()); n != 0 {
		t.Fatalf("Leases() after lost response = %d, want 0 (handler ran)", n)
	}
}

func TestStall(t *testing.T) {
	fake, c := start(t)
	fake.AddPool(pool("a", "ns", 1))
	fake.Stall("ClaimVM", 500*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	startAt := time.Now()
	_, err := c.lease.ClaimVM(ctx, &poolmgrv1alpha1.ClaimVMRequest{Pool: ref("a", "ns"), RequestId: "req-1"})
	wantCode(t, err, codes.DeadlineExceeded)
	if took := time.Since(startAt); took > 200*time.Millisecond {
		t.Fatalf("stalled call took %v, want under 200ms", took)
	}
}

func waitSubscribers(t *testing.T, fake *fakepoolmgr.Server, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for fake.SubscriberCount() < n {
		if time.Now().After(deadline) {
			t.Fatalf("SubscriberCount() = %d, want %d", fake.SubscriberCount(), n)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestDropSubscribers(t *testing.T) {
	fake, c := start(t)
	fake.AddPool(pool("a", "ns", 1))
	stream := subscribe(t, c, ref("a", "ns"))
	waitSubscribers(t, fake, 1)
	fake.DropSubscribers()
	_, err := recvWithin(t, stream, 2*time.Second)
	if errors.Is(err, io.EOF) {
		t.Fatalf("Recv returned EOF, want status Unavailable")
	}
	wantCode(t, err, codes.Unavailable)
}

func TestRequireBasicAuth(t *testing.T) {
	fake, c := start(t)
	fake.AddPool(pool("a", "ns", 1))
	fake.RequireBasicAuth("s3cret")
	_, err := c.lease.ListLeases(context.Background(), &poolmgrv1alpha1.ListLeasesRequest{})
	wantCode(t, err, codes.Unauthenticated)
	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "basic "+base64.StdEncoding.EncodeToString([]byte("s3cret")))
	if _, err := c.lease.ListLeases(ctx, &poolmgrv1alpha1.ListLeasesRequest{}); err != nil {
		t.Fatalf("ListLeases with token: %v", err)
	}
}

func TestGetPool(t *testing.T) {
	fake, c := start(t)
	fake.AddPool(pool("a", "ns", 1))
	p, err := c.admin.GetPool(context.Background(), &poolmgrv1alpha1.GetPoolRequest{Ref: ref("a", "ns")})
	if err != nil {
		t.Fatalf("GetPool: %v", err)
	}
	if got := p.GetSpec().GetHeartbeatInterval().AsDuration(); got != 20*time.Second {
		t.Fatalf("heartbeat_interval = %v, want 20s", got)
	}
	_, err = c.admin.GetPool(context.Background(), &poolmgrv1alpha1.GetPoolRequest{Ref: ref("nope", "ns")})
	wantCode(t, err, codes.NotFound)
	_, err = c.admin.ListPools(context.Background(), &poolmgrv1alpha1.ListPoolsRequest{})
	wantCode(t, err, codes.Unimplemented)
}

func TestAddPoolReplacesSpec(t *testing.T) {
	fake, c := start(t)
	fake.AddPool(pool("a", "ns", 1))
	claim(t, c, "a", "ns", "req-1")
	p := pool("a", "ns", 1)
	p.HeartbeatInterval = 40 * time.Second
	fake.AddPool(p)
	got, err := c.admin.GetPool(context.Background(), &poolmgrv1alpha1.GetPoolRequest{Ref: ref("a", "ns")})
	if err != nil {
		t.Fatalf("GetPool: %v", err)
	}
	if got.GetSpec().GetHeartbeatInterval().AsDuration() != 40*time.Second {
		t.Fatalf("heartbeat_interval not replaced: %v", got.GetSpec().GetHeartbeatInterval().AsDuration())
	}
	if n := len(fake.Leases()); n != 1 {
		t.Fatalf("Leases() after AddPool = %d, want 1 (leases kept)", n)
	}
}

func TestDeleteVMEmits(t *testing.T) {
	fake, c := start(t)
	fake.AddPool(pool("a", "ns", 1))
	resp := claim(t, c, "a", "ns", "req-1")
	stream := subscribe(t, c, ref("a", "ns"))
	if _, err := recvWithin(t, stream, 2*time.Second); err != nil { // replayed VM_CLAIMED
		t.Fatalf("Recv: %v", err)
	}
	fake.DeleteVM("ns", "a", resp.GetVmUid(), poolmgrv1alpha1.EventType_VM_DELETED_DUE_TO_EXPIRY)
	ev, err := recvWithin(t, stream, 2*time.Second)
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if ev.GetType() != poolmgrv1alpha1.EventType_VM_DELETED_DUE_TO_EXPIRY || ev.GetVmUid() != resp.GetVmUid() {
		t.Fatalf("event = %v, want expiry deletion of %s", ev, resp.GetVmUid())
	}
	if n := len(fake.Leases()); n != 0 {
		t.Fatalf("Leases() after DeleteVM = %d, want 0", n)
	}
	_, err = c.lease.Heartbeat(context.Background(), &poolmgrv1alpha1.HeartbeatRequest{LeaseId: resp.GetLeaseId()})
	wantCode(t, err, codes.NotFound)
}

func TestSubscribeReplaysLargeOutbox(t *testing.T) {
	fake, c := start(t)
	fake.AddPool(pool("a", "ns", 1))
	const n = 200
	for i := range n {
		fake.Emit(&poolmgrv1alpha1.Event{PoolName: "a", PoolNamespace: "ns", VmUid: fmt.Sprintf("vm-%03d", i), Type: poolmgrv1alpha1.EventType_VM_DELETED_DUE_TO_EXPIRY})
	}
	stream := subscribe(t, c, ref("a", "ns"))
	for i := range n {
		ev, err := recvWithin(t, stream, 2*time.Second)
		if err != nil {
			t.Fatalf("Recv %d: %v", i, err)
		}
		if ev.GetId() != int64(i+1) {
			t.Fatalf("event %d has id %d", i, ev.GetId())
		}
	}
	// The server must still accept calls and live events after a large replay.
	claim(t, c, "a", "ns", "req-1")
	if ev, err := recvWithin(t, stream, 2*time.Second); err != nil || ev.GetType() != poolmgrv1alpha1.EventType_VM_CLAIMED {
		t.Fatalf("live event after replay = %v, %v", ev, err)
	}
}

func TestEmitDoesNotBlockOnSlowSubscriber(t *testing.T) {
	fake, c := start(t)
	fake.AddPool(pool("a", "ns", 1))
	stream := subscribe(t, c, ref("a", "ns"))
	waitSubscribers(t, fake, 1)
	done := make(chan struct{})
	go func() {
		for i := range 300 {
			fake.Emit(&poolmgrv1alpha1.Event{PoolName: "a", PoolNamespace: "ns", VmUid: fmt.Sprintf("vm-%03d", i), Type: poolmgrv1alpha1.EventType_VM_EXPIRING_SOON})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Emit blocked on a subscriber that is not reading")
	}
	for i := range 300 {
		if _, err := recvWithin(t, stream, 2*time.Second); err != nil {
			t.Fatalf("Recv %d: %v", i, err)
		}
	}
}

func TestPoolsWithSameNameInOtherNamespaces(t *testing.T) {
	fake, c := start(t)
	fake.AddPool(pool("a", "ns1", 1))
	fake.AddPool(pool("a", "ns2", 1))
	one := claim(t, c, "a", "ns1", "req-1")
	two := claim(t, c, "a", "ns2", "req-2")
	if one.GetVmUid() == two.GetVmUid() {
		t.Fatalf("both namespaces handed out vm %q", one.GetVmUid())
	}
	if n := len(fake.Leases()); n != 2 {
		t.Fatalf("Leases() = %d, want 2", n)
	}
}

func TestFailNextStacks(t *testing.T) {
	fake, c := start(t)
	fake.AddPool(pool("a", "ns", 1))
	fake.FailNext("ClaimVM", codes.Unavailable)
	fake.FailNext("ClaimVM", codes.Unavailable)
	for i := range 2 {
		_, err := c.lease.ClaimVM(context.Background(), &poolmgrv1alpha1.ClaimVMRequest{Pool: ref("a", "ns"), RequestId: "req-1"})
		if status.Code(err) != codes.Unavailable {
			t.Fatalf("call %d: code %v, want Unavailable", i, status.Code(err))
		}
	}
	claim(t, c, "a", "ns", "req-1")
}
