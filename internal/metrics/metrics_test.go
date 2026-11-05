package metrics_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/liquidmetal-dev/muster/internal/metrics"
	"github.com/liquidmetal-dev/muster/internal/session"
	"github.com/liquidmetal-dev/muster/internal/version"
)

type fixedSessions []session.Info

func (f fixedSessions) Sessions() []session.Info { return f }

func scrape(t *testing.T, m *metrics.Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape status = %d", rec.Code)
	}
	b, _ := io.ReadAll(rec.Body)
	return string(b)
}

func wantSeries(t *testing.T, body string, series ...string) {
	t.Helper()
	for _, s := range series {
		if !strings.Contains(body, s+"\n") {
			t.Errorf("scrape has no line %q", s)
		}
	}
}

func TestClaimSeries(t *testing.T) {
	m := metrics.New(metrics.Config{})
	m.ObserveClaimAttempt("p", true)
	m.ObserveClaimAttempt("p", false)
	m.ObserveClaim("p", 3*time.Second)
	wantSeries(t, scrape(t, m),
		`muster_claim_attempts_total{pool="p"} 2`,
		`muster_claim_exhaustions_total{pool="p"} 1`,
		`muster_claim_duration_seconds_count{pool="p"} 1`,
		`muster_claim_duration_seconds_sum{pool="p"} 3`)
}

func TestFinishedSkipsHistogramWhenAdopted(t *testing.T) {
	m := metrics.New(metrics.Config{})
	m.ObserveFinished("p", session.Ended, session.ReasonExecExit, 0, true)
	body := scrape(t, m)
	wantSeries(t, body, `muster_sessions_finished_total{outcome="ended",pool="p",reason="exec_exit"} 1`)
	if strings.Contains(body, `muster_session_duration_seconds_count{outcome="ended",pool="p"}`) {
		t.Error("adopted session observed in muster_session_duration_seconds")
	}
	m.ObserveFinished("p", session.Failed, session.ReasonJITError, time.Minute, false)
	wantSeries(t, scrape(t, m), `muster_session_duration_seconds_count{outcome="failed",pool="p"} 1`)
}

func TestReleaseSeries(t *testing.T) {
	m := metrics.New(metrics.Config{})
	m.ObserveRelease("p", session.Ended, session.ReasonExecExit, false)
	m.ObserveRelease("p", session.Ended, session.ReasonExecExit, true)
	wantSeries(t, scrape(t, m),
		`muster_vm_releases_total{outcome="ended",pool="p",reason="exec_exit",result="error"} 1`,
		`muster_vm_releases_total{outcome="ended",pool="p",reason="exec_exit",result="ok"} 1`)
}

func TestOnlineSeries(t *testing.T) {
	m := metrics.New(metrics.Config{})
	m.ObserveOnline("p", 12*time.Second)
	wantSeries(t, scrape(t, m), `muster_queued_to_online_seconds_count{pool="p"} 1`)
}

func TestGitHubStatusClass(t *testing.T) {
	m := metrics.New(metrics.Config{})
	m.ObserveCall("GET /x", 404)
	m.ObserveCall("GET /x", 200)
	m.ObserveCall("GET /x", 0)
	m.ObserveError("GET /x", "network")
	wantSeries(t, scrape(t, m),
		`muster_github_requests_total{endpoint="GET /x",status="4xx"} 1`,
		`muster_github_requests_total{endpoint="GET /x",status="2xx"} 1`,
		`muster_github_requests_total{endpoint="GET /x",status="error"} 1`,
		`muster_github_errors_total{endpoint="GET /x",kind="network"} 1`)
}

func TestWebhookAndPollSeries(t *testing.T) {
	m := metrics.New(metrics.Config{})
	m.ObserveDelivery("queued", "handled")
	m.ObservePoll("acme/app", "ok", 300*time.Millisecond)
	wantSeries(t, scrape(t, m),
		`muster_webhook_deliveries_total{action="queued",outcome="handled"} 1`,
		`muster_poll_duration_seconds_count{outcome="ok",repo="acme/app"} 1`)
}

func TestReconcileDecisions(t *testing.T) {
	m := metrics.New(metrics.Config{})
	m.ObserveDecision("release_lease", "ok")
	wantSeries(t, scrape(t, m), `muster_reconcile_decisions_total{action="release_lease",outcome="ok"} 1`)
}

func TestReady(t *testing.T) {
	m := metrics.New(metrics.Config{})
	m.SetReady("battery", false)
	m.SetReady("github", true)
	wantSeries(t, scrape(t, m), `muster_ready{check="battery"} 0`, `muster_ready{check="github"} 1`)
}

func TestCollectorZeroForIdlePool(t *testing.T) {
	m := metrics.New(metrics.Config{Pools: []string{"a"}, Sessions: fixedSessions{}})
	body := scrape(t, m)
	for _, st := range []string{"PENDING", "CLAIMING", "STARTING", "RUNNING"} {
		wantSeries(t, body, `muster_sessions{pool="a",state="`+st+`"} 0`)
	}
	if strings.Contains(body, `state="ENDED"`) {
		t.Error("ENDED exported with no ENDED session")
	}
}

func TestCollectorCountsSessions(t *testing.T) {
	m := metrics.New(metrics.Config{Pools: []string{"a"}, Sessions: fixedSessions{
		{Pool: "a", State: session.Running}, {Pool: "a", State: session.Running}, {Pool: "a", State: session.Ended},
	}})
	wantSeries(t, scrape(t, m), `muster_sessions{pool="a",state="RUNNING"} 2`, `muster_sessions{pool="a",state="ENDED"} 1`)
}

func TestSessionsFunc(t *testing.T) {
	var infos []session.Info
	m := metrics.New(metrics.Config{Pools: []string{"a"}, Sessions: metrics.SessionsFunc(func() []session.Info { return infos })})
	infos = []session.Info{{Pool: "a", State: session.Pending}}
	wantSeries(t, scrape(t, m), `muster_sessions{pool="a",state="PENDING"} 1`)
}

func TestBuildInfo(t *testing.T) {
	m := metrics.New(metrics.Config{Version: version.Info{Version: "v1.2.3", Commit: "abc"}})
	wantSeries(t, scrape(t, m), `muster_build_info{commit="abc",version="v1.2.3"} 1`)
}

func TestBucketsExact(t *testing.T) {
	m := metrics.New(metrics.Config{})
	m.ObserveClaim("p", time.Second)
	m.ObserveOnline("p", time.Second)
	m.ObserveFinished("p", session.Ended, session.ReasonExecExit, time.Second, false)
	m.ObservePoll("r", "ok", time.Millisecond)
	body := scrape(t, m)
	wantSeries(t, body,
		`muster_claim_duration_seconds_bucket{pool="p",le="600"} 1`,
		`muster_queued_to_online_seconds_bucket{pool="p",le="600"} 1`,
		`muster_session_duration_seconds_bucket{outcome="ended",pool="p",le="28800"} 1`,
		`muster_poll_duration_seconds_bucket{outcome="ok",repo="r",le="10"} 1`)
	if strings.Contains(body, `muster_claim_duration_seconds_bucket{pool="p",le="0.005"}`) {
		t.Error("claim histogram uses the default buckets")
	}
}

func TestGoCollector(t *testing.T) {
	if body := scrape(t, metrics.New(metrics.Config{})); !strings.Contains(body, "go_goroutines ") {
		t.Error("no Go runtime metrics")
	}
}
