package session

import "testing"

func TestStateString(t *testing.T) {
	want := map[State]string{
		Pending: "PENDING", Claiming: "CLAIMING", Starting: "STARTING", Running: "RUNNING",
		Ended: "ENDED", Failed: "FAILED", Released: "RELEASED",
	}
	for s, w := range want {
		if got := s.String(); got != w {
			t.Errorf("%d.String() = %q, want %q", int(s), got, w)
		}
	}
}

func TestStateActive(t *testing.T) {
	for _, s := range []State{Pending, Claiming, Starting, Running} {
		if !s.Active() {
			t.Errorf("%v.Active() = false, want true", s)
		}
	}
	for _, s := range []State{Ended, Failed, Released} {
		if s.Active() {
			t.Errorf("%v.Active() = true, want false", s)
		}
	}
}
