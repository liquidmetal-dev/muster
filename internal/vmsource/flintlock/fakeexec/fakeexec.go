// Package fakeexec is an in-memory flintlock for tests. It implements
// MicroVMExec.ExecCommand and MicroVM.ServerInfo with the same status codes
// as flintlock v0.15.2 for the cases muster handles:
//
//   - INVALID_ARGUMENT when the first message is not a start message with
//     uid set, and with cmd set unless shell is true (the real server
//     accepts an empty cmd in shell mode).
//   - UNKNOWN for an unknown microvm uid, and for a Result.Abort error that
//     is not a status (the real server returns plain errors for lookup and
//     guest-agent relay failures).
//   - FAILED_PRECONDITION for a microvm that is not ready (SetNotReady).
//   - UNIMPLEMENTED for ExecCommand when exec is disabled: the real server
//     does not register MicroVMExec without --enable-exec-api.
//
// Every other MicroVM RPC returns UNIMPLEMENTED.
//
// Tests seed VMs with AddVM, set the reported version and exec state with
// SetServerInfo, inject faults with FailNext, hold ServerInfo with
// HoldServerInfo, and observe traffic with
// Calls and ServerInfoCount. The server runs on any listener via Serve, or
// on an in-process bufconn via ServeBufconn.
package fakeexec

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"

	mvmv1 "github.com/liquidmetal-dev/flintlock/api/services/microvm/v1alpha1"
	mvmexecv1 "github.com/liquidmetal-dev/flintlock/api/services/microvmexec/v1alpha1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Result is how a Handler ends a command.
type Result struct {
	ExitCode int32
	// ErrorFrame, when set, is sent as an error frame before exit_code, as
	// the real server does for a guest-agent error event.
	ErrorFrame string
	// Abort, when set, is returned from the RPC and no exit_code is sent.
	Abort error
}

// Handler runs one command. stdin yields the bytes the client sent and
// ends at stdin_eof, or at once when the start message has no stdin.
// Writes to stdout and stderr are sent as frames.
type Handler func(ctx context.Context, start *mvmexecv1.ExecStart, stdin io.Reader, stdout, stderr io.Writer) Result

// Call is one ExecCommand that reached a handler.
type Call struct {
	Start    *mvmexecv1.ExecStart
	Stdin    []byte
	StdinEOF bool
}

// Server is the fake. The zero value is not usable; call New.
type Server struct {
	mvmv1.UnimplementedMicroVMServer
	mvmexecv1.UnimplementedMicroVMExecServer

	mu              sync.Mutex
	version         string
	execEnabled     bool
	vms             map[string]Handler
	notReady        map[string]bool
	failNext        map[string][]codes.Code
	calls           []Call
	serverInfoCount int
	hold            chan struct{}

	gs *grpc.Server
}

// New returns a fake that reports v0.15.2 with exec enabled and has no VMs.
func New() *Server {
	return &Server{
		version:     "v0.15.2",
		execEnabled: true,
		vms:         map[string]Handler{},
		notReady:    map[string]bool{},
		failNext:    map[string][]codes.Code{},
	}
}

// SetServerInfo sets the version ServerInfo reports and whether exec is
// enabled. With exec disabled, ExecCommand returns UNIMPLEMENTED.
func (s *Server) SetServerInfo(version string, execEnabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.version = version
	s.execEnabled = execEnabled
}

// AddVM makes uid known and runs h for each command on it.
func (s *Server) AddVM(uid string, h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vms[uid] = h
}

// SetNotReady makes ExecCommand on uid return FAILED_PRECONDITION.
func (s *Server) SetNotReady(uid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notReady[uid] = true
}

// FailNext makes the next call of method ("ExecCommand" or "ServerInfo")
// return code. Calls queue in order.
func (s *Server) FailNext(method string, c codes.Code) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failNext[method] = append(s.failNext[method], c)
}

// HoldServerInfo makes ServerInfo block, after it is counted, until the
// returned release is called.
func (s *Server) HoldServerInfo() (release func()) {
	ch := make(chan struct{})
	s.mu.Lock()
	s.hold = ch
	s.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.hold = nil
			s.mu.Unlock()
			close(ch)
		})
	}
}

// Calls returns the ExecCommand calls that reached a handler, in order.
func (s *Server) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Call(nil), s.calls...)
}

// ServerInfoCount returns the number of ServerInfo calls, faults included.
func (s *Server) ServerInfoCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.serverInfoCount
}

// Register adds the MicroVM and MicroVMExec services to gs.
func (s *Server) Register(gs *grpc.Server) {
	mvmv1.RegisterMicroVMServer(gs, s)
	mvmexecv1.RegisterMicroVMExecServer(gs, s)
}

// Serve builds a gRPC server with opts and serves on lis until Stop. It
// blocks like grpc.Server.Serve.
func (s *Server) Serve(lis net.Listener, opts ...grpc.ServerOption) error {
	gs := grpc.NewServer(opts...)
	s.Register(gs)
	s.mu.Lock()
	s.gs = gs
	s.mu.Unlock()
	return gs.Serve(lis)
}

// Stop stops the server started by Serve.
func (s *Server) Stop() {
	s.mu.Lock()
	gs := s.gs
	s.mu.Unlock()
	if gs != nil {
		gs.Stop()
	}
}

