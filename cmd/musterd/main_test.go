package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunVersion(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"-version"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, errb.String())
	}
	if !strings.HasPrefix(out.String(), "musterd v") {
		t.Fatalf("stdout = %q, want prefix %q", out.String(), "musterd v")
	}
	if !strings.HasSuffix(out.String(), "\n") {
		t.Fatalf("stdout = %q, want a trailing newline", out.String())
	}
	if errb.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", errb.String())
	}
}

func TestRunDefaultConfigPathMissing(t *testing.T) {
	if _, err := os.Stat(defaultConfigPath); err == nil {
		t.Skipf("%s exists on this machine", defaultConfigPath)
	}
	var out, errb bytes.Buffer
	code := run(nil, &out, &errb)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr: %s", code, errb.String())
	}
	for _, want := range []string{"musterd: invalid configuration " + defaultConfigPath, "no such file"} {
		if !strings.Contains(errb.String(), want) {
			t.Errorf("stderr = %q, want it to contain %q", errb.String(), want)
		}
	}
	if out.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", out.String())
	}
}

func TestRunUnknownFlag(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"-bogus"}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "flag provided but not defined: -bogus") {
		t.Fatalf("stderr = %q, want the unknown flag error", errb.String())
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("pipe closed") }

func TestRunVersionWriteError(t *testing.T) {
	var errb bytes.Buffer
	code := run([]string{"-version"}, failWriter{}, &errb)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(errb.String(), "pipe closed") {
		t.Fatalf("stderr = %q, want the write error", errb.String())
	}
}

// writeFixture copies internal/config's valid fixture, applies mutate and
// writes it to a temp file.
func writeFixture(t *testing.T, mutate func(m map[string]any)) string {
	t.Helper()
	raw, err := os.ReadFile("../../internal/config/testdata/valid.json")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(m)
	}
	raw, err = json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "musterd.json")
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func setSecrets(t *testing.T) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MUSTER_APP_KEY", string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})))
	t.Setenv("MUSTER_WEBHOOK_SECRET", "wh")
}

func TestRunInvalidConfigNamesField(t *testing.T) {
	path := writeFixture(t, func(m map[string]any) {
		m["battery"].(map[string]any)["addr"] = ""
		m["pools"].([]any)[0].(map[string]any)["labels"] = []any{"linux"}
	})
	var out, errb bytes.Buffer
	code := run([]string{"-config", path}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr: %s", code, errb.String())
	}
	for _, want := range []string{"battery.addr: required", `pools["ubuntu-small"].labels: must include "self-hosted"`} {
		if !strings.Contains(errb.String(), want) {
			t.Errorf("stderr = %q, want it to contain %q", errb.String(), want)
		}
	}
	if out.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", out.String())
	}
}

func TestRunValidConfig(t *testing.T) {
	setSecrets(t)
	path := writeFixture(t, nil)
	var out, errb bytes.Buffer
	code := run([]string{"-config", path}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, errb.String())
	}
	want := "musterd: configuration loaded from " + path + " (1 pools)\n"
	if out.String() != want {
		t.Fatalf("stdout = %q, want %q", out.String(), want)
	}
	if errb.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", errb.String())
	}
}
