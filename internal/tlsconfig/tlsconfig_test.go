package tlsconfig_test

import (
	"crypto/tls"
	"errors"
	"io/fs"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/liquidmetal-dev/muster/internal/tlsconfig"
	"github.com/liquidmetal-dev/muster/internal/tlsconfig/tlstest"
)

func TestClientCredentialsRejectsMixedSettings(t *testing.T) {
	tests := []struct {
		name string
		c    tlsconfig.Client
		want string
	}{
		{"insecure with ca", tlsconfig.Client{Insecure: true, CAFile: "x"}, "insecure"},
		{"insecure with cert", tlsconfig.Client{Insecure: true, CertFile: "x", KeyFile: "y"}, "insecure"},
		{"cert without key", tlsconfig.Client{CertFile: "x"}, "key_file"},
		{"key without cert", tlsconfig.Client{KeyFile: "x"}, "cert_file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.c.Credentials()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Credentials() error = %v, want one containing %q", err, tt.want)
			}
		})
	}
}

func TestClientCredentialsInsecure(t *testing.T) {
	creds, err := tlsconfig.Client{Insecure: true}.Credentials()
	if err != nil {
		t.Fatalf("Credentials() error = %v", err)
	}
	if got := creds.Info().SecurityProtocol; got != "insecure" {
		t.Fatalf("SecurityProtocol = %q, want %q", got, "insecure")
	}
}

func TestClientCredentialsMissingCAFile(t *testing.T) {
	_, err := tlsconfig.Client{CAFile: t.TempDir() + "/missing.pem"}.Credentials()
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Credentials() error = %v, want one wrapping fs.ErrNotExist", err)
	}
}

func TestTLSConfigMinVersion(t *testing.T) {
	certs := tlstest.Generate(t, "localhost")
	cfg, err := tlsconfig.Client{CAFile: certs.CAFile}.TLSConfig()
	if err != nil {
		t.Fatalf("TLSConfig() error = %v", err)
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("MinVersion = %x, want %x", cfg.MinVersion, tls.VersionTLS12)
	}
}

func TestTLSConfigInsecureIsNil(t *testing.T) {
	cfg, err := tlsconfig.Client{Insecure: true}.TLSConfig()
	if err != nil || cfg != nil {
		t.Fatalf("TLSConfig() = %v, %v; want nil, nil", cfg, err)
	}
}

func TestClientHandshakeTLS(t *testing.T) {
	certs := tlstest.Generate(t, "localhost")
	addr := serveTLS(t, certs, false)
	cfg, err := tlsconfig.Client{CAFile: certs.CAFile}.TLSConfig()
	if err != nil {
		t.Fatalf("TLSConfig() error = %v", err)
	}
	if err := handshake(addr, cfg); err != nil {
		t.Fatalf("handshake: %v", err)
	}
}

func TestClientHandshakeMTLS(t *testing.T) {
	certs := tlstest.Generate(t, "localhost")
	addr := serveTLS(t, certs, true)

	withCert, err := tlsconfig.Client{CAFile: certs.CAFile, CertFile: certs.ClientCertFile, KeyFile: certs.ClientKeyFile}.TLSConfig()
	if err != nil {
		t.Fatalf("TLSConfig() error = %v", err)
	}
	if err := handshake(addr, withCert); err != nil {
		t.Fatalf("handshake with client cert: %v", err)
	}

	withoutCert, err := tlsconfig.Client{CAFile: certs.CAFile}.TLSConfig()
	if err != nil {
		t.Fatalf("TLSConfig() error = %v", err)
	}
	if err := handshake(addr, withoutCert); err == nil {
		t.Fatal("handshake without client cert succeeded, want failure")
	}
}

// serveTLS listens on loopback with the server pair and accepts one connection per handshake.
func serveTLS(t *testing.T, certs tlstest.Certs, requireClient bool) string {
	t.Helper()
	pair, err := tls.LoadX509KeyPair(certs.ServerCertFile, certs.ServerKeyFile)
	if err != nil {
		t.Fatalf("load server pair: %v", err)
	}
	cfg := &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	if requireClient {
		pool, err := tlsconfig.Client{CAFile: certs.CAFile}.TLSConfig()
		if err != nil {
			t.Fatalf("ca pool: %v", err)
		}
		cfg.ClientCAs = pool.RootCAs
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	lis, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = lis.Close() })
	go func() {
		for {
			conn, err := lis.Accept()
			if err != nil {
				return
			}
			go func() {
				// Write one byte after a good handshake so the client can
				// tell success from a post-handshake rejection (TLS 1.3
				// reports a missing client certificate only after the
				// client's Handshake returns).
				if tc, ok := conn.(*tls.Conn); ok && tc.Handshake() == nil {
					_, _ = tc.Write([]byte{1})
				}
				_ = conn.Close()
			}()
		}
	}()
	return lis.Addr().String()
}

