package fakegithub

import (
	"sort"
	"strings"
	"testing"
	"time"
)

// Behavior controls how a runner behaves after its JIT configuration is
// issued. Zero durations take the defaults set with SetDefaultBehavior, or
// the package defaults: online after 1s, run for 5s, deregister 500ms after
// completion.
type Behavior struct {
	// NeverRegister keeps the runner offline forever.
	NeverRegister bool
	// HoldIdle brings the runner online but never gives it a job.
	HoldIdle bool
	// OnlineAfter is the delay from JIT config to online.
	OnlineAfter time.Duration
	// RunFor is the job duration and the SleepSeconds in the encoded config.
	RunFor time.Duration
	// DeregisterAfter is the delay from completed to removal from the list.
	DeregisterAfter time.Duration
	// ExitCode is encoded in the JIT config; non-zero gives conclusion "failure".
	ExitCode int
}

var packageDefaults = Behavior{OnlineAfter: time.Second, RunFor: 5 * time.Second, DeregisterAfter: 500 * time.Millisecond}

// withDefaults fills zero durations of b from d.
func (b Behavior) withDefaults(d Behavior) Behavior {
	if b.OnlineAfter == 0 {
		b.OnlineAfter = d.OnlineAfter
	}
	if b.RunFor == 0 {
		b.RunFor = d.RunFor
	}
	if b.DeregisterAfter == 0 {
		b.DeregisterAfter = d.DeregisterAfter
	}
	return b
}

// SetDefaultBehavior sets the behavior of runners that no NextRunner call
// covers. Zero durations keep the package defaults.
func (s *Server) SetDefaultBehavior(b Behavior) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.defaults = b.withDefaults(packageDefaults)
}

// NextRunner queues a behavior for the next JIT configuration request.
// Repeated calls are consumed in order.
func (s *Server) NextRunner(b Behavior) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextBehaviors = append(s.nextBehaviors, b)
}

func (s *Server) takeBehaviorLocked() Behavior {
	if len(s.nextBehaviors) == 0 {
		return s.defaults
	}
	b := s.nextBehaviors[0]
	s.nextBehaviors = s.nextBehaviors[1:]
	return b.withDefaults(s.defaults)
}

