package vmsource_test

import (
	"context"
	"errors"
	"testing"

	"github.com/liquidmetal-dev/muster/internal/vmsource"
)

func TestUnavailableExecer(t *testing.T) {
	_, err := vmsource.UnavailableExecer{}.Exec(context.Background(), vmsource.HostInfo{}, "vm", vmsource.ExecSpec{})
	if !errors.Is(err, vmsource.ErrExecUnavailable) {
		t.Fatalf("Exec() error = %v, want ErrExecUnavailable", err)
	}
}

func TestEventTypeString(t *testing.T) {
	tests := []struct {
		typ  vmsource.EventType
		want string
	}{
		{vmsource.EventVMLost, "vm-lost"},
		{vmsource.EventVMExpiring, "vm-expiring"},
		{vmsource.EventType(9), "unknown(9)"},
	}
	for _, tt := range tests {
		if got := tt.typ.String(); got != tt.want {
			t.Errorf("EventType(%d).String() = %q, want %q", tt.typ, got, tt.want)
		}
	}
}
