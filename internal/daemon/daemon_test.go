package daemon_test

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liquidmetal-dev/muster/internal/daemon"
	"github.com/liquidmetal-dev/muster/internal/vmsource/battery/fakepoolmgr"
)

func TestReadyAfterStartup(t *testing.T) {
	f := newFakes(t)
	r := f.start(t, startOptions{})
	r.waitReady()
	if code, _, err := get(r.opsURL + "/healthz"); err != nil || code != http.StatusOK {
		t.Fatalf("GET /healthz = %d, %v, want 200", code, err)
	}
	if n := f.Pool.CallCount("CreatePool"); n != 1 {
		t.Fatalf("CreatePool calls = %d, want 1", n)
	}
	if n := f.Pool.CallCount("ListLeases"); n < 1 {
		t.Fatalf("ListLeases calls = %d, want at least 1", n)
	}
}

func TestQueuedDeliveryReachesDemand(t *testing.T) {
	f := newFakes(t)
	r := f.start(t, startOptions{})
	r.waitReady()
	f.GitHub.SetWebhook(r.webhookURL+"/webhook", webhookSecret)
	if _, err := f.GitHub.QueueJob(repoA, labels); err != nil {
		t.Fatalf("QueueJob: %v", err)
	}
	ds := f.GitHub.Deliveries()
	if len(ds) == 0 {
		t.Fatal("no delivery was sent")
	}
	if d := ds[len(ds)-1]; d.Action != "queued" || d.Err != nil || d.Status < 200 || d.Status > 299 {
		t.Fatalf("delivery = action %q status %d err %v, want a 2xx queued delivery", d.Action, d.Status, d.Err)
	}
	waitFor(t, 10*time.Second, func() bool { return f.Pool.CallCount("ClaimVM") >= 1 },
		func() string { return "no ClaimVM after the queued delivery; logs:\n" + r.logs.String() })
}

func TestPolledJobReachesDemand(t *testing.T) {
	f := newFakes(t)
	if _, err := f.GitHub.QueueJob(repoA, labels); err != nil {
		t.Fatalf("QueueJob: %v", err)
	}
	r := f.start(t, startOptions{mutate: func(m map[string]any) {
		delete(m, "listen")
		delete(m["github"].(map[string]any), "webhook_secret_env")
		set(m, "github.poll", map[string]any{"enabled": true, "interval": "10s", "repos": []any{repoA}})
	}})
	waitFor(t, 10*time.Second, func() bool { return f.Pool.CallCount("ClaimVM") >= 1 },
		func() string { return "no ClaimVM after the poll; logs:\n" + r.logs.String() })
}

func TestSignalShutsDownCleanly(t *testing.T) {
	f := newFakes(t)
	r := f.start(t, startOptions{})
	r.waitReady()
	r.signal()
	if err := r.mustReturn(5 * time.Second); err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	for _, u := range []string{r.webhookURL, r.opsURL} {
		addr := strings.TrimPrefix(u, "http://")
		if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
			_ = c.Close()
			t.Errorf("dial %s after shutdown succeeded, want refused", addr)
		}
	}
}

func TestReadyz503DuringStartup(t *testing.T) {
	f := newFakes(t)
	f.Pool.Stall("ListLeases", 3*time.Second)
	r := f.start(t, startOptions{})
	waitFor(t, 5*time.Second, func() bool { return f.Pool.CallCount("ListLeases") >= 1 },
		func() string { return "session reconciliation did not start; logs:\n" + r.logs.String() })
	code, body, err := get(r.opsURL + "/readyz")
	if err != nil || code != http.StatusServiceUnavailable || !strings.Contains(body, `{"name":"started","ok":false`) {
		t.Fatalf("GET /readyz = %d %q %v, want 503 with started not ok", code, body, err)
	}
	if code, _, err := get(r.opsURL + "/healthz"); err != nil || code != http.StatusOK {
		t.Fatalf("GET /healthz = %d, %v, want 200", code, err)
	}
}