// WaitFor polls cond every 5ms of wall-clock time until it returns true or
// timeout passes, then fails the test.
func (s *Server) WaitFor(tb testing.TB, timeout time.Duration, cond func() bool) {
	tb.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			tb.Fatalf("fakegithub: WaitFor: condition not met within %s", timeout)
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// wake tells the scheduler that state with a deadline changed.
func (s *Server) wake() {
	select {
	case s.wakeCh <- struct{}{}:
	default:
	}
}

// run is the scheduler goroutine. It sleeps on the clock until the earliest
// pending deadline or a wake, then applies every due transition.
func (s *Server) run() {
	defer s.wg.Done()
	for {
		s.mu.Lock()
		deadline, pending := s.earliestLocked()
		wait := deadline.Sub(s.clock.Now())
		s.mu.Unlock()

		var timerC <-chan time.Time
		var stop func() bool
		if pending {
			t := s.clock.NewTimer(wait)
			timerC, stop = t.C(), t.Stop
			// The clock may have moved between reading Now and arming the
			// timer. Re-check so a deadline that is already due is not lost.
			if !s.clock.Now().Before(deadline) {
				stop()
				s.step()
				continue
			}
		}
		select {
		case <-s.done:
			if stop != nil {
				stop()
			}
			return
		case <-s.wakeCh:
		case <-timerC:
		}
		if stop != nil {
			stop()
		}
		s.step()
	}
}

func (s *Server) earliestLocked() (time.Time, bool) {
	var earliest time.Time
	found := false
	consider := func(t time.Time) {
		if !t.IsZero() && (!found || t.Before(earliest)) {
			earliest, found = t, true
		}
	}
	for _, r := range s.runners {
		for _, t := range allTransitions {
			consider(r.deadline(t))
		}
	}
	return earliest, found
}

type pendingDelivery struct {
	action string
	job    jobState
}

// step applies every due transition and pairing under the mutex, then
// delivers the resulting webhooks in order outside it.
func (s *Server) step() {
	s.mu.Lock()
	out := s.advanceLocked()
	s.mu.Unlock()
	for _, d := range out {
		s.deliver(d.action, d.job)
	}
}

// transition is one step of the runner lifecycle.
type transition int

const (
	transitionOnline transition = iota
	transitionComplete
	transitionDeregister
)

// deadline returns when the transition t is due for r, zero when not pending.
func (r *runnerState) deadline(t transition) time.Time {
	switch t {
	case transitionOnline:
		return r.onlineAt
	case transitionComplete:
		return r.completeAt
	default:
		return r.deregisterAt
	}
}

var allTransitions = []transition{transitionOnline, transitionComplete, transitionDeregister}

type dueEvent struct {
	at     time.Time
	runner *runnerState
	kind   transition
}

// advanceLocked applies every transition whose deadline has passed, each at
// its own deadline and in deadline order, pairing runners with jobs after
// each one. A clock jump past several deadlines therefore produces the same
// sequence as advancing to each deadline in turn.
func (s *Server) advanceLocked() []pendingDelivery {
	now := s.clock.Now()
	var out []pendingDelivery
	for {
		ev, ok := s.nextDueLocked(now)
		if !ok {
			break
		}
		out = append(out, s.applyLocked(ev)...)
	}
	return append(out, s.pairLocked(now)...)
}

// nextDueLocked returns the earliest pending transition at or before now.
func (s *Server) nextDueLocked(now time.Time) (dueEvent, bool) {
	var best dueEvent
	found := false
	for _, r := range s.runners {
		for _, kind := range allTransitions {
			at := r.deadline(kind)
			if at.IsZero() || at.After(now) {
				continue
			}
			ev := dueEvent{at: at, runner: r, kind: kind}
			if !found || ev.at.Before(best.at) || (ev.at.Equal(best.at) && ev.runner.ID < best.runner.ID) {
				best, found = ev, true
			}
		}
	}
	return best, found
}

// applyLocked performs one transition at its deadline and pairs afterwards.
func (s *Server) applyLocked(ev dueEvent) []pendingDelivery {
	r, at := ev.runner, ev.at
	var out []pendingDelivery
	switch ev.kind {
	case transitionOnline:
		r.onlineAt = time.Time{}
		r.Status = statusOnline
	case transitionComplete:
		r.completeAt = time.Time{}
		if js, ok := s.jobs[r.JobID]; ok {
			js.Status = jobCompleted
			js.completedAt = at
			js.Conclusion = "success"
			if r.behavior.ExitCode != 0 {
				js.Conclusion = "failure"
			}
			out = append(out, pendingDelivery{action: jobCompleted, job: *js})
		}
		r.Busy, r.JobID, r.Finished = false, 0, true
		r.deregisterAt = at.Add(r.behavior.DeregisterAfter)
	case transitionDeregister:
		delete(s.runners, r.ID)
	}
	return append(out, s.pairLocked(at)...)
}

// pairLocked gives every eligible online runner, in id order, the oldest
// announced queued job its scope and labels cover.
func (s *Server) pairLocked(now time.Time) []pendingDelivery {
	runners := make([]*runnerState, 0, len(s.runners))
	for _, r := range s.runners {
		if r.Status == statusOnline && !r.Busy && !r.Finished && !r.behavior.HoldIdle {
			runners = append(runners, r)
		}
	}
	sort.Slice(runners, func(i, j int) bool { return runners[i].ID < runners[j].ID })
	jobs := make([]*jobState, 0, len(s.jobs))
	for _, j := range s.jobs {
		if j.Status == jobQueued && j.announced {
			jobs = append(jobs, j)
		}
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].ID < jobs[j].ID })

	var out []pendingDelivery
	for _, r := range runners {
		for i, j := range jobs {
			if !covers(r, j) {
				continue
			}
			j.Status = jobInProgress
			j.startedAt = now
			j.RunnerID, j.RunnerName, j.runnerGroupID = r.ID, r.Name, r.RunnerGroupID
			r.Busy, r.JobID = true, j.ID
			r.completeAt = now.Add(r.behavior.RunFor)
			out = append(out, pendingDelivery{action: jobInProgress, job: *j})
			jobs = append(jobs[:i], jobs[i+1:]...)
			break
		}
	}
	return out
}

// covers reports whether runner r may run job j: its scope covers the
// job's repository and its labels are a superset of the job's labels.
func covers(r *runnerState, j *jobState) bool {
	owner, _, _ := strings.Cut(j.Repo, "/")
	if r.Scope.Org != "" && r.Scope.Org != owner {
		return false
	}
	if r.Scope.Repo != "" && r.Scope.Repo != j.Repo {
		return false
	}
	for _, want := range j.Labels {
		found := false
		for _, have := range r.Labels {
			if have == want {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
