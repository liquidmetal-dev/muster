package battery

import (
	"errors"
	"log/slog"
	"time"

	"google.golang.org/grpc"

	"github.com/liquidmetal-dev/muster/internal/tlsconfig"
	"github.com/liquidmetal-dev/muster/internal/vmsource"
)

// Default values used when a Config field is zero.
const (
	DefaultCallTimeout = 10 * time.Second
	DefaultMaxAttempts = 3
)

// Config describes one poolmgrd endpoint and how to talk to it.
type Config struct {
	// Addr is the poolmgrd gRPC endpoint, host:port.
	Addr string
	// Namespace scopes every pool, lease and event this source touches.
	Namespace string
	// TLS selects the transport credentials.
	TLS tlsconfig.Client
	// BasicAuthToken, when set, is sent as "authorization: basic
	// <base64(token)>" on every call. poolmgrd checks it when configured
	// with basic_auth_token. It is never logged.
	BasicAuthToken string
	// CallTimeout bounds each RPC attempt. Zero means DefaultCallTimeout.
	CallTimeout time.Duration
	// MaxAttempts bounds the attempts of one call including the first.
	// Zero means DefaultMaxAttempts.
	MaxAttempts int
	// Logger receives retry and reconnect messages. Nil means slog.Default().
	Logger *slog.Logger
	// Execer runs commands in acquired VMs. Nil means
	// vmsource.UnavailableExecer, so VM.Exec returns ErrExecUnavailable.
	Execer vmsource.Execer

	// dialOptions is appended after the credentials. Only in-package tests
	// can set it, so nothing outside the package can replace the
	// credentials that TLS selected.
	dialOptions []grpc.DialOption
}

func (c Config) withDefaults() Config {
	if c.CallTimeout == 0 {
		c.CallTimeout = DefaultCallTimeout
	}
	if c.MaxAttempts == 0 {
		c.MaxAttempts = DefaultMaxAttempts
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.Execer == nil {
		c.Execer = vmsource.UnavailableExecer{}
	}
	return c
}

// Validate reports a Config that cannot work. It runs on the config with
// defaults applied.
func (c Config) Validate() error {
	if c.Addr == "" {
		return errors.New("battery: addr is required")
	}
	if c.Namespace == "" {
		return errors.New("battery: namespace is required")
	}
	if err := c.TLS.Validate(); err != nil {
		return err
	}
	if c.MaxAttempts < 1 {
		return errors.New("battery: max_attempts must be at least 1")
	}
	if c.CallTimeout <= 0 {
		return errors.New("battery: call_timeout must be positive")
	}
	return nil
}
