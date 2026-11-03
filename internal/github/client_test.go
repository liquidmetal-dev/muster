package github_test

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liquidmetal-dev/muster/internal/fakegithub"
	"github.com/liquidmetal-dev/muster/internal/github"
)

// recorder wraps the default transport, keeps request paths, and can rewrite
// the headers of the next responses.
type recorder struct {
	mu      sync.Mutex
	paths   []string
	rewrite []func(http.Header)
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.paths = append(r.paths, req.URL.Path)
	r.mu.Unlock()
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.rewrite) > 0 {
		r.rewrite[0](resp.Header)
		r.rewrite = r.rewrite[1:]
	}
	return resp, nil
}

func (r *recorder) Paths() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.paths...)
}

func (r *recorder) install(c *github.Config) { c.HTTPClient = &http.Client{Transport: r} }

func TestNewNoNetwork(t *testing.T) {
	e := newEnv(t)
	e.AppClient(t)
	e.PATClient(t)
	if n := len(e.Fake.Requests()); n != 0 {
		t.Fatalf("requests = %d, want 0", n)
	}
}

func TestNewRejectsBothOrNeither(t *testing.T) {
	key := fakegithub.AppKey(t)
	for name, cfg := range map[string]github.Config{
		"neither": {},
		"both":    {Token: "x", App: &github.AppAuth{AppID: 1, InstallationID: 2, PrivateKey: key}},
		"no key":  {App: &github.AppAuth{AppID: 1, InstallationID: 2}},
		"bad url": {Token: "x", BaseURL: "::"},
	} {
		if _, err := github.New(cfg); !errors.Is(err, github.ErrInvalidRequest) {
			t.Errorf("%s: err = %v, want ErrInvalidRequest", name, err)
		}
	}
}

func TestNewBaseURLForms(t *testing.T) {
	e := newEnv(t)
	for _, suffix := range []string{"", "/", "/api/v3", "/api/v3/"} {
		t.Run("url"+suffix, func(t *testing.T) {
			rec := &recorder{}
			c := e.AppClient(t, rec.install, func(c *github.Config) { c.BaseURL = e.URL + suffix })
			if err := c.Ping(context.Background()); err != nil {
				t.Fatalf("Ping: %v", err)
			}
			for _, p := range rec.Paths() {
				if !strings.HasPrefix(p, "/api/v3/app/installations/") {
					t.Fatalf("path %q: want the GHES /api/v3/ prefix", p)
				}
			}
		})
	}
}

func TestNoPreemptiveRateLimitCheck(t *testing.T) {
	e := newEnv(t)
	// go-github checks the reset against the wall clock, not Config.Clock.
	reset := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	rec := &recorder{rewrite: []func(http.Header){func(h http.Header) {
		h.Set("X-RateLimit-Remaining", "0")
		h.Set("X-RateLimit-Reset", reset)
	}}}
	c := e.PATClient(t, rec.install)
	for i := range 2 {
		if err := c.Ping(context.Background()); err != nil {
			t.Fatalf("Ping %d: %v", i, err)
		}
	}
	if n := e.Fake.RequestCount(fakegithub.PatternUser); n != 2 {
		t.Fatalf("requests = %d, want 2: the second call must reach GitHub", n)
	}
}

func TestPingApp(t *testing.T) {
	e := newEnv(t)
	if err := e.AppClient(t).Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestPingPAT(t *testing.T) {
	e := newEnv(t)
	if err := e.PATClient(t).Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestPingAppWrongInstallation(t *testing.T) {
	e := newEnv(t)
	c := e.AppClient(t, func(c *github.Config) { c.App.InstallationID = 999 })
	if err := c.Ping(context.Background()); !errors.Is(err, github.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
