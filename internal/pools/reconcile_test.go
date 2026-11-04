package pools_test

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/liquidmetal-dev/muster/internal/config"
	"github.com/liquidmetal-dev/muster/internal/pools"
	"github.com/liquidmetal-dev/muster/internal/tlsconfig"
	"github.com/liquidmetal-dev/muster/internal/vmsource/battery"
	"github.com/liquidmetal-dev/muster/internal/vmsource/battery/fakepoolmgr"
)

var _ pools.Admin = (*battery.Admin)(nil)

const ns = "muster"

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type harness struct {
	fake *fakepoolmgr.Server
	src  *battery.Source
	cfg  *config.Config
	log  *syncBuffer
}

// newHarness serves the fake on loopback TCP, because battery.Config has
// no public dial option for bufconn. Hosts h1 and h2 are registered, and
// pools a (h1) and b (h1, h2) are configured.
func newHarness(t *testing.T) *harness {
	t.Helper()
	fake := fakepoolmgr.New()
	fake.SeedHost(fakepoolmgr.Host{Name: "h1", Address: "10.0.0.1:9090"})
	fake.SeedHost(fakepoolmgr.Host{Name: "h2", Address: "10.0.0.2:9090"})
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = fake.Serve(lis) }()
	t.Cleanup(fake.Stop)
	src, err := battery.New(battery.Config{
		Addr: lis.Addr().String(), Namespace: ns, TLS: tlsconfig.Client{Insecure: true},
		CallTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("battery.New: %v", err)
	}
	t.Cleanup(func() { _ = src.Close() })
	return &harness{
		fake: fake, src: src, log: &syncBuffer{},
		cfg: cfgOf(testPool("a", 2, "h1"), testPool("b", 1, "h1", "h2")),
	}
}

func (h *harness) run(t *testing.T, prune bool) (pools.Result, error) {
	t.Helper()
	return pools.Reconcile(context.Background(), pools.Config{
		Admin: h.src.Admin(), Config: h.cfg, Prune: prune,
		Logger: slog.New(slog.NewTextHandler(h.log, nil)),
	})
}

func (h *harness) mustRun(t *testing.T, prune bool) pools.Result {
	t.Helper()
	res, err := h.run(t, prune)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return res
}

func (h *harness) wantStored(t *testing.T, i int) {
	t.Helper()
	p := h.cfg.Pools[i]
	got, ok := h.fake.PoolSpec(ns, p.Name)
	if !ok {
		t.Fatalf("pool %s not in battery", p.Name)
	}
	if want := pools.Spec(ns, p, h.cfg.Defaults); !proto.Equal(got, want) {
		t.Fatalf("pool %s spec = %v\nwant %v", p.Name, got, want)
	}
}

func seedOrphan(fake *fakepoolmgr.Server, namespace, name string, capacity int) {
	fake.AddPool(fakepoolmgr.Pool{Name: name, Namespace: namespace, Capacity: capacity,
		HeartbeatInterval: 20 * time.Second, HeartbeatExpiryThreshold: 60 * time.Second})
}

func wantNames(t *testing.T, field string, got []string, want ...string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("%s = %v, want %v", field, got, want)
	}
}

func TestReconcileCreatesOnFirstRun(t *testing.T) {
	h := newHarness(t)
	res := h.mustRun(t, false)
	wantNames(t, "Created", res.Created, "a", "b")
	h.wantStored(t, 0)
	h.wantStored(t, 1)
	if n := h.fake.CallCount("UpdatePool"); n != 0 {
		t.Fatalf("UpdatePool calls = %d, want 0", n)
	}
}

func TestReconcileNoOpWhenUnchanged(t *testing.T) {
	h := newHarness(t)
	h.mustRun(t, false)
	creates, updates := h.fake.CallCount("CreatePool"), h.fake.CallCount("UpdatePool")
	res := h.mustRun(t, false)
	wantNames(t, "Unchanged", res.Unchanged, "a", "b")
	if h.fake.CallCount("CreatePool") != creates || h.fake.CallCount("UpdatePool") != updates {
		t.Fatalf("second run wrote to battery: creates %d→%d, updates %d→%d",
			creates, h.fake.CallCount("CreatePool"), updates, h.fake.CallCount("UpdatePool"))
	}
}

func TestReconcileUpdatesWhenSizeChanges(t *testing.T) {
	h := newHarness(t)
	h.mustRun(t, false)
	h.cfg.Pools[0].Size = 3
	res := h.mustRun(t, false)
	wantNames(t, "Updated", res.Updated, "a")
	wantNames(t, "Unchanged", res.Unchanged, "b")
	got, _ := h.fake.PoolSpec(ns, "a")
	if got.GetSize() != 3 {
		t.Fatalf("size = %d, want 3", got.GetSize())
	}
}

