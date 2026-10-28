package config_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/liquidmetal-dev/muster/internal/config"
)

func TestDurationUnmarshal(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want time.Duration
		err  string
	}{
		{"minutes", `"10m"`, 10 * time.Minute, ""},
		{"mixed", `"1h30m"`, 90 * time.Minute, ""},
		{"number", `600`, 0, `must be a duration string such as "10m"`},
		{"word", `"soon"`, 0, "invalid duration"},
		{"empty", `""`, 0, "invalid duration"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d config.Duration
			err := json.Unmarshal([]byte(tt.in), &d)
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) {
					t.Fatalf("error = %v, want one containing %q", err, tt.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if d.Duration() != tt.want {
				t.Fatalf("Duration() = %v, want %v", d.Duration(), tt.want)
			}
		})
	}
}

func TestDurationMarshalRoundTrip(t *testing.T) {
	raw, err := json.Marshal(config.Duration(2 * time.Minute))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(raw) != `"2m0s"` {
		t.Fatalf("marshal = %s, want %q", raw, `"2m0s"`)
	}
	var back config.Duration
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Duration() != 2*time.Minute {
		t.Fatalf("round trip = %v, want 2m", back.Duration())
	}
}
