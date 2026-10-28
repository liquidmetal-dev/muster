package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadSecretFromFile(t *testing.T) {
	s, err := readSecret("github.token_file", writeTemp(t, "tok\n"), "github.token_env", "")
	if err != nil {
		t.Fatalf("readSecret() error = %v", err)
	}
	if string(s.Value()) != "tok" {
		t.Fatalf("Value() = %q, want tok", s.Value())
	}
}

func TestReadSecretFromEnv(t *testing.T) {
	t.Setenv("MUSTER_T", "  tok \r\n")
	s, err := readSecret("github.token_file", "", "github.token_env", "MUSTER_T")
	if err != nil {
		t.Fatalf("readSecret() error = %v", err)
	}
	if string(s.Value()) != "tok" {
		t.Fatalf("Value() = %q, want tok", s.Value())
	}
}

func TestReadSecretEnvUnset(t *testing.T) {
	_, err := readSecret("github.token_file", "", "github.token_env", "MUSTER_UNSET")
	for _, want := range []string{"github.token_env", "MUSTER_UNSET", "not set"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want one containing %q", err, want)
		}
	}
}

func TestReadSecretEnvEmpty(t *testing.T) {
	t.Setenv("MUSTER_E", "")
	_, err := readSecret("github.token_file", "", "github.token_env", "MUSTER_E")
	if err == nil || !strings.Contains(err.Error(), "MUSTER_E is not set") {
		t.Fatalf("error = %v, want the not-set error", err)
	}
}

func TestReadSecretFileMissing(t *testing.T) {
	_, err := readSecret("github.token_file", filepath.Join(t.TempDir(), "missing"), "github.token_env", "")
	if !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), "github.token_file") {
		t.Fatalf("error = %v, want fs.ErrNotExist at github.token_file", err)
	}
}

func TestReadSecretEmptyAfterTrim(t *testing.T) {
	_, err := readSecret("github.token_file", writeTemp(t, "\n\n"), "github.token_env", "")
	if err == nil || !strings.Contains(err.Error(), "secret is empty") {
		t.Fatalf("error = %v, want secret is empty", err)
	}
}

func TestReadSecretErrorNeverContainsValue(t *testing.T) {
	t.Setenv("MUSTER_S", "s3cret")
	_, err := readSecret("github.token_file", filepath.Join(t.TempDir(), "missing"), "github.token_env", "MUSTER_S")
	if err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("error = %v, must not contain the value", err)
	}
}
