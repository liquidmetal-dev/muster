package session_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	poolmgrv1alpha1 "github.com/liquidmetal-dev/battery/api/proto/poolmgr/v1alpha1"
	mvmexecv1 "github.com/liquidmetal-dev/flintlock/api/services/microvmexec/v1alpha1"
	"google.golang.org/grpc"

	"github.com/liquidmetal-dev/muster/internal/clock"
	"github.com/liquidmetal-dev/muster/internal/config"
	"github.com/liquidmetal-dev/muster/internal/fakegithub"
	"github.com/liquidmetal-dev/muster/internal/github"
	"github.com/liquidmetal-dev/muster/internal/session"
	"github.com/liquidmetal-dev/muster/internal/tlsconfig"
	"github.com/liquidmetal-dev/muster/internal/vmsource/battery"
	"github.com/liquidmetal-dev/muster/internal/vmsource/battery/fakepoolmgr"
	"github.com/liquidmetal-dev/muster/internal/vmsource/flintlock"
	"github.com/liquidmetal-dev/muster/internal/vmsource/flintlock/fakeexec"
)

const (
	testRepo   = "liquidmetal-dev/muster"
	testPool   = "linux"
	testNS     = "muster"
	patToken   = "ghp_testtoken"
	pollPeriod = 30 * time.Second
)

var startTime = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

// syncBuffer is a log sink that tests read while sessions write.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// staticDemand is a settable session.Demand.
type staticDemand struct {
	mu sync.Mutex
	n  map[string]int
}

func (d *staticDemand) Desired(pool string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.n[pool]
}

func (d *staticDemand) set(pool string, n int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.n[pool] = n
}

type envOptions struct {
	capacity int
	pools    []config.Pool
	mutate   func(*session.Config)
}

type envOption func(*envOptions)

func withCapacity(n int) envOption { return func(o *envOptions) { o.capacity = n } }

func withPools(p ...config.Pool) envOption { return func(o *envOptions) { o.pools = p } }

func withConfig(f func(*session.Config)) envOption { return func(o *envOptions) { o.mutate = f } }

type env struct {
	t        *testing.T
	Clock    *clock.Manual
	Pool     *fakepoolmgr.Server
	Exec     *fakeexec.Server
	GitHub   *fakegithub.Server
	Source   *battery.Source
	Client   *github.Client
	Rec      *recordingGitHub
	Demand   *staticDemand
	Logs     *syncBuffer
	Registry *session.Registry
	Config   session.Config

	ended chan session.Info

	hmu     sync.Mutex
	handler fakeexec.Handler
}

func testPoolConfig(name string) config.Pool {
	return config.Pool{
		Name:               name,
		Scope:              config.Scope{Repo: testRepo},
		Labels:             []string{"self-hosted", "linux"},
		Size:               1,
		MaxConcurrent:      2,
		MaxSessionDuration: config.Duration(6 * time.Hour),
		WorkFolder:         config.DefaultWorkFolder,
	}
}

