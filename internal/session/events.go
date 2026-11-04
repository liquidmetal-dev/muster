package session

import (
	"context"
	"time"

	"github.com/liquidmetal-dev/muster/internal/vmsource"
)

const (
	eventsBackoffBase  = time.Second
	eventsBackoffLimit = 30 * time.Second
)

// watchEvents follows the source's VM events for one pool until ctx
// ends, and subscribes again whenever the stream closes.
func (r *Registry) watchEvents(ctx context.Context, pool string) {
	bo := newBackoff(eventsBackoffBase, eventsBackoffLimit, nil)
	for {
		ch, err := r.cfg.Source.Events(ctx, pool)
		if err == nil {
			bo.reset()
			for ev := range ch {
				r.handleEvent(ev)
			}
		}
		if ctx.Err() != nil {
			return
		}
		r.log.Warn("VM event stream closed; subscribing again", "pool", pool, "err", err)
		if !r.sleep(ctx, bo.next()) {
			return
		}
	}
}

func (r *Registry) handleEvent(ev vmsource.Event) {
	r.mu.Lock()
	var s *session
	for _, c := range r.sessions {
		if c.vmID == ev.VMID && c.pool.cfg.Name == ev.Pool {
			s = c
			break
		}
	}
	r.mu.Unlock()
	if s == nil {
		return
	}
	log := r.log.With("session", s.id, "pool", ev.Pool, "vm", ev.VMID)
	switch ev.Type {
	case vmsource.EventVMLost:
		log.Warn("VM lost")
		s.post(mailbox{lost: true})
	case vmsource.EventVMExpiring:
		log.Warn("VM lease expiring")
	}
}
