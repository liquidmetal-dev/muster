//go:build e2e

package e2e

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// daemon is one subprocess of the suite, with its output in a log file.
type daemon struct {
	name    string
	logPath string
	cmd     *exec.Cmd
	done    chan struct{}
	err     error
	// expected is set before the test stops or kills the process, so its exit is no failure.
	expected atomic.Bool
}

var (
	daemonsMu  sync.Mutex
	daemons    []*daemon
	logCounter = map[string]int{}
)

// startDaemon runs args[0] with the rest of args and the extra env, and writes its output to
// <logDir>/<name>-<n>.log. With a pidDir, it also writes <pidDir>/<name>.pid for cleanup.sh.
func startDaemon(name, logDir, pidDir string, env []string, args ...string) (*daemon, error) {
	daemonsMu.Lock()
	logCounter[name]++
	n := logCounter[name]
	daemonsMu.Unlock()

	logPath := filepath.Join(logDir, fmt.Sprintf("%s-%d.log", name, n))
	logFile, err := os.Create(logPath) //nolint:gosec // a path in the suite's log dir
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(args[0], args[1:]...) //nolint:gosec // the suite starts its own pinned binaries
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("start %s: %w", name, err)
	}
	if pidDir != "" {
		_ = os.WriteFile(filepath.Join(pidDir, name+".pid"), []byte(fmt.Sprint(cmd.Process.Pid)), 0o600)
	}
	d := &daemon{name: name, logPath: logPath, cmd: cmd, done: make(chan struct{})}
	go func() {
		d.err = cmd.Wait()
		_ = logFile.Close()
		close(d.done)
	}()
	daemonsMu.Lock()
	daemons = append(daemons, d)
	daemonsMu.Unlock()
	return d, nil
}

// Stop sends sig. With a zero timeout it returns at once; otherwise it waits up to timeout
// and then kills the process.
func (d *daemon) Stop(sig os.Signal, timeout time.Duration) error {
	d.expected.Store(true)
	if err := d.cmd.Process.Signal(sig); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	if timeout == 0 {
		return nil
	}
	select {
	case <-d.done:
		return nil
	case <-time.After(timeout):
		return d.Kill()
	}
}

// Kill sends SIGKILL and waits for the exit.
func (d *daemon) Kill() error {
	d.expected.Store(true)
	if err := d.cmd.Process.Signal(syscall.SIGKILL); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	<-d.done
	return nil
}

// Wait waits up to timeout for the exit and returns the exit code.
func (d *daemon) Wait(timeout time.Duration) (int, error) {
	select {
	case <-d.done:
		return d.cmd.ProcessState.ExitCode(), nil
	case <-time.After(timeout):
		return -1, fmt.Errorf("%s did not exit within %s", d.name, timeout)
	}
}

// Exited reports whether the process has exited, and the error Wait returned.
func (d *daemon) Exited() (bool, error) {
	select {
	case <-d.done:
		return true, d.err
	default:
		return false, nil
	}
}

func (d *daemon) running() bool {
	exited, _ := d.Exited()
	return !exited
}

// LogTail returns the last n lines of the daemon's log.
func (d *daemon) LogTail(n int) string {
	b, err := os.ReadFile(d.logPath)
	if err != nil {
		return fmt.Sprintf("(%s: %v)", d.logPath, err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// unexpectedExit returns an error for the first daemon that exited without the test asking.
func unexpectedExit() error {
	daemonsMu.Lock()
	defer daemonsMu.Unlock()
	for _, d := range daemons {
		if exited, err := d.Exited(); exited && !d.expected.Load() {
			return fmt.Errorf("%s exited unexpectedly (%w); log %s:\n%s", d.name, err, d.logPath, d.LogTail(50))
		}
	}
	return nil
}

// lastDaemon returns the most recently started daemon with the name, or nil.
func lastDaemon(name string) *daemon {
	daemonsMu.Lock()
	defer daemonsMu.Unlock()
	for i := len(daemons) - 1; i >= 0; i-- {
		if daemons[i].name == name {
			return daemons[i]
		}
	}
	return nil
}
