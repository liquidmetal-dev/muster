// Package server is muster's HTTP(S) listener. It serves two listeners:
// the webhook listener, which routes POST /webhook to the webhook handler
// (design §2), and the ops listener from NewOps, which serves /metrics,
// /healthz and /readyz on metrics_addr (requirements OBS-1 and OBS-3).
// Other routes are added with Handle before Serve. With a tls block it
// serves HTTPS from the configured certificate and key; without one it
// serves plain HTTP (requirement INTAKE-8).
package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/liquidmetal-dev/muster/internal/config"
)

// Timeouts of the listener. WriteTimeout bounds every response well inside
// the 10 s that GitHub allows a webhook (requirement INTAKE-4).
const (
	ReadHeaderTimeout = 5 * time.Second
	ReadTimeout       = 10 * time.Second
	WriteTimeout      = 10 * time.Second
	IdleTimeout       = 60 * time.Second
)

// Config configures a Server.
type Config struct {
	// Listen is the listener address and optional TLS.
	Listen config.Listen
	// Webhook serves POST /webhook. Nil mounts no webhook route.
	Webhook http.Handler
	// Logger is nil for slog.Default().
	Logger *slog.Logger
}

// Server is one listener.
type Server struct {
	addr string
	mux  *http.ServeMux
	http *http.Server
}

// New returns a Server. It loads the TLS certificate and key now, so that
// a bad file fails at startup.
func New(cfg Config) (*Server, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	var tlsCfg *tls.Config
	if cfg.Listen.TLS != nil {
		var err error
		if tlsCfg, err = cfg.Listen.TLS.TLSConfig(); err != nil {
			return nil, fmt.Errorf("server: %w", err)
		}
	}
	mux := http.NewServeMux()
	if cfg.Webhook != nil {
		mux.Handle("POST /webhook", cfg.Webhook)
	}
	return &Server{
		addr: cfg.Listen.Addr,
		mux:  mux,
		http: &http.Server{
			Handler:           mux,
			TLSConfig:         tlsCfg,
			ReadHeaderTimeout: ReadHeaderTimeout,
			ReadTimeout:       ReadTimeout,
			WriteTimeout:      WriteTimeout,
			IdleTimeout:       IdleTimeout,
			ErrorLog:          slog.NewLogLogger(cfg.Logger.Handler(), slog.LevelWarn),
		},
	}, nil
}

// Handle adds a route. Call it before Serve.
func (s *Server) Handle(pattern string, h http.Handler) {
	s.mux.Handle(pattern, h)
}

// ListenAndServe listens on the configured address and serves until
// Shutdown.
func (s *Server) ListenAndServe() error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("server: %w", err)
	}
	return s.Serve(ln)
}

// Serve serves on ln until Shutdown, and then returns nil.
func (s *Server) Serve(ln net.Listener) error {
	if s.http.TLSConfig != nil {
		ln = tls.NewListener(ln, s.http.TLSConfig)
	}
	if err := s.http.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown stops the listener and waits, until ctx is done, for open
// requests to finish.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}