func TestReconcileOverwritesDriftInBattery(t *testing.T) {
	h := newHarness(t)
	drifted := pools.Spec(ns, h.cfg.Pools[0], h.cfg.Defaults)
	drifted.CreateCommands = []string{"echo changed by hand"}
	h.fake.AddPool(fakepoolmgr.Pool{Name: "a", Namespace: ns, Spec: drifted})
	res := h.mustRun(t, false)
	wantNames(t, "Updated", res.Updated, "a")
	h.wantStored(t, 0)
}

func TestReconcileUnknownHostRejected(t *testing.T) {
	h := newHarness(t)
	h.cfg.Pools[0].FlintlockHosts = []string{"h1", "hx"}
	res, err := h.run(t, false)
	if err == nil || !strings.Contains(err.Error(), "pool a:") || !strings.Contains(err.Error(), "unknown hosts") {
		t.Fatalf("Reconcile() = %v, want a pool a unknown hosts error", err)
	}
	wantNames(t, "Created", res.Created, "b")
	h.wantStored(t, 1)
}

func TestReconcileOrphanLoggedNotPruned(t *testing.T) {
	h := newHarness(t)
	seedOrphan(h.fake, ns, "old", 0)
	seedOrphan(h.fake, "other", "old", 0)
	res := h.mustRun(t, false)
	wantNames(t, "Orphans", res.Orphans, "old")
	wantNames(t, "Pruned", res.Pruned)
	if n := h.fake.CallCount("DeletePool"); n != 0 {
		t.Fatalf("DeletePool calls = %d, want 0", n)
	}
	if out := h.log.String(); !strings.Contains(out, "orphan pool") || !strings.Contains(out, "pool=old") {
		t.Fatalf("log = %q, want an orphan pool line for old", out)
	}
	if strings.Contains(h.log.String(), "namespace=other") {
		t.Fatalf("log reports the pool in another namespace: %q", h.log.String())
	}
}

func TestReconcileOrphanPrunedWithFlag(t *testing.T) {
	h := newHarness(t)
	seedOrphan(h.fake, ns, "old", 0)
	seedOrphan(h.fake, "other", "old", 0)
	res := h.mustRun(t, true)
	wantNames(t, "Orphans", res.Orphans, "old")
	wantNames(t, "Pruned", res.Pruned, "old")
	if _, ok := h.fake.PoolSpec(ns, "old"); ok {
		t.Fatalf("muster/old still exists")
	}
	if _, ok := h.fake.PoolSpec("other", "old"); !ok {
		t.Fatalf("other/old was deleted")
	}
}

func TestReconcileOrphanInUseKept(t *testing.T) {
	h := newHarness(t)
	seedOrphan(h.fake, ns, "old", 1)
	if _, err := h.src.Acquire(context.Background(), "old", "req-1"); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	res := h.mustRun(t, true)
	wantNames(t, "Pruned", res.Pruned)
	if !strings.Contains(h.log.String(), "still has leased VMs") {
		t.Fatalf("log = %q, want the leased VMs warning", h.log.String())
	}
	if _, ok := h.fake.PoolSpec(ns, "old"); !ok {
		t.Fatalf("pool in use was deleted")
	}
}

func TestReconcileCreateLostResponse(t *testing.T) {
	h := newHarness(t)
	h.fake.LoseNext("CreatePool")
	res := h.mustRun(t, false)
	if !slices.Contains(res.Created, "a") && !slices.Contains(res.Unchanged, "a") {
		t.Fatalf("Result = %+v, want a created or unchanged", res)
	}
	h.wantStored(t, 0)
}

func TestReconcileNeverCallsHostAdminMutations(t *testing.T) {
	h := newHarness(t)
	seedOrphan(h.fake, ns, "old", 0)
	h.mustRun(t, true)
	h.cfg.Pools[0].Size = 4
	h.mustRun(t, true)
	allowed := []string{"GetPool", "CreatePool", "UpdatePool", "ListPools", "DeletePool"}
	for _, c := range h.fake.Calls() {
		if !slices.Contains(allowed, c.Method) {
			t.Fatalf("Reconcile called %s", c.Method)
		}
	}
}

func TestReconcileStopsOnCancelledContext(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := pools.Reconcile(ctx, pools.Config{Admin: h.src.Admin(), Config: h.cfg})
	if err == nil {
		t.Fatal("Reconcile() = nil, want the context error")
	}
	if n := h.fake.CallCount("CreatePool"); n != 0 {
		t.Fatalf("CreatePool calls = %d, want 0", n)
	}
}
