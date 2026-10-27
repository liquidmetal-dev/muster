package battery

import (
	"context"
	"errors"
	"strings"
	"testing"

	poolmgrv1alpha1 "github.com/liquidmetal-dev/battery/api/proto/poolmgr/v1alpha1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/liquidmetal-dev/muster/internal/tlsconfig"
	"github.com/liquidmetal-dev/muster/internal/vmsource"
	"github.com/liquidmetal-dev/muster/internal/vmsource/battery/fakepoolmgr"
)

func TestNewRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{"no addr", Config{Namespace: "muster", TLS: tlsconfig.Client{Insecure: true}}, "addr"},
		{"no namespace", Config{Addr: "x:1", TLS: tlsconfig.Client{Insecure: true}}, "namespace"},
		{"mixed tls", Config{Addr: "x:1", Namespace: "muster", TLS: tlsconfig.Client{Insecure: true, CAFile: "x"}}, "insecure"},
		{"bad attempts", Config{Addr: "x:1", Namespace: "muster", TLS: tlsconfig.Client{Insecure: true}, MaxAttempts: -1}, "max_attempts"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.cfg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("New() error = %v, want one containing %q", err, tt.want)
			}
		})
	}
}

func TestAcquireReturnsVMWithHost(t *testing.T) {
	fake := fakepoolmgr.New()
	seedPool(fake, "a", 1)
	src := newTestSource(t, fake, nil)
	vm := acquire(t, src, "a", "req-1")
	if vm.ID() != fake.Leases()[0].GetVmUid() {
		t.Fatalf("ID() = %q, want the fake's vm uid %q", vm.ID(), fake.Leases()[0].GetVmUid())
	}
	if vm.LeaseID() == "" {
		t.Fatal("LeaseID() is empty")
	}
	if got := vm.Host(); got != (vmsource.HostInfo{Name: "host-a", Address: "127.0.0.1:9090"}) {
		t.Fatalf("Host() = %+v", got)
	}
	if vm.Pool() != "a" {
		t.Fatalf("Pool() = %q, want a", vm.Pool())
	}
}

func TestAcquireExhausted(t *testing.T) {
	fake := fakepoolmgr.New()
	seedPool(fake, "a", 1)
	src := newTestSource(t, fake, nil)
	acquire(t, src, "a", "req-1")
	_, err := src.Acquire(context.Background(), "a", "req-2")
	if !errors.Is(err, vmsource.ErrExhausted) {
		t.Fatalf("Acquire error = %v, want ErrExhausted", err)
	}
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("status.Code = %v, want ResourceExhausted", status.Code(err))
	}
	if n := fake.CallCount("ClaimVM"); n != 2 {
		t.Fatalf("CallCount(ClaimVM) = %d, want 2 (no retry on exhausted)", n)
	}
}

func TestAcquireSameRequestIDSameLease(t *testing.T) {
	fake := fakepoolmgr.New()
	seedPool(fake, "a", 2)
	src := newTestSource(t, fake, nil)
	first := acquire(t, src, "a", "req-1")
	second := acquire(t, src, "a", "req-1")
	if first.ID() != second.ID() || first.LeaseID() != second.LeaseID() {
		t.Fatalf("second Acquire = %s/%s, want %s/%s", second.ID(), second.LeaseID(), first.ID(), first.LeaseID())
	}
}

func TestAcquireUnknownPool(t *testing.T) {
	src := newTestSource(t, fakepoolmgr.New(), nil)
	_, err := src.Acquire(context.Background(), "nope", "req-1")
	if !errors.Is(err, vmsource.ErrPoolNotFound) {
		t.Fatalf("Acquire error = %v, want ErrPoolNotFound", err)
	}
}

func TestAcquireEmptyRequestID(t *testing.T) {
	fake := fakepoolmgr.New()
	seedPool(fake, "a", 1)
	src := newTestSource(t, fake, nil)
	if _, err := src.Acquire(context.Background(), "a", ""); err == nil {
		t.Fatal("Acquire with empty requestID succeeded, want error")
	}
	if n := fake.CallCount("ClaimVM"); n != 0 {
		t.Fatalf("CallCount(ClaimVM) = %d, want 0", n)
	}
}

func TestAcquireUsesNamespace(t *testing.T) {
	fake := fakepoolmgr.New()
	seedPool(fake, "a", 1)
	src := newTestSource(t, fake, nil)
	acquire(t, src, "a", "req-1")
	var req *poolmgrv1alpha1.ClaimVMRequest
	for _, c := range fake.Calls() {
		if c.Method == "ClaimVM" {
			req = c.Req.(*poolmgrv1alpha1.ClaimVMRequest)
		}
	}
	if req == nil || req.GetPool().GetNamespace() != "muster" || req.GetPool().GetName() != "a" || req.GetRequestId() != "req-1" {
		t.Fatalf("ClaimVM request = %v, want pool muster/a request_id req-1", req)
	}
}

