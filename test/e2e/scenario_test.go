//go:build e2e

package e2e

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	poolmgrv1alpha1 "github.com/liquidmetal-dev/battery/api/proto/poolmgr/v1alpha1"
	flintlockv1 "github.com/liquidmetal-dev/flintlock/api/services/microvm/v1alpha1"
	flintlocktypes "github.com/liquidmetal-dev/flintlock/api/types"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/liquidmetal-dev/muster/internal/fakegithub"
)

func TestHarness(t *testing.T) {
	s := newScenario(t, scenarioOpts{PoolSize: 1})
	s.StartMusterd()

	waitFor(t, 5*time.Minute, "one warm VM in the pool", func() bool {
		vms := s.MicroVMs()
		return len(vms) == 1 && vms[0].GetStatus().GetState() == flintlocktypes.MicroVMStatus_CREATED && len(s.Leases()) == 0
	})
	if got := s.Metric("muster_ready", map[string]string{"check": "battery"}); got != 1 {
		t.Fatalf("muster_ready{check=battery} = %v, want 1", got)
	}
	battery, github := s.Metric("muster_ready", map[string]string{"check": "battery"}), s.Metric("muster_ready", map[string]string{"check": "github"})
	if all := s.Metric("muster_ready", nil); all < battery+github {
		t.Fatalf("muster_ready summed over check = %v, want at least %v + %v", all, battery, github)
	}
	if none := s.Metric("muster_ready", map[string]string{"check": "no-such-check"}); none != 0 {
		t.Fatalf("muster_ready{check=no-such-check} = %v, want 0", none)
	}
}

const (
	e2eToken = "ghp_e2e"
	e2eRepo  = "e2e-org/repo"
	e2ePool  = "p"
)

var jobLabels = []string{"self-hosted", "muster-e2e"}

type scenarioOpts struct {
	PoolSize        int
	MaxConcurrent   int
	ShutdownGrace   time.Duration
	DefaultBehavior fakegithub.Behavior
}

// scenario is one e2e scenario's fake GitHub and musterd, in a battery namespace of its own.
type scenario struct {
	t    *testing.T
	opts scenarioOpts

	GH        *fakegithub.Server
	Namespace string
	Pool      string
	Repo      string
	Musterd   *daemon

	dir           string
	webhookSecret string
	listenAddr    string
	metricsAddr   string
}