// newEnv wires a registry to the fake poolmgrd, fake exec server and fake
// GitHub over loopback, all on one manual clock.
func newEnv(t *testing.T, opts ...envOption) *env {
	t.Helper()
	o := envOptions{capacity: 2, pools: []config.Pool{testPoolConfig(testPool)}}
	for _, f := range opts {
		f(&o)
	}
	e := &env{
		t:      t,
		Clock:  clock.NewManual(startTime),
		Pool:   fakepoolmgr.New(),
		Exec:   fakeexec.New(),
		Demand: &staticDemand{n: map[string]int{}},
		Logs:   &syncBuffer{},
		ended:  make(chan session.Info, 16),
	}
	e.handler = runUntilCancelled

	execAddr := serve(t, e.Exec.Serve)
	for _, p := range o.pools {
		e.Pool.AddPool(fakepoolmgr.Pool{
			Name: p.Name, Namespace: testNS, Capacity: o.capacity,
			HeartbeatInterval: 20 * time.Second, HeartbeatExpiryThreshold: 60 * time.Second,
			Host: &poolmgrv1alpha1.HostInfo{Name: "host-a", Address: execAddr},
		})
		for i := range o.capacity {
			e.Exec.AddVM(testNS+"-"+p.Name+"-vm-"+strconv.Itoa(i+1), e.dispatch)
		}
		e.Demand.set(p.Name, 1)
	}
	poolAddr := serve(t, e.Pool.Serve)

	execer, err := flintlock.NewExecer(config.Flintlock{TLS: tlsconfig.Client{Insecure: true}})
	if err != nil {
		t.Fatalf("NewExecer: %v", err)
	}
	t.Cleanup(func() { _ = execer.Close() })
	e.Source, err = battery.New(battery.Config{
		Addr: poolAddr, Namespace: testNS, TLS: tlsconfig.Client{Insecure: true},
		CallTimeout: 5 * time.Second, Execer: execer,
	})
	if err != nil {
		t.Fatalf("battery.New: %v", err)
	}
	t.Cleanup(func() { _ = e.Source.Close() })

	e.GitHub = fakegithub.New(fakegithub.Config{PATs: map[string]string{patToken: "octocat"}, Clock: e.Clock})
	e.GitHub.AddRepo(testRepo)
	url := e.GitHub.Start(t)
	e.Client, err = github.New(github.Config{BaseURL: url, Token: patToken, Clock: e.Clock, MaxAttempts: 1})
	if err != nil {
		t.Fatalf("github.New: %v", err)
	}

	e.Rec = &recordingGitHub{Client: e.Client}
	e.Config = session.Config{
		Pools:        o.pools,
		Namespace:    testNS,
		Defaults:     defaults(),
		PollInterval: pollPeriod,
		Source:       e.Source,
		GitHub:       e.Rec,
		Demand:       e.Demand,
		Clock:        e.Clock,
		Logger:       slog.New(slog.NewJSONHandler(e.Logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		OnEnd:        func(i session.Info) { e.ended <- i },
	}
	if o.mutate != nil {
		o.mutate(&e.Config)
	}
	e.Registry, err = session.New(e.Config)
	if err != nil {
		t.Fatalf("session.New: %v", err)
	}
	return e
}

func defaults() config.Defaults {
	return config.Defaults{
		ClaimDeadline:      config.Duration(10 * time.Minute),
		StartTimeout:       config.Duration(2 * time.Minute),
		IdleTimeout:        config.Duration(5 * time.Minute),
		MaxSessionDuration: config.Duration(6 * time.Hour),
	}
}

func serve(t *testing.T, fn func(net.Listener, ...grpc.ServerOption) error) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = fn(lis) }()
	return lis.Addr().String()
}

// start runs the registry until the test ends.
func (e *env) start() {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = e.Registry.Run(ctx)
	}()
	e.t.Cleanup(func() {
		cancel()
		<-done
	})
	waitFor(e.t, func() bool {
		return !errors.Is(e.Registry.Create(context.Background(), "\x00probe"), session.ErrNotRunning)
	})
}

// create adds one session to pool and returns its id.
func (e *env) create(pool string) string {
	e.t.Helper()
	before := len(e.Registry.Sessions())
	if err := e.Registry.Create(context.Background(), pool); err != nil {
		e.t.Fatalf("Create(%s): %v", pool, err)
	}
	var id string
	waitFor(e.t, func() bool {
		s := e.Registry.Sessions()
		if len(s) <= before {
			return false
		}
		id = newest(s, pool)
		return id != ""
	})
	return id
}

func newest(infos []session.Info, pool string) string {
	for i := len(infos) - 1; i >= 0; i-- {
		if infos[i].Pool == pool {
			return infos[i].ID
		}
	}
	return ""
}

// info returns the live session with id, or false once it is removed.
func (e *env) info(id string) (session.Info, bool) {
	for _, i := range e.Registry.Sessions() {
		if i.ID == id {
			return i, true
		}
	}
	return session.Info{}, false
}

func (e *env) state(id string) session.State {
	i, ok := e.info(id)
	if !ok {
		return session.Released
	}
	return i.State
}

// waitState waits in real time for the session to reach s.
func (e *env) waitState(id string, s session.State) {
	e.t.Helper()
	waitFor(e.t, func() bool { return e.state(id) == s })
}

// driveState advances the clock by step until the session reaches s.
func (e *env) driveState(id string, step time.Duration, s session.State) {
	e.t.Helper()
	e.drive(step, func() bool { return e.state(id) == s })
}

// ended returns the next session that reached RELEASED.
func (e *env) endedInfo() session.Info {
	e.t.Helper()
	select {
	case i := <-e.ended:
		return i
	case <-time.After(10 * time.Second):
		e.t.Fatal("no session ended")
		return session.Info{}
	}
}

