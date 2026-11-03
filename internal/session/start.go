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
	es   vmsource.ExecSession
	done <-chan execResult
}

func (e *execRun) close() { _ = e.es.Close() }

// start requests the JIT configuration, starts the runner with it on
// stdin, and waits for GitHub to report the runner online or busy.
func (r *Registry) start(ctx context.Context, s *session) (*execRun, Reason, bool) {
	r.setState(s, Starting)
	timeout := r.clock.NewTimer(r.cfg.Defaults.StartTimeout.Duration())
	defer timeout.Stop()

	run, reason, ok := r.launch(ctx, s)
	if !ok {
		return nil, reason, false
	}
	for {
		select {
		case <-ctx.Done():
			run.close()
			return nil, "", false
		case <-timeout.C():
			run.close()
			return nil, ReasonStartTimeout, false
		case res := <-run.done:
			s.log.Warn("runner exited before it came online", "exit_code", res.code, "err", res.err)
			run.close()
			return nil, ReasonExecError, false
		case <-s.notify:
			m := s.drain()
			s.seen = s.seen || m.listed || m.online
			if m.lost {
				run.close()
				return nil, ReasonVMLost, false
			}
			if m.online || m.busy {
				r.mu.Lock()
				s.state = Running
				s.busy = s.busy || m.busy
				r.mu.Unlock()
				s.log.Info("runner online", "busy", m.busy)
				s.repost(m)
				return run, "", true
			}
		}
	}
}

// launch gets the JIT configuration and starts the runner. The encoded
// configuration lives only in this function.
func (r *Registry) launch(ctx context.Context, s *session) (*execRun, Reason, bool) {
	groupID, err := r.runnerGroupID(ctx, s.pool)
	if err != nil {
		s.log.Warn("resolve runner group", "err", err)
		return nil, ReasonJITError, false
	}
	jit, err := r.cfg.GitHub.GenerateJITConfig(ctx, s.pool.scope, github.JITRequest{
		Name:          s.runner,
		Labels:        s.pool.cfg.Labels,
		RunnerGroupID: groupID,
		WorkFolder:    s.pool.cfg.WorkFolder,
	})
	if err != nil {
		s.log.Warn("generate JIT config", "err", err)
		return nil, ReasonJITError, false
	}
	s.runnerID = jit.RunnerID
	encoded := jit.Encoded
	jit = github.JITConfig{}

	es, err := s.vm.Exec(ctx, vmsource.ExecSpec{Cmd: runnerCommand, User: runnerUser, HasStdin: true})
	if err != nil {
		s.log.Warn("start runner", "err", err)
		return nil, ReasonExecError, false
	}
	r.forwardOutput(ctx, s, es)
	_, werr := io.WriteString(es.Stdin(), encoded.Reveal())
	encoded = ""
	cerr := es.Stdin().Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = es.Close()
		s.log.Warn("send runner config", "err", err)
		return nil, ReasonExecError, false
	}
	done := make(chan execResult, 1)
	go func() {
		code, err := es.Wait(ctx)
		done <- execResult{code: code, err: err}
	}()
	return &execRun{es: es, done: done}, "", true
}

// runnerGroupID resolves the pool's runner group once and caches it.
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

// heartbeat keeps the lease alive until ctx ends. A lease that is gone
// is reported to the session as a lost VM.
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
