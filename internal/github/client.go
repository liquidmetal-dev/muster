package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	gogithub "github.com/google/go-github/v93/github"
)

// Client calls the GitHub REST API. It is safe for concurrent use.
type Client struct {
	cfg  Config
	base *url.URL
	// gh authenticates with the installation token or PAT; jwt with the App
	// JWT, for the endpoints that only accept it.
	gh  *gogithub.Client
	jwt *gogithub.Client
	src tokenSource
}

// New builds a Client. It makes no network calls; the first call that needs
// an installation token fetches it.
func New(cfg Config) (*Client, error) {
	cfg = cfg.withDefaults()
	base, err := parseBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	next := http.DefaultTransport
	if cfg.HTTPClient != nil && cfg.HTTPClient.Transport != nil {
		next = cfg.HTTPClient.Transport
	}
	c := &Client{cfg: cfg, base: base}
	if cfg.App != nil {
		c.jwt, err = newGoGitHub(base, &jwtTransport{auth: *cfg.App, clock: cfg.Clock, next: next})
		if err != nil {
			return nil, err
		}
		c.src = &appSource{clock: cfg.Clock, exchange: c.exchangeToken}
	} else {
		c.src = patSource{token: cfg.Token}
	}
	c.gh, err = newGoGitHub(base, next)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func parseBaseURL(raw string) (*url.URL, error) {
	if raw == "" {
		raw = "https://api.github.com/"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("%w: base URL %q", ErrInvalidRequest, raw)
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	return u, nil
}

func newGoGitHub(base *url.URL, rt http.RoundTripper) (*gogithub.Client, error) {
	b := base.String()
	gh, err := gogithub.NewClient(
		gogithub.WithHTTPClient(&http.Client{Transport: rt}),
		gogithub.WithURLs(&b, &b),
	)
	if err != nil {
		return nil, fmt.Errorf("github: build client: %w", err)
	}
	return gh, nil
}

func (c *Client) exchangeToken(ctx context.Context) (string, time.Time, error) {
	tok, _, err := c.jwt.Apps.CreateInstallationToken(ctx, c.cfg.App.InstallationID, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	return tok.GetToken(), tok.GetExpiresAt().Time, nil
}
