//go:build e2e

package e2e

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// exited starts a daemon that exits at once with the code and waits for the exit.
func exited(t *testing.T, code string) *daemon {
	t.Helper()
	d, err := startDaemon("exits-"+code, t.TempDir(), "", nil, "/bin/sh", "-c", "exit "+code)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Wait(10 * time.Second); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestUnexpectedExitClearsOnceReported(t *testing.T) {
	d := exited(t, "3")
	if err := unexpectedExit(); err == nil {
		t.Fatal("unexpectedExit = nil, want the exit of the daemon")
	}
	d.reported()
	if err := unexpectedExit(); err != nil {
		t.Fatalf("after reported, unexpectedExit = %v, want nil", err)
	}
}

func TestUnexpectedExitZeroMessage(t *testing.T) {
	d := exited(t, "0")
	defer d.reported()
	err := unexpectedExit()
	if err == nil || strings.Contains(err.Error(), "%!") || !strings.Contains(err.Error(), "exit status 0") {
		t.Fatalf("unexpectedExit = %v, want a message with exit status 0", err)
	}
}

func TestRPCFailureNamesDeadDaemon(t *testing.T) {
	d := exited(t, "1")
	defer d.reported()
	err := rpcFailure("ListLeases", errors.New("rpc error: code = Unavailable"))
	if !strings.Contains(err.Error(), "exited unexpectedly") || !strings.Contains(err.Error(), "ListLeases") {
		t.Fatalf("rpcFailure = %v, want the RPC and the daemon exit", err)
	}
}
