package musterctl

import (
	"bytes"
	"strings"
	"testing"
)

func execute(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errb bytes.Buffer
	cmd := NewRootCmd()
	cmd.SetArgs(args)
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	err = cmd.Execute()
	return out.String(), errb.String(), err
}

func TestVersionCommand(t *testing.T) {
	out, _, err := execute(t, "version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.HasPrefix(out, "musterctl v") || !strings.HasSuffix(out, "\n") {
		t.Fatalf("stdout = %q, want prefix %q and a trailing newline", out, "musterctl v")
	}
}

func TestRootWithoutArgsShowsHelp(t *testing.T) {
	out, _, err := execute(t)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	if !strings.Contains(out, "Usage:") {
		t.Fatalf("stdout = %q, want help text", out)
	}
}

func TestRootUnknownCommand(t *testing.T) {
	_, _, err := execute(t, "bogus")
	if err == nil {
		t.Fatal("unknown command: err = nil, want an error")
	}
	if code := ExitCode(err); code != 1 {
		t.Fatalf("ExitCode(%v) = %d, want 1", err, code)
	}
}

func TestExitCodeNil(t *testing.T) {
	if code := ExitCode(nil); code != 0 {
		t.Fatalf("ExitCode(nil) = %d, want 0", code)
	}
}
