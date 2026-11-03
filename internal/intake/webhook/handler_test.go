package webhook_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-github/v93/github"

	"github.com/liquidmetal-dev/muster/internal/clock"
	"github.com/liquidmetal-dev/muster/internal/config"
	"github.com/liquidmetal-dev/muster/internal/demand"
	"github.com/liquidmetal-dev/muster/internal/dispatch"
	"github.com/liquidmetal-dev/muster/internal/intake/webhook"
)

const secret = "s3cret"

var (
	t0     = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	labels = []string{"self-hosted", "linux"}
	pools  = []config.Pool{{
		Name:          "linux",
		Scope:         config.Scope{Org: "acme"},
		Labels:        labels,
		MaxConcurrent: 10,
	}}
)

type fakeDemand struct {
	mu        sync.Mutex
	queued    []demand.Job
	started   []int64
	completed []int64
}

func (f *fakeDemand) Queued(j demand.Job) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queued = append(f.queued, j)
}

func (f *fakeDemand) Started(id int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, id)
}

func (f *fakeDemand) Completed(id int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completed = append(f.completed, id)
}

func (f *fakeDemand) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.queued) + len(f.started) + len(f.completed)
}

// newHandler fills Secret, Pools and Demand when cfg leaves them unset.
func newHandler(t *testing.T, cfg webhook.Config) *webhook.Handler {
	t.Helper()
	if !cfg.Secret.IsSet() {
		cfg.Secret = config.NewSecret([]byte(secret))
	}
	if cfg.Pools == nil {
		cfg.Pools = pools
	}
	if cfg.Demand == nil {
		cfg.Demand = &fakeDemand{}
	}
	h, err := webhook.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

func debugLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func sign(key string, body []byte) string {
	m := hmac.New(sha256.New, []byte(key))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// deliver posts body to h. An empty event, id or sig omits that header.
func deliver(t *testing.T, h http.Handler, event, id string, body []byte, sig string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	if event != "" {
		r.Header.Set("X-GitHub-Event", event)
	}
	if id != "" {
		r.Header.Set("X-GitHub-Delivery", id)
	}
	if sig != "" {
		r.Header.Set("X-Hub-Signature-256", sig)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

type job struct {
	action     string
	id         int64
	labels     []string
	repo       string
	runner     string
	conclusion string
	created    time.Time
}

// matching is a queued job that matches pools.
func matching(id int64) job {
	return job{action: "queued", id: id, labels: labels, repo: "acme/app", created: t0}
}

func (j job) body(t *testing.T) []byte {
	t.Helper()
	wj := &github.WorkflowJob{Labels: j.labels}
	if j.id != 0 {
		wj.ID = new(j.id)
	}
	if j.runner != "" {
		wj.RunnerName = new(j.runner)
	}
	if j.conclusion != "" {
		wj.Conclusion = new(j.conclusion)
	}
	if !j.created.IsZero() {
		wj.CreatedAt = &github.Timestamp{Time: j.created}
	}
	ev := github.WorkflowJobEvent{Action: new(j.action), WorkflowJob: wj}
	if j.repo != "" {
		ev.Repo = &github.Repository{FullName: new(j.repo)}
	}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// send delivers j as a correctly signed workflow_job delivery.
func send(t *testing.T, h http.Handler, id string, j job) int {
	t.Helper()
	b := j.body(t)
	return deliver(t, h, "workflow_job", id, b, sign(secret, b)).Code
}

func TestNewRequiresSecretAndDemand(t *testing.T) {
	if _, err := webhook.New(webhook.Config{Demand: &fakeDemand{}}); err == nil {
		t.Fatal("New without Secret: got nil error")
	}
	if _, err := webhook.New(webhook.Config{Secret: config.NewSecret([]byte("s"))}); err == nil {
		t.Fatal("New without Demand: got nil error")
	}
}

func TestBadSignatureIs401(t *testing.T) {
	body := matching(1).body(t)
	tests := []struct {
		name string
		sig  string
	}{
		{"missing", ""},
		{"wrong secret", sign("other", body)},
		{"other body", sign(secret, []byte(`{}`))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &fakeDemand{}
			m := &webhook.Counters{}
			h := newHandler(t, webhook.Config{Demand: d, Metrics: m})
			if code := deliver(t, h, "workflow_job", "d1", body, tt.sig).Code; code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", code)
			}
			if d.calls() != 0 {
				t.Fatalf("demand calls = %d, want 0", d.calls())
			}
			if got := m.Count("", webhook.OutcomeBadSignature); got != 1 {
				t.Fatalf("bad_signature count = %d, want 1", got)
			}
			if code := send(t, h, "d1", matching(1)); code != http.StatusOK || len(d.queued) != 1 {
				t.Fatalf("valid retry: status = %d, queued = %d, want 200 and 1", code, len(d.queued))
			}
		})
	}
}

func TestQueuedCallsQueued(t *testing.T) {
	d := &fakeDemand{}
	m := &webhook.Counters{}
	h := newHandler(t, webhook.Config{Demand: d, Metrics: m})
	if code := send(t, h, "d1", matching(42)); code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	want := []demand.Job{{ID: 42, Labels: labels, Repo: "acme/app", QueuedAt: t0}}
	if len(d.queued) != 1 || d.queued[0].ID != want[0].ID || !slices.Equal(d.queued[0].Labels, want[0].Labels) ||
		d.queued[0].Repo != want[0].Repo || !d.queued[0].QueuedAt.Equal(t0) {
		t.Fatalf("queued = %+v, want %+v", d.queued, want)
	}
	if got := m.Count("queued", webhook.OutcomeHandled); got != 1 {
		t.Fatalf("handled count = %d, want 1", got)
	}
}

func TestQueuedWithoutCreatedAtHasZeroQueuedAt(t *testing.T) {
	d := &fakeDemand{}
	h := newHandler(t, webhook.Config{Demand: d})
	j := matching(42)
	j.created = time.Time{}
	send(t, h, "d1", j)
	if len(d.queued) != 1 || !d.queued[0].QueuedAt.IsZero() {
		t.Fatalf("queued = %+v, want one job with zero QueuedAt", d.queued)
	}
}

func TestInProgressCallsStarted(t *testing.T) {
	d := &fakeDemand{}
	var buf bytes.Buffer
	h := newHandler(t, webhook.Config{Demand: d, Logger: debugLogger(&buf)})
	j := matching(42)
	j.action, j.runner = "in_progress", "muster-x-1"
	if code := send(t, h, "d1", j); code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if !slices.Equal(d.started, []int64{42}) {
		t.Fatalf("started = %v, want [42]", d.started)
	}
	if !strings.Contains(buf.String(), "runner_name=muster-x-1") {
		t.Fatalf("log has no runner_name:\n%s", buf.String())
	}
}

func TestCompletedCallsCompleted(t *testing.T) {
	for _, conclusion := range []string{"success", "failure", "cancelled", "skipped"} {
		t.Run(conclusion, func(t *testing.T) {
			d := &fakeDemand{}
			var buf bytes.Buffer
			h := newHandler(t, webhook.Config{Demand: d, Logger: debugLogger(&buf)})
			j := matching(42)
			j.action, j.runner, j.conclusion = "completed", "muster-x-2", conclusion
			if code := send(t, h, "d1", j); code != http.StatusOK {
				t.Fatalf("status = %d, want 200", code)
			}
			if !slices.Equal(d.completed, []int64{42}) {
				t.Fatalf("completed = %v, want [42]", d.completed)
			}
			if !strings.Contains(buf.String(), "runner_name=muster-x-2") {
				t.Fatalf("log has no runner_name:\n%s", buf.String())
			}
		})
	}
}

func TestReplayIgnored(t *testing.T) {
	d := &fakeDemand{}
	m := &webhook.Counters{}
	h := newHandler(t, webhook.Config{Demand: d, Metrics: m})
	for i := range 2 {
		if code := send(t, h, "d1", matching(42)); code != http.StatusOK {
			t.Fatalf("delivery %d: status = %d, want 200", i, code)
		}
	}
	if len(d.queued) != 1 {
		t.Fatalf("queued calls = %d, want 1", len(d.queued))
	}
	if got := m.Count("queued", webhook.OutcomeDuplicate); got != 1 {
		t.Fatalf("duplicate count = %d, want 1", got)
	}
}

func TestReplayAfterTTLProcessed(t *testing.T) {
	d := &fakeDemand{}
	c := clock.NewManual(t0)
	h := newHandler(t, webhook.Config{Demand: d, Clock: c})
	send(t, h, "d1", matching(42))
	c.Advance(webhook.DeliveryTTL + time.Second)
	send(t, h, "d1", matching(42))
	if len(d.queued) != 2 {
		t.Fatalf("queued calls = %d, want 2", len(d.queued))
	}
}

func TestConcurrentDuplicateHandledOnce(t *testing.T) {
	d := &fakeDemand{}
	h := newHandler(t, webhook.Config{Demand: d})
	body := matching(42).body(t)
	sig := sign(secret, body)
	var wg sync.WaitGroup
	codes := make([]int, 20)
	for i := range codes {
		wg.Go(func() { codes[i] = deliver(t, h, "workflow_job", "same", body, sig).Code })
	}
	wg.Wait()
	for i, code := range codes {
		if code != http.StatusOK {
			t.Fatalf("delivery %d: status = %d, want 200", i, code)
		}
	}
	if len(d.queued) != 1 {
		t.Fatalf("queued calls = %d, want 1", len(d.queued))
	}
}

func TestOtherEventsIgnored(t *testing.T) {
	waiting := matching(42)
	waiting.action = "waiting"
	tests := []struct {
		name, event string
		body        []byte
	}{
		{"ping", "ping", []byte(`{"zen":"x"}`)},
		{"push", "push", []byte(`{"ref":"refs/heads/main"}`)},
		{"waiting", "workflow_job", waiting.body(t)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &fakeDemand{}
			m := &webhook.Counters{}
			h := newHandler(t, webhook.Config{Demand: d, Metrics: m})
			if code := deliver(t, h, tt.event, "d1", tt.body, sign(secret, tt.body)).Code; code != http.StatusOK {
				t.Fatalf("status = %d, want 200", code)
			}
			if d.calls() != 0 {
				t.Fatalf("demand calls = %d, want 0", d.calls())
			}
			action := ""
			if tt.name == "waiting" {
				action = "waiting"
			}
			if got := m.Count(action, webhook.OutcomeIgnored); got != 1 {
				t.Fatalf("ignored count = %d, want 1", got)
			}
		})
	}
}

func TestIgnoredActionNotRecorded(t *testing.T) {
	d := &fakeDemand{}
	h := newHandler(t, webhook.Config{Demand: d})
	waiting := matching(42)
	waiting.action = "waiting"
	send(t, h, "d1", waiting)
	send(t, h, "d1", matching(42))
	if len(d.queued) != 1 {
		t.Fatalf("queued calls = %d, want 1", len(d.queued))
	}
}

func TestMissingHeadersIs400(t *testing.T) {
	body := matching(42).body(t)
	sig := sign(secret, body)
	d := &fakeDemand{}
	h := newHandler(t, webhook.Config{Demand: d})
	if code := deliver(t, h, "", "d1", body, sig).Code; code != http.StatusBadRequest {
		t.Fatalf("no event: status = %d, want 400", code)
	}
	if code := deliver(t, h, "workflow_job", "", body, sig).Code; code != http.StatusBadRequest {
		t.Fatalf("no delivery: status = %d, want 400", code)
	}
	if d.calls() != 0 {
		t.Fatalf("demand calls = %d, want 0", d.calls())
	}
}

func TestBadBodyIs400AndNotRecorded(t *testing.T) {
	noID := matching(0)
	noRepo := matching(42)
	noRepo.repo = ""
	tests := []struct {
		name string
		body []byte
	}{
		{"not json", []byte(`{`)},
		{"no job id", noID.body(t)},
		{"no repository", noRepo.body(t)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &fakeDemand{}
			h := newHandler(t, webhook.Config{Demand: d})
			if code := deliver(t, h, "workflow_job", "d1", tt.body, sign(secret, tt.body)).Code; code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", code)
			}
			if d.calls() != 0 {
				t.Fatalf("demand calls = %d, want 0", d.calls())
			}
			if code := send(t, h, "d1", matching(42)); code != http.StatusOK || len(d.queued) != 1 {
				t.Fatalf("redelivery: status = %d, queued = %d, want 200 and 1", code, len(d.queued))
			}
		})
	}
}

