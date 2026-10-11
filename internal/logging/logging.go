// Package logging builds the process-wide slog logger.
package logging

import (
	"io"
	"log/slog"
	"strings"
)

// New returns a logger writing to w in the given format ("json" or "text")
// at the given level ("debug", "info", "warn", "error").
func New(w io.Writer, format, level string) *slog.Logger {
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lv}
	if format == "text" {
		return slog.New(slog.NewTextHandler(w, opts))
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}
