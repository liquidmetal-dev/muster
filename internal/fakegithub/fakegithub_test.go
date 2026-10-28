package fakegithub_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/go-github/v93/github"

	"github.com/liquidmetal-dev/muster/internal/clock"
	"github.com/liquidmetal-dev/muster/internal/fakegithub"
)

var startTime = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

const (
	appID          = int64(123)
	installationID = int64(456)
	patToken       = "ghp_pat"
)

type env struct {
	fake   *fakegithub.Server
	url    string
	key    *rsa.PrivateKey
	clock  *clock.Manual
	token  string
	client *github.Client // authenticated with an installation token
}

// start runs a fake with the standard test configuration plus opts, mints
// an installation token through the API and returns everything a test needs.
func start(t *testing.T, opts ...func(*fakegithub.Config)) env {
	t.Helper()
	key := fakegithub.AppKey(t)
	manual := clock.NewManual(startTime)
	cfg := fakegithub.Config{
		AppID:               appID,
		AppPublicKey:        &key.PublicKey,
		InstallationID:      installationID,
		InstallationAccount: "liquidmetal-dev",
		TokenTTL:            time.Hour,
		PATs:                map[string]string{patToken: "octocat"},
		Clock:               manual,
	}
	for _, o := range opts {
		o(&cfg)
	}
	fake := fakegithub.New(cfg)
	url := fake.Start(t)
	now := cfg.Clock.Now()
	jwt := signJWT(t, key, "123", now, now.Add(9*time.Minute))
	tok, _, err := jwtClient(t, url, jwt).Apps.CreateInstallationToken(context.Background(), installationID, nil)
	if err != nil {
		t.Fatalf("CreateInstallationToken: %v", err)
	}
	return env{fake: fake, url: url, key: key, clock: manual, token: tok.GetToken(), client: newClient(t, url, tok.GetToken())}
}

// signJWT builds an RS256 JWT with the standard library.
func signJWT(t *testing.T, key *rsa.PrivateKey, iss string, iat, exp time.Time) string {
	t.Helper()
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{"iss": iss, "iat": iat.Unix(), "exp": exp.Unix()})
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	signingInput := header + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signingInput + "." + enc.EncodeToString(sig)
}

func newClient(t *testing.T, base, token string) *github.Client {
	t.Helper()
	b := base + "/"
	c, err := github.NewClient(github.WithURLs(&b, &b), github.WithAuthToken(token))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func jwtClient(t *testing.T, base, jwt string) *github.Client {
	t.Helper()
	return newClient(t, base, jwt)
}

// wantStatus asserts that err is a go-github error with the given HTTP status.
func wantStatus(t *testing.T, err error, status int) {
	t.Helper()
	if err == nil {
		t.Fatalf("want HTTP %d, got no error", status)
	}
	var er *github.ErrorResponse
	if errors.As(err, &er) {
		if er.Response.StatusCode != status {
			t.Fatalf("want HTTP %d, got %d: %v", status, er.Response.StatusCode, err)
		}
		return
	}
	var rl *github.RateLimitError
	if errors.As(err, &rl) {
		if rl.Response.StatusCode != status {
			t.Fatalf("want HTTP %d, got %d: %v", status, rl.Response.StatusCode, err)
		}
		return
	}
	var ab *github.AbuseRateLimitError
	if errors.As(err, &ab) {
		if ab.Response.StatusCode != status {
			t.Fatalf("want HTTP %d, got %d: %v", status, ab.Response.StatusCode, err)
		}
		return
	}
	t.Fatalf("want HTTP %d, got %T: %v", status, err, err)
}

// rawGet sends a GET with an explicit Authorization header and returns the response.
func rawGet(t *testing.T, url, authorization string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func ctx() context.Context { return context.Background() }
