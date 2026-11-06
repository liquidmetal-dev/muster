//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"syscall"
	"testing"
	"time"

	"github.com/liquidmetal-dev/flintlock/api/types"

	"github.com/liquidmetal-dev/muster/internal/fakegithub"
)

func TestDrainStopsNewSessions(t *testing.T) {
	s := newScenario(t, scenarioOpts{PoolSize: 2, MaxConcurrent: 2, ShutdownGrace: 3 * time.Minute})
	s.GH.NextRunner(fakegithub.Behavior{RunFor: 20 * time.Second})
	s.GH.NextRunner(fakegithub.Behavior{RunFor: 90 * time.Second})
	s.StartMusterd()
	waitFor(t, 3*time.Minute, "two warm VMs in the pool", func() bool {
		n := 0
		for _, vm := range s.MicroVMs() {
			if vm.GetStatus().GetState() == types.MicroVMStatus_CREATED {
				n++
			}
		}
		return n == 2
	})

	a, c := s.QueueJob(), s.QueueJob()
	waitFor(t, 2*time.Minute, "both jobs in progress on two leases", func() bool {
		return s.Job(a.ID).Status == jobInProgress && s.Job(c.ID).Status == jobInProgress && len(s.Leases()) == 2
	})
	first := map[string]bool{}
	for _, l := range s.Leases() {
		first[l.GetLeaseId()] = true
	}

	// max_concurrent is 2, so B is demand that waits for capacity.
	b := s.QueueJob()
	waitFor(t, 30*time.Second, "musterd took B's queued delivery", func() bool {
		for _, d := range s.GH.Deliveries() {
			if d.JobID == b.ID && d.Action == "queued" && d.Status/100 == 2 {
				return true
			}
		}
		return false
	})

	if err := s.Musterd.Stop(syscall.SIGTERM, 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "musterd not ready", func() bool { return s.ReadyStatus() == http.StatusServiceUnavailable })

	// The runners take A and C in either order; the first to finish frees capacity while the
	// other keeps musterd in its shutdown grace.
	done := func(j fakegithub.Job) bool { return s.Job(j.ID).Status == jobCompleted }
	waitFor(t, 2*time.Minute, "one of A and C completed", func() bool { return done(a) || done(c) })
	// Claims retried while battery readied the warm VMs, so count from here.
	attempts := map[string]string{"pool": s.Pool}
	base := s.Metric("muster_claim_attempts_total", attempts)
	holds(t, 20*time.Second, "no claim for B", func() error {
		if n := s.Metric("muster_claim_attempts_total", attempts); n != base {
			return fmt.Errorf("claim attempts = %v, want %v", n, base)
		}
		for _, l := range s.Leases() {
			if !first[l.GetLeaseId()] {
				return fmt.Errorf("new lease %s", l.GetLeaseId())
			}
		}
		if st := s.Job(b.ID).Status; st != jobQueued {
			return fmt.Errorf("job B is %s", st)
		}
		return nil
	})

	waitFor(t, 3*time.Minute, "A and C completed", func() bool { return done(a) && done(c) })
	code, err := s.Musterd.Wait(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("musterd exit code = %d, want 0", code)
	}
	waitFor(t, time.Minute, "no leases", func() bool { return len(s.Leases()) == 0 })
	if j := s.Job(b.ID); j.Status != jobQueued {
		t.Fatalf("job B status = %s, want queued", j.Status)
	}
}
