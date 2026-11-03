package flintlock

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	mvmexecv1 "github.com/liquidmetal-dev/flintlock/api/services/microvmexec/v1alpha1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/liquidmetal-dev/muster/internal/vmsource"
	"github.com/liquidmetal-dev/muster/internal/vmsource/flintlock/fakeexec"
)

func exitWith(code int32) fakeexec.Handler {
	return func(context.Context, *mvmexecv1.ExecStart, io.Reader, io.Writer, io.Writer) fakeexec.Result {
		return fakeexec.Result{ExitCode: code}
	}
}

type output struct {
	stdout, stderr string
	code           int
	err            error
}

// drain reads both streams concurrently, then waits.
func drain(t *testing.T, s vmsource.ExecSession) output {
	t.Helper()
	var out, errOut bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(&out, s.Stdout()) }()
	go func() { defer wg.Done(); _, _ = io.Copy(&errOut, s.Stderr()) }()
	wg.Wait()
	code, err := s.Wait(t.Context())
	return output{stdout: out.String(), stderr: errOut.String(), code: code, err: err}
}

func execOK(t *testing.T, e *Execer, spec vmsource.ExecSpec) vmsource.ExecSession {
	t.Helper()
	s, err := e.Exec(t.Context(), hostA, "vm-1", spec)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestExecStdinReachesFake(t *testing.T) {
	fake := fakeexec.New()
	fake.AddVM("vm-1", func(_ context.Context, _ *mvmexecv1.ExecStart, stdin io.Reader, _, _ io.Writer) fakeexec.Result {
		_, _ = io.Copy(io.Discard, stdin)
		return fakeexec.Result{}
	})
	e := newTestExecer(t, fake)
	s := execOK(t, e, vmsource.ExecSpec{Cmd: "cat", HasStdin: true})

	want := bytes.Repeat([]byte("0123456789abcdef"), 200<<10/16)
	if n, err := s.Stdin().Write(want); err != nil || n != len(want) {
		t.Fatalf("Write = %d, %v; want %d, nil", n, err, len(want))
	}
	if err := s.Stdin().Close(); err != nil {
		t.Fatalf("Stdin Close: %v", err)
	}
	if o := drain(t, s); o.err != nil || o.code != 0 {
		t.Fatalf("Wait = %d, %v; want 0, nil", o.code, o.err)
	}
	calls := fake.Calls()
	if len(calls) != 1 || !bytes.Equal(calls[0].Stdin, want) || !calls[0].StdinEOF {
		t.Fatalf("fake got %d bytes, eof=%v; want %d bytes and eof", len(calls[0].Stdin), calls[0].StdinEOF, len(want))
	}
	for i, n := range calls[0].StdinFrames {
		if n > stdinChunk {
			t.Fatalf("stdin frame %d is %d bytes, want at most %d", i, n, stdinChunk)
		}
	}
	if n := len(calls[0].StdinFrames); n < 4 {
		t.Fatalf("stdin frames = %d, want at least 4 for 200 KiB", n)
	}
}

func TestExecStreamsOutputAndExitCode(t *testing.T) {
	fake := fakeexec.New()
	fake.AddVM("vm-1", func(_ context.Context, _ *mvmexecv1.ExecStart, _ io.Reader, stdout, stderr io.Writer) fakeexec.Result {
		_, _ = io.WriteString(stdout, "out")
		_, _ = io.WriteString(stderr, "err")
		return fakeexec.Result{ExitCode: 7}
	})
	o := drain(t, execOK(t, newTestExecer(t, fake), vmsource.ExecSpec{Cmd: "x"}))
	if o.stdout != "out" || o.stderr != "err" || o.code != 7 || o.err != nil {
		t.Fatalf("got %+v; want out, err, 7, nil", o)
	}
}

func TestExecMapsSpec(t *testing.T) {
	fake := fakeexec.New()
	fake.AddVM("vm-1", exitWith(0))
	e := newTestExecer(t, fake)
	s := execOK(t, e, vmsource.ExecSpec{
		Cmd:      "/usr/local/bin/muster-runner",
		Args:     []string{"--a", "b"},
		Cwd:      "/home/runner",
		Env:      []string{"A=1", "B=x=y"},
		User:     "runner",
		Timeout:  1500 * time.Millisecond,
		HasStdin: true,
	})
	_ = s.Stdin().Close()
	drain(t, s)

	got := fake.Calls()[0].Start
	if got.GetUid() != "vm-1" || got.GetCmd() != "/usr/local/bin/muster-runner" || got.GetCwd() != "/home/runner" ||
		got.GetUser() != "runner" || !got.GetHasStdin() || got.GetShell() || got.GetTimeoutSeconds() != 2 {
		t.Fatalf("start = %v", got)
	}
	if a := got.GetArgs(); len(a) != 2 || a[0] != "--a" || a[1] != "b" {
		t.Fatalf("args = %v", a)
	}
	if env := got.GetEnv(); len(env) != 2 || env["A"] != "1" || env["B"] != "x=y" {
		t.Fatalf("env = %v", env)
	}
}

func TestTimeoutSeconds(t *testing.T) {
	tests := []struct {
		in      time.Duration
		want    int32
		wantErr bool
	}{
		{in: 0, want: 0},
		{in: time.Nanosecond, want: 1},
		{in: 1500 * time.Millisecond, want: 2},
		{in: 3 * time.Second, want: 3},
		{in: -time.Second, wantErr: true},
		{in: (math.MaxInt32 + 1) * time.Second, wantErr: true},
	}
	for _, tt := range tests {
		got, err := timeoutSeconds(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Fatalf("timeoutSeconds(%v) = %d, %v; want %d, err=%v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestExecRejectsBadEnv(t *testing.T) {
	for _, env := range [][]string{{"NOEQ"}, {"=v"}, {"A=1", "A=2"}} {
		fake := fakeexec.New()
		fake.AddVM("vm-1", exitWith(0))
		e := newTestExecer(t, fake)
		if _, err := e.Exec(t.Context(), hostA, "vm-1", vmsource.ExecSpec{Cmd: "x", Env: env}); err == nil {
			t.Fatalf("Exec with env %q succeeded, want an error", env)
		}
		if fake.ServerInfoCount() != 0 || len(fake.Calls()) != 0 || len(e.conns) != 0 {
			t.Fatalf("env %q: the Execer reached the host before validating", env)
		}
	}
}

func TestExecErrorFrame(t *testing.T) {
	fake := fakeexec.New()
	fake.AddVM("vm-1", func(context.Context, *mvmexecv1.ExecStart, io.Reader, io.Writer, io.Writer) fakeexec.Result {
		return fakeexec.Result{ErrorFrame: "boom", ExitCode: 1}
	})
	o := drain(t, execOK(t, newTestExecer(t, fake), vmsource.ExecSpec{Cmd: "x"}))
	if o.code != 1 || o.err == nil || !strings.Contains(o.err.Error(), "boom") {
		t.Fatalf("Wait = %d, %v; want 1 and an error with boom", o.code, o.err)
	}
}

func TestExecEmptyErrorFrame(t *testing.T) {
	fake := fakeexec.New()
	fake.AddVM("vm-1", func(context.Context, *mvmexecv1.ExecStart, io.Reader, io.Writer, io.Writer) fakeexec.Result {
		return fakeexec.Result{SendErrorFrame: true, ExitCode: 1}
	})
	o := drain(t, execOK(t, newTestExecer(t, fake), vmsource.ExecSpec{Cmd: "x"}))
	if o.code != 1 || o.err == nil {
		t.Fatalf("Wait = %d, %v; want 1 and an error", o.code, o.err)
	}
}

func TestExecAbortWithoutExitCode(t *testing.T) {
	fake := fakeexec.New()
	fake.AddVM("vm-1", func(context.Context, *mvmexecv1.ExecStart, io.Reader, io.Writer, io.Writer) fakeexec.Result {
		return fakeexec.Result{Abort: status.Error(codes.Unknown, "relay")}
	})
	o := drain(t, execOK(t, newTestExecer(t, fake), vmsource.ExecSpec{Cmd: "x"}))
	if o.code != -1 || status.Code(o.err) != codes.Unknown {
		t.Fatalf("Wait = %d, %v; want -1 and code Unknown", o.code, o.err)
	}
}

func TestExecAfterCloseDoesNotDial(t *testing.T) {
	fake := fakeexec.New()
	fake.AddVM("vm-1", exitWith(0))
	e := newTestExecer(t, fake)
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := e.Exec(t.Context(), hostA, "vm-1", vmsource.ExecSpec{Cmd: "x"}); err == nil {
		t.Fatalf("Exec after Close succeeded, want an error")
	}
	e.mu.Lock()
	n := len(e.conns)
	e.mu.Unlock()
	if n != 0 || fake.ServerInfoCount() != 0 {
		t.Fatalf("after Close: %d connections, %d ServerInfo calls; want 0 and 0", n, fake.ServerInfoCount())
	}
}

func TestExecUnavailableCodes(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*fakeexec.Server)
	}{
		{name: "not ready", setup: func(s *fakeexec.Server) { s.SetNotReady("vm-1") }},
		{name: "unimplemented", setup: func(s *fakeexec.Server) { s.FailNext("ExecCommand", codes.Unimplemented) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := fakeexec.New()
			fake.AddVM("vm-1", exitWith(0))
			tt.setup(fake)
			s, err := newTestExecer(t, fake).Exec(t.Context(), hostA, "vm-1", vmsource.ExecSpec{Cmd: "x"})
			if err == nil {
				err = drain(t, s).err
			}
			if !errors.Is(err, vmsource.ErrExecUnavailable) {
				t.Fatalf("err = %v, want ErrExecUnavailable", err)
			}
		})
	}
}

func TestExecLazyCheck(t *testing.T) {
	fake := fakeexec.New()
	fake.AddVM("vm-1", exitWith(0))
	fake.SetServerInfo("v0.15.2", false)
	e := newTestExecer(t, fake)

	if _, err := e.Exec(t.Context(), hostA, "vm-1", vmsource.ExecSpec{Cmd: "x"}); !errors.Is(err, ErrExecDisabled) {
		t.Fatalf("Exec on a disabled host = %v, want ErrExecDisabled", err)
	}
	if n := len(fake.Calls()); n != 0 {
		t.Fatalf("calls = %d, want 0", n)
	}

	fake.SetServerInfo("v0.15.2", true)
	drain(t, execOK(t, e, vmsource.ExecSpec{Cmd: "x"}))
	if n := fake.ServerInfoCount(); n != 2 {
		t.Fatalf("ServerInfoCount = %d, want 2 (a failure is not kept)", n)
	}
	drain(t, execOK(t, e, vmsource.ExecSpec{Cmd: "x"}))
	if n := fake.ServerInfoCount(); n != 2 {
		t.Fatalf("ServerInfoCount = %d, want 2 (a pass is kept)", n)
	}
}

func TestExecCheckUnreachableThenRecovers(t *testing.T) {
	fake := fakeexec.New()
	fake.AddVM("vm-1", exitWith(0))
	fake.FailNext("ServerInfo", codes.Unavailable)
	e := newTestExecer(t, fake)
	if _, err := e.Exec(t.Context(), hostA, "vm-1", vmsource.ExecSpec{Cmd: "x"}); status.Code(err) != codes.Unavailable {
		t.Fatalf("Exec = %v, want code Unavailable", err)
	}
	drain(t, execOK(t, e, vmsource.ExecSpec{Cmd: "x"}))
}

func TestConcurrentFirstExecSharesCheck(t *testing.T) {
	fake := fakeexec.New()
	fake.AddVM("vm-1", exitWith(0))
	release := fake.HoldServerInfo()
	e := newTestExecer(t, fake)
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := e.Exec(t.Context(), hostA, "vm-1", vmsource.ExecSpec{Cmd: "x"})
			if err != nil {
				errs <- err
				return
			}
			defer func() { _ = s.Close() }()
			_, _ = io.Copy(io.Discard, s.Stdout())
			_, _ = io.Copy(io.Discard, s.Stderr())
			if _, err := s.Wait(t.Context()); err != nil {
				errs <- err
			}
		}()
	}
	// While the first ServerInfo is held, every other caller is either
	// waiting on it or would have sent its own call by now.
	time.Sleep(200 * time.Millisecond)
	if n := fake.ServerInfoCount(); n != 1 {
		release()
		t.Fatalf("ServerInfoCount while held = %d, want 1", n)
	}
	release()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("Exec: %v", err)
	}
	if n := fake.ServerInfoCount(); n != 1 {
		t.Fatalf("ServerInfoCount = %d, want 1", n)
	}
}

