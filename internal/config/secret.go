package config

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

const redacted = "[redacted]"

// Secret holds a sensitive value resolved by Load. Every text form of a
// Secret prints [redacted] (requirement SEC-3).
type Secret struct {
	value []byte
}

// NewSecret wraps a value.
func NewSecret(b []byte) Secret {
	return Secret{value: b}
}

// Value returns the secret bytes.
func (s Secret) Value() []byte {
	return s.value
}

// IsSet reports whether a value was resolved.
func (s Secret) IsSet() bool {
	return len(s.value) > 0
}

// String implements fmt.Stringer and redacts.
func (s Secret) String() string {
	return redacted
}

// GoString implements fmt.GoStringer and redacts.
func (s Secret) GoString() string {
	return "config.Secret{" + redacted + "}"
}

// LogValue implements slog.LogValuer and redacts.
func (s Secret) LogValue() slog.Value {
	return slog.StringValue(redacted)
}

// MarshalJSON writes "[redacted]".
func (s Secret) MarshalJSON() ([]byte, error) {
	return []byte(`"` + redacted + `"`), nil
}

// readSecret resolves a secret from file or from the environment variable
// env. filePath and envPath are the JSON paths of the two settings, used
// in errors. The value is trimmed of surrounding whitespace and must not
// be empty. When both sources are set, file wins; the static rules reject
// that case before readSecret runs.
func readSecret(filePath, file, envPath, env string) (Secret, error) {
	var raw []byte
	path := filePath
	switch {
	case file != "":
		b, err := os.ReadFile(filepath.Clean(file))
		if err != nil {
			return Secret{}, fmt.Errorf("%s: read %s: %w", filePath, file, err)
		}
		raw = b
	case env != "":
		path = envPath
		v, ok := os.LookupEnv(env)
		if !ok || v == "" {
			return Secret{}, fmt.Errorf("%s: environment variable %s is not set", envPath, env)
		}
		raw = []byte(v)
	default:
		return Secret{}, fmt.Errorf("%s: no source set", filePath)
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return Secret{}, errors.New(path + ": secret is empty")
	}
	return Secret{value: raw}, nil
}
