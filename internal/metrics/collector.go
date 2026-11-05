package metrics

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/liquidmetal-dev/muster/internal/session"
)

var activeStates = []session.State{session.Pending, session.Claiming, session.Starting, session.Running}

// sessionCollector counts the live sessions at scrape time, so the gauge
// cannot drift from the registry.
type sessionCollector struct {
	pools    []string
	sessions Sessions
	desc     *prometheus.Desc
}

func newSessionCollector(pools []string, s Sessions) *sessionCollector {
	return &sessionCollector{
		pools:    pools,
		sessions: s,
		desc:     prometheus.NewDesc("muster_sessions", "Live sessions by pool and state.", []string{"pool", "state"}, nil),
	}
}

func (c *sessionCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }

func (c *sessionCollector) Collect(ch chan<- prometheus.Metric) {
	type key struct {
		pool  string
		state session.State
	}
	counts := map[key]int{}
	for _, p := range c.pools {
		for _, st := range activeStates {
			counts[key{p, st}] = 0
		}
	}
	for _, i := range c.sessions.Sessions() {
		counts[key{i.Pool, i.State}]++
	}
	for k, n := range counts {
		ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, float64(n), k.pool, k.state.String())
	}
}
