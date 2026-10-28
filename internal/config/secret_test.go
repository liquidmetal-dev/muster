package config_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/liquidmetal-dev/muster/internal/config"
)

func TestSecretRedaction(t *testing.T) {
	s := config.NewSecret([]byte("hunter2"))
	for name, out := range map[string]string{
		"Sprint": fmt.Sprint(s),
		"%#v":    fmt.Sprintf("%#v", s),
		"%v":     fmt.Sprintf("%v", s),
		"%+v":    fmt.Sprintf("%+v", &s),
	} {
		if strings.Contains(out, "hunter2") {
			t.Errorf("%s = %q leaks the value", name, out)
		}
	}
	if got := s.String(); got != "[redacted]" {
		t.Errorf("String() = %q, want [redacted]", got)
	}
	raw, err := json.Marshal(s)
	if err != nil || string(raw) != `"[redacted]"` {
		t.Errorf("json.Marshal = %s, %v; want \"[redacted]\"", raw, err)
	}
	wrapped, err := json.Marshal(struct{ S config.Secret }{s})
	if err != nil || bytes.Contains(wrapped, []byte("hunter2")) {
		t.Errorf("json.Marshal(struct) = %s, %v; leaks the value", wrapped, err)
	}
	var logBuf bytes.Buffer
	slog.New(slog.NewTextHandler(&logBuf, nil)).Info("x", slog.Any("token", s))
	if strings.Contains(logBuf.String(), "hunter2") {
		t.Errorf("slog output %q leaks the value", logBuf.String())
	}
	if string(s.Value()) != "hunter2" {
		t.Errorf("Value() = %q, want hunter2", s.Value())
	}
	if !s.IsSet() || (config.Secret{}).IsSet() {
		t.Errorf("IsSet() wrong: set=%v empty=%v", s.IsSet(), (config.Secret{}).IsSet())
	}
}
