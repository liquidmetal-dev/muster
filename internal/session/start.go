package session

import (
	"context"
	"errors"
	"io"

	"github.com/liquidmetal-dev/muster/internal/github"
	"github.com/liquidmetal-dev/muster/internal/vmsource"
)

const (
	runnerCommand = "/usr/local/bin/muster-runner"
	runnerUser    = "runner"
)

type execResult struct {
	code int
	err  error
}

// execRun is the runner process of a session that reached RUNNING.
type execRun struct {
	es     vmsource.ExecSession
	done   <-chan execResult
	cancel context.CancelFunc
	// broken is set when the stream ended without an exit code.
	broken bool
}

func (e *execRun) close() {
	_ = e.es.Close()
	e.cancel()
}

type launched struct {
	run    *execRun
	reason Reason
}

// start requests the JIT configuration, starts the runner with it on
// stdin, and waits for GitHub to report the runner online or busy. The
// start timeout covers all of it. It returns the observations that the
// RUNNING phase still needs. A nil run means the session did not start.
func (r *Registry) start(ctx context.Context, s *session) (*execRun, mailbox, Reason) {
	r.setState(s, Starting)
	timeout := r.clock.NewTimer(r.cfg.Defaults.StartTimeout.Duration())
	defer timeout.Stop()
	maxD := r.clock.NewTimer(r.untilMaxDuration(s))
	defer maxD.Stop()
	// expired prefers a due deadline when select also had a result ready.
	expired := func() (Reason, bool) {
		select {
		case <-maxD.C():
			return ReasonMaxDuration, true
		case <-timeout.C():
			return ReasonStartTimeout, true
		default:
			return "", false
		}
	}

	launchCtx, cancelLaunch := context.WithCancel(ctx)
	result := make(chan launched, 1)
	go func() {
		run, reason := r.launch(launchCtx, s)
		result <- launched{run: run, reason: reason}
	}()
	abandon := func() {
		cancelLaunch()
		if l := <-result; l.run != nil {
			l.run.close()
		}
	}
	var run *execRun
	select {
	case <-ctx.Done():
		abandon()
		return nil, mailbox{}, ""
	case <-timeout.C():
		abandon()
		return nil, mailbox{}, ReasonStartTimeout
	case <-maxD.C():
		abandon()
		return nil, mailbox{}, ReasonMaxDuration
	case l := <-result:
		if l.run == nil {
			cancelLaunch()
			if reason, ok := expired(); ok {
				return nil, mailbox{}, reason
			}
			return nil, mailbox{}, l.reason
		}
		run = l.run
		run.cancel = cancelLaunch
		if reason, ok := expired(); ok {
			run.close()
			return nil, mailbox{}, reason
		}
	}

	var carry mailbox
	execDone := run.done
	for {
		select {
		case <-ctx.Done():
			run.close()
			return nil, mailbox{}, ""
		case <-timeout.C():
			run.close()
			return nil, mailbox{}, ReasonStartTimeout
		case <-maxD.C():
			run.close()
			return nil, mailbox{}, ReasonMaxDuration
		case res := <-execDone:
			if res.err != nil {
				s.logBrokenStream(res.err)
				execDone = nil
				run.broken = true
				continue
			}
			s.log.Warn("runner exited before it came online", "exit_code", res.code)
			run.close()
			return nil, mailbox{}, ReasonExecError
		case <-s.notify:
			m := s.drain()
			s.observe(m)
			if m.lost {
				run.close()
				return nil, mailbox{}, ReasonVMLost
			}
			carry.completed = carry.completed || m.completed
			carry.busy = carry.busy || m.busy || m.completed
			if m.online || carry.busy {
				if reason, ok := expired(); ok {
					run.close()
					return nil, mailbox{}, reason
				}
				r.mu.Lock()
				s.state = Running
				s.busy = s.busy || carry.busy
				r.mu.Unlock()
				s.log.Info("runner online", "busy", carry.busy)
				carry.absent = m.absent
				return run, carry, ""
			}
		}
	}
}

