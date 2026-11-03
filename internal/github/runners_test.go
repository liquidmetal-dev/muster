package github_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liquidmetal-dev/muster/internal/fakegithub"
	"github.com/liquidmetal-dev/muster/internal/github"
)

// recordBodies keeps the request bodies sent to each path.
type recordBodies struct {
	mu     sync.Mutex
	bodies map[string][][]byte
}

func (r *recordBodies) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(b))
		r.mu.Lock()
		if r.bodies == nil {
			r.bodies = map[string][][]byte{}
		}
		r.bodies[req.URL.Path] = append(r.bodies[req.URL.Path], b)
		r.mu.Unlock()
	}
	return http.DefaultTransport.RoundTrip(req)
}

func (r *recordBodies) get(path string) [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bodies[path]
}

func jit(name string, labels ...string) github.JITRequest {
	if len(labels) == 0 {
		labels = []string{"self-hosted", "x"}
	}
	return github.JITRequest{Name: name, Labels: labels, RunnerGroupID: 1}
}

func TestJITConfigOrg(t *testing.T) {
	e := newEnv(t)
	e.Fake.AddOrg("acme")
	gid, err := e.Fake.AddRunnerGroup("acme", "ci")
	if err != nil {
		t.Fatal(err)
	}
	rec := &recordBodies{}
	c := e.AppClient(t, func(c *github.Config) { c.HTTPClient = &http.Client{Transport: rec} })
	req := jit("muster-ns-p-abc")
	req.RunnerGroupID = gid
	cfg, err := c.GenerateJITConfig(context.Background(), github.OrgScope("acme"), req)
	if err != nil {
		t.Fatalf("GenerateJITConfig: %v", err)
	}
	decoded, err := fakegithub.DecodeJITConfig(cfg.Encoded.Reveal())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RunnerID <= 0 || decoded.RunnerName != "muster-ns-p-abc" || decoded.RunnerID != cfg.RunnerID {
		t.Fatalf("cfg %+v decoded %+v", cfg, decoded)
	}
	runners := e.Fake.Runners()
	if len(runners) != 1 || runners[0].RunnerGroupID != gid || strings.Join(runners[0].Labels, ",") != "self-hosted,x" {
		t.Fatalf("fake runners = %+v", runners)
	}
	req.Name, req.WorkFolder = "muster-ns-p-def", "w"
	if _, err := c.GenerateJITConfig(context.Background(), github.OrgScope("acme"), req); err != nil {
		t.Fatal(err)
	}
	bodies := rec.get("/api/v3/orgs/acme/actions/runners/generate-jitconfig")
	if len(bodies) != 2 {
		t.Fatalf("JIT bodies = %d, want 2", len(bodies))
	}
	for i, want := range []string{"_work", "w"} {
		var body struct {
			WorkFolder string `json:"work_folder"`
		}
		if err := json.Unmarshal(bodies[i], &body); err != nil {
			t.Fatal(err)
		}
		if body.WorkFolder != want {
			t.Errorf("body %d work_folder = %q, want %q", i, body.WorkFolder, want)
		}
	}
}

func TestJITConfigRepo(t *testing.T) {
	e := newEnv(t)
	e.Fake.AddRepo("acme/app")
	s, _ := github.RepoScope("acme/app")
	if _, err := e.AppClient(t).GenerateJITConfig(context.Background(), s, jit("muster-ns-p-abc")); err != nil {
		t.Fatalf("GenerateJITConfig: %v", err)
	}
}

