package session

import (
	"testing"
	"time"
)

func TestBackoff(t *testing.T) {
	full := newBackoff(time.Second, 5*time.Second, func() float64 { return 1 })
	for i, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second, 5 * time.Second} {
		if got := full.next(); got != want {
			t.Errorf("full next() #%d = %v, want %v", i, got, want)
		}
	}
	full.reset()
	if got := full.next(); got != time.Second {
		t.Errorf("after reset next() = %v, want 1s", got)
	}

	half := newBackoff(time.Second, 5*time.Second, func() float64 { return 0 })
	for i, want := range []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 2500 * time.Millisecond} {
		if got := half.next(); got != want {
			t.Errorf("half next() #%d = %v, want %v", i, got, want)
		}
	}
}

func TestBackoffDefaultRand(t *testing.T) {
	b := newBackoff(time.Second, time.Second, nil)
	for range 20 {
		if d := b.next(); d < 500*time.Millisecond || d > time.Second {
			t.Fatalf("next() = %v, want within [500ms, 1s]", d)
		}
	}
}
