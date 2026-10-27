// Package fakepoolmgr is an in-memory poolmgrd for tests. It implements
// battery's Lease and Events services and PoolAdmin.GetPool, with the same
// status codes as the real server for the cases muster handles: NOT_FOUND
// for an unknown pool or lease, RESOURCE_EXHAUSTED for an empty pool,
// INVALID_ARGUMENT for a request id reused on another pool. Every other
// PoolAdmin RPC returns UNIMPLEMENTED.
//
// Tests seed pools with AddPool, inject faults with FailNext, LoseNext and
// Stall, observe traffic with Calls and CallCount, and drive events with
// DeleteVM, Emit and DropSubscribers. The server runs on any listener via
// Serve, or on an in-process bufconn via ServeBufconn.
package fakepoolmgr

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	poolmgrv1alpha1 "github.com/liquidmetal-dev/battery/api/proto/poolmgr/v1alpha1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Pool seeds one pool.
type Pool struct {
	Name      string
	Namespace string
	// Capacity is the number of AVAILABLE VMs created up front.
	Capacity int
	// HeartbeatInterval and HeartbeatExpiryThreshold are returned by GetPool;
	// the threshold also sets a lease's expires_at on claim and heartbeat.
	HeartbeatInterval        time.Duration
	HeartbeatExpiryThreshold time.Duration
	// Host is reported on every claim from this pool. Nil means
	// {Name: "host-a", Address: "127.0.0.1:9090"}.
	Host *poolmgrv1alpha1.HostInfo
}

// Call is one recorded RPC.
type Call struct {
	// Method is the short RPC name: "ClaimVM", "Heartbeat", "ReleaseVM",
	// "ListLeases", "Subscribe", "GetPool", ...
	Method string
	Req    proto.Message
}

type poolKey struct{ ns, name string }

type vm struct {
	uid    string
	pool   poolKey
	leased bool
}

type lease struct {
	id        string
	vmUID     string
	pool      poolKey
	requestID string
	claimedAt time.Time
	lastBeat  time.Time
	expiresAt time.Time
}

// subscriber is one open Subscribe stream. Events queue in a slice so
// neither a large replay nor a slow reader can block emit or the server.
type subscriber struct {
	filter *poolKey
	mu     sync.Mutex
	queue  []*poolmgrv1alpha1.Event
	notify chan struct{} // capacity 1; a token means the queue is non-empty
	drop   chan struct{}
}

func (sub *subscriber) push(ev *poolmgrv1alpha1.Event) {
	sub.mu.Lock()
	sub.queue = append(sub.queue, ev)
	sub.mu.Unlock()
	select {
	case sub.notify <- struct{}{}:
	default:
	}
}

func (sub *subscriber) pop() []*poolmgrv1alpha1.Event {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	out := sub.queue
	sub.queue = nil
	return out
}

// Server is the fake. Use New.
type Server struct {
	poolmgrv1alpha1.UnimplementedPoolAdminServer

	mu          sync.Mutex
	pools       map[poolKey]Pool
	vms         map[string]*vm
	leases      map[string]*lease
	byRequest   map[string]string // request id -> lease id
	outbox      []*poolmgrv1alpha1.Event
	nextEventID map[poolKey]int64
	subs        map[*subscriber]struct{}
	calls       []Call
	failNext    map[string][]codes.Code
	loseNext    map[string]bool
	stallNext   map[string]time.Duration
	authHeader  string
	seq         int

	gs *grpc.Server
}

// New returns an empty fake.
func New() *Server {
	return &Server{
		pools:       map[poolKey]Pool{},
		vms:         map[string]*vm{},
		leases:      map[string]*lease{},
		byRequest:   map[string]string{},
		nextEventID: map[poolKey]int64{},
		subs:        map[*subscriber]struct{}{},
		failNext:    map[string][]codes.Code{},
		loseNext:    map[string]bool{},
		stallNext:   map[string]time.Duration{},
	}
}

// Register adds the Lease, Events and PoolAdmin services to gs.
func (s *Server) Register(gs *grpc.Server) {
	poolmgrv1alpha1.RegisterLeaseServer(gs, s)
	poolmgrv1alpha1.RegisterEventsServer(gs, s)
	poolmgrv1alpha1.RegisterPoolAdminServer(gs, s)
}

// Serve builds a gRPC server with opts plus the fake's interceptors and
// serves on lis until Stop. It blocks like grpc.Server.Serve.
func (s *Server) Serve(lis net.Listener, opts ...grpc.ServerOption) error {
	opts = append(opts, grpc.ChainUnaryInterceptor(s.unary), grpc.ChainStreamInterceptor(s.stream))
	gs := grpc.NewServer(opts...)
	s.Register(gs)
	s.mu.Lock()
	s.gs = gs
	s.mu.Unlock()
	return gs.Serve(lis)
}

