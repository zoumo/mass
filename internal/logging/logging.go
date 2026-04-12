// Package logging provides pluggable slog handler construction.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/lmittmann/tint"
)

// NewHandler returns a slog.Handler for the given format.
// Supported formats: "pretty" (colored terminal), "text" (slog default), "json".
func NewHandler(format string, level slog.Level, w io.Writer) slog.Handler {
	opts := &slog.HandlerOptions{Level: level}

	switch strings.ToLower(format) {
	case "json":
		return slog.NewJSONHandler(w, opts)
	case "text":
		return slog.NewTextHandler(w, opts)
	case "pretty":
		return tint.NewHandler(w, &tint.Options{
			Level:      level,
			TimeFormat: time.DateTime,
		})
	default:
		return tint.NewHandler(w, &tint.Options{
			Level:      level,
			TimeFormat: time.DateTime,
		})
	}
}

// ParseLevel parses a log level string into slog.Level.
// Accepted values: "debug", "info", "warn", "error" (case-insensitive).
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("unknown log level %q (valid: debug, info, warn, error)", s)
	}
}
