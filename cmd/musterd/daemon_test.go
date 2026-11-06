package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/liquidmetal-dev/muster/internal/fakegithub"
	"github.com/liquidmetal-dev/muster/internal/vmsource/battery/fakepoolmgr"
	"github.com/liquidmetal-dev/muster/internal/vmsource/flintlock/fakeexec"
)

const (
	testRepo  = "liquidmetal-dev/muster"
	testToken = "ghp_testtoken"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func tcpListener(t *testing.T) net.Listener {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return lis
}

// freeAddr returns an address that was free a moment ago.
func freeAddr(t *testing.T) string {
	t.Helper()
	lis := tcpListener(t)
	addr := lis.Addr().String()
	_ = lis.Close()
	return addr
}

// daemonConfig starts the fakes and writes a configuration for them. It
// returns the path, the metrics address and the fake battery, which is nil
// when batteryAddr is set.
func daemonConfig(t *testing.T, batteryAddr string) (path, metricsAddr string, pm *fakepoolmgr.Server) {
	t.Helper()
	ex := fakeexec.New()
	ex.SetServerInfo("v1.0.0", true)
	exLis := tcpListener(t)
	go func() { _ = ex.Serve(exLis) }()
	t.Cleanup(ex.Stop)

	if batteryAddr == "" {
		pm = fakepoolmgr.New()
		pmLis := tcpListener(t)
		go func() { _ = pm.Serve(pmLis) }()
		t.Cleanup(pm.Stop)
		pm.SeedHost(fakepoolmgr.Host{Name: "h1", Address: exLis.Addr().String()})
		batteryAddr = pmLis.Addr().String()
	}

	gh := fakegithub.New(fakegithub.Config{PATs: map[string]string{testToken: "octocat"}})
	gh.AddRepo(testRepo)
	ghURL := gh.Start(t)

	t.Setenv("MUSTER_TEST_TOKEN", testToken)
	t.Setenv("MUSTER_TEST_WEBHOOK_SECRET", "wh")
	metricsAddr = freeAddr(t)
	m := map[string]any{
		"listen":         map[string]any{"addr": freeAddr(t)},
		"metrics_addr":   metricsAddr,
		"shutdown_grace": "1s",
		"github": map[string]any{ //nolint:gosec // the _env keys name variables and hold no secret
			"api_base_url":       ghURL,
			"token_env":          "MUSTER_TEST_TOKEN",
			"webhook_secret_env": "MUSTER_TEST_WEBHOOK_SECRET",
		},
		"battery":   map[string]any{"addr": batteryAddr, "tls": map[string]any{"insecure": true}, "namespace": "muster"},
		"flintlock": map[string]any{"tls": map[string]any{"insecure": true}},
		"pools": []any{map[string]any{
			"name":            "linux",
			"scope":           map[string]any{"repo": testRepo},
			"labels":          []any{"self-hosted", "linux"},
			"size":            1,
			"max_concurrent":  1,
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
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(t.TempDir(), "musterd.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, metricsAddr, pm
}

// startRun runs musterd with args and waits until /readyz returns 200.
func startRun(t *testing.T, args []string, metricsAddr string) (code <-chan int, stderr *lockedBuffer) {
	t.Helper()
	errb := &lockedBuffer{}
	c := make(chan int, 1)
	go func() { c <- run(args, io.Discard, errb) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := http.Get("http://" + metricsAddr + "/readyz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("musterd did not become ready; stderr:\n%s", errb)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return c, errb
}

// stopRun sends SIGTERM to the test process and returns run's exit code.
func stopRun(t *testing.T, code <-chan int, errb *lockedBuffer) int {
	t.Helper()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case c := <-code:
		return c
	case <-time.After(5 * time.Second):
		t.Fatalf("run did not return after SIGTERM; stderr:\n%s", errb)
		return -1
	}
}

func TestRunServesUntilSIGTERM(t *testing.T) {
	path, metricsAddr, _ := daemonConfig(t, "")
	code, errb := startRun(t, []string{"-config", path}, metricsAddr)
	if c := stopRun(t, code, errb); c != 0 {
		t.Fatalf("exit code = %d, want 0; stderr:\n%s", c, errb)
	}
	if !strings.Contains(errb.String(), "configuration loaded") {
		t.Fatalf("stderr = %q, want the configuration loaded log", errb)
	}
}

func TestRunPrunePoolsFlag(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want int
	}{{nil, 0}, {[]string{"-prune-pools"}, 1}} {
		t.Run(fmt.Sprintf("%v", tc.args), func(t *testing.T) {
			path, metricsAddr, pm := daemonConfig(t, "")
			pm.AddPool(fakepoolmgr.Pool{Name: "orphan", Namespace: "muster"})
			code, errb := startRun(t, append([]string{"-config", path}, tc.args...), metricsAddr)
			n := pm.CallCount("DeletePool")
			if c := stopRun(t, code, errb); c != 0 {
				t.Fatalf("exit code = %d, want 0; stderr:\n%s", c, errb)
			}
			if n != tc.want {
				t.Fatalf("DeletePool calls = %d, want %d", n, tc.want)
			}
		})
	}
}

func TestRunStartupFailureExits1(t *testing.T) {
	path, _, _ := daemonConfig(t, freeAddr(t))
	var out bytes.Buffer
	errb := &lockedBuffer{}
	code := make(chan int, 1)
	go func() { code <- run([]string{"-config", path}, &out, errb) }()
	select {
	case c := <-code:
		if c != 1 || !strings.Contains(errb.String(), "startup:") {
			t.Fatalf("exit code = %d, stderr = %q; want 1 and a startup error", c, errb)
		}
	case <-time.After(60 * time.Second):
		t.Fatalf("run did not return; stderr:\n%s", errb)
	}
}