func newScenario(t *testing.T, opts scenarioOpts) *scenario {
	t.Helper()
	if infra == nil {
		t.Fatal("e2e: the shared stack did not start")
	}
	if opts.PoolSize == 0 {
		opts.PoolSize = 1
	}
	if opts.MaxConcurrent == 0 {
		opts.MaxConcurrent = opts.PoolSize
	}
	if opts.ShutdownGrace == 0 {
		opts.ShutdownGrace = 30 * time.Second
	}
	s := &scenario{
		t:             t,
		opts:          opts,
		Namespace:     "e2e-" + strings.ToLower(t.Name()) + "-" + randHex(2),
		Pool:          e2ePool,
		Repo:          e2eRepo,
		dir:           t.TempDir(),
		webhookSecret: randHex(16),
	}
	var err error
	if s.listenAddr, err = freeAddr(); err != nil {
		t.Fatal(err)
	}
	if s.metricsAddr, err = freeAddr(); err != nil {
		t.Fatal(err)
	}

	s.GH = fakegithub.New(fakegithub.Config{
		PATs:          map[string]string{e2eToken: "e2e"},
		WebhookURL:    "http://" + s.listenAddr + "/webhook",
		WebhookSecret: s.webhookSecret,
	})
	s.GH.AddRepo(s.Repo)
	if opts.DefaultBehavior != (fakegithub.Behavior{}) {
		s.GH.SetDefaultBehavior(opts.DefaultBehavior)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: s.GH.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(lis) }()

	cfg, _ := json.Marshal(map[string]any{
		"listen":         map[string]any{"addr": s.listenAddr},
		"metrics_addr":   s.metricsAddr,
		"shutdown_grace": opts.ShutdownGrace.String(),
		"log":            map[string]any{"format": "text", "level": "debug"},
		"github": map[string]any{ //nolint:gosec // the _env keys name variables and hold no secret
			"api_base_url":       "http://" + lis.Addr().String(),
			"token_env":          "MUSTER_E2E_TOKEN",
			"webhook_secret_env": "MUSTER_E2E_WEBHOOK_SECRET",
		},
		"battery":   map[string]any{"addr": infra.batteryAddr, "tls": map[string]any{"insecure": true}, "namespace": s.Namespace},
		"flintlock": map[string]any{"tls": map[string]any{"insecure": true}},
		// The long heartbeat expiry keeps battery from expiring a lease that musterd should
		// release, so a scenario sees musterd's release and not the expiry.
		"defaults": map[string]any{
			"start_timeout": "2m", "idle_timeout": "1m", "claim_deadline": "3m",
			"heartbeat_interval": "30s", "heartbeat_expiry_threshold": "5m",
		},
		"pools": []any{map[string]any{
			"name":            s.Pool,
			"scope":           map[string]any{"repo": s.Repo},
			"labels":          jobLabels,
			"size":            opts.PoolSize,
			"max_concurrent":  opts.MaxConcurrent,
			"flintlock_hosts": []any{hostName},
			"microvm_template": map[string]any{
				"vcpu":       2,
				"memoryInMb": 1024,
				"kernel":     map[string]any{"image": infra.env["E2E_KERNEL_IMAGE"], "filename": "boot/vmlinux"},
				"rootVolume": map[string]any{"id": "root", "source": map[string]any{"containerSource": infra.env["E2E_IMAGE"]}},
				// flintlock requires an interface. The bridge has no DHCP, and guest-agent
				// talks over vsock, so the guest needs no address.
				"interfaces": []any{map[string]any{"deviceId": "eth1", "type": "TAP"}},
			},
		}},
	})
	if err := os.WriteFile(filepath.Join(s.dir, "musterd.json"), cfg, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		s.teardown()
		_ = srv.Close()
		s.GH.Close()
	})
	return s
}

// StartMusterd starts musterd and waits until it is ready.
func (s *scenario) StartMusterd() {
	s.t.Helper()
	d, err := startDaemon("musterd", infra.logDir, "",
		[]string{"MUSTER_E2E_TOKEN=" + e2eToken, "MUSTER_E2E_WEBHOOK_SECRET=" + s.webhookSecret},
		infra.musterd, "-config", filepath.Join(s.dir, "musterd.json"))
	if err != nil {
		s.t.Fatal(err)
	}
	s.Musterd = d
	waitFor(s.t, 90*time.Second, "musterd ready", func() bool { return s.ReadyStatus() == http.StatusOK })
}

// RestartMusterd starts musterd again after the test stopped or killed it.
func (s *scenario) RestartMusterd() {
	s.t.Helper()
	if s.Musterd != nil && s.Musterd.running() {
		s.t.Fatal("RestartMusterd: musterd is still running")
	}
	s.StartMusterd()
}

