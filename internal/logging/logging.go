// Package logging provides pluggable slog handler construction.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/lmittmann/tint"
	"github.com/spf13/pflag"
	"gopkg.in/natefinch/lumberjack.v2"
)

// LogConfig holds logging configuration that can be populated from CLI flags.
type LogConfig struct {
	Level      string // "debug"|"info"|"warn"|"error"
	Format     string // "auto"|"pretty"|"text"|"json"
	Path       string // log directory (empty = stderr)
	Filename   string // log filename within Path (set programmatically, not via flag)
	MaxSizeMB  int    // max size in MB before rotation (lumberjack)
	MaxAgeDays int    // max age in days before deletion (lumberjack)
}

// AddFlags registers logging flags on the given flag set.
func (c *LogConfig) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&c.Level, "log-level", "info", "log level (trace, debug, info, warn, error)")
	fs.StringVar(&c.Format, "log-format", "auto", "log format (auto, pretty, text, json)")
	fs.StringVar(&c.Path, "log-path", c.Path, "log directory (empty = stderr)")
	fs.IntVar(&c.MaxSizeMB, "log-max-size", 100, "max log file size in MB before rotation")
	fs.IntVar(&c.MaxAgeDays, "log-max-age", 7, "max log file age in days before deletion")
}

// SetDefaultPath sets the log file path if it was not explicitly provided.
func (c *LogConfig) SetDefaultPath(path string) {
	if c.Path == "" {
		c.Path = path
	}
}

// Build creates a slog.Logger from the config.
// Returns the logger and a cleanup function that should be deferred.
func (c *LogConfig) Build() (*slog.Logger, func(), error) {
	level, err := ParseLevel(c.Level)
	if err != nil {
		level = slog.LevelInfo
	}

	var w io.Writer
	cleanup := func() {}

	if c.Path != "" {
		logFile := filepath.Join(c.Path, c.Filename)
		lj := &lumberjack.Logger{
			Filename: logFile,
			MaxSize:  c.MaxSizeMB,
			MaxAge:   c.MaxAgeDays,
		}
		w = lj
		cleanup = func() { _ = lj.Close() }
	} else {
		w = os.Stderr
	}

	format := resolveFormat(c.Format, w)
	handler := NewHandler(format, level, w)
	return slog.New(handler), cleanup, nil
}

// resolveFormat resolves "auto" to a concrete format based on whether the
// writer is a terminal. Non-"auto" values pass through unchanged.
func resolveFormat(format string, w io.Writer) string {
	if !strings.EqualFold(format, "auto") {
		return format
	}
	if f, ok := w.(*os.File); ok && term.IsTerminal(f.Fd()) {
		return "pretty"
	}
	return "text"
}

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
		noColor := true
		if f, ok := w.(*os.File); ok {
			noColor = !term.IsTerminal(f.Fd())
		}
		return tint.NewHandler(w, &tint.Options{
			Level:      level,
			TimeFormat: time.DateTime,
			NoColor:    noColor,
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
	case "trace":
		return LevelTrace, nil
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("unknown log level %q (valid: trace, debug, info, warn, error)", s)
	}
}
