package github

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/liquidmetal-dev/muster/internal/clock"
	"github.com/liquidmetal-dev/muster/internal/config"
)

// AppAuth is GitHub App authentication (requirement GH-1).
type AppAuth struct {
	AppID          int64
	InstallationID int64
	PrivateKey     *rsa.PrivateKey
}

// Config configures a Client. Exactly one of App or Token is set.
type Config struct {
	// BaseURL is the REST API root; empty means https://api.github.com/.
	// Any other host is treated as GitHub Enterprise Server (GH-4).
	BaseURL string
	App     *AppAuth
	// Token is a personal access token.
	Token string
	// HTTPClient supplies the base transport; nil uses http.DefaultTransport.
	HTTPClient *http.Client
	// Clock drives token expiry and JWT times; nil uses clock.Real().
	Clock clock.Clock
	// Logger defaults to slog.Default().
	Logger *slog.Logger
	// Metrics defaults to NopMetrics.
	Metrics Metrics
	// CallTimeout bounds each HTTP attempt. Defaults to 30s.
	CallTimeout time.Duration
	// MaxAttempts bounds attempts per call, the first included. Defaults to 5.
	MaxAttempts int
	// MaxWait is the longest single wait before a retry. A rate limit that
	// asks for longer is returned as *RateLimitedError. Defaults to 60s.
	MaxWait time.Duration

	sleep func(ctx context.Context, d time.Duration) error
}

func (c Config) withDefaults() Config {
	if c.Clock == nil {
		c.Clock = clock.Real()
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.Metrics == nil {
		c.Metrics = NopMetrics{}
	}
	if c.CallTimeout <= 0 {
		c.CallTimeout = 30 * time.Second
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 5
	}
	if c.MaxWait <= 0 {
		c.MaxWait = 60 * time.Second
	}
	if c.sleep == nil {
		c.sleep = sleepContext
	}
	return c
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// FromConfig builds a Config from loaded configuration. Load has already
// resolved the secrets; FromConfig parses the App private key.
func FromConfig(cfg *config.Config) (Config, error) {
	gh := cfg.GitHub
	out := Config{BaseURL: gh.APIBaseURL}
	switch {
	case gh.App != nil && gh.Token.IsSet():
		return Config{}, fmt.Errorf("%w: both an App and a token are configured", ErrInvalidRequest)
	case gh.App != nil:
		key, err := parsePrivateKey(gh.App.PrivateKey.Value())
		if err != nil {
			return Config{}, fmt.Errorf("%w: app private key: %w", ErrInvalidRequest, err)
		}
		out.App = &AppAuth{AppID: gh.App.AppID, InstallationID: gh.App.InstallationID, PrivateKey: key}
	case gh.Token.IsSet():
		out.Token = string(gh.Token.Value())
	default:
		return Config{}, fmt.Errorf("%w: no GitHub credential is configured", ErrInvalidRequest)
	}
	return out, nil
}

// parsePrivateKey accepts PKCS#1 and PKCS#8 PEM, the two forms GitHub
// issues for App keys.
func parsePrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("no PEM block")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse key: %w", err)
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("not an RSA key")
	}
	return rsaKey, nil
}
