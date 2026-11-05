// Package logging builds musterd's slog logger from the log block of the
// configuration (requirement OBS-2).
package logging

import (
	"io"
	"log/slog"
	"strings"

	"github.com/liquidmetal-dev/muster/internal/config"
)

// New returns a logger that writes to w in cfg's format at cfg's level.
// cfg comes from config.Parse, so both fields are set and valid.
func New(w io.Writer, cfg config.Log) *slog.Logger {
	var level slog.Level
	if err := level.UnmarshalText([]byte(strings.ToUpper(cfg.Level))); err != nil {
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	if cfg.Format == config.LogFormatText {
		return slog.New(slog.NewTextHandler(w, opts))
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}