// ServeBufconn serves on an in-process listener for the life of the test
// and returns the dial option that reaches it. Dial with the target
// "passthrough:///fakeexec".
func (s *Server) ServeBufconn(t testing.TB) grpc.DialOption {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(func() { s.Stop(); _ = lis.Close() })
	return grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	})
}

func (s *Server) takeFault(method string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.failNext[method]
	if len(q) == 0 {
		return nil
	}
	s.failNext[method] = q[1:]
	return status.Errorf(q[0], "fakeexec: injected %s fault", method)
}

// ServerInfo reports the configured version and exec state.
func (s *Server) ServerInfo(ctx context.Context, _ *emptypb.Empty) (*mvmv1.ServerInfoResponse, error) {
	s.mu.Lock()
	s.serverInfoCount++
	hold := s.hold
	s.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return nil, status.FromContextError(ctx.Err()).Err()
		}
	}
	if err := s.takeFault("ServerInfo"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	resp := &mvmv1.ServerInfoResponse{
		Version: &mvmv1.VersionInfo{Version: s.version},
		Exec:    &mvmv1.GuestAgentServiceInfo{Enabled: s.execEnabled},
	}
	if s.execEnabled {
		resp.Exec.Address = "fakeexec"
	}
	return resp, nil
}

// ExecCommand runs the handler of the VM named in the start message.
func (s *Server) ExecCommand(stream mvmexecv1.MicroVMExec_ExecCommandServer) error {
	if err := s.takeFault("ExecCommand"); err != nil {
		return err
	}
	s.mu.Lock()
	enabled := s.execEnabled
	s.mu.Unlock()
	if !enabled {
		return status.Error(codes.Unimplemented, "unknown service microvmexec.services.api.v1alpha1.MicroVMExec")
	}

	first, err := stream.Recv()
	if err != nil {
		return err
	}
	start := first.GetStart()
	if start == nil || start.GetUid() == "" || (start.GetCmd() == "" && !start.GetShell()) {
		return status.Error(codes.InvalidArgument, "first message must be a start message with uid and cmd set")
	}

	s.mu.Lock()
	h, ok := s.vms[start.GetUid()]
	notReady := s.notReady[start.GetUid()]
	s.mu.Unlock()
	if !ok {
		return status.Errorf(codes.Unknown, "getting microvm: microvm %s not found", start.GetUid())
	}
	if notReady {
		return status.Error(codes.FailedPrecondition, "microvm is not ready (state: pending)")
	}

	in := &stdinRecorder{}
	pr, pw := io.Pipe()
	if start.GetHasStdin() {
		go in.pump(stream, pw)
	} else {
		_ = pw.Close()
	}

	out := &sender{stream: stream}
	res := h(stream.Context(), start, pr, out.writer(stdoutFrame), out.writer(stderrFrame))
	_ = pr.Close()

	stdin, eof := in.snapshot()
	s.mu.Lock()
	s.calls = append(s.calls, Call{Start: start, Stdin: stdin, StdinEOF: eof})
	s.mu.Unlock()

	if res.Abort != nil {
		return res.Abort
	}
	if res.ErrorFrame != "" {
		if err := out.send(&mvmexecv1.ExecCommandResponse{Payload: &mvmexecv1.ExecCommandResponse_Error{Error: res.ErrorFrame}}); err != nil {
			return err
		}
	}
	return out.send(&mvmexecv1.ExecCommandResponse{Payload: &mvmexecv1.ExecCommandResponse_ExitCode{ExitCode: res.ExitCode}})
}

type stdinRecorder struct {
	mu   sync.Mutex
	data []byte
	eof  bool
}

func (r *stdinRecorder) pump(stream mvmexecv1.MicroVMExec_ExecCommandServer, pw *io.PipeWriter) {
	for {
		req, err := stream.Recv()
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		switch p := req.GetPayload().(type) {
		case *mvmexecv1.ExecCommandRequest_Stdin:
			r.mu.Lock()
			r.data = append(r.data, p.Stdin...)
			r.mu.Unlock()
			if _, err := pw.Write(p.Stdin); err != nil {
				return
			}
		case *mvmexecv1.ExecCommandRequest_StdinEof:
			r.mu.Lock()
			r.eof = true
			r.mu.Unlock()
			_ = pw.Close()
			return
		}
	}
}

func (r *stdinRecorder) snapshot() ([]byte, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]byte(nil), r.data...), r.eof
}

type frameKind int

const (
	stdoutFrame frameKind = iota
	stderrFrame
)

// sender serialises Send, which is not safe for concurrent use.
type sender struct {
	mu     sync.Mutex
	stream mvmexecv1.MicroVMExec_ExecCommandServer
}

func (s *sender) send(resp *mvmexecv1.ExecCommandResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stream.Send(resp)
}

func (s *sender) writer(k frameKind) io.Writer {
	return writerFunc(func(p []byte) (int, error) {
		data := append([]byte(nil), p...)
		resp := &mvmexecv1.ExecCommandResponse{Payload: &mvmexecv1.ExecCommandResponse_Stdout{Stdout: data}}
		if k == stderrFrame {
			resp = &mvmexecv1.ExecCommandResponse{Payload: &mvmexecv1.ExecCommandResponse_Stderr{Stderr: data}}
		}
		if err := s.send(resp); err != nil {
			return 0, err
		}
		return len(p), nil
	})
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