// ReadyStatus returns the status code of musterd's /readyz, or 0 when it does not answer.
func (s *scenario) ReadyStatus() int {
	resp, err := httpClient.Get("http://" + s.metricsAddr + "/readyz")
	if err != nil {
		return 0
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// Leases returns battery's leases for the scenario's pool.
func (s *scenario) Leases() []*poolmgrv1alpha1.LeaseRecord {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := infra.leases.ListLeases(ctx, &poolmgrv1alpha1.ListLeasesRequest{
		PoolRef: &poolmgrv1alpha1.PoolRef{Name: s.Pool, Namespace: s.Namespace},
	})
	if err != nil {
		s.t.Fatalf("ListLeases: %v", err)
	}
	return resp.GetLeases()
}

// MicroVMs returns flintlock's microVMs in the scenario's namespace, which battery uses as
// the flintlock namespace of its VMs.
func (s *scenario) MicroVMs() []*flintlocktypes.MicroVM {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := infra.microvms.ListMicroVMs(ctx, &flintlockv1.ListMicroVMsRequest{Namespace: s.Namespace})
	if err != nil {
		s.t.Fatalf("ListMicroVMs: %v", err)
	}
	return resp.GetMicrovm()
}

// HasMicroVM reports whether flintlock still has the microVM with the uid.
func (s *scenario) HasMicroVM(uid string) bool {
	for _, vm := range s.MicroVMs() {
		if vm.GetSpec().GetUid() == uid {
			return true
		}
	}
	return false
}

// Metric sums the series of the named metric whose labels include every given pair. It
// returns 0 when no series matches or musterd does not answer.
func (s *scenario) Metric(name string, labels map[string]string) float64 {
	resp, err := httpClient.Get("http://" + s.metricsAddr + "/metrics")
	if err != nil {
		return 0
	}
	defer func() { _ = resp.Body.Close() }()
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(resp.Body)
	if err != nil {
		s.t.Fatalf("parse /metrics: %v", err)
	}
	var sum float64
	for _, m := range families[name].GetMetric() {
		if !hasLabels(m, labels) {
			continue
		}
		switch {
		case m.GetCounter() != nil:
			sum += m.GetCounter().GetValue()
		case m.GetGauge() != nil:
			sum += m.GetGauge().GetValue()
		case m.GetHistogram() != nil:
			sum += float64(m.GetHistogram().GetSampleCount())
		}
	}
	return sum
}

func hasLabels(m *dto.Metric, want map[string]string) bool {
	have := map[string]string{}
	for _, l := range m.GetLabel() {
		have[l.GetName()] = l.GetValue()
	}
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

// Job returns the fake GitHub's current state of the job.
func (s *scenario) Job(id int64) fakegithub.Job {
	for _, j := range s.GH.Jobs() {
		if j.ID == id {
			return j
		}
	}
	s.t.Fatalf("job %d not found", id)
	return fakegithub.Job{}
}

// QueueJob queues a job with the pool's labels.
func (s *scenario) QueueJob() fakegithub.Job {
	s.t.Helper()
	j, err := s.GH.QueueJob(s.Repo, jobLabels)
	if err != nil {
		s.t.Fatalf("QueueJob: %v", err)
	}
	return j
}

// teardown stops musterd, then removes the scenario's pool and checks nothing is left.
func (s *scenario) teardown() {
	t := s.t
	if s.Musterd != nil && s.Musterd.running() {
		_ = s.Musterd.Stop(syscall.SIGTERM, s.opts.ShutdownGrace+30*time.Second)
	}
	if !poll(2*time.Minute, func() bool { return len(s.Leases()) == 0 }) {
		t.Errorf("teardown: leases left in %s: %v; daemon logs in %s", s.Namespace, s.Leases(), infra.logDir)
		for _, l := range s.Leases() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_, _ = infra.leases.ReleaseVM(ctx, &poolmgrv1alpha1.ReleaseVMRequest{LeaseId: l.GetLeaseId()})
			cancel()
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	_, err := infra.pools.DeletePool(ctx, &poolmgrv1alpha1.DeletePoolRequest{
		Ref: &poolmgrv1alpha1.PoolRef{Name: s.Pool, Namespace: s.Namespace},
	})
	cancel()
	if err != nil && status.Code(err) != codes.NotFound {
		t.Errorf("teardown: DeletePool %s/%s: %v", s.Namespace, s.Pool, err)
	}
	if !poll(2*time.Minute, func() bool { return len(s.MicroVMs()) == 0 }) {
		t.Errorf("teardown: microVMs left in %s: %d; daemon logs in %s", s.Namespace, len(s.MicroVMs()), infra.logDir)
	}
}

// waitFor polls cond every 500ms until it holds. It fails the test at once when a daemon exits
// that the test did not stop, and on timeout with the tail of the musterd and flintlockd logs.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if err := unexpectedExit(); err != nil {
			t.Fatalf("waiting for %s: %v", what, err)
		}
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s\n%s", timeout, what, logTails())
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// poll is waitFor without the test failure, for teardown.
func poll(timeout time.Duration, cond func() bool) bool {
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		if cond() {
			return true
		}
	}
	return cond()
}

func logTails() string {
	var b strings.Builder
	for _, name := range []string{"musterd", "flintlockd"} {
		if d := lastDaemon(name); d != nil {
			fmt.Fprintf(&b, "--- last 50 lines of %s (%s)\n%s\n", name, d.logPath, d.LogTail(50))
		}
	}
	return b.String()
}

var httpClient = &http.Client{Timeout: 5 * time.Second}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
