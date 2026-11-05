package pools

import (
	"context"
	"errors"
	"fmt"
	"sync"

	poolmgrv1alpha1 "github.com/liquidmetal-dev/battery/api/proto/poolmgr/v1alpha1"

	"github.com/liquidmetal-dev/muster/internal/config"
	"github.com/liquidmetal-dev/muster/internal/vmsource"
)

// HostLister lists the hosts registered in battery.
type HostLister interface {
	ListHosts(ctx context.Context) ([]*poolmgrv1alpha1.Host, error)
}

// HostCheck is the config.Checker that fails when a pool names a host
// battery does not know (POOL-3). Muster only reads hosts; registering
// them is the operator's job.
type HostCheck struct {
	l HostLister

	mu     sync.Mutex
	listed map[string]string // name -> address; nil until listed
}

var _ config.Checker = (*HostCheck)(nil)

// NewHostCheck returns a HostCheck over l.
func NewHostCheck(l HostLister) *HostCheck { return &HostCheck{l: l} }

// Name implements config.Checker.
func (*HostCheck) Name() string { return "battery hosts" }

// Check implements config.Checker. It lists hosts once and reports every
// unknown host of every pool, in configuration order.
func (h *HostCheck) Check(ctx context.Context, cfg *config.Config) error {
	known, err := h.list(ctx, true)
	if err != nil {
		return err
	}
	var errs []error
	for _, p := range cfg.Pools {
		for _, name := range p.FlintlockHosts {
			if _, ok := known[name]; !ok {
				errs = append(errs, fmt.Errorf("pools[%s]: host %q is not registered in battery; add it with HostAdmin.AddHost", p.Name, name))
			}
		}
	}
	return errors.Join(errs...)
}

// Resolve is a flintlock.HostResolver over the hosts that the last Check
// listed, so the exec check reads the same list. Names battery does not
// know are left out; Check reports them.
func (h *HostCheck) Resolve(ctx context.Context, names []string) ([]vmsource.HostInfo, error) {
	known, err := h.list(ctx, false)
	if err != nil {
		return nil, err
	}
	var out []vmsource.HostInfo
	for _, n := range names {
		if addr, ok := known[n]; ok {
			out = append(out, vmsource.HostInfo{Name: n, Address: addr})
		}
	}
	return out, nil
}

func (h *HostCheck) list(ctx context.Context, fresh bool) (map[string]string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.listed != nil && !fresh {
		return h.listed, nil
	}
	hosts, err := h.l.ListHosts(ctx)
	if err != nil {
		return nil, fmt.Errorf("list hosts: %w", err)
	}
	h.listed = make(map[string]string, len(hosts))
	for _, host := range hosts {
		h.listed[host.GetName()] = host.GetAddress()
	}
	return h.listed, nil
}
