package webhook

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liquidmetal-dev/muster/internal/clock"
)

var t0 = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

func TestDeliverySet(t *testing.T) {
	c := clock.NewManual(t0)
	s := newDeliverySet(c)
	if !s.firstSeen("a") {
		t.Fatal("first a: got duplicate")
	}
	if s.firstSeen("a") {
		t.Fatal("second a: got first")
	}
	if !s.firstSeen("b") {
		t.Fatal("first b: got duplicate")
	}
	c.Advance(DeliveryTTL - time.Second)
	if s.firstSeen("a") {
		t.Fatal("a within TTL: got first")
	}
	c.Advance(2 * time.Second)
	if !s.firstSeen("a") {
		t.Fatal("a after TTL: got duplicate")
	}
}

func TestDeliverySetPrunes(t *testing.T) {
	c := clock.NewManual(t0)
	s := newDeliverySet(c)
	for _, id := range []string{"a", "b", "c"} {
		s.firstSeen(id)
	}
	c.Advance(DeliveryTTL + time.Second)
	s.firstSeen("x")
	if len(s.ids) != 1 {
		t.Fatalf("len(ids) = %d, want 1", len(s.ids))
	}
}

func TestDeliverySetPruneRateLimited(t *testing.T) {
	c := clock.NewManual(t0)
	s := newDeliverySet(c)
	s.firstSeen("a")
	c.Advance(30 * time.Second)
	s.firstSeen("b")
	if !s.lastPrune.Equal(t0) {
		t.Fatalf("lastPrune = %v, want %v", s.lastPrune, t0)
	}
}

func TestDeliverySetConcurrent(t *testing.T) {
	s := newDeliverySet(clock.NewManual(t0))
	var first atomic.Int32
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			if s.firstSeen("same") {
				first.Add(1)
			}
		})
	}
	wg.Wait()
	if got := first.Load(); got != 1 {
		t.Fatalf("first = %d, want 1", got)
	}
}
