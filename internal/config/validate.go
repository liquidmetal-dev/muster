package config

import (
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// MinPollInterval protects GitHub's rate limit.
const MinPollInterval = 10 * time.Second

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// IsDNSLabel reports whether s is a lowercase DNS label of at most 63
// characters. Pool names and the battery namespace must be, because they
// become battery pool names and GitHub runner-name prefixes.
func IsDNSLabel(s string) bool {
	return len(s) <= 63 && dnsLabel.MatchString(s)
}

const (
	msgHostPort = "must be host:port with a port between 1 and 65535"
	msgDNSLabel = "must be a lowercase DNS label of at most 63 characters"
)

// validate runs every static rule, recording failures in errs. It does
// not change cfg.
func validate(cfg *Config, errs *errorList) {
	validateListen(cfg, errs)
	validateAPI(cfg, errs)
	validateAddr("metrics_addr", cfg.MetricsAddr, errs)
	validateGitHub(cfg, errs)
	validateBattery(cfg, errs)
	validateFlintlock(cfg, errs)
	validateDefaults(cfg, errs)
	validatePools(cfg, errs)
	validateCrossCutting(cfg, errs)
}

func validateListen(cfg *Config, errs *errorList) {
	if cfg.Listen == nil {
		return
	}
	validateAddr("listen.addr", cfg.Listen.Addr, errs)
	if cfg.Listen.TLS != nil {
		if err := cfg.Listen.TLS.Validate(); err != nil {
			errs.addErr("listen.tls", err)
		}
	}
}

func validateAPI(cfg *Config, errs *errorList) {
	if cfg.API == nil {
		return
	}
	validateAddr("api.addr", cfg.API.Addr, errs)
	if err := cfg.API.TLS.Validate(); err != nil {
		errs.addErr("api.tls", err)
	}
}

func validateGitHub(cfg *Config, errs *errorList) {
	gh := &cfg.GitHub
	if u, err := url.Parse(gh.APIBaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		errs.add("github.api_base_url", "must be an http or https URL")
	}

	hasToken := gh.TokenFile != "" || gh.TokenEnv != ""
	if (gh.App != nil) == hasToken {
		errs.add("github", "exactly one of app or token_file/token_env must be set")
	}
	if gh.TokenFile != "" && gh.TokenEnv != "" {
		errs.add("github.token_file", "set together with token_env; set exactly one")
	}
	if gh.App != nil {
		if gh.App.AppID <= 0 {
			errs.add("github.app.app_id", "must be positive")
		}
		if gh.App.InstallationID <= 0 {
			errs.add("github.app.installation_id", "must be positive")
		}
		if (gh.App.PrivateKeyFile != "") == (gh.App.PrivateKeyEnv != "") {
			errs.add("github.app", "exactly one of private_key_file or private_key_env must be set")
		}
	}

	hasSecret := gh.WebhookSecretFile != "" || gh.WebhookSecretEnv != ""
	switch {
	case gh.WebhookSecretFile != "" && gh.WebhookSecretEnv != "":
		errs.add("github.webhook_secret_file", "set together with webhook_secret_env; set exactly one")
	case cfg.Listen != nil && !hasSecret:
		errs.add("github.webhook_secret_file", "required when listen is set (or webhook_secret_env)")
	}

	if gh.Poll.Enabled {
		validatePoll(&gh.Poll, errs)
	}
}

func validatePoll(p *Poll, errs *errorList) {
	if len(p.Repos) == 0 {
		errs.add("github.poll.repos", "required when poll is enabled")
	}
	if p.Interval.Duration() < MinPollInterval {
		errs.add("github.poll.interval", "must be at least %s", MinPollInterval)
	}
	seen := map[string]int{}
	for i, repo := range p.Repos {
		path := "github.poll.repos[" + strconv.Itoa(i) + "]"
		if !isOwnerName(repo) {
			errs.add(path, "must be owner/name")
			continue
		}
		key := strings.ToLower(repo)
		if j, dup := seen[key]; dup {
			errs.add(path, "duplicate of repos[%d]", j)
			continue
		}
		seen[key] = i
	}
}

func validateBattery(cfg *Config, errs *errorList) {
	if cfg.Battery.Addr == "" {
		errs.add("battery.addr", "required")
	} else {
		validateAddr("battery.addr", cfg.Battery.Addr, errs)
	}
	if !IsDNSLabel(cfg.Battery.Namespace) {
		errs.add("battery.namespace", msgDNSLabel)
	}
	if err := cfg.Battery.TLS.Validate(); err != nil {
		errs.addErr("battery.tls", err)
	}
}

func validateFlintlock(cfg *Config, errs *errorList) {
	if err := cfg.Flintlock.TLS.Validate(); err != nil {
		errs.addErr("flintlock.tls", err)
	}
	for name, host := range cfg.Flintlock.Hosts {
		if name == "" {
			errs.add("flintlock.hosts", "host name must not be empty")
			continue
		}
		if err := host.TLS.Validate(); err != nil {
			errs.addErr("flintlock.hosts["+strconv.Quote(name)+"].tls", err)
		}
	}
}

func validateDefaults(cfg *Config, errs *errorList) {
	for _, d := range []struct {
		path string
		v    Duration
	}{
		{"defaults.claim_deadline", cfg.Defaults.ClaimDeadline},
		{"defaults.start_timeout", cfg.Defaults.StartTimeout},
		{"defaults.idle_timeout", cfg.Defaults.IdleTimeout},
		{"defaults.max_session_duration", cfg.Defaults.MaxSessionDuration},
	} {
		if d.v <= 0 {
			errs.add(d.path, "must be positive")
		}
	}
}

// validateAddr requires host:port with a numeric port in 1..65535.
// net.SplitHostPort alone accepts ":" with an empty port.
func validateAddr(path, addr string, errs *errorList) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		errs.add(path, msgHostPort)
		return
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		errs.add(path, msgHostPort)
	}
}

// isOwnerName reports whether s has the form owner/name with both parts
// valid owner-style names.
func isOwnerName(s string) bool {
	owner, name, ok := strings.Cut(s, "/")
	return ok && isOwnerPart(owner) && isOwnerPart(name)
}

// isOwnerPart reports whether s can be a GitHub owner or repository name:
// non-empty, no "/", no whitespace.
func isOwnerPart(s string) bool {
	return s != "" && !strings.Contains(s, "/") && strings.IndexFunc(s, unicode.IsSpace) < 0
}