func TestJITConfigValidation(t *testing.T) {
	e := newEnv(t)
	c := e.AppClient(t)
	for name, req := range map[string]github.JITRequest{
		"empty name":    {Labels: []string{"x"}, RunnerGroupID: 1},
		"long name":     {Name: strings.Repeat("a", 65), Labels: []string{"x"}, RunnerGroupID: 1},
		"no labels":     {Name: "n", RunnerGroupID: 1},
		"invalid scope": {Name: "n", Labels: []string{"x"}},
	} {
		s := github.OrgScope("acme")
		if name == "invalid scope" {
			s = github.Scope{}
		}
		if _, err := c.GenerateJITConfig(context.Background(), s, req); !errors.Is(err, github.ErrInvalidRequest) {
			t.Errorf("%s: err = %v, want ErrInvalidRequest", name, err)
		}
	}
	if n := len(e.Fake.Requests()); n != 0 {
		t.Fatalf("requests = %d, want 0", n)
	}
	if _, err := c.GenerateJITConfig(context.Background(), github.OrgScope("acme"), github.JITRequest{Name: strings.Repeat("a", 64), Labels: []string{"x"}}); errors.Is(err, github.ErrInvalidRequest) {
		t.Fatal("64-character name rejected")
	}
}

func TestJITConfigDuplicate(t *testing.T) {
	e := newEnv(t)
	e.Fake.AddOrg("acme")
	c := e.AppClient(t)
	if _, err := c.GenerateJITConfig(context.Background(), github.OrgScope("acme"), jit("dup")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GenerateJITConfig(context.Background(), github.OrgScope("acme"), jit("dup")); !errors.Is(err, github.ErrRunnerExists) {
		t.Fatalf("err = %v, want ErrRunnerExists", err)
	}
}

func TestJITConfigRedacted(t *testing.T) {
	e := newEnv(t)
	e.Fake.AddOrg("acme")
	c := e.AppClient(t)
	cfg, err := c.GenerateJITConfig(context.Background(), github.OrgScope("acme"), jit("secret"))
	if err != nil {
		t.Fatal(err)
	}
	secret := cfg.Encoded.Reveal()
	var logged, jsonLogged bytes.Buffer
	slog.New(slog.NewTextHandler(&logged, nil)).Info("jit", slog.Any("cfg", cfg), slog.Any("encoded", cfg.Encoded))
	slog.New(slog.NewJSONHandler(&jsonLogged, nil)).Info("jit", slog.Any("cfg", cfg), slog.Any("encoded", cfg.Encoded))
	marshalled, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// A variable format keeps vet from rejecting verbs a caller might misuse.
	otherVerbs := "%d %x %q %10.3s %d"
	_, dupErr := c.GenerateJITConfig(context.Background(), github.OrgScope("acme"), jit("secret"))
	for name, out := range map[string]string{
		"fmt":   fmt.Sprintf("%v %+v %#v %s", cfg, cfg, cfg, cfg.Encoded),
		"verbs": fmt.Sprintf(otherVerbs, cfg.Encoded, cfg.Encoded, cfg.Encoded, cfg.Encoded, cfg),
		"slog":  logged.String(),
		"json":  jsonLogged.String() + string(marshalled),
		"error": fmt.Sprint(dupErr),
	} {
		if strings.Contains(out, secret) || strings.Contains(out, secret[:16]) {
			t.Errorf("%s output leaks the JIT config: %s", name, out)
		}
	}
}

func TestJITErrorOmitsResponseMessage(t *testing.T) {
	e := newEnv(t)
	e.Fake.AddOrg("acme")
	c := e.AppClient(t)
	for _, status := range []int{http.StatusConflict, http.StatusUnprocessableEntity} {
		e.Fake.FailNext(fakegithub.PatternOrgJITConfig, fakegithub.Fault{Status: status, Message: "echo SECRETBODY"})
		_, err := c.GenerateJITConfig(context.Background(), github.OrgScope("acme"), jit("n"))
		if err == nil {
			t.Fatalf("status %d: want error", status)
		}
		if strings.Contains(err.Error(), "SECRETBODY") {
			t.Errorf("status %d: error carries the response message: %v", status, err)
		}
	}
}

func TestJITSecondaryLimitErrorOmitsMessage(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v3/orgs/acme/actions/runners/generate-jitconfig", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"You have exceeded a secondary rate limit SECRETBODY","documentation_url":"https://docs.github.com/rest/overview/rate-limits-for-the-rest-api#about-secondary-rate-limits"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, err := github.New(github.Config{Token: "x", BaseURL: srv.URL, MaxWait: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.GenerateJITConfig(context.Background(), github.OrgScope("acme"), jit("n"))
	if !errors.Is(err, github.ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
	if strings.Contains(err.Error(), "SECRETBODY") {
		t.Errorf("error carries the response message: %v", err)
	}
}

func TestNoRetry5xxPost(t *testing.T) {
	e := newEnv(t)
	e.Fake.AddOrg("acme")
	e.Fake.FailNext(fakegithub.PatternOrgJITConfig, fakegithub.Fault{Status: 500, Message: "boom"})
	if _, err := e.AppClient(t).GenerateJITConfig(context.Background(), github.OrgScope("acme"), jit("n")); err == nil {
		t.Fatal("want error")
	}
	if n := e.Fake.RequestCount(fakegithub.PatternOrgJITConfig); n != 1 {
		t.Fatalf("requests = %d, want 1", n)
	}
}

func TestRetry429Post(t *testing.T) {
	e := newEnv(t)
	e.Fake.AddOrg("acme")
	e.Fake.FailNext(fakegithub.PatternOrgJITConfig, fakegithub.Fault{Status: 429, Header: header("Retry-After", "1"), Message: "slow down"})
	if _, err := e.AppClient(t).GenerateJITConfig(context.Background(), github.OrgScope("acme"), jit("replayed")); err != nil {
		t.Fatalf("GenerateJITConfig: %v", err)
	}
	if r := e.Fake.Runners(); len(r) != 1 || r[0].Name != "replayed" {
		t.Fatalf("runners = %+v, want one named replayed", r)
	}
}

func seedRunners(t *testing.T, c *github.Client, s github.Scope, n int, prefix string) {
	t.Helper()
	for i := range n {
		if _, err := c.GenerateJITConfig(context.Background(), s, jit(fmt.Sprintf("%s%d", prefix, i))); err != nil {
			t.Fatal(err)
		}
	}
}

func TestListRunnersPrefixPaginated(t *testing.T) {
	e := newEnv(t)
	e.Fake.AddOrg("acme")
	c := e.AppClient(t)
	seedRunners(t, c, github.OrgScope("acme"), 130, "muster-ns-p-")
	seedRunners(t, c, github.OrgScope("acme"), 20, "other-")
	got, err := c.ListRunners(context.Background(), github.OrgScope("acme"), "muster-ns-p-")
	if err != nil {
		t.Fatalf("ListRunners: %v", err)
	}
	if len(got) != 130 {
		t.Fatalf("runners = %d, want 130", len(got))
	}
	for _, r := range got {
		if !strings.HasPrefix(r.Name, "muster-ns-p-") || r.ID == 0 || len(r.Labels) != 2 {
			t.Fatalf("runner %+v", r)
		}
	}
	if n := e.Fake.RequestCount(fakegithub.PatternOrgRunners); n != 2 {
		t.Fatalf("list requests = %d, want 2", n)
	}
}

func TestUnauthorizedRefreshesOnce(t *testing.T) {
	e := newEnv(t)
	e.Fake.AddOrg("acme")
	c := e.AppClient(t)
	if _, err := c.ListRunners(context.Background(), github.OrgScope("acme"), ""); err != nil {
		t.Fatal(err)
	}
	e.Fake.FailNext(fakegithub.PatternOrgRunners, fakegithub.Fault{Status: 401, Message: "Bad credentials"})
	if _, err := c.ListRunners(context.Background(), github.OrgScope("acme"), ""); err != nil {
		t.Fatalf("ListRunners after one 401: %v", err)
	}
	if n := e.Fake.RequestCount(fakegithub.PatternAccessToken); n != 2 {
		t.Fatalf("exchanges = %d, want 2", n)
	}
	for range 2 {
		e.Fake.FailNext(fakegithub.PatternOrgRunners, fakegithub.Fault{Status: 401, Message: "Bad credentials"})
	}
	if _, err := c.ListRunners(context.Background(), github.OrgScope("acme"), ""); !errors.Is(err, github.ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	if n := e.Fake.RequestCount(fakegithub.PatternAccessToken); n != 3 {
		t.Fatalf("exchanges = %d, want 3 (one replay per call)", n)
	}
}

func TestListRunners5xxRetried(t *testing.T) {
	e := newEnv(t)
	e.Fake.AddOrg("acme")
	c := e.AppClient(t)
	seedRunners(t, c, github.OrgScope("acme"), 101, "muster-")
	e.Fake.FailNext(fakegithub.PatternOrgRunners, fakegithub.Fault{Status: 502, Message: "bad gateway"})
	got, err := c.ListRunners(context.Background(), github.OrgScope("acme"), "muster-")
	if err != nil || len(got) != 101 {
		t.Fatalf("ListRunners = %d, %v", len(got), err)
	}
	if n := e.Fake.RequestCount(fakegithub.PatternOrgRunners); n != 3 {
		t.Fatalf("list requests = %d, want 3", n)
	}
}

func TestDeleteRunner(t *testing.T) {
	e := newEnv(t)
	e.Fake.AddRepo("acme/app")
	s, _ := github.RepoScope("acme/app")
	c := e.AppClient(t)
	cfg, err := c.GenerateJITConfig(context.Background(), s, jit("gone"))
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if err := c.DeleteRunner(context.Background(), s, cfg.RunnerID); err != nil {
			t.Fatalf("delete %d: %v", i, err)
		}
	}
	if r := e.Fake.Runners(); len(r) != 0 {
		t.Fatalf("runners = %+v, want none", r)
	}

	busy, err := c.GenerateJITConfig(context.Background(), s, jit("busy", "self-hosted"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Fake.QueueJob("acme/app", []string{"self-hosted"}); err != nil {
		t.Fatal(err)
	}
	e.Clock.Advance(time.Second)
	e.Fake.WaitFor(t, 5*time.Second, func() bool {
		for _, r := range e.Fake.Runners() {
			if r.ID == busy.RunnerID && r.Busy {
				return true
			}
		}
		return false
	})
	if err := c.DeleteRunner(context.Background(), s, busy.RunnerID); !errors.Is(err, github.ErrRunnerBusy) {
		t.Fatalf("err = %v, want ErrRunnerBusy", err)
	}
}

func TestRunnerGroupID(t *testing.T) {
	e := newEnv(t)
	e.Fake.AddOrg("acme")
	gid, err := e.Fake.AddRunnerGroup("acme", "ci")
	if err != nil {
		t.Fatal(err)
	}
	e.Fake.AddOrg("big")
	var last int64
	for i := range 105 {
		if last, err = e.Fake.AddRunnerGroup("big", fmt.Sprintf("g%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	c := e.AppClient(t)
	repo, _ := github.RepoScope("acme/app")
	before := len(e.Fake.Requests())
	if id, err := c.RunnerGroupID(context.Background(), repo, "ignored"); err != nil || id != 1 {
		t.Fatalf("repo scope = %d, %v", id, err)
	}
	if n := len(e.Fake.Requests()); n != before {
		t.Fatalf("repo scope made %d requests, want 0", n-before)
	}
	tests := []struct {
		org, name string
		want      int64
		err       error
	}{
		{"acme", "ci", gid, nil},
		{"acme", "CI", gid, nil},
		{"acme", "", 1, nil},
		{"acme", "nope", 0, github.ErrRunnerGroupNotFound},
		{"big", "g104", last, nil},
		{"missing", "ci", 0, github.ErrNotFound},
	}
	for _, tt := range tests {
		id, err := c.RunnerGroupID(context.Background(), github.OrgScope(tt.org), tt.name)
		if tt.err != nil {
			if !errors.Is(err, tt.err) {
				t.Errorf("%s/%s: err = %v, want %v", tt.org, tt.name, err, tt.err)
			}
			continue
		}
		if err != nil || id != tt.want {
			t.Errorf("%s/%s = %d, %v; want %d", tt.org, tt.name, id, err, tt.want)
		}
	}
}
