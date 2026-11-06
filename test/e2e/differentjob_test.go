//go:build e2e

package e2e

import (
	"testing"
	"time"

	"github.com/liquidmetal-dev/muster/internal/fakegithub"
)

// A session serves the pool, not the job that triggered it: when that job is cancelled while
// the runner is still offline, the runner takes the next queued job.
func TestRunnerTakesDifferentJob(t *testing.T) {
	s := newScenario(t, scenarioOpts{PoolSize: 1})
	// The stub sleeps for RunFor from its start, so RunFor must outlast OnlineAfter.
	s.GH.NextRunner(fakegithub.Behavior{OnlineAfter: 20 * time.Second, RunFor: 60 * time.Second})
	s.StartMusterd()

	a := s.QueueJob()
	var runner fakegithub.Runner
	waitFor(t, 60*time.Second, "one lease and an offline runner", func() bool {
		r, ok := s.sessionRunner()
		runner = r
		return len(s.Leases()) == 1 && ok && r.Status == "offline"
	})
	if err := s.GH.CancelJob(a.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	b := s.QueueJob()

	waitFor(t, 3*time.Minute, "B completed with success on the first runner", func() bool {
		j := s.Job(b.ID)
		return j.Status == "completed" && j.Conclusion == "success" && j.RunnerName == runner.Name
	})
	waitFor(t, 2*time.Minute, "the lease released", func() bool { return len(s.Leases()) == 0 })
	// The duration histogram counts successful claims only, not retries of an exhausted pool.
	if got := s.Metric("muster_claim_duration_seconds", map[string]string{"pool": s.Pool}); got != 1 {
		t.Fatalf("successful claims = %v, want 1", got)
	}
	if got := s.Metric("muster_sessions_finished_total", map[string]string{"pool": s.Pool}); got != 1 {
		t.Fatalf("finished sessions = %v, want 1", got)
	}
}
