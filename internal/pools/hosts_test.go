package pools_test

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	poolmgrv1alpha1 "github.com/liquidmetal-dev/battery/api/proto/poolmgr/v1alpha1"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/liquidmetal-dev/muster/internal/config"
	"github.com/liquidmetal-dev/muster/internal/pools"
	"github.com/liquidmetal-dev/muster/internal/tlsconfig"
	"github.com/liquidmetal-dev/muster/internal/vmsource"
	"github.com/liquidmetal-dev/muster/internal/vmsource/flintlock"
	"github.com/liquidmetal-dev/muster/internal/vmsource/flintlock/fakeexec"
)

func durationOf(d time.Duration) *durationpb.Duration { return durationpb.New(d) }

type stubLister struct {
	mu    sync.Mutex
	hosts []*poolmgrv1alpha1.Host
	err   error
	calls int
}

func (s *stubLister) ListHosts(context.Context) ([]*poolmgrv1alpha1.Host, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.hosts, s.err
}

func (s *stubLister) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func lister(hosts map[string]string) *stubLister {
	l := &stubLister{}
	for name, addr := range hosts {
		l.hosts = append(l.hosts, &poolmgrv1alpha1.Host{Name: name, Address: addr})
	}
	return l
}

func cfgOf(ps ...config.Pool) *config.Config {
	return &config.Config{Battery: config.Battery{Namespace: "muster"}, Defaults: testDefaults(), Pools: ps}
}

func TestHostCheckUnknown(t *testing.T) {
	hc := pools.NewHostCheck(lister(map[string]string{"h1": "10.0.0.1:9090"}))
	err := hc.Check(context.Background(), cfgOf(testPool("a", 1, "h1", "hx"), testPool("b", 1, "hy")))
	if err == nil {
		t.Fatal("Check() = nil, want unknown host errors")
	}
	for _, want := range []string{
		`pools[a]: host "hx" is not registered in battery; add it with HostAdmin.AddHost`,
		`pools[b]: host "hy" is not registered in battery; add it with HostAdmin.AddHost`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Check() = %q, want it to contain %q", err, want)
		}
	}
	if strings.Contains(err.Error(), `"h1"`) {
		t.Errorf("Check() = %q, reports the known host h1", err)
	}
}

func TestHostCheckAllKnown(t *testing.T) {
	hc := pools.NewHostCheck(lister(map[string]string{"h1": "a:1", "h2": "b:1"}))
	if err := hc.Check(context.Background(), cfgOf(testPool("a", 1, "h1", "h2"))); err != nil {
		t.Fatalf("Check() = %v, want nil", err)
	}
	if hc.Name() != "battery hosts" {
		t.Fatalf("Name() = %q", hc.Name())
	}
}

func TestHostCheckListError(t *testing.T) {
	boom := errors.New("boom")
	hc := pools.NewHostCheck(&stubLister{err: boom})
	if err := hc.Check(context.Background(), cfgOf(testPool("a", 1, "h1"))); !errors.Is(err, boom) {
		t.Fatalf("Check() = %v, want it to wrap boom", err)
	}
}

func TestHostCheckResolveReusesList(t *testing.T) {
	l := lister(map[string]string{"h1": "10.0.0.1:9090"})
	hc := pools.NewHostCheck(l)
	_ = hc.Check(context.Background(), cfgOf(testPool("a", 1, "h1", "hx")))
	got, err := hc.Resolve(context.Background(), []string{"h1", "hx"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 1 || got[0] != (vmsource.HostInfo{Name: "h1", Address: "10.0.0.1:9090"}) {
		t.Fatalf("Resolve() = %v", got)
	}
	if n := l.count(); n != 1 {
		t.Fatalf("ListHosts calls = %d, want 1", n)
	}

	l2 := lister(map[string]string{"h1": "10.0.0.1:9090"})
	if _, err := pools.NewHostCheck(l2).Resolve(context.Background(), []string{"h1"}); err != nil {
		t.Fatalf("Resolve without Check: %v", err)
	}
	if n := l2.count(); n != 1 {
		t.Fatalf("ListHosts calls without Check = %d, want 1", n)
	}
}

func TestHostCheckWithFlintlockChecker(t *testing.T) {
	fake := fakeexec.New()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = fake.Serve(lis) }()
	t.Cleanup(fake.Stop)

	e, err := flintlock.NewExecer(config.Flintlock{TLS: tlsconfig.Client{Insecure: true}})
	if err != nil {
		t.Fatalf("NewExecer: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })

	hc := pools.NewHostCheck(lister(map[string]string{"h1": lis.Addr().String()}))
	cfg := cfgOf(testPool("a", 1, "h1", "hx"))
	err = config.Check(context.Background(), cfg, hc, flintlock.NewChecker(e, hc.Resolve, slog.New(slog.DiscardHandler)))
	if err == nil {
		t.Fatal("Check() = nil, want the unknown host")
	}
	lines := strings.Split(err.Error(), "\n")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "battery hosts: ") || !strings.Contains(lines[0], `"hx"`) {
		t.Fatalf("Check() = %q, want one battery hosts line about hx", err)
	}
	if fake.ServerInfoCount() != 1 {
		t.Fatalf("ServerInfo calls = %d, want 1 for h1", fake.ServerInfoCount())
	}
}
