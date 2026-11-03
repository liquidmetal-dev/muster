package fakeexec_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	mvmv1 "github.com/liquidmetal-dev/flintlock/api/services/microvm/v1alpha1"
	mvmexecv1 "github.com/liquidmetal-dev/flintlock/api/services/microvmexec/v1alpha1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/liquidmetal-dev/muster/internal/vmsource/flintlock/fakeexec"
)

func dial(t *testing.T, fake *fakeexec.Server) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient("passthrough:///fakeexec",
		grpc.WithTransportCredentials(insecure.NewCredentials()), fake.ServeBufconn(t))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

type frame struct {
	kind string
	data string
	code int32
}

// collect reads frames until the stream ends and returns them with the
// terminating error (nil when the stream ended with io.EOF).
func collect(t *testing.T, stream mvmexecv1.MicroVMExec_ExecCommandClient) ([]frame, error) {
	t.Helper()
	var frames []frame
	for {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return frames, nil
		}
		if err != nil {
			return frames, err
		}
		switch p := resp.GetPayload().(type) {
		case *mvmexecv1.ExecCommandResponse_Stdout:
			frames = append(frames, frame{kind: "stdout", data: string(p.Stdout)})
		case *mvmexecv1.ExecCommandResponse_Stderr:
			frames = append(frames, frame{kind: "stderr", data: string(p.Stderr)})
		case *mvmexecv1.ExecCommandResponse_ExitCode:
			frames = append(frames, frame{kind: "exit", code: p.ExitCode})
		case *mvmexecv1.ExecCommandResponse_Error:
			frames = append(frames, frame{kind: "error", data: p.Error})
		}
	}
}

func start(uid, cmd string, hasStdin bool) *mvmexecv1.ExecCommandRequest {
	return &mvmexecv1.ExecCommandRequest{Payload: &mvmexecv1.ExecCommandRequest_Start{
		Start: &mvmexecv1.ExecStart{Uid: uid, Cmd: cmd, HasStdin: hasStdin},
	}}
}

func TestServerInfo(t *testing.T) {
	fake := fakeexec.New()
	client := mvmv1.NewMicroVMClient(dial(t, fake))
	ctx := t.Context()

	resp, err := client.ServerInfo(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatalf("ServerInfo: %v", err)
	}
	if got := resp.GetVersion().GetVersion(); got != "v0.15.2" {
		t.Fatalf("version = %q, want v0.15.2", got)
	}
	if !resp.GetExec().GetEnabled() {
		t.Fatalf("exec.enabled = false, want true")
	}

	fake.SetServerInfo("v0.14.0", false)
	resp, err = client.ServerInfo(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatalf("ServerInfo: %v", err)
	}
	if got := resp.GetVersion().GetVersion(); got != "v0.14.0" {
		t.Fatalf("version = %q, want v0.14.0", got)
	}
	if resp.GetExec().GetEnabled() {
		t.Fatalf("exec.enabled = true, want false")
	}
	if got := fake.ServerInfoCount(); got != 2 {
		t.Fatalf("ServerInfoCount = %d, want 2", got)
	}
}

func TestExecRoundTrip(t *testing.T) {
	fake := fakeexec.New()
	fake.AddVM("vm-1", func(_ context.Context, _ *mvmexecv1.ExecStart, stdin io.Reader, stdout, stderr io.Writer) fakeexec.Result {
		in, _ := io.ReadAll(stdin)
		_, _ = stdout.Write(in)
		_, _ = io.WriteString(stderr, "warn")
		return fakeexec.Result{ExitCode: 3}
	})
	client := mvmexecv1.NewMicroVMExecClient(dial(t, fake))

	stream, err := client.ExecCommand(t.Context())
	if err != nil {
		t.Fatalf("ExecCommand: %v", err)
	}
	for _, req := range []*mvmexecv1.ExecCommandRequest{
		start("vm-1", "cat", true),
		{Payload: &mvmexecv1.ExecCommandRequest_Stdin{Stdin: []byte("hello")}},
		{Payload: &mvmexecv1.ExecCommandRequest_StdinEof{StdinEof: true}},
	} {
		if err := stream.Send(req); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}
	frames, err := collect(t, stream)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	want := []frame{{kind: "stdout", data: "hello"}, {kind: "stderr", data: "warn"}, {kind: "exit", code: 3}}
	if len(frames) != len(want) {
		t.Fatalf("frames = %+v, want %+v", frames, want)
	}
	for i := range want {
		if frames[i] != want[i] {
			t.Fatalf("frame %d = %+v, want %+v", i, frames[i], want[i])
		}
	}

	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	if string(calls[0].Stdin) != "hello" || !calls[0].StdinEOF || calls[0].Start.GetUid() != "vm-1" {
		t.Fatalf("call = %+v", calls[0])
	}
}

