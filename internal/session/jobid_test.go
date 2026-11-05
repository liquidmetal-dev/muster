package session_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	mvmexecv1 "github.com/liquidmetal-dev/flintlock/api/services/microvmexec/v1alpha1"
	"google.golang.org/grpc/codes"

	"github.com/liquidmetal-dev/muster/internal/session"
	"github.com/liquidmetal-dev/muster/internal/vmsource/flintlock/fakeexec"
)

func wantJobID(t *testing.T, rec map[string]any, want float64) {
	t.Helper()
	if rec["job_id"] != want {
		t.Fatalf("job_id = %v, want %v in %v", rec["job_id"], want, rec)
	}
}

func TestJobStartedAddsJobIDToLogs(t *testing.T) {
	e := newEnv(t)
	c := e.capture()
	e.start()
	_, runner, _ := e.running(c)
	e.Registry.JobStarted(runner, 4242)
	wantJobID(t, e.logRecord("runner busy"), 4242)
}

func TestJobStartedBeforeOnline(t *testing.T) {
	e := newEnv(t)
	c := e.capture()
	e.start()
	id := e.create(testPool)
	c.waitStdin(t)
	info, _ := e.info(id)
	e.Registry.JobStarted(info.RunnerName, 77)
	e.waitState(id, session.Running)
	wantJobID(t, e.logRecord("runner online"), 77)
}

func TestRunnerOutputGetsJobID(t *testing.T) {
	e := newEnv(t)
	after := make(chan struct{})
	e.setHandler(func(ctx context.Context, _ *mvmexecv1.ExecStart, stdin io.Reader, stdout, _ io.Writer) fakeexec.Result {
		_, _ = io.Copy(io.Discard, stdin)
		_, _ = io.WriteString(stdout, "before\n")
		select {
		case <-after:
			_, _ = io.WriteString(stdout, "after\n")
		case <-ctx.Done():
		}
		<-ctx.Done()
		return fakeexec.Result{}
	})
	e.start()
	id := e.create(testPool)
	e.waitLog(`"line":"before"`)
	info, _ := e.info(id)
	e.Registry.JobStarted(info.RunnerName, 4242)
	e.waitState(id, session.Running)
	close(after)
	var rec map[string]any
	waitFor(t, func() bool {
		rec = e.logLine(`"line":"after"`)
		return rec != nil
	})
	wantJobID(t, rec, 4242)
}

func TestJobStartedDuringHeartbeat(t *testing.T) {
	e := newEnv(t)
	c := e.capture()
	e.start()
	_, runner, _ := e.running(c)
	for i := range 20 {
		e.Pool.FailNext("Heartbeat", codes.Unavailable)
		e.Registry.JobStarted(runner, int64(i+1))
		e.Clock.Advance(time.Second)
	}
	e.waitLog(`"msg":"heartbeat"`)
	if !strings.Contains(e.Logs.String(), `"job_id":`) {
		t.Fatal("no line has job_id")
	}
}