// Stop stops the server started by Serve.
func (s *Server) Stop() {
	s.mu.Lock()
	gs := s.gs
	s.mu.Unlock()
	if gs != nil {
		gs.Stop()
	}
}

// ServeBufconn serves on an in-process listener for the life of the test
// and returns the dial option that reaches it. Dial with the target
// "passthrough:///fakepoolmgr".
func (s *Server) ServeBufconn(t testing.TB) grpc.DialOption {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(func() { s.Stop(); _ = lis.Close() })
	return grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	})
}

// AddPool seeds a pool with Capacity available VMs. An existing pool with
// the same name and namespace gets the new spec and keeps its VMs and
// leases.
func (s *Server) AddPool(p Pool) {
	if p.Host == nil {
		p.Host = &poolmgrv1alpha1.HostInfo{Name: "host-a", Address: "127.0.0.1:9090"}
	}
	key := poolKey{p.Namespace, p.Name}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, exists := s.pools[key]
	s.pools[key] = p
	if exists {
		return
	}
	for i := range p.Capacity {
		uid := fmt.Sprintf("%s-%s-vm-%d", p.Namespace, p.Name, i+1)
		s.vms[uid] = &vm{uid: uid, pool: key}
	}
}

// FailNext makes the next call of method fail with code before its
// handler runs. Repeated calls stack: each injected failure is consumed by
// one call, in order.
func (s *Server) FailNext(method string, code codes.Code) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failNext[method] = append(s.failNext[method], code)
}

// LoseNext makes the next call of method run its handler and then return
// UNAVAILABLE, as if the response was lost.
func (s *Server) LoseNext(method string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loseNext[method] = true
}

// Stall makes the next call of method wait d, or until its context ends,
// before the handler runs.
func (s *Server) Stall(method string, d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stallNext[method] = d
}

// RequireBasicAuth rejects every call without
// "authorization: basic <base64(token)>" with UNAUTHENTICATED.
func (s *Server) RequireBasicAuth(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authHeader = "basic " + base64.StdEncoding.EncodeToString([]byte(token))
}

// Calls returns every recorded RPC in order.
func (s *Server) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Call(nil), s.calls...)
}

// CallCount returns how many times method was called, including calls
// that FailNext rejected.
func (s *Server) CallCount(method string) int {
	n := 0
	for _, c := range s.Calls() {
		if c.Method == method {
			n++
		}
	}
	return n
}

// Leases returns every lease as a LeaseRecord.
func (s *Server) Leases() []*poolmgrv1alpha1.LeaseRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.leaseRecords(nil)
}

// DeleteVM removes a VM and its lease, if any, and emits typ for it. It is
// how a test simulates expiry, pool deletion or host drain.
func (s *Server) DeleteVM(namespace, pool, vmUID string, typ poolmgrv1alpha1.EventType) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.vms, vmUID)
	for id, l := range s.leases {
		if l.vmUID == vmUID {
			delete(s.leases, id)
			delete(s.byRequest, l.requestID)
		}
	}
	s.emit(&poolmgrv1alpha1.Event{PoolName: pool, PoolNamespace: namespace, VmUid: vmUID, Type: typ})
}

// Emit assigns an id and created_at to ev, stores it in the outbox and
// fans it out to matching subscribers.
func (s *Server) Emit(ev *poolmgrv1alpha1.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emit(ev)
}

// SubscriberCount returns how many Subscribe streams are open. A client's
// Subscribe returns before the server registers the stream, so tests wait
// on this before they drop or emit.
func (s *Server) SubscriberCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs)
}

// DropSubscribers ends every open Subscribe stream with UNAVAILABLE.
func (s *Server) DropSubscribers() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sub := range s.subs {
		close(sub.drop)
		delete(s.subs, sub)
	}
}

// --- interceptors ---

func methodName(full string) string {
	return full[strings.LastIndex(full, "/")+1:]
}

// before records the call and applies auth, FailNext and Stall. It returns
// a non-nil error to reject the call, and whether LoseNext applies.
func (s *Server) before(ctx context.Context, method string, req proto.Message) (lose bool, err error) {
	s.mu.Lock()
	s.calls = append(s.calls, Call{Method: method, Req: req})
	auth := s.authHeader
	var code codes.Code
	fail := len(s.failNext[method]) > 0
	if fail {
		code = s.failNext[method][0]
		s.failNext[method] = s.failNext[method][1:]
	}
	stall, stalled := s.stallNext[method]
	delete(s.stallNext, method)
	lose = s.loseNext[method]
	delete(s.loseNext, method)
	s.mu.Unlock()

	if auth != "" {
		md, _ := metadata.FromIncomingContext(ctx)
		if vals := md.Get("authorization"); len(vals) == 0 || vals[0] != auth {
			return false, status.Error(codes.Unauthenticated, "fakepoolmgr: missing or wrong authorization")
		}
	}
	if fail {
		return false, status.Error(code, "fakepoolmgr: injected")
	}
	if stalled {
		select {
		case <-time.After(stall):
		case <-ctx.Done():
			return false, status.FromContextError(ctx.Err()).Err()
		}
	}
	return lose, nil
}

