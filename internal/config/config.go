package config

import (
	"encoding/json/jsontext"
	"strings"

	"github.com/liquidmetal-dev/flintlock/api/types"

	"github.com/liquidmetal-dev/muster/internal/tlsconfig"
)

// Config is the whole configuration file.
type Config struct {
	// Listen is the webhook listener. Nil disables the webhook intake.
	Listen *Listen `json:"listen,omitempty"`
	// API is the gRPC API listener. Nil means the API is not served.
	API *API `json:"api,omitempty"`
	// MetricsAddr serves Prometheus metrics. Defaults to ":9090".
	MetricsAddr string    `json:"metrics_addr,omitempty"`
	GitHub      GitHub    `json:"github"`
	Battery     Battery   `json:"battery"`
	Flintlock   Flintlock `json:"flintlock"`
	Defaults    Defaults  `json:"defaults"`
	Pools       []Pool    `json:"pools"`
}

// Listen is the webhook listener.
type Listen struct {
	// Addr defaults to ":8443".
	Addr string `json:"addr,omitempty"`
	// TLS is optional. Nil serves plain HTTP; terminate TLS in front of
	// the listener in that case (requirement INTAKE-8).
	TLS *tlsconfig.Server `json:"tls,omitempty"`
}

// API is the gRPC API listener.
type API struct {
	// Addr defaults to ":9443".
	Addr string `json:"addr,omitempty"`
	// TLS is required: plaintext needs an explicit insecure (SEC-2).
	TLS tlsconfig.Server `json:"tls"`
}

// GitHub holds the single GitHub credential and the intake settings.
type GitHub struct {
	// APIBaseURL defaults to https://api.github.com (GH-4).
	APIBaseURL string `json:"api_base_url,omitempty"`
	// App selects GitHub App authentication. Exactly one of App or a token
	// source must be set.
	App *App `json:"app,omitempty"`
	// TokenFile and TokenEnv are the two sources of a personal access
	// token. At most one is set.
	TokenFile string `json:"token_file,omitempty"`
	TokenEnv  string `json:"token_env,omitempty"`
	// WebhookSecretFile and WebhookSecretEnv are the two sources of the
	// webhook HMAC secret. One is required when Listen is set.
	WebhookSecretFile string `json:"webhook_secret_file,omitempty"`
	WebhookSecretEnv  string `json:"webhook_secret_env,omitempty"`
	Poll              Poll   `json:"poll"`

	// Token and WebhookSecret are resolved by Load.
	Token         Secret `json:"-"`
	WebhookSecret Secret `json:"-"`
}

// App is the GitHub App credential.
type App struct {
	AppID          int64 `json:"app_id"`
	InstallationID int64 `json:"installation_id"`
	// PrivateKeyFile and PrivateKeyEnv are the two sources of the PEM
	// private key. Exactly one is set.
	PrivateKeyFile string `json:"private_key_file,omitempty"`
	PrivateKeyEnv  string `json:"private_key_env,omitempty"`
	// PrivateKey is the PEM, resolved by Load.
	PrivateKey Secret `json:"-"`
}

// Poll is the polling intake.
type Poll struct {
	Enabled bool `json:"enabled"`
	// Interval defaults to 60s and must be at least 10s.
	Interval Duration `json:"interval,omitempty"`
	// Repos lists owner/name repositories to poll.
	Repos []string `json:"repos,omitempty"`
}

// Battery is the poolmgrd endpoint.
type Battery struct {
	Addr string `json:"addr"`
	// Namespace defaults to "muster".
	Namespace string `json:"namespace,omitempty"`
	// TLS absent means TLS with the system CA pool.
	TLS tlsconfig.Client `json:"tls"`
}

// Flintlock holds the TLS settings for flintlock hosts.
type Flintlock struct {
	// TLS applies to every host without an override.
	TLS tlsconfig.Client `json:"tls"`
	// Hosts holds per-host overrides keyed by battery host name.
	Hosts map[string]Host `json:"hosts,omitempty"`
}

// Host is one per-host override.
type Host struct {
	TLS tlsconfig.Client `json:"tls"`
}

// TLSFor returns the override for host, or the global setting.
func (f Flintlock) TLSFor(host string) tlsconfig.Client {
	if h, ok := f.Hosts[host]; ok {
		return h.TLS
	}
	return f.TLS
}

// Defaults are the session timers.
type Defaults struct {
	ClaimDeadline      Duration `json:"claim_deadline,omitempty"`
	StartTimeout       Duration `json:"start_timeout,omitempty"`
	IdleTimeout        Duration `json:"idle_timeout,omitempty"`
	MaxSessionDuration Duration `json:"max_session_duration,omitempty"`
}

// Pool is one runner pool (POOL-1).
type Pool struct {
	Name   string   `json:"name"`
	Scope  Scope    `json:"scope"`
	Labels []string `json:"labels"`
	Size   int      `json:"size"`
	// MaxConcurrent defaults to twice Size (POOL-6).
	MaxConcurrent  int      `json:"max_concurrent,omitempty"`
	FlintlockHosts []string `json:"flintlock_hosts"`
	CreateCommands []string `json:"create_commands,omitempty"`
	// MicroVMTemplate is a flintlock MicroVMSpec in protojson.
	MicroVMTemplate jsontext.Value `json:"microvm_template"`
	// Template is the decoded MicroVMTemplate, set by Parse.
	Template *types.MicroVMSpec `json:"-"`
}

// Scope is where a pool registers its runners (GH-2).
type Scope struct {
	// Org registers at organization scope. Exactly one of Org or Repo.
	Org string `json:"org,omitempty"`
	// Repo is owner/name and registers at repository scope.
	Repo string `json:"repo,omitempty"`
	// RunnerGroup is only valid with Org.
	RunnerGroup string `json:"runner_group,omitempty"`
}

// Owner returns the organization or the owner part of Repo.
func (s Scope) Owner() string {
	if s.Org != "" {
		return s.Org
	}
	owner, _, _ := strings.Cut(s.Repo, "/")
	return owner
}
