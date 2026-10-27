package battery

import (
	"context"
	"errors"
	"io"
	"time"

	poolmgrv1alpha1 "github.com/liquidmetal-dev/battery/api/proto/poolmgr/v1alpha1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/liquidmetal-dev/muster/internal/vmsource"
)

// eventTypeVMDeletedOnHostDrain is poolmgrv1alpha1.EventType
// VM_DELETED_ON_HOST_DRAIN, added to battery after v0.5.0. It is mapped by
// number so a newer poolmgrd is understood before the module is bumped.
const eventTypeVMDeletedOnHostDrain poolmgrv1alpha1.EventType = 12

const eventBuffer = 16

// Events implements vmsource.Source. The first Subscribe runs here and its
// error is returned (grpc-go fails fast when the connection is down or the
// client is closed). A goroutine then reads the stream and resubscribes
// with backoff after a transient failure. A permanent status closes the
// channel while ctx is still live; the consumer must then treat its VMs
// as unobserved. The channel also closes when ctx ends.
func (s *Source) Events(ctx context.Context, pool string) (<-chan vmsource.Event, error) {
	if pool == "" {
		return nil, errors.New("battery: Events needs a pool name")
	}
	req := &poolmgrv1alpha1.SubscribeRequest{Pool: s.poolRef(pool)}
	stream, err := s.events.Subscribe(ctx, req)
	if err != nil {
		return nil, mapError("Subscribe", vmsource.ErrPoolNotFound, err)
	}
	ch := make(chan vmsource.Event, eventBuffer)
	go s.readEvents(ctx, pool, req, stream, ch)
	return ch, nil
}

func (s *Source) readEvents(ctx context.Context, pool string, req *poolmgrv1alpha1.SubscribeRequest, stream grpc.ServerStreamingClient[poolmgrv1alpha1.Event], out chan<- vmsource.Event) {
	defer close(out)
	bo := newBackoff(500*time.Millisecond, 30*time.Second)
	var lastID int64
	var err error
	for {
		// stream is nil when the last Subscribe failed; err then holds its
		// error and the loop goes straight to the transient check.
		if stream != nil {
			before := lastID
			err = s.drain(ctx, stream, &lastID, out)
			if ctx.Err() != nil {
				return
			}
			if lastID > before {
				bo.reset() // the stream delivered something, so the outage is over
			}
		}
		if !transientStreamError(err) {
			s.cfg.Logger.Error("battery: event subscription failed permanently", "pool", pool, "err", err)
			return
		}
		d := bo.delay()
		s.cfg.Logger.Warn("battery: event stream dropped, resubscribing", "pool", pool, "err", err, "after", d)
		if s.sleep(ctx, d) != nil {
			return
		}
		stream, err = s.events.Subscribe(ctx, req)
	}
}

// drain forwards events from stream until it fails. It skips events with
// an id at or below *lastID (the outbox replay after a resubscribe).
func (s *Source) drain(ctx context.Context, stream grpc.ServerStreamingClient[poolmgrv1alpha1.Event], lastID *int64, out chan<- vmsource.Event) error {
	for {
		raw, err := stream.Recv()
		if err != nil {
			return err
		}
		if raw.GetId() <= *lastID {
			continue
		}
		*lastID = raw.GetId()
		ev, keep := mapEvent(raw)
		if !keep {
			continue
		}
		select {
		case out <- ev:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// transientStreamError reports whether the stream should be re-opened.
func transientStreamError(err error) bool {
	if errors.Is(err, io.EOF) {
		return true
	}
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded, codes.Internal, codes.Unknown, codes.Aborted, codes.Canceled:
		return true
	default:
		return false
	}
}

// mapEvent converts a battery event. It returns false for events that do
// not affect a lease holder.
func mapEvent(ev *poolmgrv1alpha1.Event) (vmsource.Event, bool) {
	var typ vmsource.EventType
	switch ev.GetType() {
	case poolmgrv1alpha1.EventType_VM_DELETED_DUE_TO_EXPIRY,
		poolmgrv1alpha1.EventType_VM_DELETED_ON_POOL_DELETE,
		eventTypeVMDeletedOnHostDrain:
		typ = vmsource.EventVMLost
	case poolmgrv1alpha1.EventType_VM_EXPIRING_SOON:
		typ = vmsource.EventVMExpiring
	default:
		return vmsource.Event{}, false
	}
	return vmsource.Event{Type: typ, Pool: ev.GetPoolName(), VMID: ev.GetVmUid(), At: ev.GetCreatedAt().AsTime()}, true
}
