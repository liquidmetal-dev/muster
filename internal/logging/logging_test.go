package logging_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/liquidmetal-dev/muster/internal/config"
	"github.com/liquidmetal-dev/muster/internal/logging"
)

func TestNewJSON(t *testing.T) {
	var buf bytes.Buffer
	log := logging.New(&buf, config.Log{Format: config.LogFormatJSON, Level: config.LogLevelInfo})
	log.Debug("hidden")
	log.Info("x", "session_id", "abc")
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1: %q", len(lines), buf.String())
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("line is not JSON: %v", err)
	}
	if rec["session_id"] != "abc" || rec["msg"] != "x" {
		t.Fatalf("record = %v", rec)
	}
}

func TestNewTextDebug(t *testing.T) {
	var buf bytes.Buffer
	log := logging.New(&buf, config.Log{Format: config.LogFormatText, Level: config.LogLevelDebug})
	log.Debug("y")
	if !strings.Contains(buf.String(), "level=DEBUG") || !strings.Contains(buf.String(), "msg=y") {
		t.Fatalf("output = %q", buf.String())
	}
}
