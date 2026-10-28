package config_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/liquidmetal-dev/muster/internal/config"
)

type ruleCase struct {
	name   string
	mutate func(m map[string]any)
	want   string // "" means Parse must succeed
}

func runRuleCases(t *testing.T, tests []ruleCase) {
	t.Helper()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseMutated(t, tt.mutate)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("Parse() error = %v, want nil", err)
				}
				return
			}
			wantErr(t, err, tt.want)
		})
	}
}

func TestValidateSections(t *testing.T) {
	runRuleCases(t, []ruleCase{
		{"listen tls empty", func(m map[string]any) { set(m, "listen.tls", map[string]any{}) }, "listen.tls: "},
		{"listen tls cert and key ok", func(m map[string]any) {
			set(m, "listen.tls", map[string]any{"cert_file": "/c", "key_file": "/k"})
		}, ""},
		{"listen without tls ok", func(m map[string]any) { del(m, "listen.tls") }, ""},
		{"api tls insecure with cert", func(m map[string]any) {
			set(m, "api.tls", map[string]any{"insecure": true, "cert_file": "/c"})
		}, "api.tls: "},
		{"api tls key_env ok", func(m map[string]any) {
			set(m, "api.tls", map[string]any{"cert_file": "/c", "key_env": "K"})
		}, ""},
		{"api without tls", func(m map[string]any) { del(m, "api.tls") }, "api.tls: "},
		{"api absent ok", func(m map[string]any) { del(m, "api") }, ""},
		{"metrics_addr no port", func(m map[string]any) { m["metrics_addr"] = "localhost" }, "metrics_addr: must be host:port"},
		{"metrics_addr empty port", func(m map[string]any) { m["metrics_addr"] = ":" }, "metrics_addr: must be host:port with a port between 1 and 65535"},
		{"metrics_addr port zero", func(m map[string]any) { m["metrics_addr"] = ":0" }, "metrics_addr: must be host:port with a port between 1 and 65535"},
		{"metrics_addr port too big", func(m map[string]any) { m["metrics_addr"] = ":70000" }, "metrics_addr: must be host:port with a port between 1 and 65535"},
		{"metrics_addr port not numeric", func(m map[string]any) { m["metrics_addr"] = "localhost:http" }, "metrics_addr: must be host:port with a port between 1 and 65535"},
		{"metrics_addr ipv6 ok", func(m map[string]any) { m["metrics_addr"] = "[::1]:9090" }, ""},
		{"listen.addr bad", func(m map[string]any) { set(m, "listen.addr", "8443") }, "listen.addr: must be host:port"},
		{"api.addr bad", func(m map[string]any) { set(m, "api.addr", ":x") }, "api.addr: must be host:port"},
		{"battery.addr bad port", func(m map[string]any) { set(m, "battery.addr", "poolmgrd:0") }, "battery.addr: must be host:port"},
		{"api_base_url ftp", func(m map[string]any) { set(m, "github.api_base_url", "ftp://x") }, "github.api_base_url: must be an http or https URL"},
		{"api_base_url no host", func(m map[string]any) { set(m, "github.api_base_url", "https://") }, "github.api_base_url: must be an http or https URL"},
		{"app and token", func(m map[string]any) { set(m, "github.token_env", "T") }, "github: exactly one of app or token_file/token_env"},
		{"neither app nor token", func(m map[string]any) { del(m, "github.app") }, "github: exactly one of app or token_file/token_env"},
		{"token_file and token_env", func(m map[string]any) {
			del(m, "github.app")
			set(m, "github.token_file", "/t")
			set(m, "github.token_env", "T")
		}, "github.token_file: set together with token_env"},
		{"token only ok", func(m map[string]any) { del(m, "github.app"); set(m, "github.token_env", "T") }, ""},
		{"token_file only ok", func(m map[string]any) { del(m, "github.app"); set(m, "github.token_file", "/t") }, ""},
		{"app_id zero", func(m map[string]any) { set(m, "github.app.app_id", 0) }, "github.app.app_id: must be positive"},
		{"installation_id negative", func(m map[string]any) { set(m, "github.app.installation_id", -1) }, "github.app.installation_id: must be positive"},
		{"two key sources", func(m map[string]any) { set(m, "github.app.private_key_file", "/k") }, "github.app: exactly one of private_key_file or private_key_env"},
		{"no key source", func(m map[string]any) { del(m, "github.app.private_key_env") }, "github.app: exactly one of private_key_file or private_key_env"},
		{"app with private_key_file ok", func(m map[string]any) {
			del(m, "github.app.private_key_env")
			set(m, "github.app.private_key_file", "/k")
		}, ""},
		{"webhook secret missing with listen", func(m map[string]any) { del(m, "github.webhook_secret_env") }, "github.webhook_secret_file: required when listen is set"},
		{"webhook secret both", func(m map[string]any) { set(m, "github.webhook_secret_file", "/w") }, "github.webhook_secret_file: set together with webhook_secret_env"},
		{"webhook_secret_file ok", func(m map[string]any) {
			del(m, "github.webhook_secret_env")
			set(m, "github.webhook_secret_file", "/w")
		}, ""},
		{"no listen no secret ok", func(m map[string]any) { del(m, "listen"); del(m, "github.webhook_secret_env") }, ""},
		{"poll enabled no repos", func(m map[string]any) { del(m, "github.poll.repos") }, "github.poll.repos: required when poll is enabled"},
		{"poll interval 5s", func(m map[string]any) { set(m, "github.poll.interval", "5s") }, "github.poll.interval: must be at least 10s"},
		{"poll interval 10s ok", func(m map[string]any) { set(m, "github.poll.interval", "10s") }, ""},
		{"poll disabled short interval ok", func(m map[string]any) {
			set(m, "github.poll.enabled", false)
			set(m, "github.poll.interval", "1s")
		}, ""},
		{"repo bad form", func(m map[string]any) { set(m, "github.poll.repos", []any{"liquidmetal-dev"}) }, "github.poll.repos[0]: must be owner/name"},
		{"repo extra slash", func(m map[string]any) { set(m, "github.poll.repos", []any{"a/b/c"}) }, "github.poll.repos[0]: must be owner/name"},
		{"repo duplicate case", func(m map[string]any) {
			set(m, "github.poll.repos", []any{"liquidmetal-dev/muster", "LiquidMetal-Dev/Muster"})
		}, "github.poll.repos[1]: duplicate of repos[0]"},
		{"repo form ok", func(m map[string]any) {
			set(m, "github.poll.repos", []any{"liquidmetal-dev/muster", "liquidmetal-dev/battery"})
		}, ""},
		{"battery addr missing", func(m map[string]any) { set(m, "battery.addr", "") }, "battery.addr: required"},
		{"battery namespace uppercase", func(m map[string]any) { set(m, "battery.namespace", "Muster") }, "battery.namespace: must be a lowercase DNS label"},
		{"namespace ok", func(m map[string]any) { set(m, "battery.namespace", "ci-1") }, ""},
		{"battery tls mixed", func(m map[string]any) { set(m, "battery.tls", map[string]any{"insecure": true, "ca_file": "x"}) }, "battery.tls: "},
		{"flintlock tls mixed", func(m map[string]any) { set(m, "flintlock.tls", map[string]any{"insecure": true, "ca_file": "x"}) }, "flintlock.tls: "},
		{"flintlock host empty name", func(m map[string]any) {
			set(m, "flintlock.hosts", map[string]any{"": map[string]any{"tls": map[string]any{"insecure": true}}})
		}, "flintlock.hosts: host name must not be empty"},
		{"flintlock host tls mixed", func(m map[string]any) {
			set(m, "flintlock.hosts.host-a.tls", map[string]any{"insecure": true, "ca_file": "x"})
		}, `flintlock.hosts["host-a"].tls: `},
		{"flintlock host override ok", func(m map[string]any) {
			set(m, "flintlock.hosts", map[string]any{"host-b": map[string]any{"tls": map[string]any{"ca_file": "/ca"}}})
		}, ""},
		{"claim_deadline negative", func(m map[string]any) { set(m, "defaults.claim_deadline", "-1m") }, "defaults.claim_deadline: must be positive"},
		{"start_timeout negative", func(m map[string]any) { set(m, "defaults.start_timeout", "-1s") }, "defaults.start_timeout: must be positive"},
		{"idle_timeout negative", func(m map[string]any) { set(m, "defaults.idle_timeout", "-1s") }, "defaults.idle_timeout: must be positive"},
		{"max_session_duration negative", func(m map[string]any) { set(m, "defaults.max_session_duration", "-1h") }, "defaults.max_session_duration: must be positive"},
	})
}

