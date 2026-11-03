package github

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/liquidmetal-dev/muster/internal/clock"
)

// sameOrigin is the rule go-github applies before it sends credentials:
// same scheme, hostname and port, with the scheme's default port implied.
func sameOrigin(u, base *url.URL) bool {
	return strings.EqualFold(u.Scheme, base.Scheme) &&
		strings.EqualFold(u.Hostname(), base.Hostname()) &&
		port(u) == port(base)
}

func port(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	switch strings.ToLower(u.Scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	}
	return ""
}

// jwtTransport authenticates as the App with a JWT minted for each request.
type jwtTransport struct {
	auth  AppAuth
	clock clock.Clock
	base  *url.URL
	next  http.RoundTripper
}

func (t *jwtTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !sameOrigin(r.URL, t.base) {
		return t.next.RoundTrip(r)
	}
	jwt, err := signAppJWT(t.auth.PrivateKey, t.auth.AppID, t.clock.Now())
	if err != nil {
		return nil, err
	}
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+jwt)
	return t.next.RoundTrip(r)
}

// authTransport sends the installation token or PAT. After a 401 on an
// installation token it drops the token and replays the request once.
type authTransport struct {
	src  tokenSource
	app  bool
	base *url.URL
	next http.RoundTripper
}

func (t *authTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !sameOrigin(r.URL, t.base) {
		return t.next.RoundTrip(r)
	}
	resp, tok, err := t.send(r)
	if err != nil || resp.StatusCode != http.StatusUnauthorized || !t.app {
		return resp, err
	}
	if r.Body != nil && r.Body != http.NoBody && r.GetBody == nil {
		return resp, nil
	}
	drain(resp)
	t.src.Invalidate(r.Context(), tok)
	resp, _, err = t.send(r)
	return resp, err
}

func (t *authTransport) send(r *http.Request) (*http.Response, string, error) {
	tok, err := t.src.Token(r.Context())
	if err != nil {
		return nil, "", err
	}
	out := r.Clone(r.Context())
	if r.GetBody != nil {
		if out.Body, err = r.GetBody(); err != nil {
			return nil, "", err
		}
	}
	scheme := "token "
	if t.app {
		scheme = "Bearer "
	}
	out.Header.Set("Authorization", scheme+tok)
	resp, err := t.next.RoundTrip(out)
	return resp, tok, err
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

// retryTransport applies the rate limit and retry policy to every attempt.
// Rate limited responses (429, or 403 with Retry-After or no remaining
// quota) are retried for every method, because GitHub did not process the
// request. Server errors and network failures are retried only for
// idempotent methods, so a POST that may have succeeded is never sent again.
type retryTransport struct {
	cfg  Config
	next http.RoundTripper
}

func (t *retryTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	ctx := r.Context()
	endpoint := endpointFrom(ctx, r)
	body, err := readBody(r)
	if err != nil {
		return nil, err
	}
	idempotent := r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodDelete
	for attempt := 1; ; attempt++ {
		resp, err := t.attempt(ctx, r, body)
		last := attempt >= t.cfg.MaxAttempts
		var wait time.Duration
		switch {
		case err != nil:
			kind := "network"
			if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				kind = "timeout"
			}
			t.cfg.Metrics.ObserveCall(endpoint, 0)
			t.cfg.Metrics.ObserveError(endpoint, kind)
			if ctx.Err() != nil || !idempotent || last {
				return nil, err
			}
			wait = t.backoff(attempt)
		case rateLimited(resp):
			t.cfg.Metrics.ObserveCall(endpoint, resp.StatusCode)
			t.cfg.Metrics.ObserveError(endpoint, "rate_limited")
			var known bool
			wait, known = t.rateLimitWait(resp)
			if !known {
				wait = t.backoff(attempt)
			}
			drain(resp)
			if wait > t.cfg.MaxWait || last {
				rl := &RateLimitedError{Endpoint: endpoint}
				if known {
					rl.Reset = t.cfg.Clock.Now().Add(wait)
				}
				return nil, rl
			}
		case resp.StatusCode >= 500:
			t.cfg.Metrics.ObserveCall(endpoint, resp.StatusCode)
			t.cfg.Metrics.ObserveError(endpoint, "server")
			if !idempotent || last {
				return resp, nil
			}
			drain(resp)
			wait = t.backoff(attempt)
		default:
			t.cfg.Metrics.ObserveCall(endpoint, resp.StatusCode)
			if resp.StatusCode >= 400 {
				t.cfg.Metrics.ObserveError(endpoint, "client")
			}
			return resp, nil
		}
		t.cfg.Logger.Debug("github: retrying call", "endpoint", endpoint, "attempt", attempt, "after", wait)
		if err := t.cfg.sleep(ctx, wait); err != nil {
			return nil, err
		}
	}
}

// attempt sends one request under CallTimeout and reads the whole response
// body before the timeout is released, so the body stays readable.
func (t *retryTransport) attempt(ctx context.Context, r *http.Request, body []byte) (*http.Response, error) {
	actx, cancel := context.WithTimeout(ctx, t.cfg.CallTimeout)
	defer cancel()
	req := r.Clone(actx)
	if body != nil {
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
		req.ContentLength = int64(len(body))
	}
	resp, err := t.next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("github: read response: %w", err)
	}
	resp.Body = io.NopCloser(bytes.NewReader(data))
	resp.ContentLength = int64(len(data))
	return resp, nil
}

func readBody(r *http.Request) ([]byte, error) {
	if r.Body == nil || r.Body == http.NoBody {
		return nil, nil
	}
	body, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("github: read request body: %w", err)
	}
	return body, nil
}

func rateLimited(resp *http.Response) bool {
	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		return true
	case http.StatusForbidden:
		return resp.Header.Get("Retry-After") != "" || resp.Header.Get("X-RateLimit-Remaining") == "0"
	}
	return false
}

// rateLimitWait reads the wait GitHub asked for: Retry-After in seconds or
// as an HTTP date, else the primary limit's X-RateLimit-Reset.
func (t *retryTransport) rateLimitWait(resp *http.Response) (time.Duration, bool) {
	now := t.cfg.Clock.Now()
	if v := resp.Header.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second, true
		}
		if when, err := http.ParseTime(v); err == nil {
			return max(when.Sub(now), 0), true
		}
	}
	if resp.Header.Get("X-RateLimit-Remaining") == "0" {
		if unix, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			return max(time.Unix(unix, 0).Sub(now), 0), true
		}
	}
	return 0, false
}

// backoff is full jitter over an exponential ceiling capped at MaxWait.
func (t *retryTransport) backoff(attempt int) time.Duration {
	ceiling := min(t.cfg.MaxWait, time.Second<<min(attempt-1, 30))
	return rand.N(ceiling) //nolint:gosec // jitter, not security
}
