package flintlock

import (
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/status"

	"github.com/liquidmetal-dev/muster/internal/vmsource"
	"github.com/liquidmetal-dev/muster/internal/vmsource/flintlock/fakeexec"
)

func TestCheckHost(t *testing.T) {
	tests := []struct {
		version string
		enabled bool
		want    error
		text    string
	}{
		{version: "v0.15.2", enabled: true},
		{version: "v0.16.0", enabled: true},
		{version: "0.15.3", enabled: true},
		{version: "v0.15.1", enabled: true, want: ErrUnsupportedVersion, text: "v0.15.1 is older than v0.15.2"},
		{version: "v0.15.2-rc.1", enabled: true, want: ErrUnsupportedVersion},
		{version: "dev", enabled: true, want: ErrUnsupportedVersion, text: `"dev"`},
		{version: "v0.15.2", enabled: false, want: ErrExecDisabled, text: "--enable-exec-api"},
		{version: "v0.14.0", enabled: false, want: ErrUnsupportedVersion, text: "v0.14.0 is older than v0.15.2"},
	}
	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			fake := fakeexec.New()
			fake.SetServerInfo(tt.version, tt.enabled)
			e := newTestExecer(t, fake)
			err := e.CheckHost(t.Context(), hostA)
			if tt.want == nil {
				if err != nil {
					t.Fatalf("CheckHost: %v", err)
				}
				return
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("CheckHost = %v, want %v", err, tt.want)
			}
			if !errors.Is(err, vmsource.ErrExecUnavailable) {
				t.Fatalf("CheckHost = %v, want it to match ErrExecUnavailable", err)
			}
			if !strings.Contains(err.Error(), "host-a") || !strings.Contains(err.Error(), tt.text) {
				t.Fatalf("CheckHost = %q, want host-a and %q in it", err, tt.text)
			}
		})
	}
}

func TestCheckHostUnavailable(t *testing.T) {
	fake := fakeexec.New()
	fake.FailNext("ServerInfo", codes.Unavailable)
	e := newTestExecer(t, fake)
	err := e.CheckHost(t.Context(), hostA)
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("CheckHost = %v (code %v), want Unavailable", err, status.Code(err))
	}
	if errors.Is(err, vmsource.ErrExecUnavailable) {
		t.Fatalf("CheckHost = %v, want it not to match ErrExecUnavailable", err)
	}
	if !strings.Contains(err.Error(), "host-a") {
		t.Fatalf("CheckHost = %q, want host-a in it", err)
	}
}

func TestConnCachedPerHost(t *testing.T) {
	e := newTestExecer(t, fakeexec.New())
	a1, err := e.conn(hostA)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	a2, _ := e.conn(hostA)
	if a1 != a2 {
		t.Fatal("same host got two connections, want one")
	}
	otherAddr, _ := e.conn(vmsource.HostInfo{Name: "host-a", Address: "passthrough:///other"})
	if otherAddr == a1 {
		t.Fatal("new address reused the old connection")
	}
	otherName, _ := e.conn(vmsource.HostInfo{Name: "host-b", Address: bufconnAddr})
	if otherName == a1 {
		t.Fatal("new name reused the old connection")
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for _, c := range []interface{ GetState() connectivity.State }{a1, otherAddr, otherName} {
		if s := c.GetState(); s != connectivity.Shutdown {
			t.Fatalf("state after Close = %v, want Shutdown", s)
		}
	}
}