func TestValidateDefaultsExplicitValues(t *testing.T) {
	cfg, err := parseMutated(t, func(m map[string]any) {
		for _, k := range []string{"claim_deadline", "start_timeout", "idle_timeout", "max_session_duration"} {
			set(m, "defaults."+k, "1m")
		}
	})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	for name, got := range map[string]time.Duration{
		"claim_deadline":       cfg.Defaults.ClaimDeadline.Duration(),
		"start_timeout":        cfg.Defaults.StartTimeout.Duration(),
		"idle_timeout":         cfg.Defaults.IdleTimeout.Duration(),
		"max_session_duration": cfg.Defaults.MaxSessionDuration.Duration(),
	} {
		if got != time.Minute {
			t.Errorf("%s = %v, want 1m", name, got)
		}
	}
}

func TestValidateAPIBaseURLTrailingSlash(t *testing.T) {
	cfg, err := parseMutated(t, func(m map[string]any) { set(m, "github.api_base_url", "https://ghe.example.com/api/v3/") })
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cfg.GitHub.APIBaseURL != "https://ghe.example.com/api/v3" {
		t.Fatalf("APIBaseURL = %q, want the trailing slash removed", cfg.GitHub.APIBaseURL)
	}
}

func TestValidateCollectsAllErrors(t *testing.T) {
	_, err := parseMutated(t, func(m map[string]any) {
		m["metrics_addr"] = "x"
		set(m, "battery.addr", "")
	})
	if err == nil {
		t.Fatal("Parse() error = nil")
	}
	for _, want := range []string{"metrics_addr:", "battery.addr:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err.Error(), want)
		}
	}
	if lines := strings.Count(err.Error(), "\n") + 1; lines < 2 {
		t.Errorf("error has %d lines, want at least 2", lines)
	}
}