func TestBodySizeLimit(t *testing.T) {
	d := &fakeDemand{}
	m := &webhook.Counters{}
	h := newHandler(t, webhook.Config{Demand: d, Metrics: m})
	ping := []byte(`{"zen":"x"}`)
	atCap := append(append([]byte{}, ping...), bytes.Repeat([]byte(" "), webhook.MaxBodyBytes-len(ping))...)
	if code := deliver(t, h, "ping", "d1", atCap, sign(secret, atCap)).Code; code != http.StatusOK {
		t.Fatalf("at cap: status = %d, want 200", code)
	}
	over := append(atCap, ' ')
	if code := deliver(t, h, "ping", "d2", over, sign(secret, over)).Code; code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over cap: status = %d, want 413", code)
	}
	if got := m.Count("", webhook.OutcomeTooLarge); got != 1 {
		t.Fatalf("too_large count = %d, want 1", got)
	}
}

func TestUnmatchedLabelsIgnored(t *testing.T) {
	var buf bytes.Buffer
	logger := debugLogger(&buf)
	reg := demand.New(demand.Config{Pools: pools, Logger: logger})
	m := &webhook.Counters{}
	h := newHandler(t, webhook.Config{Demand: reg, Metrics: m, Logger: logger})
	n := 0
	for _, action := range []string{"queued", "in_progress", "completed"} {
		for _, j := range []job{
			{action: action, id: 100, labels: []string{"gpu"}, repo: "acme/app"},
			{action: action, id: 101, labels: []string{"self-hosted"}, repo: "other/app"},
		} {
			n++
			if code := send(t, h, "d"+string(rune('a'+n)), j); code != http.StatusOK {
				t.Fatalf("%s %+v: status = %d, want 200", action, j, code)
			}
		}
		if got := m.Count(action, webhook.OutcomeIgnored); got != 2 {
			t.Fatalf("%s ignored count = %d, want 2", action, got)
		}
	}
	if got := reg.Total(); got != 0 {
		t.Fatalf("Total = %d, want 0", got)
	}
	send(t, h, "matched", matching(7))
	send(t, h, "unmatched-close", job{action: "completed", id: 7, labels: []string{"gpu"}, repo: "acme/app"})
	if got := reg.Total(); got != 1 {
		t.Fatalf("Total after unmatched close = %d, want 1", got)
	}
	for line := range strings.Lines(buf.String()) {
		if !strings.Contains(line, "level=DEBUG") {
			t.Fatalf("log line above debug: %s", line)
		}
	}
}