func TestSignalDuringStartupReturnsNil(t *testing.T) {
	f := newFakes(t)
	f.Pool.Stall("ListLeases", 30*time.Second)
	r := f.start(t, startOptions{})
	waitFor(t, 5*time.Second, func() bool { return f.Pool.CallCount("ListLeases") >= 1 },
		func() string { return "session reconciliation did not start; logs:\n" + r.logs.String() })
	r.signal()
	if err := r.mustReturn(5 * time.Second); err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
}

func TestGitHubAuthFailureFailsStartup(t *testing.T) {
	f := newFakes(t)
	r := f.start(t, startOptions{options: func(o *daemon.Options) {
		o.Config.GitHub.Token = configSecret("ghp_unknown")
	}})
	err := r.mustReturn(10 * time.Second)
	if err == nil || !strings.Contains(err.Error(), "startup: config check: github: ") {
		t.Fatalf("Run = %v, want a startup config check error naming github", err)
	}
}

func TestWebhookListenFailsStartup(t *testing.T) {
	f := newFakes(t)
	held := listen(t)
	t.Cleanup(func() { _ = held.Close() })
	addr := held.Addr().String()
	r := f.start(t, startOptions{noListen: true, mutate: func(m map[string]any) {
		set(m, "listen.addr", addr)
	}})
	sawReady := make(chan bool, 1)
	go func() {
		ready := false
		for range 100 {
			if code, _, err := get(r.opsURL + "/readyz"); err == nil && code == http.StatusOK {
				ready = true
			}
			time.Sleep(10 * time.Millisecond)
		}
		sawReady <- ready
	}()
	err := r.mustReturn(10 * time.Second)
	if err == nil || !strings.Contains(err.Error(), "startup: webhook listener") || !strings.Contains(err.Error(), addr) {
		t.Fatalf("Run = %v, want a startup webhook listener error naming %s", err, addr)
	}
	if <-sawReady {
		t.Fatal("/readyz returned 200 before the webhook listener failed")
	}
}

func TestRuntimeServeErrorExitsWithError(t *testing.T) {
	f := newFakes(t)
	trip := &tripListener{Listener: listen(t), tripped: make(chan struct{})}
	t.Cleanup(trip.trip)
	r := f.start(t, startOptions{noListen: true, options: func(o *daemon.Options) {
		o.WebhookListener = trip
	}})
	r.waitReady()
	trip.trip()
	err := r.mustReturn(5 * time.Second)
	if err == nil || !strings.Contains(err.Error(), "webhook listener") {
		t.Fatalf("Run = %v, want a webhook listener error", err)
	}
}

func TestPrunePoolsDeletesOrphans(t *testing.T) {
	for _, prune := range []bool{false, true} {
		t.Run(fmt.Sprintf("prune=%v", prune), func(t *testing.T) {
			f := newFakes(t)
			f.Pool.AddPool(fakepoolmgr.Pool{Name: "orphan", Namespace: testNS})
			r := f.start(t, startOptions{options: func(o *daemon.Options) { o.PrunePools = prune }})
			r.waitReady()
			want := 0
			if prune {
				want = 1
			}
			if n := f.Pool.CallCount("DeletePool"); n != want {
				t.Fatalf("DeletePool calls = %d, want %d", n, want)
			}
		})
	}
}

func TestAPIConfiguredWarns(t *testing.T) {
	f := newFakes(t)
	r := f.start(t, startOptions{mutate: func(m map[string]any) {
		m["api"] = map[string]any{"addr": "127.0.0.1:9443", "tls": map[string]any{"insecure": true}}
	}})
	r.waitReady()
	if logs := r.logs.String(); !strings.Contains(logs, "level=WARN") || !strings.Contains(logs, "gRPC API is not implemented yet") {
		t.Fatalf("logs have no WARN about the gRPC API:\n%s", logs)
	}
}

