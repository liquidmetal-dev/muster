package config_test

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/liquidmetal-dev/muster/internal/config"
	"github.com/liquidmetal-dev/muster/internal/tlsconfig"
)

func TestParseValidFixture(t *testing.T) {
	cfg, err := parseMutated(t, nil)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cfg.Pools[0].Name != "ubuntu-small" {
		t.Fatalf("Pools[0].Name = %q, want %q", cfg.Pools[0].Name, "ubuntu-small")
	}
}

func TestParseEmptyInput(t *testing.T) {
	_, err := config.Parse(strings.NewReader("  \n"))
	wantErr(t, err, "configuration is empty")
}

func TestParseUnknownField(t *testing.T) {
	_, err := parseMutated(t, func(m map[string]any) { m["max_concurent"] = 1 })
	wantErr(t, err, "max_concurent: unknown field")
}

func TestParseUnknownNestedField(t *testing.T) {
	_, err := parseMutated(t, func(m map[string]any) { pool(m, 0)["sizee"] = 1 })
	wantErr(t, err, "pools[0].sizee: unknown field")
}

func TestParseDurationNamesField(t *testing.T) {
	_, err := parseMutated(t, func(m map[string]any) { set(m, "defaults.claim_deadline", 600) })
	wantErr(t, err, `defaults.claim_deadline: must be a duration string such as "10m"`)
}

func TestParseBadDurationNamesField(t *testing.T) {
	_, err := parseMutated(t, func(m map[string]any) { set(m, "github.poll.interval", "soon") })
	wantErr(t, err, "github.poll.interval:")
}

func TestParseTypeErrorNamesField(t *testing.T) {
	_, err := parseMutated(t, func(m map[string]any) { pool(m, 0)["size"] = "x" })
	wantErr(t, err, "pools[0].size:")
}

func TestParseDuplicateKey(t *testing.T) {
	raw, err := os.ReadFile("testdata/valid.json")
	if err != nil {
		t.Fatal(err)
	}
	dup := bytes.Replace(raw, []byte(`"metrics_addr": ":9090",`), []byte(`"metrics_addr": ":1", "metrics_addr": ":2",`), 1)
	_, err = config.Parse(bytes.NewReader(dup))
	wantErr(t, err, "metrics_addr")
}

func TestParseTrailingData(t *testing.T) {
	raw, err := os.ReadFile("testdata/valid.json")
	if err != nil {
		t.Fatal(err)
	}
	_, err = config.Parse(bytes.NewReader(append(raw, []byte("\n{}")...)))
	if err == nil {
		t.Fatal("Parse() error = nil, want one for trailing data")
	}
}

func TestParseSyntaxError(t *testing.T) {
	_, err := config.Parse(strings.NewReader("{"))
	var fe *config.FieldError
	if !errors.As(err, &fe) {
		t.Fatalf("error = %v, want a *FieldError", err)
	}
	if fe.Path != "" {
		t.Fatalf("Path = %q, want empty", fe.Path)
	}
}

