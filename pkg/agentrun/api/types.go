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
//
// SessionID is optional: when empty, the agent-run's initial session
// (the one opened during Create's handshake) is used — preserves
// backward compatibility for single-session callers. Multi-session
// callers must pass an explicit SessionID obtained from session/new.
type SessionPromptParams struct {
	SessionID string         `json:"sessionId,omitempty"`
	Prompt    []ContentBlock `json:"prompt"`
}

// SessionPromptResult is returned by the "session/prompt" method.
type SessionPromptResult struct {
	StopReason string `json:"stopReason"`
}

// SessionCancelParams is the JSON body for the "session/cancel" method.
// Optional SessionID — same semantics as SessionPromptParams.SessionID.
// Pre-multi-session callers passed no params at all; both empty body and
// missing SessionID work as "cancel the initial session".
type SessionCancelParams struct {
	SessionID string `json:"sessionId,omitempty"`
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
//
// Note: watchId is injected by the jsonrpc transport layer (Client.Watch),
// not set by callers.
type SessionWatchEventParams struct {
	FromSeq *int `json:"fromSeq,omitempty"`
}

// SessionWatchEventResult is returned by "session/watch_event".
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
// Optional SessionID — same semantics as SessionPromptParams.SessionID.
type SessionSetModelParams struct {
	SessionID string `json:"sessionId,omitempty"`
	ModelID   string `json:"modelId"`
}

// SessionSetModelResult is returned by "session/set_model".
type SessionSetModelResult struct{}

// SessionNewParams is the JSON body for the "session/new" method —
// opens an additional ACP session on the running agent.
//
// Cwd is required: each session is scoped to its own working directory.
// McpServers is optional per-session MCP overrides layered on the
// bundle-level config.
type SessionNewParams struct {
	Cwd        string                `json:"cwd"`
	McpServers []SessionNewMcpServer `json:"mcpServers,omitempty"`
}

// SessionNewMcpServer mirrors acp.McpServer's wire shape for transport
// over JSON-RPC. Kept minimal — extend as actual cases demand.
type SessionNewMcpServer struct {
	Name    string            `json:"name"`
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// SessionNewResult is returned by "session/new".
type SessionNewResult struct {
	SessionID string `json:"sessionId"`
}

// SessionEndParams is the JSON body for "session/end" — removes runtime
// tracking of a session. The agent process's per-session state remains
// until cancelled or the process exits (ACP has no explicit end-session
// RPC); this method only releases the agent-run's local map entry.
type SessionEndParams struct {
	SessionID string `json:"sessionId"`
}

// SessionEndResult is returned by "session/end".
type SessionEndResult struct{}

// SessionListResult lists active session IDs on the agent. Used by
// callers (e.g. massctl) to inspect the agent's current sessions.
type SessionListResult struct {
	SessionIDs []string `json:"sessionIds"`
}

// RuntimePhaseResult is returned by "runtime/status".
type RuntimePhaseResult struct {
	State    apiruntime.State      `json:"state"`
	Recovery RuntimeStatusRecovery `json:"recovery"`
}
