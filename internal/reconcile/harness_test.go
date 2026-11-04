package reconcile_test

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	poolmgrv1alpha1 "github.com/liquidmetal-dev/battery/api/proto/poolmgr/v1alpha1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/liquidmetal-dev/muster/internal/clock"
	"github.com/liquidmetal-dev/muster/internal/config"
	"github.com/liquidmetal-dev/muster/internal/fakegithub"
	"github.com/liquidmetal-dev/muster/internal/github"
	"github.com/liquidmetal-dev/muster/internal/reconcile"
	"github.com/liquidmetal-dev/muster/internal/session"
	"github.com/liquidmetal-dev/muster/internal/tlsconfig"
	"github.com/liquidmetal-dev/muster/internal/vmsource"
	"github.com/liquidmetal-dev/muster/internal/vmsource/battery"
	"github.com/liquidmetal-dev/muster/internal/vmsource/battery/fakepoolmgr"
	"github.com/liquidmetal-dev/muster/internal/vmsource/flintlock"
)

const (
	testNS     = "muster"
	repoA      = "liquidmetal-dev/muster"
	repoB      = "liquidmetal-dev/other"
	poolLinux  = "linux"
	poolArm    = "linux-arm"
	poolOld    = "old"
	patToken   = "ghp_testtoken"
	pollPeriod = 30 * time.Second
	idA        = "aaaaaaaaaaaa"
	idB        = "bbbbbbbbbbbb"
)

var (
	startTime = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	labels    = []string{"self-hosted", "linux"}
)

type zeroDemand struct{}

func (zeroDemand) Desired(string) int { return 0 }

type envOptions struct {
	pools  []config.Pool
	source func(vmsource.Source) vmsource.Source
}

type envOption func(*envOptions)

func withPools(p ...config.Pool) envOption { return func(o *envOptions) { o.pools = p } }

func withSource(f func(vmsource.Source) vmsource.Source) envOption {
	return func(o *envOptions) { o.source = f }
}

func poolConfig(name, repo string) config.Pool {
	return config.Pool{
		Name:               name,
		Scope:              config.Scope{Repo: repo},
		Labels:             labels,
		Size:               1,
		MaxConcurrent:      2,
		MaxSessionDuration: config.Duration(6 * time.Hour),
		WorkFolder:         config.DefaultWorkFolder,
	}
}

type env struct {
	t        *testing.T
	Clock    *clock.Manual
	Pool     *fakepoolmgr.Server
	PoolAddr string
	GitHub   *fakegithub.Server
	Battery  *battery.Source
	Source   vmsource.Source
	Client   *github.Client
	Registry *session.Registry
	Pools    []config.Pool
	Counters *reconcile.Counters

	ended chan session.Info
}

