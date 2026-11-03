package session

import "strconv"

// State is the lifecycle state of a session.
type State int

// Session states.
const (
	Pending State = iota
	Claiming
	Starting
	Running
	Ended
	Failed
	Released
)

var stateNames = [...]string{"PENDING", "CLAIMING", "STARTING", "RUNNING", "ENDED", "FAILED", "RELEASED"}

func (s State) String() string {
	if s >= 0 && int(s) < len(stateNames) {
		return stateNames[s]
	}
	return "State(" + strconv.Itoa(int(s)) + ")"
}

// Active reports whether the session counts toward the pool's active
// sessions: PENDING, CLAIMING, STARTING or RUNNING.
func (s State) Active() bool {
	return s >= Pending && s <= Running
}

// Reason says why a session ended or failed.
type Reason string

// End reasons.
const (
	ReasonExecExit     Reason = "exec_exit"
	ReasonJobCompleted Reason = "job_completed"
	ReasonRunnerAbsent Reason = "runner_absent"
	ReasonIdleTimeout  Reason = "idle_timeout"
	ReasonMaxDuration  Reason = "max_duration"
)

// Failure reasons.
const (
	ReasonNoDemand      Reason = "no_demand"
	ReasonClaimDeadline Reason = "claim_deadline"
	ReasonClaimError    Reason = "claim_error"
	ReasonJITError      Reason = "jit_error"
	ReasonExecError     Reason = "exec_error"
	ReasonStartTimeout  Reason = "start_timeout"
	ReasonVMLost        Reason = "vm_lost"
)

// outcome is the state a session ends in for this reason.
func (r Reason) outcome() State {
	switch r {
	case ReasonExecExit, ReasonJobCompleted, ReasonRunnerAbsent, ReasonIdleTimeout, ReasonMaxDuration:
		return Ended
	default:
		return Failed
	}
}
