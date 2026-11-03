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
	switch {
	case cfg.App != nil && cfg.Token != "":
		return nil, fmt.Errorf("%w: both an App and a token are configured", ErrInvalidRequest)
	case cfg.App == nil && cfg.Token == "":
		return nil, fmt.Errorf("%w: no credential is configured", ErrInvalidRequest)
	case cfg.App != nil && cfg.App.PrivateKey == nil:
		return nil, fmt.Errorf("%w: the App has no private key", ErrInvalidRequest)
	}
	cfg = cfg.withDefaults()
	base, err := parseBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	next := http.DefaultTransport
	if cfg.HTTPClient != nil && cfg.HTTPClient.Transport != nil {
		next = cfg.HTTPClient.Transport
	}
	retry := &retryTransport{cfg: cfg, next: next}
	c := &Client{cfg: cfg, base: base}
	if cfg.App != nil {
		c.jwt, err = newGoGitHub(base, &jwtTransport{auth: *cfg.App, clock: cfg.Clock, base: base, next: retry})
		if err != nil {
			return nil, err
		}
		c.src = newAppSource(cfg.Clock, c.exchangeToken)
	} else {
		c.src = patSource{token: cfg.Token}
	}
	c.gh, err = newGoGitHub(base, &authTransport{src: c.src, app: cfg.App != nil, base: base, next: retry})
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

// newGoGitHub disables go-github's own rate limit precheck: it fails a call
// before it reaches retryTransport, which is the only rate limit policy.
func newGoGitHub(base *url.URL, rt http.RoundTripper) (*gogithub.Client, error) {
	b := base.String()
	urls := gogithub.WithURLs(&b, &b)
	if !strings.EqualFold(base.Hostname(), "api.github.com") {
		urls = gogithub.WithEnterpriseURLs(b, b)
	}
	gh, err := gogithub.NewClient(
		gogithub.WithHTTPClient(&http.Client{Transport: rt}),
		urls,
		gogithub.WithDisableRateLimitCheck(),
	)
	if err != nil {
		return nil, fmt.Errorf("github: build client: %w", err)
	}
	return gh, nil
}

func (c *Client) exchangeToken(ctx context.Context) (string, time.Time, error) {
	ctx = withEndpoint(ctx, endpointAccessToken)
	tok, _, err := c.jwt.Apps.CreateInstallationToken(ctx, c.cfg.App.InstallationID, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	return tok.GetToken(), tok.GetExpiresAt().Time, nil
}

// Ping checks that the credential works: the App installation is readable
// with the App JWT, or the PAT identifies a user.
func (c *Client) Ping(ctx context.Context) error {
	if c.cfg.App != nil {
		_, err := c.installation(ctx)
		return err
	}
	_, _, err := c.gh.Users.Get(withEndpoint(ctx, endpointUser), "")
	return mapError(err)
}

func (c *Client) installation(ctx context.Context) (*gogithub.Installation, error) {
	inst, _, err := c.jwt.Apps.GetInstallation(withEndpoint(ctx, endpointInstallation), c.cfg.App.InstallationID)
	return inst, mapError(err)
}
