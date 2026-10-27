package main

import (
	"bytes"
	"errors"
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

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("pipe closed") }

func TestRunVersionWriteError(t *testing.T) {
	var errb bytes.Buffer
	code := run([]string{"-version"}, failWriter{}, &errb)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(errb.String(), "pipe closed") {
		t.Fatalf("stderr = %q, want the write error", errb.String())
	}
}
