package fakepoolmgr

import (
	"context"
	"slices"
	"strings"

	poolmgrv1alpha1 "github.com/liquidmetal-dev/battery/api/proto/poolmgr/v1alpha1"
)

// Host seeds one registered flintlock host.
type Host struct {
	Name    string
	Address string
}

// SeedHost registers a host, replacing one with the same name.
func (s *Server) SeedHost(h Host) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hosts[h.Name] = h
}

// ListHosts implements poolmgrv1alpha1.HostAdminServer. Hosts are ordered
// by name.
func (s *Server) ListHosts(context.Context, *poolmgrv1alpha1.ListHostsRequest) (*poolmgrv1alpha1.ListHostsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	resp := &poolmgrv1alpha1.ListHostsResponse{}
	for _, h := range s.hosts {
		resp.Hosts = append(resp.Hosts, &poolmgrv1alpha1.HostStatus{
			Host: &poolmgrv1alpha1.Host{Name: h.Name, Address: h.Address},
		})
	}
	slices.SortFunc(resp.Hosts, func(a, b *poolmgrv1alpha1.HostStatus) int {
		return strings.Compare(a.GetHost().GetName(), b.GetHost().GetName())
	})
	return resp, nil
}
