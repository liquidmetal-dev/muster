//go:build e2e

package e2e

import (
	"testing"
	"time"

	"github.com/liquidmetal-dev/flintlock/api/types"

	"github.com/liquidmetal-dev/muster/internal/fakegithub"
)

func TestExhaustedBackoffRecovers(t *testing.T) {
	s := newScenario(t, scenarioOpts{PoolSize: 1, MaxConcurrent: 3, DefaultBehavior: fakegithub.Behavior{RunFor: 15 * time.Second}})
	s.StartMusterd()
	waitFor(t, 3*time.Minute, "one warm VM in the pool", func() bool {
		vms := s.MicroVMs()
		return len(vms) == 1 && vms[0].GetStatus().GetState() == types.MicroVMStatus_CREATED
	})

	// The three claims arrive within milliseconds, and a refill needs a VM boot, so at least
	// one claim finds the pool exhausted.
	jobs := []fakegithub.Job{s.QueueJob(), s.QueueJob(), s.QueueJob()}
	waitFor(t, 60*time.Second, "an exhausted claim", func() bool {
		return s.Metric("muster_claim_exhaustions_total", map[string]string{"pool": s.Pool}) >= 1
	})
	waitFor(t, 5*time.Minute, "all three jobs completed with success", func() bool {
		for _, j := range jobs {
			if !s.JobSucceeded(j.ID) {
				return false
			}
		}
		return true
	})
	runners := map[string]bool{}
	for _, j := range jobs {
		runners[s.Job(j.ID).RunnerName] = true
	}
	if len(runners) != 3 {
		t.Fatalf("the jobs ran on %d runners, want 3: %v", len(runners), runners)
	}
	if got := s.Metric("muster_sessions_finished_total", map[string]string{"pool": s.Pool, "outcome": "failed", "reason": "claim_error"}); got != 0 {
		t.Fatalf("muster_sessions_finished_total{outcome=failed,reason=claim_error} = %v, want 0", got)
	}
}