// launch gets the JIT configuration and starts the runner. The encoded
// configuration lives only in this function.
func (r *Registry) launch(ctx context.Context, s *session) (*execRun, Reason) {
	groupID, err := r.runnerGroupID(ctx, s.pool)
	if err != nil {
		s.log.Warn("resolve runner group", "err", err)
		return nil, ReasonJITError
	}
	jit, err := r.cfg.GitHub.GenerateJITConfig(ctx, s.pool.scope, github.JITRequest{
		Name:          s.runner,
		Labels:        s.pool.cfg.Labels,
		RunnerGroupID: groupID,
		WorkFolder:    s.pool.cfg.WorkFolder,
	})
	if err != nil {
		s.log.Warn("generate JIT config", "err", err)
		return nil, ReasonJITError
	}
	s.runnerID = jit.RunnerID
	encoded := jit.Encoded
	jit = github.JITConfig{}

	es, err := s.vm.Exec(ctx, vmsource.ExecSpec{Cmd: runnerCommand, User: runnerUser, HasStdin: true})
	if err != nil {
		s.log.Warn("start runner", "err", err)
		return nil, ReasonExecError
	}
	r.forwardOutput(ctx, s, es)
	_, werr := io.WriteString(es.Stdin(), encoded.Reveal())
	encoded = ""
	cerr := es.Stdin().Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = es.Close()
		s.log.Warn("send runner config", "err", err)
		return nil, ReasonExecError
	}
	done := make(chan execResult, 1)
	go func() {
		code, err := es.Wait(ctx)
		done <- execResult{code: code, err: err}
	}()
	return &execRun{es: es, done: done, cancel: func() {}}, ""
}

func (r *Registry) runnerGroupID(ctx context.Context, p *pool) (int64, error) {
	r.mu.Lock()
	id, ok := r.groupIDs[p.cfg.Name]
	r.mu.Unlock()
	if ok {
		return id, nil
	}
	id, err := r.cfg.GitHub.RunnerGroupID(ctx, p.scope, p.cfg.Scope.RunnerGroup)
	if err != nil {
		return 0, err
	}
	r.mu.Lock()
	r.groupIDs[p.cfg.Name] = id
	r.mu.Unlock()
	return id, nil
}

// heartbeat reports a lease that is gone as a lost VM, so the session
// goroutine decides what that means in its current state.
func (r *Registry) heartbeat(ctx context.Context, s *session) {
	interval := s.vm.KeepAliveInterval()
	t := r.clock.NewTimer(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C():
		}
		if err := s.vm.KeepAlive(ctx); err != nil {
			if errors.Is(err, vmsource.ErrLeaseNotFound) {
				s.log.Warn("lease is gone")
				s.post(mailbox{lost: true})
				return
			}
			if ctx.Err() != nil {
				return
			}
			s.log.Warn("heartbeat", "err", err)
		}
		t.Reset(interval)
	}
}

// JobStarted records that GitHub reported a job in progress on the named
// runner. Unknown names are ignored.
func (r *Registry) JobStarted(runnerName string) {
	r.mu.Lock()
	s := r.byRunnerLocked(runnerName)
	if s != nil {
		s.busy = true
	}
	r.mu.Unlock()
	if s != nil {
		s.post(mailbox{busy: true})
	}
}

// JobCompleted records that GitHub reported the job on the named runner
// completed. Unknown names are ignored.
func (r *Registry) JobCompleted(runnerName string) {
	r.mu.Lock()
	s := r.byRunnerLocked(runnerName)
	r.mu.Unlock()
	if s != nil {
		s.post(mailbox{completed: true})
	}
}

func (r *Registry) byRunnerLocked(name string) *session {
	for _, s := range r.sessions {
		if s.runner == name && s.state.Active() {
			return s
		}
	}
	return nil
}
