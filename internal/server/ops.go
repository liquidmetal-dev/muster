package server

import (
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/liquidmetal-dev/muster/internal/config"
)

// OpsConfig configures the ops listener.
type OpsConfig struct {
	// Addr is metrics_addr.
	Addr string
	// Metrics serves GET /metrics. Required.
	Metrics http.Handler
	// Ready serves GET /readyz. Required.
	Ready http.Handler
	// Logger is nil for slog.Default().
	Logger *slog.Logger
}

// NewOps returns the ops listener. GET /healthz returns 200 while the
// process serves HTTP.
func NewOps(cfg OpsConfig) (*Server, error) {
	if cfg.Metrics == nil || cfg.Ready == nil {
		return nil, errors.New("server: metrics and ready handlers are required")
	}
	s, err := New(Config{Listen: config.Listen{Addr: cfg.Addr}, Logger: cfg.Logger})
	if err != nil {
		return nil, err
	}
	s.Handle("GET /metrics", cfg.Metrics)
	s.Handle("GET /readyz", cfg.Ready)
	s.Handle("GET /healthz", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok\n")
	}))
	return s, nil
}
