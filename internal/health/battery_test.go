package health_test

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	"github.com/liquidmetal-dev/muster/internal/clock"
	"github.com/liquidmetal-dev/muster/internal/health"
	"github.com/liquidmetal-dev/muster/internal/tlsconfig"
	"github.com/liquidmetal-dev/muster/internal/vmsource/battery"
	"github.com/liquidmetal-dev/muster/internal/vmsource/battery/fakepoolmgr"
)

func batteryProbe(t *testing.T) (*fakepoolmgr.Server, health.Probe) {
	t.Helper()
	pm := fakepoolmgr.New()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = pm.Serve(lis) }()
	t.Cleanup(pm.Stop)
	src, err := battery.New(battery.Config{Addr: lis.Addr().String(), Namespace: "muster", TLS: tlsconfig.Client{Insecure: true}})
	if err != nil {
		t.Fatalf("battery.New: %v", err)
	}
	t.Cleanup(func() { _ = src.Close() })
	admin := src.Admin()
	return pm, health.Probe{Name: "battery", Check: func(ctx context.Context) error {
		_, err := admin.ListHosts(ctx)
		return err
	}}
}

func TestReadyzFlipsOnBatteryOutage(t *testing.T) {
	clk := clock.NewManual(t0)
	pm, probe := batteryProbe(t)
	r := run(t, health.Config{Clock: clk, Probes: []health.Probe{probe}})
	r.MarkStarted()
	probedAt(t, r, t0)
	if code, b := readyz(t, r); code != http.StatusOK {
		t.Fatalf("readyz = %d %+v, want 200", code, b)
	}
	for range battery.DefaultMaxAttempts {
		pm.FailNext("ListHosts", codes.Unavailable)
	}
	clk.Advance(health.DefaultInterval)
	probedAt(t, r, t0.Add(health.DefaultInterval))
	if code, b := readyz(t, r); code != http.StatusServiceUnavailable || b.Checks[1].OK {
		t.Fatalf("readyz during outage = %d %+v, want 503", code, b)
	}
	clk.Advance(health.DefaultInterval)
	probedAt(t, r, t0.Add(2*health.DefaultInterval))
	if code, b := readyz(t, r); code != http.StatusOK {
		t.Fatalf("readyz after outage = %d %+v, want 200", code, b)
	}
}

func TestBatteryProbeStallTimesOut(t *testing.T) {
	clk := clock.NewManual(t0)
	pm, probe := batteryProbe(t)
	pm.Stall("ListHosts", time.Minute)
	began := time.Now()
	r := run(t, health.Config{Clock: clk, Timeout: 200 * time.Millisecond, Probes: []health.Probe{probe}})
	probedAt(t, r, t0)
	if el := time.Since(began); el > 2*time.Second {
		t.Fatalf("probe took %v, want under 2s", el)
	}
	_, sts := r.Ready()
	if sts[1].OK || !strings.Contains(sts[1].Err, "deadline") {
		t.Fatalf("status = %+v, want a deadline failure", sts[1])
	}
}
