package webhook

import (
	"sync"
	"time"

	"github.com/liquidmetal-dev/muster/internal/clock"
)

// DeliveryTTL is how long a delivery id is remembered (requirement
// INTAKE-3).
const DeliveryTTL = 24 * time.Hour

const pruneEvery = time.Minute

// deliverySet remembers delivery ids for DeliveryTTL. It is safe for
// concurrent use.
type deliverySet struct {
	mu        sync.Mutex
	clock     clock.Clock
	ids       map[string]time.Time
	lastPrune time.Time
}

func newDeliverySet(c clock.Clock) *deliverySet {
	return &deliverySet{clock: c, ids: map[string]time.Time{}, lastPrune: c.Now()}
}

// firstSeen records id and reports whether it was not already recorded
// within DeliveryTTL.
func (s *deliverySet) firstSeen(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()
	if now.Sub(s.lastPrune) >= pruneEvery {
		for k, at := range s.ids {
			if now.Sub(at) >= DeliveryTTL {
				delete(s.ids, k)
			}
		}
		s.lastPrune = now
	}
	if at, ok := s.ids[id]; ok && now.Sub(at) < DeliveryTTL {
		return false
	}
	s.ids[id] = now
	return true
}
