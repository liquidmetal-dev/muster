package flintlock

import (
	"net"
	"testing"

	"google.golang.org/grpc"

	"github.com/liquidmetal-dev/muster/internal/config"
	"github.com/liquidmetal-dev/muster/internal/tlsconfig"
	"github.com/liquidmetal-dev/muster/internal/vmsource"
	"github.com/liquidmetal-dev/muster/internal/vmsource/flintlock/fakeexec"
)

const bufconnAddr = "passthrough:///fakeexec"

var hostA = vmsource.HostInfo{Name: "host-a", Address: bufconnAddr}

// newTestExecer returns an Execer whose every connection reaches fake over
// bufconn.
func newTestExecer(t *testing.T, fake *fakeexec.Server, opts ...Option) *Execer {
	t.Helper()
	e, err := NewExecer(config.Flintlock{TLS: tlsconfig.Client{Insecure: true}}, opts...)
	if err != nil {
		t.Fatalf("NewExecer: %v", err)
	}
	e.dialOptions = []grpc.DialOption{fake.ServeBufconn(t)}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

// newTCPExecer returns an Execer that dials real addresses.
func newTCPExecer(t *testing.T, cfg config.Flintlock, opts ...Option) *Execer {
	t.Helper()
	e, err := NewExecer(cfg, opts...)
	if err != nil {
		t.Fatalf("NewExecer: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

// serveTCP runs fake on a loopback TCP listener with opts and returns its
// address.
func serveTCP(t *testing.T, fake *fakeexec.Server, opts ...grpc.ServerOption) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = fake.Serve(lis, opts...) }()
	t.Cleanup(fake.Stop)
	return lis.Addr().String()
}
