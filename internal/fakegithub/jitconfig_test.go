package fakegithub_test

import (
	"encoding/base64"
	"testing"

	"github.com/liquidmetal-dev/muster/internal/fakegithub"
)

func TestJITConfigRoundTrip(t *testing.T) {
	in := fakegithub.JITConfig{RunnerID: 7, RunnerName: "muster-x", SleepSeconds: 2.5, ExitCode: 3}
	out, err := fakegithub.DecodeJITConfig(in.Encode())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out != in {
		t.Fatalf("got %+v, want %+v", out, in)
	}
}

func TestDecodeJITConfigRejectsGarbage(t *testing.T) {
	if _, err := fakegithub.DecodeJITConfig("not base64!"); err == nil {
		t.Fatal("no error for non-base64 input")
	}
	if _, err := fakegithub.DecodeJITConfig(base64.StdEncoding.EncodeToString([]byte("{"))); err == nil {
		t.Fatal("no error for truncated JSON")
	}
}