func TestParseDefaults(t *testing.T) {
	cfg, err := parseMutated(t, func(m map[string]any) {
		del(m, "metrics_addr")
		del(m, "shutdown_grace")
		del(m, "listen.addr")
		del(m, "api.addr")
		del(m, "github.api_base_url")
		del(m, "github.poll.interval")
		del(m, "battery.namespace")
		del(m, "defaults")
		delete(pool(m, 0), "max_concurrent")
	})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"metrics_addr", cfg.MetricsAddr, ":9090"},
		{"shutdown_grace", cfg.ShutdownGrace.Duration(), 30 * time.Second},
		{"listen.addr", cfg.Listen.Addr, ":8443"},
		{"api.addr", cfg.API.Addr, ":9443"},
		{"api_base_url", cfg.GitHub.APIBaseURL, "https://api.github.com"},
		{"poll.interval", cfg.GitHub.Poll.Interval.Duration(), 60 * time.Second},
		{"namespace", cfg.Battery.Namespace, "muster"},
		{"claim_deadline", cfg.Defaults.ClaimDeadline.Duration(), 10 * time.Minute},
		{"start_timeout", cfg.Defaults.StartTimeout.Duration(), 2 * time.Minute},
		{"idle_timeout", cfg.Defaults.IdleTimeout.Duration(), 5 * time.Minute},
		{"max_session_duration", cfg.Defaults.MaxSessionDuration.Duration(), 6 * time.Hour},
		{"heartbeat_interval", cfg.Defaults.HeartbeatInterval.Duration(), 30 * time.Second},
		{"heartbeat_expiry_threshold", cfg.Defaults.HeartbeatExpiryThreshold.Duration(), 90 * time.Second},
		{"pool strategy", cfg.Pools[0].Strategy(), config.ReplenishmentStrategy{Type: config.StrategyImmediateOnLease}},
		{"max_concurrent", cfg.Pools[0].MaxConcurrent, 6},
		{"pool max_session_duration", cfg.Pools[0].MaxSessionDuration.Duration(), 6 * time.Hour},
		{"pool work_folder", cfg.Pools[0].WorkFolder, "_work"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestParseExplicitValuesSurviveDefaults(t *testing.T) {
	cfg, err := parseMutated(t, func(m map[string]any) {
		pool(m, 0)["max_concurrent"] = 4
		set(m, "battery.namespace", "ci")
	})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cfg.Pools[0].MaxConcurrent != 4 || cfg.Battery.Namespace != "ci" {
		t.Fatalf("max_concurrent = %d, namespace = %q; want 4 and ci", cfg.Pools[0].MaxConcurrent, cfg.Battery.Namespace)
	}
}

func TestParseSecretsNotSettableFromJSON(t *testing.T) {
	_, err := parseMutated(t, func(m map[string]any) { set(m, "github.token", "x") })
	wantErr(t, err, "github.token: unknown field")
}

func TestParseListenWithoutTLS(t *testing.T) {
	cfg, err := parseMutated(t, func(m map[string]any) { del(m, "listen.tls") })
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cfg.Listen.TLS != nil {
		t.Fatalf("Listen.TLS = %+v, want nil", cfg.Listen.TLS)
	}
}

func TestScopeOwner(t *testing.T) {
	tests := []struct {
		s    config.Scope
		want string
	}{
		{config.Scope{Org: "a"}, "a"},
		{config.Scope{Repo: "a/b"}, "a"},
		{config.Scope{}, ""},
	}
	for _, tt := range tests {
		if got := tt.s.Owner(); got != tt.want {
			t.Errorf("%+v.Owner() = %q, want %q", tt.s, got, tt.want)
		}
	}
}

func TestFlintlockTLSFor(t *testing.T) {
	f := config.Flintlock{
		TLS:   tlsconfig.Client{CAFile: "global"},
		Hosts: map[string]config.Host{"host-a": {TLS: tlsconfig.Client{CAFile: "a"}}},
	}
	if got := f.TLSFor("host-a").CAFile; got != "a" {
		t.Fatalf("TLSFor(host-a).CAFile = %q, want a", got)
	}
	if got := f.TLSFor("host-z").CAFile; got != "global" {
		t.Fatalf("TLSFor(host-z).CAFile = %q, want global", got)
	}
}

func TestFieldErrorString(t *testing.T) {
	withPath := &config.FieldError{Path: "pools[0].name", Err: errors.New("required")}
	if got := withPath.Error(); got != "pools[0].name: required" {
		t.Fatalf("Error() = %q", got)
	}
	noPath := &config.FieldError{Err: errors.New("x")}
	if got := noPath.Error(); got != "x" {
		t.Fatalf("Error() = %q", got)
	}
}

func TestFieldErrorUnwrap(t *testing.T) {
	err := errors.Join(&config.FieldError{Path: "a", Err: fmt.Errorf("read: %w", fs.ErrNotExist)})
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("errors.Is(fs.ErrNotExist) = false")
	}
}

func TestParsePoolDefaults(t *testing.T) {
	cfg, err := parseMutated(t, func(m map[string]any) {
		set(m, "defaults.max_session_duration", "2h")
		p := secondPool(m, "explicit")
		p["max_session_duration"] = "1h"
		p["work_folder"] = "w"
	})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if got := cfg.Pools[0].MaxSessionDuration.Duration(); got != 2*time.Hour {
		t.Errorf("pools[0].max_session_duration = %v, want 2h from defaults", got)
	}
	if got := cfg.Pools[0].WorkFolder; got != config.DefaultWorkFolder {
		t.Errorf("pools[0].work_folder = %q, want %q", got, config.DefaultWorkFolder)
	}
	if got := cfg.Pools[1].MaxSessionDuration.Duration(); got != time.Hour {
		t.Errorf("pools[1].max_session_duration = %v, want 1h", got)
	}
	if got := cfg.Pools[1].WorkFolder; got != "w" {
		t.Errorf("pools[1].work_folder = %q, want w", got)
	}
}

func TestParseDefaultsLog(t *testing.T) {
	cfg, err := parseMutated(t, nil)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	want := config.Log{Format: config.LogFormatJSON, Level: config.DefaultLogLevel}
	if cfg.Log != want {
		t.Fatalf("Log = %+v, want %+v", cfg.Log, want)
	}
}

func TestParseRejectsBadLog(t *testing.T) {
	for _, tc := range []struct {
		name string
		log  map[string]any
		want string
	}{
		{"format", map[string]any{"format": "xml"}, "log.format: must be one of json, text"},
		{"format case", map[string]any{"format": "TEXT"}, "log.format: must be one of json, text"},
		{"level", map[string]any{"level": "trace"}, "log.level: must be one of debug, info, warn, error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseMutated(t, func(m map[string]any) { m["log"] = tc.log })
			wantErr(t, err, tc.want)
		})
	}
}
