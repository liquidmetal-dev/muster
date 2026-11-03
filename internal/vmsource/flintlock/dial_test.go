package flintlock

import (
	"crypto/tls"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	"github.com/liquidmetal-dev/muster/internal/config"
	"github.com/liquidmetal-dev/muster/internal/tlsconfig"
	"github.com/liquidmetal-dev/muster/internal/tlsconfig/tlstest"
	"github.com/liquidmetal-dev/muster/internal/vmsource"
	"github.com/liquidmetal-dev/muster/internal/vmsource/flintlock/fakeexec"
)

func serveTLS(t *testing.T, fake *fakeexec.Server, certs tlstest.Certs) string {
	t.Helper()
	pair, err := tls.LoadX509KeyPair(certs.ServerCertFile, certs.ServerKeyFile)
	if err != nil {
		t.Fatalf("load server pair: %v", err)
	}
	creds := credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12})
	return serveTCP(t, fake, grpc.Creds(creds))
}

func TestPerHostTLS(t *testing.T) {
	ca1 := tlstest.Generate(t, "localhost")
	ca2 := tlstest.Generate(t, "localhost")
	addr1 := serveTLS(t, fakeexec.New(), ca1)
	addr2 := serveTLS(t, fakeexec.New(), ca2)
	e := newTCPExecer(t, config.Flintlock{
		TLS:   tlsconfig.Client{CAFile: ca1.CAFile},
		Hosts: map[string]config.Host{"host-b": {TLS: tlsconfig.Client{CAFile: ca2.CAFile}}},
	})
	ctx := t.Context()

	if err := e.CheckHost(ctx, vmsource.HostInfo{Name: "host-a", Address: addr1}); err != nil {
		t.Fatalf("host-a with the global CA: %v", err)
	}
	if err := e.CheckHost(ctx, vmsource.HostInfo{Name: "host-b", Address: addr2}); err != nil {
		t.Fatalf("host-b with its override CA: %v", err)
	}
	err := e.CheckHost(ctx, vmsource.HostInfo{Name: "host-b", Address: addr1})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("host-b against the wrong CA = %v (code %v), want Unavailable", err, status.Code(err))
	}
}

func TestNewExecerRejectsBadTLS(t *testing.T) {
	_, err := NewExecer(config.Flintlock{
		TLS:   tlsconfig.Client{Insecure: true},
		Hosts: map[string]config.Host{"host-b": {TLS: tlsconfig.Client{CAFile: "/nonexistent"}}},
	})
	if err == nil {
		t.Fatal("NewExecer with a missing CA file succeeded, want an error")
	}
}
