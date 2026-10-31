package github_test

import (
	"context"
	"crypto/rsa"
	"testing"
	"time"

	"github.com/liquidmetal-dev/muster/internal/clock"
	"github.com/liquidmetal-dev/muster/internal/fakegithub"
	"github.com/liquidmetal-dev/muster/internal/github"
)

const (
	appID          = 123
	installationID = 456
	account        = "liquidmetal-dev"
	patToken       = "ghp_testtoken"
)

var startTime = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

type env struct {
	Fake  *fakegithub.Server
	URL   string
	Clock *clock.Manual
	Key   *rsa.PrivateKey
}

// newEnv starts a fake GitHub whose clock is shared with every client the
// env builds, so token expiry and JWT checks agree.
func newEnv(t *testing.T, opts ...func(*fakegithub.Config)) env {
	t.Helper()
	key := fakegithub.AppKey(t)
	manual := clock.NewManual(startTime)
	cfg := fakegithub.Config{
		AppID:               appID,
		AppPublicKey:        &key.PublicKey,
		InstallationID:      installationID,
		InstallationAccount: account,
		TokenTTL:            time.Hour,
		PATs:                map[string]string{patToken: "octocat"},
		Clock:               manual,
	}
	for _, o := range opts {
		o(&cfg)
	}
	fake := fakegithub.New(cfg)
	return env{Fake: fake, URL: fake.Start(t), Clock: manual, Key: key}
}

func noSleep(context.Context, time.Duration) error { return nil }

func (e env) client(t *testing.T, cfg github.Config, mods []func(*github.Config)) *github.Client {
	t.Helper()
	cfg.BaseURL = e.URL
	cfg.Clock = e.Clock
	github.SetSleep(&cfg, noSleep)
	for _, m := range mods {
		m(&cfg)
	}
	c, err := github.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// AppClient returns a client authenticated as the fake's App installation.
func (e env) AppClient(t *testing.T, mods ...func(*github.Config)) *github.Client {
	t.Helper()
	return e.client(t, github.Config{App: &github.AppAuth{AppID: appID, InstallationID: installationID, PrivateKey: e.Key}}, mods)
}

// PATClient returns a client authenticated with the fake's PAT.
func (e env) PATClient(t *testing.T, mods ...func(*github.Config)) *github.Client {
	t.Helper()
	return e.client(t, github.Config{Token: patToken}, mods)
}
