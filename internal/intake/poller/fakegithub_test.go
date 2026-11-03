package poller_test

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/liquidmetal-dev/muster/internal/clock"
	"github.com/liquidmetal-dev/muster/internal/config"
	"github.com/liquidmetal-dev/muster/internal/demand"
	"github.com/liquidmetal-dev/muster/internal/fakegithub"
	"github.com/liquidmetal-dev/muster/internal/github"
	"github.com/liquidmetal-dev/muster/internal/intake/poller"
	"github.com/liquidmetal-dev/muster/internal/intake/webhook"
)

const (
	pollRepo    = "acme/app"
	pat         = "ghp_polltest"
	hookSecret  = "s3cret"
	pollEvery   = 30 * time.Second
	waitTimeout = 5 * time.Second
)

var linuxPool = config.Pool{
	Name:          "linux",
	Scope:         config.Scope{Org: "acme"},
	Labels:        []string{"self-hosted", "linux"},
	Size:          1,
	MaxConcurrent: 2,
}

type pollEnv struct {
	clock    *clock.Manual
	fake     *fakegithub.Server
	registry *demand.Registry
	client   *github.Client
	counters *poller.Counters
}

// newPollEnv shares one manual clock between the fake, the client, the
// registry and the poller. With withWebhook the fake delivers to the real
// webhook intake, which feeds the same registry.
func newPollEnv(t *testing.T, withWebhook bool) *pollEnv {
	t.Helper()
	e := &pollEnv{clock: clock.NewManual(t0), counters: &poller.Counters{}}
	e.registry = demand.New(demand.Config{Pools: []config.Pool{linuxPool}, Clock: e.clock, Logger: slog.New(slog.DiscardHandler)})
	cfg := fakegithub.Config{PATs: map[string]string{pat: "octocat"}, Clock: e.clock}
	if withWebhook {
		h, err := webhook.New(webhook.Config{
			Secret: config.NewSecret([]byte(hookSecret)),
			Demand: e.registry,
			Pools:  []config.Pool{linuxPool},
			Clock:  e.clock,
			Logger: slog.New(slog.DiscardHandler),
		})
		if err != nil {
			t.Fatalf("webhook.New: %v", err)
		}
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		cfg.WebhookURL, cfg.WebhookSecret = srv.URL, hookSecret
	}
	e.fake = fakegithub.New(cfg)
	url := e.fake.Start(t)
	e.fake.AddOrg("acme")
	e.fake.AddRepo(pollRepo)
	c, err := github.New(github.Config{BaseURL: url, Token: pat, Clock: e.clock, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatalf("github.New: %v", err)
	}
	e.client = c
	return e
}

// start runs the poller until the test ends.
func (e *pollEnv) start(t *testing.T, poll config.Poll) {
	t.Helper()
	p, err := poller.New(poller.Config{
		Poll:    poll,
		Lister:  e.client,
		Demand:  e.registry,
		Clock:   e.clock,
		Logger:  slog.New(slog.DiscardHandler),
		Metrics: e.counters,
	})
	if err != nil {
		t.Fatalf("poller.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

// waitPasses waits until n listings of pollRepo have been merged.
// ObservePoll runs after Polled, so the merge has happened.
func (e *pollEnv) waitPasses(t *testing.T, n int) {
	t.Helper()
	e.fake.WaitFor(t, waitTimeout, func() bool { return e.counters.Count(pollRepo, poller.OutcomeOK) >= n })
}

func (e *pollEnv) queue(t *testing.T) fakegithub.Job {
	t.Helper()
	j, err := e.fake.QueueJob(pollRepo, linuxPool.Labels)
	if err != nil {
		t.Fatalf("QueueJob: %v", err)
	}
	return j
}

func pollAll() config.Poll {
	return config.Poll{Enabled: true, Interval: config.Duration(pollEvery), Repos: []string{pollRepo}}
}

func TestPollerAndWebhookCountJobOnce(t *testing.T) {
	e := newPollEnv(t, true)
	e.queue(t)
	if got := e.registry.Total(); got != 1 {
		t.Fatalf("Total after webhook = %d, want 1", got)
	}
	e.clock.Advance(time.Second)
	e.start(t, pollAll())
	e.waitPasses(t, 1)
	if got, want := e.registry.Total(), 1; got != want {
		t.Fatalf("Total after poll = %d, want %d", got, want)
	}
	if got := e.registry.Desired(linuxPool.Name); got != 1 {
		t.Fatalf("Desired = %d, want 1", got)
	}
}

func TestPollerClosesJobGoneFromListing(t *testing.T) {
	e := newPollEnv(t, false)
	j := e.queue(t)
	e.clock.Advance(time.Second)
	e.start(t, pollAll())
	e.waitPasses(t, 1)
	if got := e.registry.Total(); got != 1 {
		t.Fatalf("Total after first poll = %d, want 1", got)
	}
	if err := e.fake.CancelJob(j.ID); err != nil {
		t.Fatalf("CancelJob: %v", err)
	}
	e.clock.Advance(pollEvery)
	e.waitPasses(t, 2)
	if got := e.registry.Total(); got != 0 {
		t.Fatalf("Total after the job left the listing = %d, want 0", got)
	}
}

func TestDisabledPollerMakesNoCalls(t *testing.T) {
	e := newPollEnv(t, false)
	e.queue(t)
	poll := pollAll()
	poll.Enabled = false
	p, err := poller.New(poller.Config{Poll: poll, Lister: e.client, Demand: e.registry, Clock: e.clock})
	if err != nil {
		t.Fatalf("poller.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(ctx)
	}()
	select {
	case <-done:
	case <-time.After(waitTimeout):
		t.Fatal("Run of a disabled poller did not return")
	}
	if n := e.fake.RequestCount(fakegithub.PatternRuns) + e.fake.RequestCount(fakegithub.PatternRunJobs); n != 0 {
		t.Fatalf("GitHub list requests = %d, want 0", n)
	}
	if got := e.registry.Total(); got != 0 {
		t.Fatalf("Total = %d, want 0", got)
	}
}

func TestPollerQueuedJobInRunningRun(t *testing.T) {
	e := newPollEnv(t, true)
	a := e.queue(t)
	if _, err := e.fake.QueueJobInRun(a.RunID, linuxPool.Labels); err != nil {
		t.Fatalf("QueueJobInRun: %v", err)
	}
	if err := e.fake.StartJob(a.ID); err != nil {
		t.Fatalf("StartJob: %v", err)
	}
	if got := e.registry.Total(); got != 1 {
		t.Fatalf("Total after webhooks = %d, want 1", got)
	}
	e.clock.Advance(time.Second)
	e.start(t, pollAll())
	e.waitPasses(t, 1)
	if got := e.registry.Total(); got != 1 {
		t.Fatalf("Total after poll = %d, want 1 (the queued job of the running run)", got)
	}
}