func TestCancelledCallerDoesNotFailSharedCheck(t *testing.T) {
	fake := fakeexec.New()
	fake.AddVM("vm-1", exitWith(0))
	release := fake.HoldServerInfo()
	e := newTestExecer(t, fake)

	first, cancelFirst := context.WithCancel(t.Context())
	firstErr := make(chan error, 1)
	go func() {
		_, err := e.Exec(first, hostA, "vm-1", vmsource.ExecSpec{Cmd: "x"})
		firstErr <- err
	}()
	time.Sleep(100 * time.Millisecond)
	second := make(chan error, 1)
	go func() {
		s, err := e.Exec(t.Context(), hostA, "vm-1", vmsource.ExecSpec{Cmd: "x"})
		if err == nil {
			err = drain(t, s).err
			_ = s.Close()
		}
		second <- err
	}()
	time.Sleep(100 * time.Millisecond)
	cancelFirst()
	if err := <-firstErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("first Exec = %v, want context.Canceled", err)
	}
	release()
	if err := <-second; err != nil {
		t.Fatalf("second Exec = %v, want nil", err)
	}
	if n := fake.ServerInfoCount(); n != 1 {
		t.Fatalf("ServerInfoCount = %d, want 1", n)
	}
}

func TestExecCancelUnblocksReaders(t *testing.T) {
	fake := fakeexec.New()
	fake.AddVM("vm-1", func(ctx context.Context, _ *mvmexecv1.ExecStart, _ io.Reader, _, _ io.Writer) fakeexec.Result {
		<-ctx.Done()
		return fakeexec.Result{}
	})
	e := newTestExecer(t, fake)
	ctx, cancel := context.WithCancel(t.Context())
	s, err := e.Exec(ctx, hostA, "vm-1", vmsource.ExecSpec{Cmd: "x"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	readErr := make(chan error, 1)
	go func() {
		_, err := s.Stdout().Read(make([]byte, 1))
		readErr <- err
	}()
	cancel()
	select {
	case err := <-readErr:
		if err == nil {
			t.Fatal("Read after cancel returned nil error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Read still blocked 2s after cancel")
	}
	if _, err := s.Wait(t.Context()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait = %v, want context.Canceled", err)
	}
}

func TestCloseUnblocksUndrainedSession(t *testing.T) {
	fake := fakeexec.New()
	fake.AddVM("vm-1", func(_ context.Context, _ *mvmexecv1.ExecStart, _ io.Reader, _, stderr io.Writer) fakeexec.Result {
		chunk := make([]byte, 32<<10)
		for range 32 {
			if _, err := stderr.Write(chunk); err != nil {
				break
			}
		}
		return fakeexec.Result{}
	})
	s := execOK(t, newTestExecer(t, fake), vmsource.ExecSpec{Cmd: "x"})
	time.Sleep(50 * time.Millisecond)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	done := make(chan error, 1)
	go func() { _, err := s.Wait(context.Background()); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrSessionClosed) {
			t.Fatalf("Wait = %v, want ErrSessionClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait still blocked 2s after Close")
	}
}

func TestStdinWriteAfterClose(t *testing.T) {
	fake := fakeexec.New()
	fake.AddVM("vm-1", func(_ context.Context, _ *mvmexecv1.ExecStart, stdin io.Reader, _, _ io.Writer) fakeexec.Result {
		_, _ = io.Copy(io.Discard, stdin)
		return fakeexec.Result{}
	})
	fake.AddVM("vm-2", exitWith(0))
	e := newTestExecer(t, fake)

	s := execOK(t, e, vmsource.ExecSpec{Cmd: "x", HasStdin: true})
	if err := s.Stdin().Close(); err != nil {
		t.Fatalf("Stdin Close: %v", err)
	}
	if _, err := s.Stdin().Write([]byte("late")); err == nil {
		t.Fatal("Write after Stdin Close succeeded")
	}
	drain(t, s)

	ended, err := e.Exec(t.Context(), hostA, "vm-2", vmsource.ExecSpec{Cmd: "x", HasStdin: true})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	drain(t, ended)
	if _, err := ended.Stdin().Write([]byte("late")); err == nil {
		t.Fatal("Write after the session ended succeeded")
	}

	noStdin := execOK(t, e, vmsource.ExecSpec{Cmd: "x"})
	if _, err := noStdin.Stdin().Write([]byte("x")); err == nil {
		t.Fatal("Write without HasStdin succeeded")
	}
	if err := noStdin.Stdin().Close(); err != nil {
		t.Fatalf("Stdin Close without HasStdin = %v, want nil", err)
	}
	drain(t, noStdin)
}

func TestWaitIsIdempotent(t *testing.T) {
	fake := fakeexec.New()
	fake.AddVM("vm-1", exitWith(3))
	s := execOK(t, newTestExecer(t, fake), vmsource.ExecSpec{Cmd: "x"})
	type res struct {
		code int
		err  error
	}
	results := make(chan res, 2)
	for range 2 {
		go func() { c, err := s.Wait(t.Context()); results <- res{c, err} }()
	}
	_, _ = io.Copy(io.Discard, s.Stdout())
	_, _ = io.Copy(io.Discard, s.Stderr())
	for range 2 {
		if r := <-results; r.code != 3 || r.err != nil {
			t.Fatalf("Wait = %d, %v; want 3, nil", r.code, r.err)
		}
	}
	if c, err := s.Wait(t.Context()); c != 3 || err != nil {
		t.Fatalf("later Wait = %d, %v; want 3, nil", c, err)
	}
}
