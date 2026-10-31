package github_test

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/liquidmetal-dev/muster/internal/fakegithub"
	"github.com/liquidmetal-dev/muster/internal/github"
)

const installationEndpoint = "GET /app/installations/{id}"

type sleepRecorder struct {
	mu     sync.Mutex
	sleeps []time.Duration
}

func (s *sleepRecorder) sleep(_ context.Context, d time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sleeps = append(s.sleeps, d)
	return nil
}

func (s *sleepRecorder) got() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.sleeps...)
}

func (s *sleepRecorder) install(c *github.Config) { github.SetSleep(c, s.sleep) }

func header(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	return h
}

func TestRetry429RetryAfter(t *testing.T) {
	e := newEnv(t)
	var rec sleepRecorder
	c := e.AppClient(t, rec.install)
	e.Fake.FailNext(fakegithub.PatternInstallation, fakegithub.Fault{Status: 429, Header: header("Retry-After", "2"), Message: "slow down"})
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if got := rec.got(); len(got) != 1 || got[0] != 2*time.Second {
		t.Fatalf("sleeps = %v, want [2s]", got)
	}
	if n := e.Fake.RequestCount(fakegithub.PatternInstallation); n != 2 {
		t.Fatalf("requests = %d, want 2", n)
	}
}

func TestRetryAfterHTTPDate(t *testing.T) {
	e := newEnv(t)
	var rec sleepRecorder
	c := e.AppClient(t, rec.install)
	when := e.Clock.Now().Add(3 * time.Second).UTC().Format(http.TimeFormat)
	e.Fake.FailNext(fakegithub.PatternInstallation, fakegithub.Fault{Status: 429, Header: header("Retry-After", when), Message: "slow down"})
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if got := rec.got(); len(got) != 1 || got[0] < 2*time.Second || got[0] > 3*time.Second {
		t.Fatalf("sleeps = %v, want one of about 3s", got)
	}
}

func TestRetry403SecondaryLimit(t *testing.T) {
	e := newEnv(t)
	var rec sleepRecorder
	c := e.AppClient(t, rec.install)
	reset := strconv.FormatInt(e.Clock.Now().Add(5*time.Second).Unix(), 10)
	e.Fake.FailNext(fakegithub.PatternInstallation, fakegithub.Fault{Status: 403, Header: header("Retry-After", "1"), Message: "secondary rate limit"})
	e.Fake.FailNext(fakegithub.PatternInstallation, fakegithub.Fault{Status: 403, Header: header("X-RateLimit-Remaining", "0", "X-RateLimit-Reset", reset), Message: "API rate limit exceeded"})
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if got := rec.got(); len(got) != 2 || got[0] != time.Second || got[1] != 5*time.Second {
		t.Fatalf("sleeps = %v, want [1s 5s]", got)
	}
}

func TestPlain403NotRetried(t *testing.T) {
	e := newEnv(t)
	c := e.AppClient(t)
	e.Fake.FailNext(fakegithub.PatternInstallation, fakegithub.Fault{Status: 403, Message: "Resource not accessible by integration"})
	if err := c.Ping(context.Background()); err == nil {
		t.Fatal("Ping: want error")
	}
	if n := e.Fake.RequestCount(fakegithub.PatternInstallation); n != 1 {
		t.Fatalf("requests = %d, want 1", n)
	}
}

func TestRetryWaitAboveMaxWait(t *testing.T) {
	e := newEnv(t)
	var rec sleepRecorder
	c := e.AppClient(t, rec.install)
	e.Fake.FailNext(fakegithub.PatternInstallation, fakegithub.Fault{Status: 429, Header: header("Retry-After", "3600"), Message: "slow down"})
	err := c.Ping(context.Background())
	var rl *github.RateLimitedError
	if !errors.As(err, &rl) || !errors.Is(err, github.ErrRateLimited) {
		t.Fatalf("err = %v, want *RateLimitedError", err)
	}
	if want := e.Clock.Now().Add(time.Hour); !rl.Reset.Equal(want) || rl.Endpoint != installationEndpoint {
		t.Fatalf("RateLimitedError = %+v, want reset %v", rl, want)
	}
	if len(rec.got()) != 0 || e.Fake.RequestCount(fakegithub.PatternInstallation) != 1 {
		t.Fatalf("sleeps %v requests %d, want none and 1", rec.got(), e.Fake.RequestCount(fakegithub.PatternInstallation))
	}
}