func TestSecretNotLogged(t *testing.T) {
	var buf bytes.Buffer
	h := newHandler(t, webhook.Config{Logger: debugLogger(&buf)})
	body := matching(42).body(t)
	deliver(t, h, "workflow_job", "d1", body, sign("other", body))
	send(t, h, "d2", matching(43))
	if strings.Contains(buf.String(), secret) {
		t.Fatalf("log contains the secret:\n%s", buf.String())
	}
}

type blockingSessions struct {
	once    sync.Once
	started chan struct{}
	release chan struct{}
}

func (b *blockingSessions) Counts(string) (int, int) { return 0, 0 }

func (b *blockingSessions) Create(context.Context, string) error {
	b.once.Do(func() { close(b.started) })
	<-b.release
	return nil
}

func TestResponseNotBlockedByDispatch(t *testing.T) {
	reg := demand.New(demand.Config{Pools: pools})
	sessions := &blockingSessions{started: make(chan struct{}), release: make(chan struct{})}
	d := dispatch.New(dispatch.Config{Pools: pools, Demand: reg, Sessions: sessions})
	reg.SetOnChange(d.Wake)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	t.Cleanup(func() {
		close(sessions.release)
		cancel()
		<-done
	})
	h := newHandler(t, webhook.Config{Demand: reg})

	if code := send(t, h, "first", matching(1)); code != http.StatusOK {
		t.Fatalf("first: status = %d, want 200", code)
	}
	select {
	case <-sessions.started:
	case <-time.After(2 * time.Second):
		t.Fatal("dispatcher did not call Create within 2s")
	}
	for i := range int64(5) {
		start := time.Now()
		if code := send(t, h, "next"+string(rune('a'+i)), matching(10+i)); code != http.StatusOK {
			t.Fatalf("delivery %d: status = %d, want 200", i, code)
		}
		if el := time.Since(start); el >= time.Second {
			t.Fatalf("delivery %d took %v while Create blocks, want < 1s", i, el)
		}
	}
	if got := reg.Total(); got != 6 {
		t.Fatalf("Total = %d, want 6", got)
	}
}
