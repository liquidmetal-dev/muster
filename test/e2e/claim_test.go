//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/liquidmetal-dev/muster/internal/fakegithub"
)

// sessionRunner returns the fake's runner named for the scenario's pool, if there is one.
func (s *scenario) sessionRunner() (fakegithub.Runner, bool) {
	prefix := "muster-" + s.Namespace + "-" + s.Pool + "-"
	for _, r := range s.GH.Runners() {
		if strings.HasPrefix(r.Name, prefix) && len(r.Name) == len(prefix)+12 {
			return r, true
		}
	}
	return fakegithub.Runner{}, false
}

func TestClaimExecRegister(t *testing.T) {
	s := newScenario(t, scenarioOpts{PoolSize: 1, DefaultBehavior: fakegithub.Behavior{RunFor: 20 * time.Second}})
	s.StartMusterd()

	job := s.QueueJob()
	waitFor(t, 90*time.Second, "one lease", func() bool { return len(s.Leases()) == 1 })
	vmUID := s.Leases()[0].GetVmUid()
	waitFor(t, 90*time.Second, "the session's runner online", func() bool {
		r, ok := s.sessionRunner()
		return ok && r.Status == "online"
	})
	waitFor(t, 90*time.Second, "one RUNNING session", func() bool {
		return s.Metric("muster_sessions", map[string]string{"pool": s.Pool, "state": "RUNNING"}) == 1
	})
	if !s.HasMicroVM(vmUID) {
		t.Fatalf("leased VM %s is not in flintlock", vmUID)
	}
	waitFor(t, 90*time.Second, "the job running on the session's runner", func() bool {
		r, _ := s.sessionRunner()
		j := s.Job(job.ID)
		return j.Status == "in_progress" && j.RunnerName == r.Name
	})
}

func TestCompletionReleases(t *testing.T) {
	s := newScenario(t, scenarioOpts{PoolSize: 1, DefaultBehavior: fakegithub.Behavior{RunFor: 5 * time.Second}})
	s.StartMusterd()

	job := s.QueueJob()
	waitFor(t, 90*time.Second, "one lease", func() bool { return len(s.Leases()) == 1 })
	vmUID := s.Leases()[0].GetVmUid()
	waitFor(t, 2*time.Minute, "the job completed with success", func() bool {
		j := s.Job(job.ID)
		return j.Status == "completed" && j.Conclusion == "success"
	})
	waitFor(t, 45*time.Second, "the lease released, the VM deleted and the runner gone", func() bool {
		_, runner := s.sessionRunner()
		return len(s.Leases()) == 0 && !s.HasMicroVM(vmUID) && !runner
	})
	// The stub's exit is the first end signal: the fake's completion waits for the grace.
	if got := s.Metric("muster_sessions_finished_total", map[string]string{"pool": s.Pool, "outcome": "ended", "reason": "exec_exit"}); got != 1 {
		t.Fatalf("muster_sessions_finished_total{outcome=ended,reason=exec_exit} = %v, want 1", got)
	}
	if got := s.Metric("muster_vm_releases_total", map[string]string{"pool": s.Pool, "outcome": "ended"}); got < 1 {
		t.Fatalf("muster_vm_releases_total{outcome=ended} = %v, want at least 1", got)
	}
}
