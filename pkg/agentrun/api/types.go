// Package api contains the shared wire types for the Agent Run JSON-RPC protocol.
// Both pkg/agentrun/server (agent-run server) and pkg/agentrun/client (agent-run client) import
// this package so the types have a single authoritative definition.
package api

import (
	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
)

// ────────────────────────────────────────────────────────────────────────────
// session/* wire types
// ────────────────────────────────────────────────────────────────────────────

// SessionPromptParams is the JSON body for the "session/prompt" method.
// Prompt is an array of ACP ContentBlocks supporting text, image, audio,
// resource, and resource-link content types.
type SessionPromptParams struct {
	Prompt []ContentBlock `json:"prompt"`
}

// SessionPromptResult is returned by the "session/prompt" method.
type SessionPromptResult struct {
	StopReason string `json:"stopReason"`
}

// SessionLoadParams is the JSON body for the "session/load" RPC method.
// agentd always calls this during recovery for best-effort session restore.
// agent-run checks ACP loadSession capability internally and auto-fallbacks.
type SessionLoadParams struct {
	SessionID string `json:"sessionId"`
}

// SessionWatchEventParams is the JSON body for the "session/watch_event" method.
// When FromSeq is nil, only live events are streamed (watch from HEAD).
// When FromSeq is set, historical events from that seq are replayed first via
// runtime/event_update notifications, followed by live events (K8s List-Watch pattern).
type SessionWatchEventParams struct {
	FromSeq *int `json:"fromSeq,omitempty"`
}

// SessionWatchEventResult is returned by "session/watch_event".
// WatchID is an opaque identifier assigned by the server for this watch stream.
// The client uses it to demux notifications when multiple watch streams share
// one connection (each runtime/event_update notification carries the originating WatchID).
// NextSeq is the sequence number boundary at subscription time — for diagnostics
// only. Clients should track the last received event seq for reconnection.
type SessionWatchEventResult struct {
	WatchID string `json:"watchId"`
	NextSeq int    `json:"nextSeq"`
}

// RuntimeStatusRecovery holds recovery metadata from the agent-run.s durable log.
type RuntimeStatusRecovery struct {
	LastSeq int `json:"lastSeq"`
}

// SessionSetModelParams is the JSON body for "session/set_model".
type SessionSetModelParams struct {
	ModelID string `json:"modelId"`
}

// SessionSetModelResult is returned by "session/set_model".
type SessionSetModelResult struct{}

// RuntimeStatusResult is returned by "runtime/status".
type RuntimeStatusResult struct {
	State    apiruntime.State      `json:"state"`
	Recovery RuntimeStatusRecovery `json:"recovery"`
}
