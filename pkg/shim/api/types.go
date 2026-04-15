// Package api contains the shared wire types for the Shim JSON-RPC protocol.
// Both pkg/shim/server (shim server) and pkg/shim/client (shim client) import
// this package so the types have a single authoritative definition.
package api

import (
	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
)

// ────────────────────────────────────────────────────────────────────────────
// session/* wire types
// ────────────────────────────────────────────────────────────────────────────

// SessionPromptParams is the JSON body for the "session/prompt" method.
type SessionPromptParams struct {
	Prompt string `json:"prompt"`
}

// SessionPromptResult is returned by the "session/prompt" method.
type SessionPromptResult struct {
	StopReason string `json:"stopReason"`
}

// SessionLoadParams is the JSON body for the "session/load" RPC method.
// The shim server exposes this for try_reload restart policy; mass calls it
// during recovery to restore a prior ACP session.
type SessionLoadParams struct {
	SessionID string `json:"sessionId"`
}

// SessionWatchEventParams is the JSON body for the "session/watch_event" method.
// When FromSeq is nil, only live events are streamed (watch from HEAD).
// When FromSeq is set, historical events from that seq are replayed first via
// shim/event notifications, followed by live events (K8s List-Watch pattern).
type SessionWatchEventParams struct {
	FromSeq *int `json:"fromSeq,omitempty"`
}

// SessionWatchEventResult is returned by "session/watch_event".
// NextSeq is the sequence number boundary at subscription time — for diagnostics
// only. Clients should track the last received event seq for reconnection.
type SessionWatchEventResult struct {
	NextSeq int `json:"nextSeq"`
}

// RuntimeStatusRecovery holds recovery metadata from the shim's durable log.
type RuntimeStatusRecovery struct {
	LastSeq int `json:"lastSeq"`
}

// RuntimeStatusResult is returned by "runtime/status".
type RuntimeStatusResult struct {
	State    apiruntime.State      `json:"state"`
	Recovery RuntimeStatusRecovery `json:"recovery"`
}