func TestReleaseCallsThrough(t *testing.T) {
	fake := fakepoolmgr.New()
	seedPool(fake, "a", 1)
	src := newTestSource(t, fake, nil)
	vm := acquire(t, src, "a", "req-1")
	if err := src.Release(context.Background(), vm); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if n := len(fake.Leases()); n != 0 {
		t.Fatalf("fake leases after Release = %d, want 0", n)
	}
	if n := fake.CallCount("ReleaseVM"); n != 1 {
		t.Fatalf("CallCount(ReleaseVM) = %d, want 1", n)
	}
}

func TestReleaseForeignVM(t *testing.T) {
	fake := fakepoolmgr.New()
	src := newTestSource(t, fake, nil)
	err := src.Release(context.Background(), stubVM{})
	if !errors.Is(err, ErrForeignVM) {
		t.Fatalf("Release error = %v, want ErrForeignVM", err)
	}
	if n := fake.CallCount("ReleaseVM"); n != 0 {
		t.Fatalf("CallCount(ReleaseVM) = %d, want 0", n)
	}
}

func TestReleaseLeaseGoneIsSuccess(t *testing.T) {
	fake := fakepoolmgr.New()
	seedPool(fake, "a", 1)
	src := newTestSource(t, fake, nil)
	vm := acquire(t, src, "a", "req-1")
	if err := src.Release(context.Background(), vm); err != nil {
		t.Fatalf("first Release: %v", err)
	}
	if err := src.Release(context.Background(), vm); err != nil {
		t.Fatalf("second Release = %v, want nil (already released)", err)
	}
}

func TestReleaseAfterLostResponse(t *testing.T) {
	fake := fakepoolmgr.New()
	seedPool(fake, "a", 1)
	src := newTestSource(t, fake, nil)
	vm := acquire(t, src, "a", "req-1")
	fake.LoseNext("ReleaseVM")
	if err := src.Release(context.Background(), vm); err != nil {
		t.Fatalf("Release after lost response = %v, want nil", err)
	}
	if n := fake.CallCount("ReleaseVM"); n != 2 {
		t.Fatalf("CallCount(ReleaseVM) = %d, want 2", n)
	}
	if n := len(fake.Leases()); n != 0 {
		t.Fatalf("fake leases = %d, want 0", n)
	}
}

func TestLeasesFiltersNamespace(t *testing.T) {
	fake := fakepoolmgr.New()
	seedPool(fake, "a", 1)
	fake.AddPool(fakepoolmgr.Pool{Name: "b", Namespace: "other", Capacity: 1, HeartbeatInterval: 20e9, HeartbeatExpiryThreshold: 60e9})
	src := newTestSource(t, fake, nil)
	acquire(t, src, "a", "req-a")
	if _, err := src.lease.ClaimVM(context.Background(), &poolmgrv1alpha1.ClaimVMRequest{
		Pool: &poolmgrv1alpha1.PoolRef{Name: "b", Namespace: "other"}, RequestId: "req-b"}); err != nil {
		t.Fatalf("raw ClaimVM in other namespace: %v", err)
	}
	leases, err := src.Leases(context.Background())
	if err != nil {
		t.Fatalf("Leases: %v", err)
	}
	if len(leases) != 1 {
		t.Fatalf("Leases() = %v, want exactly one", leases)
	}
	l := leases[0]
	if l.Pool != "a" || l.RequestID != "req-a" || l.VMID == "" || l.LeaseID == "" || !l.ExpiresAt.After(l.ClaimedAt) {
		t.Fatalf("Leases()[0] = %+v", l)
	}
}

func TestBasicAuthHeaderSent(t *testing.T) {
	fake := fakepoolmgr.New()
	seedPool(fake, "a", 1)
	fake.RequireBasicAuth("s3cret")
	withToken := newTestSource(t, fake, func(c *Config) { c.BasicAuthToken = "s3cret" })
	acquire(t, withToken, "a", "req-1")

	without := newTestSource(t, fakeWithAuth(t, "s3cret"), nil)
	_, err := without.Acquire(context.Background(), "a", "req-2")
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("Acquire without token: status = %v (%v), want Unauthenticated", status.Code(err), err)
	}
}

func fakeWithAuth(t *testing.T, token string) *fakepoolmgr.Server {
	t.Helper()
	fake := fakepoolmgr.New()
	seedPool(fake, "a", 1)
	fake.RequireBasicAuth(token)
	return fake
}
