package server_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/liquidmetal-dev/muster/internal/config"
	"github.com/liquidmetal-dev/muster/internal/server"
	"github.com/liquidmetal-dev/muster/internal/tlsconfig"
	"github.com/liquidmetal-dev/muster/internal/tlsconfig/tlstest"
)

var stub = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })

func newServer(t *testing.T, listen config.Listen) *server.Server {
	t.Helper()
	s, err := server.New(server.Config{Listen: listen, Webhook: stub})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// serve runs s on a loopback port and returns its host:port. Cleanup shuts
// it down and checks that Serve returned nil.
func serve(t *testing.T, s *server.Server) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	go func() { errc <- s.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
		if err := <-errc; err != nil {
			t.Errorf("Serve returned %v, want nil", err)
		}
	})
	return ln.Addr().String()
}

func do(t *testing.T, c *http.Client, method, url string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestNewRequiresWebhook(t *testing.T) {
	if _, err := server.New(server.Config{}); err == nil {
		t.Fatal("New without Webhook: got nil error")
	}
}

func TestPlainHTTPRoutesWebhook(t *testing.T) {
	addr := serve(t, newServer(t, config.Listen{}))
	if code, body := do(t, http.DefaultClient, http.MethodPost, "http://"+addr+"/webhook"); code != http.StatusOK || body != "ok" {
		t.Fatalf("POST /webhook = %d %q, want 200 ok", code, body)
	}
	if code, _ := do(t, http.DefaultClient, http.MethodGet, "http://"+addr+"/webhook"); code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /webhook = %d, want 405", code)
	}
	if code, _ := do(t, http.DefaultClient, http.MethodPost, "http://"+addr+"/other"); code != http.StatusNotFound {
		t.Fatalf("POST /other = %d, want 404", code)
	}
}

func TestInsecureTLSServesPlainHTTP(t *testing.T) {
	addr := serve(t, newServer(t, config.Listen{TLS: &tlsconfig.Server{Insecure: true}}))
	if code, _ := do(t, http.DefaultClient, http.MethodPost, "http://"+addr+"/webhook"); code != http.StatusOK {
		t.Fatalf("POST /webhook = %d, want 200", code)
	}
}

func TestTLSServesHTTPS(t *testing.T) {
	certs := tlstest.Generate(t, "localhost")
	addr := serve(t, newServer(t, config.Listen{TLS: &tlsconfig.Server{
		CertFile: certs.ServerCertFile,
		KeyFile:  certs.ServerKeyFile,
	}}))
	ca, err := os.ReadFile(certs.CAFile)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(ca)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
	if code, _ := do(t, client, http.MethodPost, "https://"+addr+"/webhook"); code != http.StatusOK {
		t.Fatalf("HTTPS POST /webhook = %d, want 200", code)
	}
	resp, err := http.Post("http://"+addr+"/webhook", "application/json", strings.NewReader("{}"))
	if err == nil {
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode == http.StatusOK {
			t.Fatal("plain HTTP to the TLS listener: got 200")
		}
	}
}

func TestNewFailsOnBadTLSFiles(t *testing.T) {
	_, err := server.New(server.Config{
		Listen:  config.Listen{TLS: &tlsconfig.Server{CertFile: "/nonexistent", KeyFile: "/nonexistent"}},
		Webhook: stub,
	})
	if err == nil {
		t.Fatal("New with missing cert files: got nil error")
	}
}

func TestHandleAddsRoute(t *testing.T) {
	s := newServer(t, config.Listen{})
	s.Handle("GET /healthz", stub)
	addr := serve(t, s)
	if code, _ := do(t, http.DefaultClient, http.MethodGet, "http://"+addr+"/healthz"); code != http.StatusOK {
		t.Fatalf("GET /healthz = %d, want 200", code)
	}
}

func TestShutdownStopsServe(t *testing.T) {
	s := newServer(t, config.Listen{})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	go func() { errc <- s.Serve(ln) }()
	do(t, http.DefaultClient, http.MethodPost, "http://"+ln.Addr().String()+"/webhook")
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("Serve returned %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not return within 1s of Shutdown")
	}
}
