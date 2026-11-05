// Package metrics serves muster's Prometheus metrics (requirement OBS-1).
// It implements the Metrics interface of every instrumented package, and
// it is the only package that imports the Prometheus client.
package metrics

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/liquidmetal-dev/muster/internal/github"
	"github.com/liquidmetal-dev/muster/internal/intake/poller"
	"github.com/liquidmetal-dev/muster/internal/intake/webhook"
	"github.com/liquidmetal-dev/muster/internal/reconcile"
	"github.com/liquidmetal-dev/muster/internal/session"
	"github.com/liquidmetal-dev/muster/internal/version"
)

// Histogram buckets, in seconds.
var (
	latencyBuckets = []float64{1, 2, 5, 10, 20, 30, 60, 120, 300, 600}
	sessionBuckets = []float64{30, 60, 300, 600, 1800, 3600, 7200, 14400, 28800}
	pollBuckets    = []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}
)

// Sessions lists the live sessions. *session.Registry satisfies it.
type Sessions interface {
	Sessions() []session.Info
}

// SessionsFunc adapts a func, so a caller can build Metrics before the
// registry exists.
type SessionsFunc func() []session.Info

// Sessions calls f.
func (f SessionsFunc) Sessions() []session.Info { return f() }

// Config configures Metrics.
type Config struct {
	// Pools are the configured pool names. Each gets a zero series for
	// every active state.
	Pools []string
	// Sessions is nil when muster_sessions is not collected.
	Sessions Sessions
	Version  version.Info
}

// Metrics holds muster's collectors on a private registry.
type Metrics struct {
	reg *prometheus.Registry

	claimAttempts    *prometheus.CounterVec
	claimExhaustions *prometheus.CounterVec
	claimDuration    *prometheus.HistogramVec
	online           *prometheus.HistogramVec
	sessionDuration  *prometheus.HistogramVec
	finished         *prometheus.CounterVec
	releases         *prometheus.CounterVec
	githubRequests   *prometheus.CounterVec
	githubErrors     *prometheus.CounterVec
	deliveries       *prometheus.CounterVec
	poll             *prometheus.HistogramVec
	decisions        *prometheus.CounterVec
	ready            *prometheus.GaugeVec
}

var (
	_ session.Metrics   = (*Metrics)(nil)
	_ github.Metrics    = (*Metrics)(nil)
	_ webhook.Metrics   = (*Metrics)(nil)
	_ poller.Metrics    = (*Metrics)(nil)
	_ reconcile.Metrics = (*Metrics)(nil)
)

// New registers every collector on a new registry.
func New(cfg Config) *Metrics {
	m := &Metrics{
		reg: prometheus.NewRegistry(),
		claimAttempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "muster_claim_attempts_total", Help: "VM claim attempts.",
		}, []string{"pool"}),
		claimExhaustions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "muster_claim_exhaustions_total", Help: "VM claim attempts that found the pool exhausted.",
		}, []string{"pool"}),
		claimDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "muster_claim_duration_seconds", Help: "Time from CLAIMING to a claimed VM, backoff included.", Buckets: latencyBuckets,
		}, []string{"pool"}),
		online: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "muster_queued_to_online_seconds", Help: "Time from the creation of a session to its runner coming online.", Buckets: latencyBuckets,
		}, []string{"pool"}),
		sessionDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "muster_session_duration_seconds", Help: "Time from the creation of a session until it finished.", Buckets: sessionBuckets,
		}, []string{"pool", "outcome"}),
		finished: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "muster_sessions_finished_total", Help: "Finished sessions by outcome and reason.",
		}, []string{"pool", "outcome", "reason"}),
		releases: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "muster_vm_releases_total", Help: "VM release attempts during session cleanup.",
		}, []string{"pool", "outcome", "reason", "result"}),
		githubRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "muster_github_requests_total", Help: "GitHub API HTTP attempts by status class.",
		}, []string{"endpoint", "status"}),
		githubErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "muster_github_errors_total", Help: "Failed GitHub API HTTP attempts by kind.",
		}, []string{"endpoint", "kind"}),
		deliveries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "muster_webhook_deliveries_total", Help: "Webhook deliveries by action and outcome.",
		}, []string{"action", "outcome"}),
		poll: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "muster_poll_duration_seconds", Help: "Time to list the queued jobs of one repository.", Buckets: pollBuckets,
		}, []string{"repo", "outcome"}),
		decisions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "muster_reconcile_decisions_total", Help: "Startup session reconciliation decisions.",
		}, []string{"action", "outcome"}),
		ready: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "muster_ready", Help: "1 when a readiness check passes, 0 when it fails.",
		}, []string{"check"}),
	}
	build := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "muster_build_info", Help: "The version of the running musterd; always 1.",
	}, []string{"version", "commit"})
	build.WithLabelValues(cfg.Version.Version, cfg.Version.Commit).Set(1)
	m.reg.MustRegister(
		m.claimAttempts, m.claimExhaustions, m.claimDuration, m.online, m.sessionDuration,
		m.finished, m.releases, m.githubRequests, m.githubErrors, m.deliveries, m.poll,
		m.decisions, m.ready, build,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	if cfg.Sessions != nil {
		m.reg.MustRegister(newSessionCollector(cfg.Pools, cfg.Sessions))
	}
	return m
}