func (s *Server) unary(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	msg, _ := req.(proto.Message)
	lose, err := s.before(ctx, methodName(info.FullMethod), msg)
	if err != nil {
		return nil, err
	}
	resp, err := handler(ctx, req)
	if lose && err == nil {
		return nil, status.Error(codes.Unavailable, "fakepoolmgr: response lost")
	}
	return resp, err
}

// recordingStream captures the first received message so Calls can show
// the Subscribe request.
type recordingStream struct {
	grpc.ServerStream
	s      *Server
	method string
}

func (r *recordingStream) RecvMsg(m any) error {
	err := r.ServerStream.RecvMsg(m)
	if msg, ok := m.(proto.Message); ok && err == nil {
		r.s.mu.Lock()
		for i := len(r.s.calls) - 1; i >= 0; i-- {
			if r.s.calls[i].Method == r.method && r.s.calls[i].Req == nil {
				r.s.calls[i].Req = msg
				break
			}
		}
		r.s.mu.Unlock()
	}
	return err
}

func (s *Server) stream(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	method := methodName(info.FullMethod)
	lose, err := s.before(ss.Context(), method, nil)
	if err != nil {
		return err
	}
	err = handler(srv, &recordingStream{ServerStream: ss, s: s, method: method})
	if lose && err == nil {
		return status.Error(codes.Unavailable, "fakepoolmgr: response lost")
	}
	return err
}

// --- Lease ---

func (s *Server) poolOf(ref *poolmgrv1alpha1.PoolRef) (Pool, poolKey, bool) {
	key := poolKey{ref.GetNamespace(), ref.GetName()}
	p, ok := s.pools[key]
	return p, key, ok
}

func (s *Server) claimResponse(l *lease, p Pool) *poolmgrv1alpha1.ClaimVMResponse {
	return &poolmgrv1alpha1.ClaimVMResponse{LeaseId: l.id, VmUid: l.vmUID, Host: p.Host}
}

// ClaimVM implements poolmgrv1alpha1.LeaseServer.
func (s *Server) ClaimVM(_ context.Context, req *poolmgrv1alpha1.ClaimVMRequest) (*poolmgrv1alpha1.ClaimVMResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, key, ok := s.poolOf(req.GetPool())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "pool %s/%s not found", key.ns, key.name)
	}
	if rid := req.GetRequestId(); rid != "" {
		if id, found := s.byRequest[rid]; found {
			l := s.leases[id]
			if l.pool != key {
				return nil, status.Errorf(codes.InvalidArgument, "request_id %q belongs to pool %s/%s", rid, l.pool.ns, l.pool.name)
			}
			return s.claimResponse(l, p), nil
		}
	}
	var free *vm
	for _, v := range s.vms {
		if v.pool == key && !v.leased && (free == nil || v.uid < free.uid) {
			free = v
		}
	}
	if free == nil {
		return nil, status.Errorf(codes.ResourceExhausted, "no available vm in pool %s/%s", key.ns, key.name)
	}
	free.leased = true
	s.seq++
	now := time.Now()
	l := &lease{
		id: fmt.Sprintf("lease-%d", s.seq), vmUID: free.uid, pool: key, requestID: req.GetRequestId(),
		claimedAt: now, lastBeat: now, expiresAt: now.Add(p.HeartbeatExpiryThreshold),
	}
	s.leases[l.id] = l
	if l.requestID != "" {
		s.byRequest[l.requestID] = l.id
	}
	s.emit(&poolmgrv1alpha1.Event{PoolName: key.name, PoolNamespace: key.ns, VmUid: l.vmUID, Type: poolmgrv1alpha1.EventType_VM_CLAIMED})
	return s.claimResponse(l, p), nil
}

// Heartbeat implements poolmgrv1alpha1.LeaseServer.
func (s *Server) Heartbeat(_ context.Context, req *poolmgrv1alpha1.HeartbeatRequest) (*poolmgrv1alpha1.HeartbeatResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.leases[req.GetLeaseId()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "lease %q not found", req.GetLeaseId())
	}
	now := time.Now()
	l.lastBeat = now
	l.expiresAt = now.Add(s.pools[l.pool].HeartbeatExpiryThreshold)
	return &poolmgrv1alpha1.HeartbeatResponse{ExpiresAt: timestamppb.New(l.expiresAt)}, nil
}