func TestIsDNSLabel(t *testing.T) {
	long := strings.Repeat("a", 63)
	for _, ok := range []string{"a", "a-b1", long, "ubuntu-24-04"} {
		if !config.IsDNSLabel(ok) {
			t.Errorf("IsDNSLabel(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "-a", "a-", "A", long + "a", "a_b", "a b"} {
		if config.IsDNSLabel(bad) {
			t.Errorf("IsDNSLabel(%q) = true, want false", bad)
		}
	}
}

// secondPool appends a copy of pools[0] with the given name and returns it.
func secondPool(m map[string]any, name string) map[string]any {
	raw, _ := json.Marshal(pool(m, 0))
	var p map[string]any
	_ = json.Unmarshal(raw, &p)
	p["name"] = name
	m["pools"] = append(m["pools"].([]any), p)
	return p
}

func TestValidatePools(t *testing.T) {
	const pp = `pools["ubuntu-small"]`
	runRuleCases(t, []ruleCase{
		{"no pools", func(m map[string]any) { m["pools"] = []any{} }, "pools: at least one pool is required"},
		{"name missing", func(m map[string]any) { pool(m, 0)["name"] = "" }, "pools[0].name: required"},
		{"name uppercase", func(m map[string]any) { pool(m, 0)["name"] = "Ubuntu" }, `pools["Ubuntu"].name: must be a lowercase DNS label`},
		{"name with digits ok", func(m map[string]any) { pool(m, 0)["name"] = "ubuntu-24-04" }, ""},
		{"name duplicate", func(m map[string]any) { secondPool(m, "ubuntu-small") }, "pools[1].name: duplicate of pools[0]"},
		{"scope both", func(m map[string]any) { pool(m, 0)["scope"] = map[string]any{"org": "a", "repo": "a/b"} }, pp + ".scope: exactly one of org or repo"},
		{"scope neither", func(m map[string]any) { pool(m, 0)["scope"] = map[string]any{} }, pp + ".scope: exactly one of org or repo"},
		{"scope repo ok", func(m map[string]any) { pool(m, 0)["scope"] = map[string]any{"repo": "liquidmetal-dev/muster"} }, ""},
		{"scope repo bad", func(m map[string]any) { pool(m, 0)["scope"] = map[string]any{"repo": "muster"} }, pp + ".scope.repo: must be owner/name"},
		{"runner_group with repo", func(m map[string]any) {
			pool(m, 0)["scope"] = map[string]any{"repo": "liquidmetal-dev/muster", "runner_group": "g"}
		}, pp + ".scope.runner_group: only valid with org"},
		{"runner_group with org ok", nil, ""},
		{"labels empty", func(m map[string]any) { pool(m, 0)["labels"] = []any{} }, pp + ".labels: required"},
		{"labels no self-hosted", func(m map[string]any) { pool(m, 0)["labels"] = []any{"linux"} }, pp + `.labels: must include "self-hosted"`},
		{"labels minimal ok", func(m map[string]any) { pool(m, 0)["labels"] = []any{"self-hosted"} }, ""},
		{"label with space", func(m map[string]any) { pool(m, 0)["labels"] = []any{"self-hosted", "a b"} }, pp + ".labels[1]: must not be empty or contain whitespace or commas"},
		{"label with comma", func(m map[string]any) { pool(m, 0)["labels"] = []any{"self-hosted", "a,b"} }, pp + ".labels[1]: must not be empty or contain whitespace or commas"},
		{"label empty", func(m map[string]any) { pool(m, 0)["labels"] = []any{"self-hosted", ""} }, pp + ".labels[1]: must not be empty or contain whitespace or commas"},
		{"label duplicate case", func(m map[string]any) { pool(m, 0)["labels"] = []any{"self-hosted", "Linux", "linux"} }, pp + ".labels[2]: duplicate of labels[1]"},
		{"size zero", func(m map[string]any) { pool(m, 0)["size"] = 0 }, pp + ".size: must be at least 1"},
		{"max_concurrent negative", func(m map[string]any) { pool(m, 0)["max_concurrent"] = -1 }, pp + ".max_concurrent: must be at least 1"},
		{"max_concurrent below size ok", func(m map[string]any) { pool(m, 0)["max_concurrent"] = 1 }, ""},
		{"hosts empty", func(m map[string]any) { pool(m, 0)["flintlock_hosts"] = []any{} }, pp + ".flintlock_hosts: required"},
		{"hosts duplicate", func(m map[string]any) { pool(m, 0)["flintlock_hosts"] = []any{"host-a", "host-a"} }, pp + ".flintlock_hosts[1]: duplicate of flintlock_hosts[0]"},
		{"host entry empty", func(m map[string]any) { pool(m, 0)["flintlock_hosts"] = []any{"", "host-a"} }, pp + ".flintlock_hosts[0]: must not be empty"},
		{"two hosts ok", func(m map[string]any) { pool(m, 0)["flintlock_hosts"] = []any{"host-a", "host-b"} }, ""},
		{"host not in overrides ok", func(m map[string]any) { pool(m, 0)["flintlock_hosts"] = []any{"host-z"} }, ""},
		{"create_commands absent ok", func(m map[string]any) { delete(pool(m, 0), "create_commands") }, ""},
		{"template missing", func(m map[string]any) { delete(pool(m, 0), "microvm_template") }, pp + ".microvm_template: required"},
		{"template no kernel", func(m map[string]any) { delete(pool(m, 0)["microvm_template"].(map[string]any), "kernel") }, pp + ".microvm_template: kernel: required"},
		{"template id", func(m map[string]any) { pool(m, 0)["microvm_template"].(map[string]any)["id"] = "x" }, pp + ".microvm_template: id: managed by muster"},
		{"template snake_case ok", func(m map[string]any) {
			tm := pool(m, 0)["microvm_template"].(map[string]any)
			tm["memory_in_mb"] = tm["memoryInMb"]
			delete(tm, "memoryInMb")
			tm["root_volume"] = tm["rootVolume"]
			delete(tm, "rootVolume")
		}, ""},
	})
}

func TestValidatePoolsSetsTemplate(t *testing.T) {
	cfg, err := parseMutated(t, nil)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	tpl := cfg.Pools[0].Template
	if tpl == nil || tpl.Vcpu != 2 || tpl.Kernel == nil || tpl.Kernel.Image != "ghcr.io/liquidmetal-dev/firecracker-kernel:6.1-20260929-1349" {
		t.Fatalf("Template = %+v, want the decoded fixture template", tpl)
	}
}

func TestValidatePoolsSizeOneDefaultsMaxConcurrent(t *testing.T) {
	cfg, err := parseMutated(t, func(m map[string]any) { pool(m, 0)["size"] = 1; delete(pool(m, 0), "max_concurrent") })
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cfg.Pools[0].MaxConcurrent != 2 {
		t.Fatalf("MaxConcurrent = %d, want 2", cfg.Pools[0].MaxConcurrent)
	}
}

func TestValidateCrossCutting(t *testing.T) {
	runRuleCases(t, []ruleCase{
		{"no intake", func(m map[string]any) {
			del(m, "listen")
			del(m, "github.webhook_secret_env")
			set(m, "github.poll.enabled", false)
		}, "at least one intake must be enabled: set listen or github.poll.enabled"},
		{"poll only ok", func(m map[string]any) { del(m, "listen"); del(m, "github.webhook_secret_env") }, ""},
		{"listen only ok", func(m map[string]any) { set(m, "github.poll.enabled", false) }, ""},
		{"both intakes ok", nil, ""},
		{"polled repo outside scopes", func(m map[string]any) { set(m, "github.poll.repos", []any{"acme/tools"}) }, `github.poll.repos[0]: "acme/tools" is not within any pool scope`},
		{"polled repo within repo scope", func(m map[string]any) {
			pool(m, 0)["scope"] = map[string]any{"repo": "liquidmetal-dev/muster"}
			set(m, "github.poll.repos", []any{"LiquidMetal-Dev/Muster"})
		}, ""},
		{"owner mismatch across pools", func(m map[string]any) {
			secondPool(m, "acme-pool")["scope"] = map[string]any{"org": "acme"}
		}, `pools["acme-pool"].scope: owner "acme" does not match owner "liquidmetal-dev" of pools["ubuntu-small"]`},
		{"owner differs by case ok", func(m map[string]any) {
			secondPool(m, "second")["scope"] = map[string]any{"org": "LiquidMetal-Dev"}
		}, ""},
		{"two pools same owner ok", func(m map[string]any) {
			secondPool(m, "second")["scope"] = map[string]any{"org": "liquidmetal-dev"}
		}, ""},
		{"owner mismatch ignored on token path", func(m map[string]any) {
			del(m, "github.app")
			set(m, "github.token_env", "T")
			secondPool(m, "acme-pool")["scope"] = map[string]any{"org": "acme"}
		}, ""},
		{"polled repo owner mismatch", func(m map[string]any) {
			secondPool(m, "acme-pool")["scope"] = map[string]any{"org": "acme"}
			set(m, "github.poll.repos", []any{"acme/x"})
		}, `github.poll.repos[0]: owner "acme" does not match owner "liquidmetal-dev" of pools["ubuntu-small"]`},
	})
}

func TestScopeCovers(t *testing.T) {
	tests := []struct {
		s    config.Scope
		repo string
		want bool
	}{
		{config.Scope{Org: "a"}, "a/x", true},
		{config.Scope{Org: "a"}, "A/x", true},
		{config.Scope{Org: "a"}, "b/x", false},
		{config.Scope{Repo: "a/x"}, "A/X", true},
		{config.Scope{Repo: "a/x"}, "a/y", false},
	}
	for _, tt := range tests {
		if got := tt.s.Covers(tt.repo); got != tt.want {
			t.Errorf("%+v.Covers(%q) = %v, want %v", tt.s, tt.repo, got, tt.want)
		}
	}
}
