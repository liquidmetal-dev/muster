package session

import (
	"bytes"
	"context"
	"io"
	"log/slog"

	"github.com/liquidmetal-dev/muster/internal/vmsource"
)

// maxLogLine bounds one logged line, so a runner that never writes a
// newline cannot grow the buffer without limit.
const maxLogLine = 64 << 10

// forwardOutput drains the runner's stdout and stderr into the debug log.
// It always reads to EOF, so output volume never stalls the runner.
func (r *Registry) forwardOutput(ctx context.Context, s *session, es vmsource.ExecSession) {
	for stream, rd := range map[string]io.Reader{"stdout": es.Stdout(), "stderr": es.Stderr()} {
		go func() {
			if !s.logger().Enabled(ctx, slog.LevelDebug) {
				_, _ = io.Copy(io.Discard, rd)
				return
			}
			w := &lineLogger{s: s, stream: stream}
			_, _ = io.Copy(w, rd)
			w.flush()
		}()
	}
}

// lineLogger reads the session's logger for each line, so a job id added
// while the runner runs reaches its output.
type lineLogger struct {
	s      *session
	stream string
	buf    []byte
}

func (l *lineLogger) Write(p []byte) (int, error) {
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		l.emit(l.buf[:i])
		l.buf = l.buf[i+1:]
	}
	if len(l.buf) >= maxLogLine {
		l.flush()
	}
	return len(p), nil
}

func (l *lineLogger) flush() {
	if len(l.buf) > 0 {
		l.emit(l.buf)
		l.buf = nil
	}
}

func (l *lineLogger) emit(line []byte) {
	l.s.logger().Debug("runner output", "stream", l.stream, "line", string(bytes.TrimSuffix(line, []byte("\r"))))
}