// ReleaseVM implements poolmgrv1alpha1.LeaseServer.
func (s *Server) ReleaseVM(_ context.Context, req *poolmgrv1alpha1.ReleaseVMRequest) (*emptypb.Empty, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.leases[req.GetLeaseId()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "lease %q not found", req.GetLeaseId())
	}
	delete(s.leases, l.id)
	delete(s.byRequest, l.requestID)
	delete(s.vms, l.vmUID)
	s.emit(&poolmgrv1alpha1.Event{PoolName: l.pool.name, PoolNamespace: l.pool.ns, VmUid: l.vmUID, Type: poolmgrv1alpha1.EventType_VM_DELETED_ON_RELEASE})
	return &emptypb.Empty{}, nil
}

// ListLeases implements poolmgrv1alpha1.LeaseServer.
func (s *Server) ListLeases(_ context.Context, req *poolmgrv1alpha1.ListLeasesRequest) (*poolmgrv1alpha1.ListLeasesResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var filter *poolKey
	if ref := req.GetPoolRef(); ref != nil {
		filter = &poolKey{ref.GetNamespace(), ref.GetName()}
	}
	return &poolmgrv1alpha1.ListLeasesResponse{Leases: s.leaseRecords(filter)}, nil
}

func (s *Server) leaseRecords(filter *poolKey) []*poolmgrv1alpha1.LeaseRecord {
	out := []*poolmgrv1alpha1.LeaseRecord{}
	for _, l := range s.leases {
		if filter != nil && l.pool != *filter {
			continue
		}
		out = append(out, &poolmgrv1alpha1.LeaseRecord{
			LeaseId: l.id, VmUid: l.vmUID, PoolName: l.pool.name, PoolNamespace: l.pool.ns, RequestId: l.requestID,
			ClaimedAt: timestamppb.New(l.claimedAt), LastHeartbeatAt: timestamppb.New(l.lastBeat), ExpiresAt: timestamppb.New(l.expiresAt),
		})
	}
	return out
}

// --- Events ---

func (s *Server) emit(ev *poolmgrv1alpha1.Event) {
	key := poolKey{ev.GetPoolNamespace(), ev.GetPoolName()}
	s.nextEventID[key]++
	ev.Id = s.nextEventID[key]
	ev.CreatedAt = timestamppb.Now()
	s.outbox = append(s.outbox, ev)
	for sub := range s.subs {
		if sub.filter == nil || *sub.filter == key {
			sub.push(ev)
		}
	}
}

// Subscribe implements poolmgrv1alpha1.EventsServer: replay the outbox,
// then stream live events until the client goes away or DropSubscribers.
func (s *Server) Subscribe(req *poolmgrv1alpha1.SubscribeRequest, stream grpc.ServerStreamingServer[poolmgrv1alpha1.Event]) error {
	sub := &subscriber{notify: make(chan struct{}, 1), drop: make(chan struct{})}
	if ref := req.GetPool(); ref != nil {
		sub.filter = &poolKey{ref.GetNamespace(), ref.GetName()}
	}
	// Snapshot the outbox and register in one critical section so no live
	// event is missed or duplicated between the replay and the live phase.
	s.mu.Lock()
	for _, ev := range s.outbox {
		if sub.filter == nil || *sub.filter == (poolKey{ev.GetPoolNamespace(), ev.GetPoolName()}) {
			sub.queue = append(sub.queue, ev)
		}
	}
	s.subs[sub] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, sub)
		s.mu.Unlock()
	}()
	for {
		for _, ev := range sub.pop() {
			select {
			case <-sub.drop:
				return status.Error(codes.Unavailable, "fakepoolmgr: subscribers dropped")
			default:
			}
			if err := stream.Send(ev); err != nil {
				return err
			}
		}
		select {
		case <-sub.notify:
		case <-sub.drop:
			return status.Error(codes.Unavailable, "fakepoolmgr: subscribers dropped")
		case <-stream.Context().Done():
			return nil
		}
	}
}

// --- PoolAdmin ---

// GetPool implements the one PoolAdmin RPC muster reads.
func (s *Server) GetPool(_ context.Context, req *poolmgrv1alpha1.GetPoolRequest) (*poolmgrv1alpha1.Pool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, key, ok := s.poolOf(req.GetRef())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "pool %s/%s not found", key.ns, key.name)
	}
	return &poolmgrv1alpha1.Pool{Spec: &poolmgrv1alpha1.PoolSpec{
		Name: p.Name, Namespace: p.Namespace,
		HeartbeatInterval:        durationpb.New(p.HeartbeatInterval),
		HeartbeatExpiryThreshold: durationpb.New(p.HeartbeatExpiryThreshold),
	}}, nil
}
