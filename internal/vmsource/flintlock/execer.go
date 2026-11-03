// Package flintlock runs commands in flintlock microvms through the
// MicroVMExec API. Execer implements vmsource.Execer for the battery
// source, and NewChecker exposes its per-host exec check to config.Check.
package flintlock

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"google.golang.org/grpc"

	"github.com/liquidmetal-dev/muster/internal/config"
	"github.com/liquidmetal-dev/muster/internal/vmsource"
)

// Execer runs commands on flintlock hosts. Each host gets one connection,
// dialled on first use with the TLS settings of its battery host name and
// kept until Close. A host is checked with ServerInfo before its first
// command; a pass is kept, a failure is not.
type Execer struct {
	cfg    config.Flintlock
	logger *slog.Logger

	mu       sync.Mutex
	conns    map[hostKey]*grpc.ClientConn
	checked  map[hostKey]bool
	inflight map[hostKey]*checkCall

	// dialOptions is appended after the credentials. It is unexported so
	// that nothing outside the package can replace the credentials that
	// TLSFor selects.
	dialOptions []grpc.DialOption
}

// Option configures an Execer.
type Option func(*Execer)

// WithLogger sets the logger. The default is slog.Default().
func WithLogger(l *slog.Logger) Option {
	return func(e *Execer) { e.logger = l }
}

// hostKey includes the address because battery can report a known host
// name at a new address.
type hostKey struct{ name, address string }

func keyOf(h vmsource.HostInfo) hostKey { return hostKey{h.Name, h.Address} }

// NewExecer returns an Execer for cfg. It fails when the global TLS
// settings or any per-host override cannot build credentials.
func NewExecer(cfg config.Flintlock, opts ...Option) (*Execer, error) {
	if _, err := cfg.TLS.Credentials(); err != nil {
		return nil, fmt.Errorf("flintlock: tls: %w", err)
	}
	for name, h := range cfg.Hosts {
		if _, err := h.TLS.Credentials(); err != nil {
			return nil, fmt.Errorf("flintlock: hosts.%s.tls: %w", name, err)
		}
	}
	e := &Execer{
		cfg:      cfg,
		logger:   slog.Default(),
		conns:    map[hostKey]*grpc.ClientConn{},
		checked:  map[hostKey]bool{},
		inflight: map[hostKey]*checkCall{},
	}
	for _, o := range opts {
		o(e)
	}
	return e, nil
}

func (e *Execer) conn(host vmsource.HostInfo) (*grpc.ClientConn, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	k := keyOf(host)
	if c, ok := e.conns[k]; ok {
		return c, nil
	}
	creds, err := e.cfg.TLSFor(host.Name).Credentials()
	if err != nil {
		return nil, fmt.Errorf("flintlock: host %s: tls: %w", host.Name, err)
	}
	opts := append([]grpc.DialOption{grpc.WithTransportCredentials(creds)}, e.dialOptions...)
	c, err := grpc.NewClient(host.Address, opts...)
	if err != nil {
		return nil, fmt.Errorf("flintlock: host %s: dial %s: %w", host.Name, host.Address, err)
	}
	e.conns[k] = c
	return c, nil
}

// Close closes every connection.
func (e *Execer) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	var errs []error
	for k, c := range e.conns {
		if err := c.Close(); err != nil {
			errs = append(errs, err)
		}
		delete(e.conns, k)
	}
	return errors.Join(errs...)
}