func TestNoListenStartsNoWebhookListener(t *testing.T) {
	f := newFakes(t)
	r := f.start(t, startOptions{mutate: func(m map[string]any) {
		delete(m, "listen")
		delete(m["github"].(map[string]any), "webhook_secret_env")
		set(m, "github.poll", map[string]any{"enabled": true, "interval": "10s", "repos": []any{repoA}})
	}})
	r.waitReady()
	if r.webhookURL != "" {
		t.Fatalf("webhookURL = %q, want none", r.webhookURL)
	}
}

// TestRequestDuringShutdownCreatesNoSession holds a queued delivery open
// across the first signal, so the handler hands off its job while the
// webhook listener shuts down.
func TestRequestDuringShutdownCreatesNoSession(t *testing.T) {
	f := newFakes(t)
	r := f.start(t, startOptions{})
	r.waitReady()

	conn, last := holdDelivery(t, r)
	time.Sleep(100 * time.Millisecond)
	r.signal()
	time.Sleep(200 * time.Millisecond)
	if _, err := conn.Write(last); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		t.Fatalf("delivery status = %d, want 2xx", resp.StatusCode)
	}
	if err := r.mustReturn(10 * time.Second); err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if n := f.Pool.CallCount("ClaimVM"); n != 0 {
		t.Fatalf("ClaimVM calls = %d, want 0 after the first signal", n)
	}
}

// holdDelivery sends a signed queued delivery without its last byte and
// returns the connection and that byte.
func holdDelivery(t *testing.T, r *running) (net.Conn, []byte) {
	t.Helper()
	body := fmt.Appendf(nil, `{"action":"queued","workflow_job":{"id":4242,"labels":["self-hosted","linux"],"created_at":"2026-11-05T12:00:00Z"},"repository":{"full_name":%q}}`, repoA)
	mac := hmac.New(sha256.New, []byte(webhookSecret))
	mac.Write(body)
	conn, err := net.Dial("tcp", strings.TrimPrefix(r.webhookURL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	head := fmt.Sprintf("POST /webhook HTTP/1.1\r\nHost: musterd\r\nContent-Type: application/json\r\n"+
		"X-GitHub-Event: workflow_job\r\nX-GitHub-Delivery: 7f0d4b3e-0000-4000-8000-000000000001\r\n"+
		"X-Hub-Signature-256: sha256=%s\r\nContent-Length: %d\r\n\r\n", hex.EncodeToString(mac.Sum(nil)), len(body))
	if _, err := conn.Write(append([]byte(head), body[:len(body)-1]...)); err != nil {
		t.Fatal(err)
	}
	return conn, body[len(body)-1:]
}

func TestSecondSignalEndsListenerWait(t *testing.T) {
	f := newFakes(t)
	r := f.start(t, startOptions{})
	r.waitReady()
	holdDelivery(t, r)
	time.Sleep(100 * time.Millisecond)
	r.signal()
	time.Sleep(100 * time.Millisecond)
	r.signal()
	if err := r.mustReturn(2 * time.Second); err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
}

func TestOpsListenerFailureDuringStartup(t *testing.T) {
	f := newFakes(t)
	f.Pool.Stall("ListLeases", 30*time.Second)
	trip := &tripListener{Listener: listen(t), tripped: make(chan struct{})}
	t.Cleanup(trip.trip)
	r := f.start(t, startOptions{options: func(o *daemon.Options) { o.OpsListener = trip }})
	waitFor(t, 5*time.Second, func() bool { return f.Pool.CallCount("ListLeases") >= 1 },
		func() string { return "session reconciliation did not start; logs:\n" + r.logs.String() })
	trip.trip()
	err := r.mustReturn(5 * time.Second)
	if err == nil || !strings.Contains(err.Error(), "ops listener") {
		t.Fatalf("Run = %v, want an ops listener error", err)
	}
}

func TestShutdownGraceBoundsListenerWait(t *testing.T) {
	f := newFakes(t)
	r := f.start(t, startOptions{})
	r.waitReady()
	holdDelivery(t, r)
	time.Sleep(100 * time.Millisecond)
	start := time.Now()
	r.signal()
	if err := r.mustReturn(5 * time.Second); err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("shutdown took %s, want it within the 1s shutdown grace plus margin", d)
	}
}
