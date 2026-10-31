package github_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/liquidmetal-dev/muster/internal/config"
	"github.com/liquidmetal-dev/muster/internal/fakegithub"
	"github.com/liquidmetal-dev/muster/internal/github"
)

func cfgWith(polled []string, scopes ...config.Scope) *config.Config {
	cfg := &config.Config{GitHub: config.GitHub{Poll: config.Poll{Repos: polled}}}
	for _, s := range scopes {
		cfg.Pools = append(cfg.Pools, config.Pool{Name: "p", Scope: s})
	}
	return cfg
}

func TestCheckerAppMatches(t *testing.T) {
	e := newEnv(t)
	e.Fake.AddOrg(account)
	e.Fake.AddRepo(account + "/muster")
	cfg := cfgWith([]string{account + "/muster"}, config.Scope{Org: account}, config.Scope{Repo: account + "/muster"})
	if err := (github.Checker{Client: e.AppClient(t)}).Check(context.Background(), cfg); err != nil {
		t.Fatalf("Check: %v", err)
	}
}

func TestCheckerOwnerCaseInsensitive(t *testing.T) {
	e := newEnv(t)
	// The fake matches org names exactly, so it holds the scope's spelling;
	// the comparison under test is scope owner against installation account.
	e.Fake.AddOrg("LiquidMetal-Dev")
	cfg := cfgWith(nil, config.Scope{Org: "LiquidMetal-Dev"})
	if err := (github.Checker{Client: e.AppClient(t)}).Check(context.Background(), cfg); err != nil {
		t.Fatalf("Check: %v", err)
	}
}

func TestCheckerAppMismatch(t *testing.T) {
	e := newEnv(t)
	e.Fake.AddOrg("other-org")
	e.Fake.AddRepo("other/x")
	cfg := cfgWith([]string{"other/x"}, config.Scope{Org: "other-org"})
	checker := github.Checker{Client: e.AppClient(t)}
	if checker.Name() != "github" {
		t.Fatalf("Name = %q", checker.Name())
	}
	err := config.Check(context.Background(), cfg, checker)
	if err == nil {
		t.Fatal("Check: want error")
	}
	msg := err.Error()
	for _, want := range []string{"github: ", "pools[0].scope", "github.poll.repos[0]", `installation account "` + account + `"`} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q lacks %q", msg, want)
		}
	}
	if n := e.Fake.RequestCount(fakegithub.PatternOrgRunners) + e.Fake.RequestCount(fakegithub.PatternRuns); n != 0 {
		t.Fatalf("probes = %d, want none for scopes that failed the owner check", n)
	}
}

func TestCheckerPAT(t *testing.T) {
	e := newEnv(t)
	e.Fake.AddOrg("acme")
	e.Fake.AddRepo("other/app")
	cfg := cfgWith([]string{"other/app"}, config.Scope{Org: "acme"}, config.Scope{Repo: "other/app"})
	if err := (github.Checker{Client: e.PATClient(t)}).Check(context.Background(), cfg); err != nil {
		t.Fatalf("Check: %v", err)
	}
	bad := e.PATClient(t, func(c *github.Config) { c.Token = "ghp_unknown" })
	if err := (github.Checker{Client: bad}).Check(context.Background(), cfg); !errors.Is(err, github.ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestCheckerProbesUnreachable(t *testing.T) {
	cfg := cfgWith([]string{account + "/polled"}, config.Scope{Org: account}, config.Scope{Repo: account + "/pooled"})
	for name, build := range map[string]func(env, *testing.T) *github.Client{
		"app": func(e env, t *testing.T) *github.Client { return e.AppClient(t) },
		"pat": func(e env, t *testing.T) *github.Client { return e.PATClient(t) },
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			err := (github.Checker{Client: build(e, t)}).Check(context.Background(), cfg)
			if !errors.Is(err, github.ErrNotFound) {
				t.Fatalf("err = %v, want ErrNotFound", err)
			}
			for _, want := range []string{"pools[0].scope", "pools[1].scope", "github.poll.repos[0]"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q lacks %q", err, want)
				}
			}
			for pattern, want := range map[string]int{fakegithub.PatternOrgRunners: 1, fakegithub.PatternRepoRunners: 1, fakegithub.PatternRuns: 1} {
				if n := e.Fake.RequestCount(pattern); n != want {
					t.Errorf("%s probes = %d, want %d", pattern, n, want)
				}
			}
		})
	}
}

func TestCheckerProbeForbidden(t *testing.T) {
	e := newEnv(t)
	e.Fake.AddRepo(account + "/app")
	e.Fake.FailNext(fakegithub.PatternRepoRunners, fakegithub.Fault{Status: 403, Message: "Resource not accessible by integration"})
	cfg := cfgWith(nil, config.Scope{Repo: account + "/app"})
	err := (github.Checker{Client: e.AppClient(t)}).Check(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "pools[0].scope") || !strings.Contains(err.Error(), "not reachable") {
		t.Fatalf("err = %v, want pools[0].scope not reachable", err)
	}
	if n := e.Fake.RequestCount(fakegithub.PatternRepoRunners); n != 1 {
		t.Fatalf("probes = %d, want 1", n)
	}
}

func TestCheckerAuthFailure(t *testing.T) {
	e := newEnv(t)
	c := e.AppClient(t, func(c *github.Config) { c.App.InstallationID = 999 })
	if err := (github.Checker{Client: c}).Check(context.Background(), cfgWith(nil, config.Scope{Org: account})); !errors.Is(err, github.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
