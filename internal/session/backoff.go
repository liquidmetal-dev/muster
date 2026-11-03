package session

import (
	"math/rand/v2"
	"time"
)

// backoff yields exponential delays from base to limit, each jittered
// into [d/2, d].
type backoff struct {
	base, limit, cur time.Duration
	rnd              func() float64
}

func newBackoff(base, limit time.Duration, rnd func() float64) *backoff {
	if rnd == nil {
		rnd = rand.Float64
	}
	return &backoff{base: base, limit: limit, rnd: rnd}
}

func (b *backoff) next() time.Duration {
	switch {
	case b.cur == 0:
		b.cur = b.base
	case b.cur < b.limit:
		b.cur = min(2*b.cur, b.limit)
	}
	half := b.cur / 2
	return half + time.Duration(b.rnd()*float64(b.cur-half))
}

func (b *backoff) reset() { b.cur = 0 }
