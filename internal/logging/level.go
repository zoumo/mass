package logging

import "log/slog"

// LevelTrace is a custom slog level for very high-frequency diagnostics
// (e.g. per-event broadcast, notification routing). Sits below Debug (-4)
// in the slog level hierarchy so it can be filtered independently.
//
// Standard slog levels for reference:
//
//	LevelTrace = -8
//	LevelDebug = -4 (slog built-in)
//	LevelInfo  =  0 (slog built-in)
//	LevelWarn  =  4 (slog built-in)
//	LevelError =  8 (slog built-in)
const LevelTrace slog.Level = -8