func TestRetryMaxAttempts(t *testing.T) {
	e := newEnv(t)
	var rec sleepRecorder
	c := e.AppClient(t, rec.install)
	for range 5 {
		e.Fake.FailNext(fakegithub.PatternInstallation, fakegithub.Fault{Status: 429, Header: header("Retry-After", "1"), Message: "slow down"})
	}
	if err := c.Ping(context.Background()); !errors.Is(err, github.ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
	if n := e.Fake.RequestCount(fakegithub.PatternInstallation); n != 5 || len(rec.got()) != 4 {
		t.Fatalf("requests %d sleeps %d, want 5 and 4", n, len(rec.got()))
	}
}

func TestRetry5xxIdempotent(t *testing.T) {
	e := newEnv(t)
	c := e.AppClient(t)
	e.Fake.FailNext(fakegithub.PatternInstallation, fakegithub.Fault{Status: 502, Message: "bad gateway"})
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if n := e.Fake.RequestCount(fakegithub.PatternInstallation); n != 2 {
		t.Fatalf("requests = %d, want 2", n)
	}
}

func TestRetryHonoursContextCancel(t *testing.T) {
	e := newEnv(t)
	block := func(ctx context.Context, _ time.Duration) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
			return errors.New("sleep was not cancelled")
		}
	}
	c := e.AppClient(t, func(cfg *github.Config) { github.SetSleep(cfg, block) })
	e.Fake.FailNext(fakegithub.PatternInstallation, fakegithub.Fault{Status: 429, Header: header("Retry-After", "30"), Message: "slow down"})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	err := c.Ping(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("cancel did not stop the wait")
	}
}

func TestJWT401NotReplayed(t *testing.T) {
	e := newEnv(t)
	c := e.AppClient(t)
	e.Fake.FailNext(fakegithub.PatternInstallation, fakegithub.Fault{Status: 401, Message: "Bad credentials"})
	if err := c.Ping(context.Background()); !errors.Is(err, github.ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	if n := e.Fake.RequestCount(fakegithub.PatternInstallation); n != 1 {
		t.Fatalf("requests = %d, want 1", n)
	}
	if n := e.Fake.RequestCount(fakegithub.PatternAccessToken); n != 0 {
		t.Fatalf("exchanges = %d, want 0", n)
	}
}

func TestCallTimeout(t *testing.T) {
	e := newEnv(t)
	var m github.Counters
	c := e.AppClient(t, func(cfg *github.Config) {
		cfg.CallTimeout = 50 * time.Millisecond
		cfg.Metrics = &m
	})
	e.Fake.Stall(fakegithub.PatternInstallation, 200*time.Millisecond)
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if got := m.Errors(installationEndpoint, "timeout"); got != 1 {
		t.Fatalf("timeout errors = %d, want 1", got)
	}
	for range 5 {
		e.Fake.Stall(fakegithub.PatternInstallation, 200*time.Millisecond)
	}
	if err := c.Ping(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
}

func TestPAT401(t *testing.T) {
	e := newEnv(t)
	c := e.PATClient(t, func(cfg *github.Config) { cfg.Token = "ghp_unknown" })
	if err := c.Ping(context.Background()); !errors.Is(err, github.ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	if n := e.Fake.RequestCount(fakegithub.PatternUser); n != 1 {
		t.Fatalf("requests = %d, want 1", n)
	}
}

func TestMetricsLabels(t *testing.T) {
	e := newEnv(t)
	var m github.Counters
	c := e.AppClient(t, func(cfg *github.Config) { cfg.Metrics = &m })
	e.Fake.FailNext(fakegithub.PatternInstallation, fakegithub.Fault{Status: 429, Header: header("Retry-After", "1"), Message: "slow down"})
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if got := m.Calls(installationEndpoint); got != 2 {
		t.Fatalf("calls = %d, want 2", got)
	}
	if got := m.Errors(installationEndpoint, "rate_limited"); got != 1 {
		t.Fatalf("rate_limited = %d, want 1", got)
	}
}
