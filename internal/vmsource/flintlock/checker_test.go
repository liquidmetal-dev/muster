package flintlock

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc/codes"

	"github.com/liquidmetal-dev/muster/internal/config"
	"github.com/liquidmetal-dev/muster/internal/tlsconfig"
	"github.com/liquidmetal-dev/muster/internal/vmsource"
	"github.com/liquidmetal-dev/muster/internal/vmsource/flintlock/fakeexec"
)

// warnCounter is a slog.Handler that counts Warn records.
type warnCounter struct {
	mu    sync.Mutex
	warns int
}

func (h *warnCounter) Enabled(context.Context, slog.Level) bool { return true }
func (h *warnCounter) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *warnCounter) WithGroup(string) slog.Handler            { return h }
func (h *warnCounter) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if r.Level == slog.LevelWarn {
		h.warns++
	}
	return nil
}

func (h *warnCounter) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.warns
}

type checkerFixture struct {
	fakes map[string]*fakeexec.Server
	warns *warnCounter
	check config.Checker
}

// newCheckerFixture serves one fake per host name on loopback TCP and
// resolves every name in hosts.
func newCheckerFixture(t *testing.T, hosts ...string) *checkerFixture {
	t.Helper()
	f := &checkerFixture{fakes: map[string]*fakeexec.Server{}, warns: &warnCounter{}}
	addrs := map[string]string{}
	for _, h := range hosts {
		fake := fakeexec.New()
		f.fakes[h] = fake
		addrs[h] = serveTCP(t, fake)
	}
	logger := slog.New(f.warns)
	e := newTCPExecer(t, config.Flintlock{TLS: tlsconfig.Client{Insecure: true}}, WithLogger(logger))
	resolve := func(_ context.Context, names []string) ([]vmsource.HostInfo, error) {
		var out []vmsource.HostInfo
		for _, n := range names {
			if a, ok := addrs[n]; ok {
				out = append(out, vmsource.HostInfo{Name: n, Address: a})
			}
		}
		return out, nil
	}
	f.check = NewChecker(e, resolve, logger)
	return f
}

func cfgWithPools(pools map[string][]string) *config.Config {
	cfg := &config.Config{}
	for name, hosts := range pools {
		cfg.Pools = append(cfg.Pools, config.Pool{Name: name, FlintlockHosts: hosts})
	}
	return cfg
}

func TestCheckerRefusesDisabledHost(t *testing.T) {
	f := newCheckerFixture(t, "host-a")
	f.fakes["host-a"].SetServerInfo("v0.15.2", false)
	err := config.Check(t.Context(), cfgWithPools(map[string][]string{"b": {"host-a"}, "a": {"host-a"}}), f.check)
	if !errors.Is(err, ErrExecDisabled) {
		t.Fatalf("Check = %v, want ErrExecDisabled", err)
	}
	for _, want := range []string{"flintlock: ", "pools [a b]", "--enable-exec-api"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Check = %q, want %q in it", err, want)
		}
	}
}

func TestCheckerPassesGoodHosts(t *testing.T) {
	f := newCheckerFixture(t, "host-a", "host-b")
	cfg := cfgWithPools(map[string][]string{"a": {"host-a", "host-b"}, "b": {"host-b"}})
	if err := config.Check(t.Context(), cfg, f.check); err != nil {
		t.Fatalf("Check: %v", err)
	}
	for name, fake := range f.fakes {
		if n := fake.ServerInfoCount(); n != 1 {
			t.Fatalf("%s ServerInfoCount = %d, want 1", name, n)
		}
	}
}

func TestCheckerSkipsUnreachableHost(t *testing.T) {
	f := newCheckerFixture(t, "host-a")
	f.fakes["host-a"].FailNext("ServerInfo", codes.Unavailable)
	if err := config.Check(t.Context(), cfgWithPools(map[string][]string{"a": {"host-a"}}), f.check); err != nil {
		t.Fatalf("Check = %v, want nil", err)
	}
	if n := f.warns.count(); n != 1 {
		t.Fatalf("warnings = %d, want 1", n)
	}
}

func TestCheckerReturnsOtherErrors(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name      string
		ctx       context.Context
		fault     codes.Code
		wantErr   string
		wantIs    error
		wantWarns int
	}{
		{name: "internal", ctx: context.Background(), fault: codes.Internal, wantErr: "pools [a]"},
		{name: "cancelled", ctx: cancelled, wantIs: context.Canceled},
		{name: "deadline", ctx: context.Background(), fault: codes.DeadlineExceeded, wantWarns: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newCheckerFixture(t, "host-a")
			if tt.fault != codes.OK {
				f.fakes["host-a"].FailNext("ServerInfo", tt.fault)
			}
			// Call the checker directly: config.Check stops before a
			// checker when ctx is already done.
			err := f.check.Check(tt.ctx, cfgWithPools(map[string][]string{"a": {"host-a"}}))
			switch {
			case tt.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Check = %v, want %q in it", err, tt.wantErr)
				}
			case tt.wantIs != nil:
				if !errors.Is(err, tt.wantIs) {
					t.Fatalf("Check = %v, want %v", err, tt.wantIs)
				}
			default:
				if err != nil {
					t.Fatalf("Check = %v, want nil", err)
				}
			}
			if n := f.warns.count(); n != tt.wantWarns {
				t.Fatalf("warnings = %d, want %d", n, tt.wantWarns)
			}
		})
	}
}

func TestCheckerSkipsUnresolvedHost(t *testing.T) {
	f := newCheckerFixture(t, "host-a")
	if err := config.Check(t.Context(), cfgWithPools(map[string][]string{"a": {"host-z"}}), f.check); err != nil {
		t.Fatalf("Check = %v, want nil", err)
	}
	if n := f.fakes["host-a"].ServerInfoCount(); n != 0 {
		t.Fatalf("ServerInfoCount = %d, want 0", n)
	}
}
