package config_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/liquidmetal-dev/muster/internal/config"
)

type fakeChecker struct {
	name  string
	err   error
	calls int
	seen  *config.Config
}

func (f *fakeChecker) Name() string { return f.name }

func (f *fakeChecker) Check(_ context.Context, cfg *config.Config) error {
	f.calls++
	f.seen = cfg
	return f.err
}

func TestCheckRunsAllAndJoins(t *testing.T) {
	hosts := &fakeChecker{name: "hosts", err: errors.New("boom")}
	github := &fakeChecker{name: "github", err: errors.New("nope")}
	err := config.Check(context.Background(), &config.Config{}, hosts, github)
	for _, want := range []string{"hosts: boom", "github: nope"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want one containing %q", err, want)
		}
	}
	if hosts.calls != 1 || github.calls != 1 {
		t.Errorf("calls = %d, %d; want 1, 1", hosts.calls, github.calls)
	}
}

func TestCheckNoCheckersIsNil(t *testing.T) {
	if err := config.Check(context.Background(), &config.Config{}); err != nil {
		t.Fatalf("Check() = %v, want nil", err)
	}
}

func TestCheckPassesConfig(t *testing.T) {
	cfg := &config.Config{}
	c := &fakeChecker{name: "x"}
	if err := config.Check(context.Background(), cfg, c); err != nil {
		t.Fatalf("Check() = %v", err)
	}
	if c.seen != cfg {
		t.Fatal("checker did not receive the config pointer")
	}
}

func TestCheckPreCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := &fakeChecker{name: "x"}
	err := config.Check(ctx, &config.Config{}, c)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Check() = %v, want context.Canceled", err)
	}
	if c.calls != 0 {
		t.Fatalf("checker ran %d times on a cancelled context", c.calls)
	}
}
