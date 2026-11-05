package daemon_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/liquidmetal-dev/muster/internal/config"
	"github.com/liquidmetal-dev/muster/internal/daemon"
	"github.com/liquidmetal-dev/muster/internal/fakegithub"
	"github.com/liquidmetal-dev/muster/internal/vmsource/battery/fakepoolmgr"
	"github.com/liquidmetal-dev/muster/internal/vmsource/flintlock/fakeexec"
)

const (
	testNS        = "muster"
	repoA         = "liquidmetal-dev/muster"
	poolName      = "linux"
	patToken      = "ghp_testtoken"
	webhookSecret = "s3cret"
)

var labels = []string{"self-hosted", "linux"}

type fakes struct {
	GitHub    *fakegithub.Server
	GitHubURL string
	Pool      *fakepoolmgr.Server
	PoolAddr  string
	Exec      *fakeexec.Server
}

func listen(t *testing.T) net.Listener {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return lis
}

func newFakes(t *testing.T) *fakes {
	t.Helper()
	f := &fakes{Exec: fakeexec.New(), Pool: fakepoolmgr.New()}
	f.Exec.SetServerInfo("v1.0.0", true)
	execLis := listen(t)
	go func() { _ = f.Exec.Serve(execLis) }()
	t.Cleanup(f.Exec.Stop)

	poolLis := listen(t)
	f.PoolAddr = poolLis.Addr().String()
	go func() { _ = f.Pool.Serve(poolLis) }()
	t.Cleanup(f.Pool.Stop)
	f.Pool.SeedHost(fakepoolmgr.Host{Name: "h1", Address: execLis.Addr().String()})

	f.GitHub = fakegithub.New(fakegithub.Config{PATs: map[string]string{patToken: "octocat"}})
	f.GitHub.AddRepo(repoA)
	f.GitHubURL = f.GitHub.Start(t)
	return f
}

// configJSON is the configuration as a JSON object, for tests to change
// before it is parsed.
func (f *fakes) configJSON() map[string]any {
	return map[string]any{
		"listen":         map[string]any{"addr": "127.0.0.1:1"},
		"metrics_addr":   "127.0.0.1:1",
		"shutdown_grace": "1s",
		"github": map[string]any{ //nolint:gosec // the _env keys name variables and hold no secret
			"api_base_url":       f.GitHubURL,
			"token_env":          "MUSTER_TEST_TOKEN",
			"webhook_secret_env": "MUSTER_TEST_WEBHOOK_SECRET",
		},
		"battery":   map[string]any{"addr": f.PoolAddr, "tls": map[string]any{"insecure": true}, "namespace": testNS},
		"flintlock": map[string]any{"tls": map[string]any{"insecure": true}},
		"pools": []any{map[string]any{
			"name":            poolName,
			"scope":           map[string]any{"repo": repoA},
			"labels":          labels,
			"size":            1,
			"max_concurrent":  2,
			"flintlock_hosts": []any{"h1"},
			"microvm_template": map[string]any{
				"vcpu":       2,
				"memoryInMb": 2048,
				"kernel":     map[string]any{"image": "ghcr.io/liquidmetal-dev/firecracker-kernel:6.1", "filename": "boot/vmlinux"},
				"rootVolume": map[string]any{"id": "root", "source": map[string]any{"containerSource": "ghcr.io/liquidmetal-dev/muster-runner:minimal-latest"}},
				"interfaces": []any{map[string]any{"deviceId": "eth1", "type": "TAP"}},
			},
		}},
	}
}

func parseConfig(t *testing.T, m map[string]any) *config.Config {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("config.Parse: %v", err)
	}
	cfg.GitHub.Token = config.NewSecret([]byte(patToken))
	cfg.GitHub.WebhookSecret = config.NewSecret([]byte(webhookSecret))
	return cfg
}

func configSecret(s string) config.Secret { return config.NewSecret([]byte(s)) }

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

type running struct {
	t          *testing.T
	opsURL     string
	webhookURL string
	sig        chan os.Signal
	done       chan error
	logs       *syncBuffer

	once sync.Once
}

type startOptions struct {
	mutate   func(map[string]any)
	options  func(*daemon.Options)
	noListen bool
}

func (f *fakes) start(t *testing.T, so startOptions) *running {
	t.Helper()
	m := f.configJSON()
	if so.mutate != nil {
		so.mutate(m)
	}
	cfg := parseConfig(t, m)
	r := &running{t: t, sig: make(chan os.Signal, 2), done: make(chan error, 1), logs: &syncBuffer{}}
	opsLis := listen(t)
	r.opsURL = "http://" + opsLis.Addr().String()
	opts := daemon.Options{
		Config:      cfg,
		Logger:      slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Signals:     r.sig,
		OpsListener: opsLis,
	}
	if cfg.Listen != nil && !so.noListen {
		whLis := listen(t)
		r.webhookURL = "http://" + whLis.Addr().String()
		opts.WebhookListener = whLis
	}
	if so.options != nil {
		so.options(&opts)
	}
	go func() { r.done <- daemon.Run(t.Context(), opts) }()
	t.Cleanup(func() {
		for range 2 {
			select {
			case r.sig <- syscall.SIGTERM:
			default:
			}
		}
		if ok, _ := r.wait(10 * time.Second); !ok {
			t.Errorf("daemon.Run did not return at cleanup; logs:\n%s", r.logs)
		}
	})
	return r
}

func (r *running) signal() { r.sig <- syscall.SIGTERM }

// wait reports whether Run returned within d, and Run's result.
func (r *running) wait(d time.Duration) (bool, error) {
	select {
	case err := <-r.done:
		r.once.Do(func() { close(r.done) })
		return true, err
	case <-time.After(d):
		return false, nil
	}
}

func (r *running) mustReturn(d time.Duration) error {
	r.t.Helper()
	ok, err := r.wait(d)
	if !ok {
		r.t.Fatalf("daemon.Run did not return within %s; logs:\n%s", d, r.logs)
	}
	return err
}

func get(url string) (int, string, error) {
	resp, err := http.Get(url) //nolint:gosec // the URL is a test listener
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	var b bytes.Buffer
	_, _ = b.ReadFrom(resp.Body)
	return resp.StatusCode, b.String(), nil
}

func (r *running) waitReady() {
	r.t.Helper()
	waitFor(r.t, 10*time.Second, func() bool {
		code, _, err := get(r.opsURL + "/readyz")
		return err == nil && code == http.StatusOK
	}, func() string { return "musterd did not become ready; logs:\n" + r.logs.String() })
}

func waitFor(t *testing.T, d time.Duration, cond func() bool, msg func() string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(msg())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func set(m map[string]any, path string, v any) {
	keys := strings.Split(path, ".")
	for _, k := range keys[:len(keys)-1] {
		m = m[k].(map[string]any)
	}
	m[keys[len(keys)-1]] = v
}

// tripListener is a listener whose Accept fails once trip is called.
type tripListener struct {
	net.Listener
	tripped chan struct{}
	once    sync.Once
}

func (l *tripListener) trip() { l.once.Do(func() { close(l.tripped) }) }

func (l *tripListener) Accept() (net.Conn, error) {
	<-l.tripped
	return nil, errors.New("listener tripped")
}
