package github_test

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liquidmetal-dev/muster/internal/fakegithub"
	"github.com/liquidmetal-dev/muster/internal/github"
)

func mustToken(t *testing.T, c *github.Client) string {
	t.Helper()
	tok, err := github.TokenFor(context.Background(), c)
	if err != nil {
		t.Fatalf("TokenFor: %v", err)
	}
	return tok
}

func TestAppTokenCached(t *testing.T) {
	e := newEnv(t)
	c := e.AppClient(t)
	a, b := mustToken(t, c), mustToken(t, c)
	if a != b || !strings.HasPrefix(a, "ghs_") {
		t.Fatalf("tokens %q, %q: want the same installation token", a, b)
	}
	if n := e.Fake.RequestCount(fakegithub.PatternAccessToken); n != 1 {
		t.Fatalf("exchanges = %d, want 1", n)
	}
}

func TestAppTokenRefreshBeforeExpiry(t *testing.T) {
	e := newEnv(t)
	c := e.AppClient(t)
	first := mustToken(t, c)
	e.Clock.Advance(54 * time.Minute)
	if got := mustToken(t, c); got != first {
		t.Fatal("token refreshed with 6m left, want reuse")
	}
	e.Clock.Advance(2 * time.Minute)
	if got := mustToken(t, c); got == first {
		t.Fatal("token not refreshed with 4m left")
	}
	if n := e.Fake.RequestCount(fakegithub.PatternAccessToken); n != 2 {
		t.Fatalf("exchanges = %d, want 2", n)
	}
}

func TestAppTokenRefreshFailureKeepsValidToken(t *testing.T) {
	e := newEnv(t)
	c := e.AppClient(t)
	first := mustToken(t, c)
	e.Clock.Advance(56 * time.Minute)
	e.Fake.FailNext(fakegithub.PatternAccessToken, fakegithub.Fault{Status: 500, Message: "boom"})
	if got := mustToken(t, c); got != first {
		t.Fatalf("got %q, want the still-valid token", got)
	}
	e.Clock.Advance(5 * time.Minute)
	e.Fake.FailNext(fakegithub.PatternAccessToken, fakegithub.Fault{Status: 500, Message: "boom"})
	if _, err := github.TokenFor(context.Background(), c); err == nil {
		t.Fatal("expired token and failed refresh: want error")
	}
}

func TestAppTokenConcurrentSingleRefresh(t *testing.T) {
	e := newEnv(t)
	c := e.AppClient(t)
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if _, err := github.TokenFor(context.Background(), c); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if n := e.Fake.RequestCount(fakegithub.PatternAccessToken); n != 1 {
		t.Fatalf("exchanges = %d, want 1", n)
	}
}

func TestAppJWTClaims(t *testing.T) {
	key := fakegithub.AppKey(t)
	t0 := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	tok, err := github.SignAppJWT(key, 123, t0)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("segments = %d", len(parts))
	}
	enc := base64.RawURLEncoding
	var header struct{ Alg, Typ string }
	var claims struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
	}
	for i, v := range []any{&header, &claims} {
		raw, err := enc.DecodeString(parts[i])
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, v); err != nil {
			t.Fatal(err)
		}
	}
	if header.Alg != "RS256" || claims.Iss != "123" ||
		claims.Iat != t0.Add(-60*time.Second).Unix() || claims.Exp != t0.Add(9*time.Minute).Unix() {
		t.Fatalf("header %+v claims %+v", header, claims)
	}
	sig, err := enc.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatalf("signature: %v", err)
	}
}

func TestPATSource(t *testing.T) {
	e := newEnv(t)
	c := e.PATClient(t)
	if got := mustToken(t, c); got != patToken {
		t.Fatalf("token = %q", got)
	}
	if n := len(e.Fake.Requests()); n != 0 {
		t.Fatalf("requests = %d, want 0", n)
	}
}

func TestAppTokenWaitHonoursContext(t *testing.T) {
	e := newEnv(t)
	c := e.AppClient(t)
	e.Fake.Stall(fakegithub.PatternAccessToken, 2*time.Second)
	go func() { _, _ = github.TokenFor(context.Background(), c) }()
	e.Fake.WaitFor(t, 5*time.Second, func() bool { return e.Fake.RequestCount(fakegithub.PatternAccessToken) == 1 })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := github.TokenFor(ctx, c); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if waited := time.Since(start); waited > time.Second {
		t.Fatalf("waited %v behind another caller's refresh, want about 100ms", waited)
	}
}
