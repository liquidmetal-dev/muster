package flintlock

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	mvmexecv1 "github.com/liquidmetal-dev/flintlock/api/services/microvmexec/v1alpha1"

	"github.com/liquidmetal-dev/muster/internal/vmsource"
)

// ErrSessionClosed is the result of a session that Close ended.
var ErrSessionClosed = errors.New("flintlock: exec session closed")

const stdinChunk = 64 << 10

var (
	errNoStdin     = errors.New("flintlock: exec started without stdin")
	errStdinClosed = errors.New("flintlock: stdin is closed")
	errEnded       = errors.New("flintlock: exec has ended")
)

// session is one running command. A single goroutine receives frames and
// writes them to the stdout and stderr pipes, so a caller that stops
// reading one stream also stops the other; the caller must drain both or
// call Close.
type session struct {
	parent context.Context
	cancel context.CancelFunc
	stream mvmexecv1.MicroVMExec_ExecCommandClient
	stdin  *stdinWriter

	stdoutR, stderrR *io.PipeReader
	stdoutW, stderrW *io.PipeWriter

	mu       sync.Mutex
	closed   bool
	finished bool
	done     chan struct{}
	code     int
	err      error
}

var _ vmsource.ExecSession = (*session)(nil)

func newSession(parent context.Context, cancel context.CancelFunc, stream mvmexecv1.MicroVMExec_ExecCommandClient, hasStdin bool) *session {
	s := &session{parent: parent, cancel: cancel, stream: stream, done: make(chan struct{})}
	s.stdoutR, s.stdoutW = io.Pipe()
	s.stderrR, s.stderrW = io.Pipe()
	s.stdin = &stdinWriter{s: s, enabled: hasStdin}
	go s.receive()
	go s.watch()
	return s
}

func (s *session) Stdin() io.WriteCloser { return s.stdin }
func (s *session) Stdout() io.Reader     { return s.stdoutR }
func (s *session) Stderr() io.Reader     { return s.stderrR }

// Wait returns the exit code. A command that ended without one returns -1.
func (s *session) Wait(ctx context.Context) (int, error) {
	select {
	case <-s.done:
		return s.code, s.err
	case <-ctx.Done():
		return -1, ctx.Err()
	}
}

// Close ends the stream and releases a receiver that is blocked on an
// unread pipe.
func (s *session) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.cancel()
	s.closeReaders(ErrSessionClosed)
	return nil
}

// watch releases the receiver when the caller's context ends before the
// command does. After a normal end the readers stay open so that the
// caller can read to EOF.
func (s *session) watch() {
	select {
	case <-s.done:
		return
	case <-s.parent.Done():
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return
	}
	s.closeReaders(fmt.Errorf("flintlock: exec: %w", s.parent.Err()))
}

func (s *session) receive() {
	var errFrame string
	gotErrFrame := false
	for {
		resp, err := s.stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			if gotErrFrame {
				s.finish(-1, fmt.Errorf("flintlock: exec error %q, then ended without exit code: %w", errFrame, wrapStatus(err)))
				return
			}
			s.finish(-1, fmt.Errorf("flintlock: exec ended without exit code: %w", wrapStatus(err)))
			return
		}
		switch p := resp.GetPayload().(type) {
		case *mvmexecv1.ExecCommandResponse_Stdout:
			if _, err := s.stdoutW.Write(p.Stdout); err != nil {
				s.finish(-1, err)
				return
			}
		case *mvmexecv1.ExecCommandResponse_Stderr:
			if _, err := s.stderrW.Write(p.Stderr); err != nil {
				s.finish(-1, err)
				return
			}
		case *mvmexecv1.ExecCommandResponse_Error:
			errFrame, gotErrFrame = p.Error, true
		case *mvmexecv1.ExecCommandResponse_ExitCode:
			var err error
			if gotErrFrame {
				err = fmt.Errorf("flintlock: exec error: %s", errFrame)
			}
			s.finish(int(p.ExitCode), err)
			return
		}
	}
}

// finish records the result once. A session ended by Close or by the
// caller's context reports that cause, not the stream error it led to.
func (s *session) finish(code int, err error) {
	s.mu.Lock()
	switch {
	case s.closed:
		code, err = -1, ErrSessionClosed
	case s.parent.Err() != nil && err != nil:
		code, err = -1, fmt.Errorf("flintlock: exec: %w", s.parent.Err())
	}
	s.code, s.err = code, err
	s.finished = true
	s.mu.Unlock()

	// A nil err closes the writers so that readers see EOF.
	_ = s.stdoutW.CloseWithError(err)
	_ = s.stderrW.CloseWithError(err)
	close(s.done)
	s.cancel()
}

func (s *session) closeReaders(err error) {
	_ = s.stdoutR.CloseWithError(err)
	_ = s.stderrR.CloseWithError(err)
}

type stdinWriter struct {
	s       *session
	enabled bool

	mu     sync.Mutex
	closed bool
}

// Write sends p in frames of at most stdinChunk bytes.
func (w *stdinWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch {
	case !w.enabled:
		return 0, errNoStdin
	case w.closed:
		return 0, errStdinClosed
	}
	select {
	case <-w.s.done:
		return 0, errEnded
	default:
	}
	n := 0
	for len(p) > 0 {
		chunk := p[:min(len(p), stdinChunk)]
		req := &mvmexecv1.ExecCommandRequest{Payload: &mvmexecv1.ExecCommandRequest_Stdin{Stdin: chunk}}
		if err := w.s.stream.Send(req); err != nil {
			if errors.Is(err, io.EOF) {
				return n, errEnded
			}
			return n, fmt.Errorf("flintlock: stdin: %w", err)
		}
		n += len(chunk)
		p = p[len(chunk):]
	}
	return n, nil
}

// Close sends EOF once. It does nothing for a command without stdin.
func (w *stdinWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.enabled || w.closed {
		return nil
	}
	w.closed = true
	err := w.s.stream.Send(&mvmexecv1.ExecCommandRequest{Payload: &mvmexecv1.ExecCommandRequest_StdinEof{StdinEof: true}})
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("flintlock: stdin: %w", err)
	}
	return w.s.stream.CloseSend()
}