// newEnv wires a running session registry to the fake poolmgrd and fake
// GitHub on one manual clock. Pools linux and linux-arm use repo A unless
// withPools says otherwise; poolmgrd also has pool old, which no config
// names.
func newEnv(t *testing.T, opts ...envOption) *env {
	t.Helper()
	o := envOptions{pools: []config.Pool{poolConfig(poolLinux, repoA), poolConfig(poolArm, repoA)}}
	for _, f := range opts {
		f(&o)
	}
	e := &env{
		t:        t,
		Clock:    clock.NewManual(startTime),
		Pool:     fakepoolmgr.New(),
		Pools:    o.pools,
		Counters: &reconcile.Counters{},
		ended:    make(chan session.Info, 16),
	}
	for _, name := range []string{poolLinux, poolArm, poolOld} {
		e.Pool.AddPool(fakepoolmgr.Pool{
			Name: name, Namespace: testNS, Capacity: 3,
			HeartbeatInterval: 20 * time.Second, HeartbeatExpiryThreshold: 60 * time.Second,
		})
	}
	e.PoolAddr = serve(t, e.Pool.Serve)

	execer, err := flintlock.NewExecer(config.Flintlock{TLS: tlsconfig.Client{Insecure: true}})
	if err != nil {
		t.Fatalf("NewExecer: %v", err)
	}
	t.Cleanup(func() { _ = execer.Close() })
	e.Battery, err = battery.New(battery.Config{
		Addr: e.PoolAddr, Namespace: testNS, TLS: tlsconfig.Client{Insecure: true},
		CallTimeout: 5 * time.Second, Execer: execer,
	})
	if err != nil {
		t.Fatalf("battery.New: %v", err)
	}
	t.Cleanup(func() { _ = e.Battery.Close() })
	e.Source = e.Battery
	if o.source != nil {
		e.Source = o.source(e.Battery)
	}

	e.GitHub = fakegithub.New(fakegithub.Config{PATs: map[string]string{patToken: "octocat"}, Clock: e.Clock})
	e.GitHub.AddRepo(repoA)
	e.GitHub.AddRepo(repoB)
	url := e.GitHub.Start(t)
	e.Client, err = github.New(github.Config{BaseURL: url, Token: patToken, Clock: e.Clock, MaxAttempts: 1})
	if err != nil {
		t.Fatalf("github.New: %v", err)
	}

	e.Registry, err = session.New(session.Config{
		Pools:        o.pools,
		Namespace:    testNS,
		Defaults:     defaults(),
		PollInterval: pollPeriod,
		Source:       e.Source,
		GitHub:       e.Client,
		Demand:       zeroDemand{},
		Clock:        e.Clock,
		Logger:       slog.New(slog.DiscardHandler),
		OnEnd:        func(i session.Info) { e.ended <- i },
	})
	if err != nil {
		t.Fatalf("session.New: %v", err)
	}
	e.startRegistry()
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

func (e *env) startRegistry() {
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

func (e *env) config() reconcile.Config {
	return reconcile.Config{
		Namespace: testNS,
		Pools:     e.Pools,
		Source:    e.Source,
		GitHub:    e.Client,
		Sessions:  e.Registry,
		Metrics:   e.Counters,
		Clock:     e.Clock,
		Logger:    slog.New(slog.DiscardHandler),
	}
}

func (e *env) reconcile() (reconcile.Result, error) {
	e.t.Helper()
	return reconcile.Run(context.Background(), e.config())
}

// reconcileDriven runs Run while it advances the manual clock, for runs
// that wait on a backoff.
func (e *env) reconcileDriven(cfg reconcile.Config) (reconcile.Result, error) {
	e.t.Helper()
	type out struct {
		res reconcile.Result
		err error
	}
	done := make(chan out, 1)
	go func() {
		res, err := reconcile.Run(context.Background(), cfg)
		done <- out{res, err}
	}()
	var got out
	e.drive(time.Second, func() bool {
		select {
		case got = <-done:
			return true
		default:
			return false
		}
	})
	return got.res, got.err
}

func runnerName(pool, id string) string { return config.RunnerNamePrefix(testNS, pool) + id }

// priorLease claims a VM for id as a previous musterd process would have.
func (e *env) priorLease(pool, id string) vmsource.VM {
	e.t.Helper()
	vm, err := e.Battery.Acquire(context.Background(), pool, id)
	if err != nil {
		e.t.Fatalf("Acquire(%s, %s): %v", pool, id, err)
	}
	return vm
}

// priorRunner registers the runner for id in repo and waits until GitHub
// lists it online.
func (e *env) priorRunner(repo, pool, id string, b fakegithub.Behavior) string {
	e.t.Helper()
	name := runnerName(pool, id)
	e.GitHub.NextRunner(b)
	e.jit(repo, name)
	e.drive(time.Second, func() bool {
		r, ok := e.runner(name)
		return ok && r.Status == "online"
	})
	return name
}

// prior leaves both halves of a session in repo A, as a previous musterd
// process would have.
func (e *env) prior(pool, id string, b fakegithub.Behavior) string {
	e.t.Helper()
	e.priorLease(pool, id)
	return e.priorRunner(repoA, pool, id, b)
}

// jit asks GitHub for the JIT configuration of a runner named name in
// repo, which registers the runner.
func (e *env) jit(repo, name string) {
	e.t.Helper()
	scope, err := github.RepoScope(repo)
	if err != nil {
		e.t.Fatalf("RepoScope: %v", err)
	}
	if _, err := e.Client.GenerateJITConfig(context.Background(), scope, github.JITRequest{Name: name, Labels: labels, RunnerGroupID: 1}); err != nil {
		e.t.Fatalf("GenerateJITConfig: %v", err)
	}
}

func (e *env) runner(name string) (fakegithub.Runner, bool) {
	for _, r := range e.GitHub.Runners() {
		if r.Name == name {
			return r, true
		}
	}
	return fakegithub.Runner{}, false
}

func (e *env) leaseIDs() []string {
	var ids []string
	for _, l := range e.Pool.Leases() {
		ids = append(ids, l.GetPoolName()+"/"+l.GetRequestId())
	}
	return ids
}

// claimRaw claims a VM in pool with no request id, which battery.Source
// never does.
func (e *env) claimRaw(pool string) {
	e.t.Helper()
	conn, err := grpc.NewClient(e.PoolAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		e.t.Fatalf("grpc.NewClient: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_, err = poolmgrv1alpha1.NewLeaseClient(conn).ClaimVM(context.Background(), &poolmgrv1alpha1.ClaimVMRequest{
		Pool: &poolmgrv1alpha1.PoolRef{Name: pool, Namespace: testNS},
	})
	if err != nil {
		e.t.Fatalf("ClaimVM: %v", err)
	}
}

func (e *env) sessionState(id string) (session.State, bool) {
	for _, i := range e.Registry.Sessions() {
		if i.ID == id {
			return i.State, true
		}
	}
	return 0, false
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

// vanishingSource makes the lease of one request id vanish just before
// its first Acquire, so Acquire claims a replacement VM, and fails the
// first Release of that replacement.
type vanishingSource struct {
	vmsource.Source
	pool      *fakepoolmgr.Server
	requestID string

	mu          sync.Mutex
	acquired    bool
	replacement string
	failed      bool
}

func (s *vanishingSource) Acquire(ctx context.Context, pool, requestID string) (vmsource.VM, error) {
	s.mu.Lock()
	first := requestID == s.requestID && !s.acquired
	s.acquired = s.acquired || first
	s.mu.Unlock()
	if first {
		for _, l := range s.pool.Leases() {
			if l.GetRequestId() == requestID {
				s.pool.DeleteVM(testNS, pool, l.GetVmUid(), poolmgrv1alpha1.EventType_VM_DELETED_DUE_TO_EXPIRY)
			}
		}
	}
	vm, err := s.Source.Acquire(ctx, pool, requestID)
	if first && err == nil {
		s.mu.Lock()
		s.replacement = vm.ID()
		s.mu.Unlock()
	}
	return vm, err
}

func (s *vanishingSource) Release(ctx context.Context, vm vmsource.VM) error {
	s.mu.Lock()
	fail := vm.ID() == s.replacement && !s.failed
	s.failed = s.failed || fail
	s.mu.Unlock()
	if fail {
		return errors.New("injected release failure")
	}
	return s.Source.Release(ctx, vm)
}