// handshake connects, completes the TLS handshake and waits for the
// server's one-byte acknowledgement.
func handshake(addr string, cfg *tls.Config) error {
	cfg = cfg.Clone()
	cfg.ServerName = "localhost"
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return err
	}
	tc := tls.Client(conn, cfg)
	defer func() { _ = tc.Close() }()
	if err := tc.Handshake(); err != nil {
		return err
	}
	_, err = tc.Read(make([]byte, 1))
	return err
}

func TestClientKeyEnv(t *testing.T) {
	certs := tlstest.Generate(t, "localhost")
	pemBytes, err := os.ReadFile(certs.ClientKeyFile)
	if err != nil {
		t.Fatalf("read client key: %v", err)
	}
	t.Setenv("MUSTER_TEST_KEY", string(pemBytes))
	cfg, err := tlsconfig.Client{CAFile: certs.CAFile, CertFile: certs.ClientCertFile, KeyEnv: "MUSTER_TEST_KEY"}.TLSConfig()
	if err != nil {
		t.Fatalf("TLSConfig() error = %v", err)
	}
	if len(cfg.Certificates) != 1 {
		t.Fatalf("len(Certificates) = %d, want 1", len(cfg.Certificates))
	}
}

func TestClientKeyEnvUnset(t *testing.T) {
	certs := tlstest.Generate(t, "localhost")
	_, err := tlsconfig.Client{CertFile: certs.ClientCertFile, KeyEnv: "MUSTER_TEST_UNSET"}.TLSConfig()
	if err == nil || !strings.Contains(err.Error(), "MUSTER_TEST_UNSET") {
		t.Fatalf("TLSConfig() error = %v, want one naming MUSTER_TEST_UNSET", err)
	}
}

func TestClientValidateKeySources(t *testing.T) {
	tests := []struct {
		name string
		c    tlsconfig.Client
		want string
	}{
		{"two key sources", tlsconfig.Client{CertFile: "x", KeyFile: "y", KeyEnv: "Z"}, "exactly one"},
		{"key_env without cert", tlsconfig.Client{KeyEnv: "Z"}, "cert_file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.c.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error = %v, want one containing %q", err, tt.want)
			}
		})
	}
}

func TestServerValidate(t *testing.T) {
	tests := []struct {
		name string
		s    tlsconfig.Server
		want string
	}{
		{"empty", tlsconfig.Server{}, "cert_file"},
		{"insecure with cert", tlsconfig.Server{Insecure: true, CertFile: "x"}, "insecure"},
		{"cert without key", tlsconfig.Server{CertFile: "x"}, "key_file"},
		{"two key sources", tlsconfig.Server{CertFile: "x", KeyFile: "y", KeyEnv: "Z"}, "exactly one"},
		{"client ca alone", tlsconfig.Server{ClientCAFile: "x"}, "cert_file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.s.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error = %v, want one containing %q", err, tt.want)
			}
		})
	}
}

func TestServerTLSConfigInsecure(t *testing.T) {
	cfg, err := tlsconfig.Server{Insecure: true}.TLSConfig()
	if err != nil || cfg != nil {
		t.Fatalf("TLSConfig() = %v, %v; want nil, nil", cfg, err)
	}
}

func TestServerTLSConfigMutual(t *testing.T) {
	certs := tlstest.Generate(t, "localhost")
	cfg, err := tlsconfig.Server{CertFile: certs.ServerCertFile, KeyFile: certs.ServerKeyFile, ClientCAFile: certs.CAFile}.TLSConfig()
	if err != nil {
		t.Fatalf("TLSConfig() error = %v", err)
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("MinVersion = %x, want %x", cfg.MinVersion, tls.VersionTLS12)
	}
	if cfg.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Fatalf("ClientAuth = %v, want RequireAndVerifyClientCert", cfg.ClientAuth)
	}
	if cfg.ClientCAs == nil {
		t.Fatal("ClientCAs = nil, want a pool")
	}
	if len(cfg.Certificates) != 1 {
		t.Fatalf("len(Certificates) = %d, want 1", len(cfg.Certificates))
	}
}

func TestServerTLSConfigNoClientCA(t *testing.T) {
	certs := tlstest.Generate(t, "localhost")
	cfg, err := tlsconfig.Server{CertFile: certs.ServerCertFile, KeyFile: certs.ServerKeyFile}.TLSConfig()
	if err != nil {
		t.Fatalf("TLSConfig() error = %v", err)
	}
	if cfg.ClientAuth != tls.NoClientCert || cfg.ClientCAs != nil {
		t.Fatalf("ClientAuth = %v, ClientCAs = %v; want NoClientCert and nil", cfg.ClientAuth, cfg.ClientCAs)
	}
}
