//go:build e2e

package e2e

import (
	"testing"
	"time"

	"github.com/liquidmetal-dev/muster/internal/fakegithub"
)

func TestRestartAdopts(t *testing.T) {
	s := newScenario(t, scenarioOpts{PoolSize: 1, DefaultBehavior: fakegithub.Behavior{RunFor: 60 * time.Second}})
	s.StartMusterd()

	job := s.QueueJob()
	waitFor(t, 2*time.Minute, "the job in progress", func() bool { return s.Job(job.ID).Status == jobInProgress })
	leases := s.Leases()
	if len(leases) != 1 {
		t.Fatalf("leases = %d, want 1", len(leases))
	}
	requestID, vmUID := leases[0].GetRequestId(), leases[0].GetVmUid()
	runnerName := s.Job(job.ID).RunnerName

	if err := s.Musterd.Kill(); err != nil {
		t.Fatal(err)
	}
	s.RestartMusterd()

	adopted := map[string]string{"action": "adopt", "outcome": "ok"}
	waitFor(t, 30*time.Second, "the session adopted", func() bool {
		return s.Metric("muster_reconcile_decisions_total", adopted) == 1 &&
			s.Metric("muster_sessions", map[string]string{"pool": s.Pool, "state": "RUNNING"}) == 1
	})
	if l := s.Leases(); len(l) != 1 || l[0].GetRequestId() != requestID {
		t.Fatalf("after restart leases = %v, want the one with request id %s", l, requestID)
	}
	if got := s.Metric("muster_reconcile_decisions_total", map[string]string{"action": "release_lease"}); got != 0 {
		t.Fatalf("release_lease decisions = %v, want 0", got)
	}

	waitFor(t, 3*time.Minute, "the job completed, the lease released and the VM deleted", func() bool {
		return s.JobSucceeded(job.ID) && len(s.Leases()) == 0 && !s.HasMicroVM(vmUID)
	})
	if j := s.Job(job.ID); j.RunnerName != runnerName {
		t.Fatalf("job ran on %s after restart, want %s", j.RunnerName, runnerName)
	}
	if n := len(s.GH.Runners()); n > 1 {
		t.Fatalf("the fake has %d runners, want at most the adopted one", n)
	}
}