func TestExecStatusCodes(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*fakeexec.Server)
		first *mvmexecv1.ExecCommandRequest
		want  codes.Code
	}{
		{
			name:  "first message is stdin",
			first: &mvmexecv1.ExecCommandRequest{Payload: &mvmexecv1.ExecCommandRequest_Stdin{Stdin: []byte("x")}},
			want:  codes.InvalidArgument,
		},
		{name: "empty cmd", first: start("vm-1", "", false), want: codes.InvalidArgument},
		{name: "unknown uid", first: start("vm-x", "true", false), want: codes.Unknown},
		{
			name:  "vm not ready",
			setup: func(s *fakeexec.Server) { s.SetNotReady("vm-1") },
			first: start("vm-1", "true", false),
			want:  codes.FailedPrecondition,
		},
		{
			name:  "exec disabled",
			setup: func(s *fakeexec.Server) { s.SetServerInfo("v0.15.2", false) },
			first: start("vm-1", "true", false),
			want:  codes.Unimplemented,
		},
		{
			name:  "injected fault",
			setup: func(s *fakeexec.Server) { s.FailNext("ExecCommand", codes.Unavailable) },
			first: start("vm-1", "true", false),
			want:  codes.Unavailable,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := fakeexec.New()
			fake.AddVM("vm-1", func(context.Context, *mvmexecv1.ExecStart, io.Reader, io.Writer, io.Writer) fakeexec.Result {
				return fakeexec.Result{}
			})
			if tt.setup != nil {
				tt.setup(fake)
			}
			client := mvmexecv1.NewMicroVMExecClient(dial(t, fake))
			stream, err := client.ExecCommand(t.Context())
			if err != nil {
				t.Fatalf("ExecCommand: %v", err)
			}
			if err := stream.Send(tt.first); err != nil && !errors.Is(err, io.EOF) {
				t.Fatalf("Send: %v", err)
			}
			_, err = collect(t, stream)
			if got := status.Code(err); got != tt.want {
				t.Fatalf("code = %v (%v), want %v", got, err, tt.want)
			}
		})
	}
}

func TestFailNextIsOneShot(t *testing.T) {
	fake := fakeexec.New()
	fake.AddVM("vm-1", func(context.Context, *mvmexecv1.ExecStart, io.Reader, io.Writer, io.Writer) fakeexec.Result {
		return fakeexec.Result{ExitCode: 0}
	})
	fake.FailNext("ExecCommand", codes.Unavailable)
	client := mvmexecv1.NewMicroVMExecClient(dial(t, fake))
	for i, want := range []codes.Code{codes.Unavailable, codes.OK} {
		stream, err := client.ExecCommand(t.Context())
		if err != nil {
			t.Fatalf("ExecCommand: %v", err)
		}
		if err := stream.Send(start("vm-1", "true", false)); err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("Send: %v", err)
		}
		_, err = collect(t, stream)
		if got := status.Code(err); got != want {
			t.Fatalf("call %d: code = %v, want %v", i, got, want)
		}
	}
}

func TestExecErrorFrameThenExit(t *testing.T) {
	fake := fakeexec.New()
	fake.AddVM("vm-1", func(context.Context, *mvmexecv1.ExecStart, io.Reader, io.Writer, io.Writer) fakeexec.Result {
		return fakeexec.Result{ErrorFrame: "boom", ExitCode: 1}
	})
	client := mvmexecv1.NewMicroVMExecClient(dial(t, fake))
	stream, err := client.ExecCommand(t.Context())
	if err != nil {
		t.Fatalf("ExecCommand: %v", err)
	}
	if err := stream.Send(start("vm-1", "true", false)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	frames, err := collect(t, stream)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	want := []frame{{kind: "error", data: "boom"}, {kind: "exit", code: 1}}
	if len(frames) != 2 || frames[0] != want[0] || frames[1] != want[1] {
		t.Fatalf("frames = %+v, want %+v", frames, want)
	}
}

func TestExecAbort(t *testing.T) {
	fake := fakeexec.New()
	fake.AddVM("vm-1", func(context.Context, *mvmexecv1.ExecStart, io.Reader, io.Writer, io.Writer) fakeexec.Result {
		return fakeexec.Result{Abort: status.Error(codes.Unknown, "relay")}
	})
	client := mvmexecv1.NewMicroVMExecClient(dial(t, fake))
	stream, err := client.ExecCommand(t.Context())
	if err != nil {
		t.Fatalf("ExecCommand: %v", err)
	}
	if err := stream.Send(start("vm-1", "true", false)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	frames, err := collect(t, stream)
	if got := status.Code(err); got != codes.Unknown {
		t.Fatalf("code = %v, want Unknown", got)
	}
	for _, f := range frames {
		if f.kind == "exit" {
			t.Fatalf("got exit frame %+v, want none", f)
		}
	}
}

func TestHoldServerInfo(t *testing.T) {
	fake := fakeexec.New()
	release := fake.HoldServerInfo()
	client := mvmv1.NewMicroVMClient(dial(t, fake))
	done := make(chan error, 1)
	go func() {
		_, err := client.ServerInfo(t.Context(), &emptypb.Empty{})
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("ServerInfo answered while held: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if n := fake.ServerInfoCount(); n != 1 {
		t.Fatalf("ServerInfoCount = %d, want 1", n)
	}
	release()
	if err := <-done; err != nil {
		t.Fatalf("ServerInfo after release: %v", err)
	}
}
