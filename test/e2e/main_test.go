//go:build e2e

// Package e2e runs musterd against real flintlockd and poolmgrd processes and the fake
// GitHub. Prepare the host with hack/e2e/provision.sh first; see docs/runbooks/e2e-suite.md.
package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	poolmgrv1alpha1 "github.com/liquidmetal-dev/battery/api/proto/poolmgr/v1alpha1"
	flintlockv1 "github.com/liquidmetal-dev/flintlock/api/services/microvm/v1alpha1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	envFile  = "/run/muster-e2e/env"
	hostName = "e2e-host"
)

// stack is the infrastructure that every e2e scenario shares.
type stack struct {
	env           map[string]string
	logDir        string
	musterd       string
	flintlockAddr string
	batteryAddr   string

	leases    poolmgrv1alpha1.LeaseClient
	pools     poolmgrv1alpha1.PoolAdminClient
	microvms  flintlockv1.MicroVMClient
	conns     []*grpc.ClientConn
	flintlock *daemon
	poolmgr   *daemon
}

var infra *stack

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	env, err := readEnv(envFile)
	if errors.Is(err, fs.ErrNotExist) {
		if os.Getenv("E2E_REQUIRED") == "1" {
			fmt.Fprintf(os.Stderr, "e2e: %s is missing; run hack/e2e/provision.sh\n", envFile)
			return 1
		}
		fmt.Printf("e2e: skipped, %s is missing; run hack/e2e/provision.sh\n", envFile)
		return 0
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: %v\n", err)
		return 1
	}
	s, err := startStack(env)
	if s != nil {
		defer s.stop()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: %v\n", err)
		return 1
	}
	infra = s
	return m.Run()
}

func readEnv(path string) (map[string]string, error) {
	f, err := os.Open(path) //nolint:gosec // the env file provision.sh writes
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	env := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), "="); ok {
			env[k] = v
		}
	}
	return env, sc.Err()
}

func startStack(env map[string]string) (*stack, error) {
	s := &stack{env: env, logDir: os.Getenv("E2E_LOG_DIR")}
	if s.logDir == "" {
		dir, err := os.MkdirTemp("", "muster-e2e-logs-")
		if err != nil {
			return nil, err
		}
		s.logDir = dir
	}
	if err := os.MkdirAll(s.logDir, 0o750); err != nil { //nolint:gosec // E2E_LOG_DIR is set by whoever runs the suite
		return nil, err
	}
	fmt.Printf("e2e: daemon logs in %s\n", s.logDir)

	work, err := os.MkdirTemp("", "muster-e2e-")
	if err != nil {
		return nil, err
	}
	s.musterd = filepath.Join(work, "musterd")
	build := exec.Command("go", "build", "-o", s.musterd, "../../cmd/musterd") //nolint:gosec // s.musterd is in a temp dir of the suite
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		return s, fmt.Errorf("build musterd: %w", err)
	}

	bin, runDir, stateDir := env["E2E_BIN_DIR"], env["E2E_RUN_DIR"], env["E2E_STATE_DIR"]
	if s.flintlockAddr, err = freeAddr(); err != nil {
		return s, err
	}
	s.flintlock, err = startDaemon("flintlockd", s.logDir, runDir, nil,
		filepath.Join(bin, "flintlockd"), "run",
		"--enable-exec-api", "--insecure",
		"--grpc-endpoint", s.flintlockAddr,
		"--bridge-name", env["E2E_BRIDGE"],
		"--containerd-socket", env["E2E_CONTAINERD_SOCKET"],
		"--containerd-hosts-dir", env["E2E_HOSTS_DIR"],
		"--firecracker-bin", filepath.Join(bin, "firecracker"),
		"--state-dir", filepath.Join(stateDir, "flintlock"),
		"--socket-dir", filepath.Join(runDir, "flintlock"),
		"--verbosity", "2")
	if err != nil {
		return s, err
	}

	if s.batteryAddr, err = freeAddr(); err != nil {
		return s, err
	}
	metricsAddr, err := freeAddr()
	if err != nil {
		return s, err
	}
	pmConfig := filepath.Join(work, "poolmgrd.json")
	cfg, _ := json.Marshal(map[string]any{
		"api_server":     map[string]any{"addr": s.batteryAddr, "tls": map[string]any{"insecure": true}},
		"metrics_addr":   metricsAddr,
		"sweep_interval": "2s",
	})
	if err := os.WriteFile(pmConfig, cfg, 0o600); err != nil {
		return s, err
	}
	s.poolmgr, err = startDaemon("poolmgrd", s.logDir, runDir, nil,
		filepath.Join(bin, "poolmgrd"), "-config", pmConfig, "-db", filepath.Join(work, "poolmgr.db"), "-log-level", "debug")
	if err != nil {
		return s, err
	}

	batteryConn, err := grpc.NewClient(s.batteryAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return s, err
	}
	flConn, err := grpc.NewClient(s.flintlockAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return s, err
	}
	s.conns = append(s.conns, batteryConn, flConn)
	s.leases = poolmgrv1alpha1.NewLeaseClient(batteryConn)
	s.pools = poolmgrv1alpha1.NewPoolAdminClient(batteryConn)
	s.microvms = flintlockv1.NewMicroVMClient(flConn)

	hosts := poolmgrv1alpha1.NewHostAdminClient(batteryConn)
	var addErr error
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(time.Second) {
		if err := unexpectedExit(); err != nil {
			return s, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, addErr = hosts.AddHost(ctx, &poolmgrv1alpha1.AddHostRequest{Host: &poolmgrv1alpha1.Host{
			Name: hostName, Address: s.flintlockAddr, Tls: &poolmgrv1alpha1.HostTLS{Insecure: true},
		}})
		cancel()
		if addErr == nil {
			return s, nil
		}
	}
	return s, fmt.Errorf("register flintlock host in battery: %w", addErr)
}

func (s *stack) stop() {
	for _, c := range s.conns {
		_ = c.Close()
	}
	for _, d := range []*daemon{s.poolmgr, s.flintlock} {
		if d != nil {
			_ = d.Stop(syscall.SIGTERM, 10*time.Second)
		}
	}
}

// freeAddr returns a loopback address with a port that was free a moment ago.
func freeAddr() (string, error) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer func() { _ = lis.Close() }()
	return lis.Addr().String(), nil
}
