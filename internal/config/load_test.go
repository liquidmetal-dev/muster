package config_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liquidmetal-dev/muster/internal/config"
	"github.com/liquidmetal-dev/muster/internal/tlsconfig/tlstest"
)

// writeFixture applies mutate to the fixture and writes it to a temp file.
func writeFixture(t *testing.T, mutate func(m map[string]any)) string {
	t.Helper()
	m := fixture(t)
	if mutate != nil {
		mutate(m)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "musterd.json")
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func rsaPEM(t *testing.T, pkcs8 bool) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if pkcs8 {
		der, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

func ecdsaPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
}

// setSecrets points the fixture's env secrets at good values.
func setSecrets(t *testing.T) {
	t.Helper()
	t.Setenv("MUSTER_APP_KEY", rsaPEM(t, false))
	t.Setenv("MUSTER_WEBHOOK_SECRET", "wh")
}

func TestLoadMissingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "missing.json")
	_, err := config.Load(p)
	if !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), p) {
		t.Fatalf("Load() error = %v, want fs.ErrNotExist naming %s", err, p)
	}
}

func TestLoadResolvesEnvSecrets(t *testing.T) {
	setSecrets(t)
	cfg, err := config.Load(writeFixture(t, nil))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.GitHub.App.PrivateKey.IsSet() {
		t.Error("App.PrivateKey not set")
	}
	if got := string(cfg.GitHub.WebhookSecret.Value()); got != "wh" {
		t.Errorf("WebhookSecret = %q, want wh", got)
	}
}

func TestLoadResolvesFileSecrets(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	secretFile := filepath.Join(dir, "wh")
	if err := os.WriteFile(tokenFile, []byte("tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secretFile, []byte("wh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(writeFixture(t, func(m map[string]any) {
		del(m, "github.app")
		set(m, "github.token_file", tokenFile)
		del(m, "github.webhook_secret_env")
		set(m, "github.webhook_secret_file", secretFile)
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := string(cfg.GitHub.Token.Value()); got != "tok" {
		t.Errorf("Token = %q, want tok", got)
	}
	if got := string(cfg.GitHub.WebhookSecret.Value()); got != "wh" {
		t.Errorf("WebhookSecret = %q, want wh", got)
	}
}

func TestLoadRejectsNonRSAKey(t *testing.T) {
	t.Setenv("MUSTER_APP_KEY", ecdsaPEM(t))
	t.Setenv("MUSTER_WEBHOOK_SECRET", "wh")
	_, err := config.Load(writeFixture(t, nil))
	wantErr(t, err, "github.app.private_key_env: not a PEM-encoded RSA private key")
}

func TestLoadAcceptsPKCS8RSAKey(t *testing.T) {
	t.Setenv("MUSTER_APP_KEY", rsaPEM(t, true))
	t.Setenv("MUSTER_WEBHOOK_SECRET", "wh")
	if _, err := config.Load(writeFixture(t, nil)); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadProbesTLSFiles(t *testing.T) {
	setSecrets(t)
	certs := tlstest.Generate(t, "localhost")
	missing := filepath.Join(t.TempDir(), "missing-ca.pem")
	withTLS := func(hostCA string) func(m map[string]any) {
		return func(m map[string]any) {
			set(m, "listen.tls", map[string]any{"cert_file": certs.ServerCertFile, "key_file": certs.ServerKeyFile})
			set(m, "battery.tls", map[string]any{"ca_file": certs.CAFile})
			set(m, "flintlock.hosts.host-a.tls", map[string]any{"ca_file": hostCA})
		}
	}
	_, err := config.Load(writeFixture(t, withTLS(missing)))
	if !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), `flintlock.hosts["host-a"].tls:`) {
		t.Fatalf("Load() error = %v, want fs.ErrNotExist at the host override", err)
	}
	if _, err := config.Load(writeFixture(t, withTLS(certs.CAFile))); err != nil {
		t.Fatalf("Load() with valid files error = %v", err)
	}
}

func TestLoadSkipsListenTLSWhenAbsent(t *testing.T) {
	setSecrets(t)
	if _, err := config.Load(writeFixture(t, func(m map[string]any) { del(m, "listen.tls") })); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadParseErrorsReturnedFirst(t *testing.T) {
	_, err := config.Load(writeFixture(t, func(m map[string]any) { set(m, "battery.addr", "") }))
	wantErr(t, err, "battery.addr: required")
	if strings.Contains(err.Error(), "not set") {
		t.Fatalf("Load() error = %v, must not resolve secrets after a parse failure", err)
	}
}

func TestLoadCollectsResolveErrors(t *testing.T) {
	_, err := config.Load(writeFixture(t, nil))
	wantErr(t, err, "github.app.private_key_env")
	wantErr(t, err, "github.webhook_secret_env")
}