// driveEnd advances the clock by step until a session ends.
func (e *env) driveEnd(step time.Duration) session.Info {
	e.t.Helper()
	var got session.Info
	e.drive(step, func() bool {
		select {
		case got = <-e.ended:
			return true
		default:
			return false
		}
	})
	return got
}

// drive advances the manual clock by step every 5 ms of real time until
// cond holds. Small steps against real time avoid racing a goroutine that
// has not armed its timer yet.
func (e *env) drive(step time.Duration, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			e.t.Fatalf("condition not met by %v of manual time", e.Clock.Now().Sub(startTime))
		}
		e.Clock.Advance(step)
		time.Sleep(5 * time.Millisecond)
	}
}

func (e *env) elapsed() time.Duration { return e.Clock.Now().Sub(startTime) }

// setHandler replaces the exec handler for every VM.
func (e *env) setHandler(h fakeexec.Handler) {
	e.hmu.Lock()
	defer e.hmu.Unlock()
	e.handler = h
}

func (e *env) dispatch(ctx context.Context, start *mvmexecv1.ExecStart, stdin io.Reader, stdout, stderr io.Writer) fakeexec.Result {
	e.hmu.Lock()
	h := e.handler
	e.hmu.Unlock()
	return h(ctx, start, stdin, stdout, stderr)
}

// runUntilCancelled reads stdin to EOF and runs until the stream ends.
func runUntilCancelled(ctx context.Context, _ *mvmexecv1.ExecStart, stdin io.Reader, _, _ io.Writer) fakeexec.Result {
	_, _ = io.Copy(io.Discard, stdin)
	<-ctx.Done()
	return fakeexec.Result{}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 10s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// recordingGitHub keeps every encoded JIT config it hands out so tests can
// look for it in logs.
type recordingGitHub struct {
	*github.Client
	mu      sync.Mutex
	encoded []string
}

func (g *recordingGitHub) GenerateJITConfig(ctx context.Context, s github.Scope, r github.JITRequest) (github.JITConfig, error) {
	c, err := g.Client.GenerateJITConfig(ctx, s, r)
	if err == nil {
		g.mu.Lock()
		g.encoded = append(g.encoded, c.Encoded.Reveal())
		g.mu.Unlock()
	}
	return c, err
}

func (g *recordingGitHub) issued() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.encoded)
}

// capture is an exec handler that records the start frame and stdin, then
// runs until the test calls exit or the stream ends.
type capture struct {
	mu        sync.Mutex
	start     *mvmexecv1.ExecStart
	stdin     []byte
	stdinDone chan struct{}
	exitCode  chan int32
}

func (e *env) capture() *capture {
	c := &capture{stdinDone: make(chan struct{}), exitCode: make(chan int32, 1)}
	e.setHandler(func(ctx context.Context, start *mvmexecv1.ExecStart, stdin io.Reader, _, _ io.Writer) fakeexec.Result {
		b, _ := io.ReadAll(stdin)
		c.mu.Lock()
		c.start, c.stdin = start, b
		c.mu.Unlock()
		close(c.stdinDone)
		select {
		case code := <-c.exitCode:
			return fakeexec.Result{ExitCode: code}
		case <-ctx.Done():
			return fakeexec.Result{}
		}
	})
	return c
}

func (c *capture) waitStdin(t *testing.T) {
	t.Helper()
	select {
	case <-c.stdinDone:
	case <-time.After(10 * time.Second):
		t.Fatal("exec handler did not read stdin to EOF")
	}
}

func (c *capture) got() (*mvmexecv1.ExecStart, []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.start, c.stdin
}

// assertNoSecret fails when any 16-character piece of an issued JIT config
// appears in the logs or in text.
func (e *env) assertNoSecret(extra ...string) {
	e.t.Helper()
	issued := e.Rec.issued()
	if len(issued) == 0 {
		e.t.Fatal("no JIT config was issued")
	}
	hay := e.Logs.String() + strings.Join(extra, "\n")
	for _, enc := range issued {
		for i := 0; i+16 <= len(enc); i += 8 {
			if strings.Contains(hay, enc[i:i+16]) {
				e.t.Fatalf("JIT config piece %q found in logs or errors", enc[i:i+16])
			}
		}
	}
}
