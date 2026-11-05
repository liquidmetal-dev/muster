package session_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liquidmetal-dev/muster/internal/metrics"
	"github.com/liquidmetal-dev/muster/internal/session"
)

// newScrapedEnv wires a metrics.Metrics into the harness. The registry
// does not exist when the metrics are built, so they read it through
// SessionsFunc.
func newScrapedEnv(t *testing.T, opts ...envOption) (*env, *metrics.Metrics) {
	t.Helper()
	var reg *session.Registry
	m := metrics.New(metrics.Config{
		Pools:    []string{testPool},
		Sessions: metrics.SessionsFunc(func() []session.Info { return reg.Sessions() }),
	})
	e := newEnv(t, append(opts, withMetrics(m))...)
	reg = e.Registry
	return e, m
}

func scrapeBody(t *testing.T, m *metrics.Metrics) string {
	t.Helper()
	srv := httptest.NewServer(m.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("scrape status = %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func TestScrapeAfterSession(t *testing.T) {
	e, m := newScrapedEnv(t)
	c := e.capture()
	e.start()
	e.running(c)
	c.exit(0)
	wantEnd(t, e.endedInfo(), session.Ended, session.ReasonExecExit)
	body := scrapeBody(t, m)
	for _, s := range []string{
		`muster_claim_attempts_total{pool="` + testPool + `"} 1`,
		`muster_queued_to_online_seconds_count{pool="` + testPool + `"} 1`,
		`muster_sessions_finished_total{outcome="ended",pool="` + testPool + `",reason="exec_exit"} 1`,
		`muster_session_duration_seconds_count{outcome="ended",pool="` + testPool + `"} 1`,
		`muster_vm_releases_total{outcome="ended",pool="` + testPool + `",reason="exec_exit",result="ok"} 1`,
		`muster_sessions{pool="` + testPool + `",state="RUNNING"} 0`,
	} {
		if !strings.Contains(body, s+"\n") {
			t.Errorf("scrape has no line %q", s)
		}
	}
}

func TestScrapeDuringSessions(t *testing.T) {
	e, m := newScrapedEnv(t, withCapacity(0))
	e.Demand.set(testPool, 10)
	e.start()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
				scrapeBody(t, m)
			}
		}
	})
	for range 10 {
		e.create(testPool)
	}
	waitFor(t, func() bool { return e.Pool.CallCount("ClaimVM") >= 10 })
	e.Demand.set(testPool, 0)
	for range 10 {
		wantEnd(t, e.driveEnd(time.Second), session.Failed, session.ReasonNoDemand)
	}
	close(stop)
	wg.Wait()
	body := scrapeBody(t, m)
	if !strings.Contains(body, `muster_sessions_finished_total{`) {
		t.Error("no finished sessions in the scrape")
	}
}
