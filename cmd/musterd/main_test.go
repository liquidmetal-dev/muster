package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunVersion(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"-version"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, errb.String())
	}
	if !strings.HasPrefix(out.String(), "musterd v") {
		t.Fatalf("stdout = %q, want prefix %q", out.String(), "musterd v")
	}
	if !strings.HasSuffix(out.String(), "\n") {
		t.Fatalf("stdout = %q, want a trailing newline", out.String())
	}
	if errb.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", errb.String())
	}
}

func TestRunNoArgsPrintsUsage(t *testing.T) {
	var out, errb bytes.Buffer
	code := run(nil, &out, &errb)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "Usage of musterd") {
		t.Fatalf("stderr = %q, want usage text", errb.String())
	}
	if out.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", out.String())
	}
}

func TestRunUnknownFlag(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"-bogus"}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "flag provided but not defined: -bogus") {
		t.Fatalf("stderr = %q, want the unknown flag error", errb.String())
	}
}
