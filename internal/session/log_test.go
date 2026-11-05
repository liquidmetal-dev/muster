package session_test

import (
	"encoding/json"
	"strings"
	"testing"

	poolmgrv1alpha1 "github.com/liquidmetal-dev/battery/api/proto/poolmgr/v1alpha1"

	"github.com/liquidmetal-dev/muster/internal/fakegithub"
	"github.com/liquidmetal-dev/muster/internal/session"
)

// logRecord waits for a JSON log line with msg and returns it decoded.
func (e *env) logRecord(msg string) map[string]any {
	e.t.Helper()
	var rec map[string]any
	waitFor(e.t, func() bool {
		for line := range strings.SplitSeq(e.Logs.String(), "\n") {
			var r map[string]any
			if json.Unmarshal([]byte(line), &r) == nil && r["msg"] == msg {
				rec = r
				return true
			}
		}
		return false
	})
	return rec
}

// logLine returns the first JSON log line that contains fragment, or nil.
func (e *env) logLine(fragment string) map[string]any {
	for line := range strings.SplitSeq(e.Logs.String(), "\n") {
		var r map[string]any
		if strings.Contains(line, fragment) && json.Unmarshal([]byte(line), &r) == nil {
			return r
		}
	}
	return nil
}

func wantSessionKeys(t *testing.T, rec map[string]any, id string, keys ...string) {
	t.Helper()
	if rec["session_id"] != id {
		t.Errorf("session_id = %v, want %s in %v", rec["session_id"], id, rec)
	}
	for _, k := range append([]string{"pool", "runner_name"}, keys...) {
		if _, ok := rec[k]; !ok {
			t.Errorf("key %q missing in %v", k, rec)
		}
	}
	for _, k := range []string{"session", "runner", "vm"} {
		if _, ok := rec[k]; ok {
			t.Errorf("old key %q present in %v", k, rec)
		}
	}
}

func TestSessionLogFields(t *testing.T) {
	e := newEnv(t)
	c := e.capture()
	e.start()
	id, _, _ := e.running(c)
	wantSessionKeys(t, e.logRecord("runner online"), id, "vm_id")
}

func TestVMLostLogFields(t *testing.T) {
	e := newEnv(t)
	c := e.capture()
	e.start()
	e.waitSubscribed()
	id, _, _ := e.running(c)
	i, _ := e.info(id)
	e.Pool.DeleteVM(testNS, testPool, i.VMID, poolmgrv1alpha1.EventType_VM_DELETED_DUE_TO_EXPIRY)
	wantSessionKeys(t, e.logRecord("VM lost"), id, "vm_id")
}

func TestVMExpiringLogFields(t *testing.T) {
	e := newEnv(t)
	c := e.capture()
	e.start()
	e.waitSubscribed()
	id, _, _ := e.running(c)
	i, _ := e.info(id)
	e.Pool.Emit(&poolmgrv1alpha1.Event{PoolName: testPool, PoolNamespace: testNS, VmUid: i.VMID, Type: poolmgrv1alpha1.EventType_VM_EXPIRING_SOON})
	wantSessionKeys(t, e.logRecord("VM lease expiring"), id, "vm_id")
}

func TestVMLostDuringLaunchLogged(t *testing.T) {
	e := newEnv(t)
	release := e.Exec.HoldServerInfo()
	defer release()
	e.start()
	e.waitSubscribed()
	id := e.create(testPool)
	waitFor(t, func() bool { return e.GitHub.RequestCount(fakegithub.PatternRepoJITConfig) == 1 })
	e.waitState(id, session.Starting)
	i, _ := e.info(id)
	e.Pool.DeleteVM(testNS, testPool, i.VMID, poolmgrv1alpha1.EventType_VM_DELETED_DUE_TO_EXPIRY)
	e.endedInfo()
	wantSessionKeys(t, e.logRecord("VM lost"), id, "vm_id")
	if n := strings.Count(e.Logs.String(), `"msg":"VM lost"`); n != 1 {
		t.Errorf("VM lost lines = %d, want 1", n)
	}
}
