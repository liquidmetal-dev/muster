package daemon

import (
	"context"
	"os"
)

// stopContexts returns stop, which is done after the first signal, and
// force, which is done after the second. Both are done when parent is.
func stopContexts(parent context.Context, sig <-chan os.Signal) (stop, force context.Context, cancel func()) {
	force, cancelForce := context.WithCancel(parent)
	stop, cancelStop := context.WithCancel(force)
	go func() {
		for _, c := range []context.CancelFunc{cancelStop, cancelForce} {
			select {
			case <-sig:
				c()
			case <-force.Done():
				return
			}
		}
	}()
	return stop, force, func() { cancelStop(); cancelForce() }
}