// Handler serves the metrics in the Prometheus text format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// SetReady records the result of a readiness check.
func (m *Metrics) SetReady(check string, ok bool) {
	v := 0.0
	if ok {
		v = 1
	}
	m.ready.WithLabelValues(check).Set(v)
}

// ObserveClaimAttempt implements session.Metrics.
func (m *Metrics) ObserveClaimAttempt(pool string, exhausted bool) {
	m.claimAttempts.WithLabelValues(pool).Inc()
	if exhausted {
		m.claimExhaustions.WithLabelValues(pool).Inc()
	}
}

// ObserveClaim implements session.Metrics.
func (m *Metrics) ObserveClaim(pool string, d time.Duration) {
	m.claimDuration.WithLabelValues(pool).Observe(d.Seconds())
}

// ObserveOnline implements session.Metrics.
func (m *Metrics) ObserveOnline(pool string, d time.Duration) {
	m.online.WithLabelValues(pool).Observe(d.Seconds())
}

// ObserveFinished implements session.Metrics.
func (m *Metrics) ObserveFinished(pool string, outcome session.State, reason session.Reason, d time.Duration, adopted bool) {
	o := outcomeLabel(outcome)
	m.finished.WithLabelValues(pool, o, string(reason)).Inc()
	if !adopted {
		m.sessionDuration.WithLabelValues(pool, o).Observe(d.Seconds())
	}
}

// ObserveRelease implements session.Metrics.
func (m *Metrics) ObserveRelease(pool string, outcome session.State, reason session.Reason, ok bool) {
	result := "error"
	if ok {
		result = "ok"
	}
	m.releases.WithLabelValues(pool, outcomeLabel(outcome), string(reason), result).Inc()
}

// ObserveCall implements github.Metrics.
func (m *Metrics) ObserveCall(endpoint string, status int) {
	m.githubRequests.WithLabelValues(endpoint, statusClass(status)).Inc()
}

// ObserveError implements github.Metrics.
func (m *Metrics) ObserveError(endpoint, kind string) {
	m.githubErrors.WithLabelValues(endpoint, kind).Inc()
}

// ObserveDelivery implements webhook.Metrics.
func (m *Metrics) ObserveDelivery(action, outcome string) {
	m.deliveries.WithLabelValues(action, outcome).Inc()
}

// ObservePoll implements poller.Metrics.
func (m *Metrics) ObservePoll(repo, outcome string, d time.Duration) {
	m.poll.WithLabelValues(repo, outcome).Observe(d.Seconds())
}

// ObserveDecision implements reconcile.Metrics.
func (m *Metrics) ObserveDecision(action, outcome string) {
	m.decisions.WithLabelValues(action, outcome).Inc()
}

func outcomeLabel(s session.State) string { return strings.ToLower(s.String()) }

// statusClass is "2xx" for 200 to 299 and so on; status 0 means that no
// response arrived.
func statusClass(status int) string {
	if status < 100 || status > 599 {
		return "error"
	}
	return strconv.Itoa(status/100) + "xx"
}
