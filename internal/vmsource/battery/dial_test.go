package battery

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	"github.com/liquidmetal-dev/muster/internal/tlsconfig"
	"github.com/liquidmetal-dev/muster/internal/tlsconfig/tlstest"
	"github.com/liquidmetal-dev/muster/internal/vmsource/battery/fakepoolmgr"
)

// serveTLS runs the fake on a loopback TCP listener with the server pair
// and, when requireClient is set, mutual TLS against the test CA.
func serveTLS(t *testing.T, fake *fakepoolmgr.Server, certs tlstest.Certs, requireClient bool) string {
	t.Helper()
	pair, err := tls.LoadX509KeyPair(certs.ServerCertFile, certs.ServerKeyFile)
	if err != nil {
		t.Fatalf("load server pair: %v", err)
	}
	cfg := &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	if requireClient {
		caPEM, err := os.ReadFile(certs.CAFile)
		if err != nil {
			t.Fatalf("read ca: %v", err)
		}
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(caPEM)
		cfg.ClientCAs = pool
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return serve(t, fake, grpc.Creds(credentials.NewTLS(cfg)))
}

func serve(t *testing.T, fake *fakepoolmgr.Server, opts ...grpc.ServerOption) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = fake.Serve(lis, opts...) }()
	t.Cleanup(fake.Stop)
	return lis.Addr().String()
}

func newTCPSource(t *testing.T, addr string, tlsCfg tlsconfig.Client) *Source {
	t.Helper()
	src, err := New(Config{Addr: addr, Namespace: "muster", TLS: tlsCfg, MaxAttempts: 1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = src.Close() })
	return src
}

func TestDialTLS(t *testing.T) {
	fake := fakepoolmgr.New()
	seedPool(fake, "a", 1)
	certs := tlstest.Generate(t, "localhost")
	addr := serveTLS(t, fake, certs, false)
	src := newTCPSource(t, addr, tlsconfig.Client{CAFile: certs.CAFile})
	acquire(t, src, "a", "req-1")
}

func TestDialMTLS(t *testing.T) {
	fake := fakepoolmgr.New()
	seedPool(fake, "a", 1)
	certs := tlstest.Generate(t, "localhost")
	addr := serveTLS(t, fake, certs, true)

	withCert := newTCPSource(t, addr, tlsconfig.Client{CAFile: certs.CAFile, CertFile: certs.ClientCertFile, KeyFile: certs.ClientKeyFile})
	acquire(t, withCert, "a", "req-1")

	withoutCert := newTCPSource(t, addr, tlsconfig.Client{CAFile: certs.CAFile})
	_, err := withoutCert.Acquire(context.Background(), "a", "req-2")
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("Acquire without client cert: %v (code %v), want Unavailable", err, status.Code(err))
	}
}

func TestDialInsecure(t *testing.T) {
	fake := fakepoolmgr.New()
	seedPool(fake, "a", 1)
	addr := serve(t, fake)
	src := newTCPSource(t, addr, tlsconfig.Client{Insecure: true})
	acquire(t, src, "a", "req-1")
}

func TestDialWrongCA(t *testing.T) {
	fake := fakepoolmgr.New()
	seedPool(fake, "a", 1)
	certs := tlstest.Generate(t, "localhost")
	other := tlstest.Generate(t, "localhost")
	addr := serveTLS(t, fake, certs, false)
	src := newTCPSource(t, addr, tlsconfig.Client{CAFile: other.CAFile})
	if _, err := src.Acquire(context.Background(), "a", "req-1"); err == nil {
		t.Fatal("Acquire with the wrong CA succeeded, want failure")
	}
	if n := len(fake.Leases()); n != 0 {
		t.Fatalf("fake leases = %d, want 0", n)
	}
}
