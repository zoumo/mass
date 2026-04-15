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

// SessionSubscribeParams is the JSON body for the "session/subscribe" method.
type SessionSubscribeParams struct {
	AfterSeq *int `json:"afterSeq,omitempty"`
	FromSeq  *int `json:"fromSeq,omitempty"`
}

// SessionSubscribeResult is returned by "session/subscribe".
type SessionSubscribeResult struct {
	NextSeq int                `json:"nextSeq"`
	Entries []ShimEvent `json:"entries,omitempty"`
}

// ────────────────────────────────────────────────────────────────────────────
// runtime/* wire types
// ────────────────────────────────────────────────────────────────────────────

// RuntimeHistoryParams is the JSON body for the "runtime/history" method.
type RuntimeHistoryParams struct {
	FromSeq *int `json:"fromSeq,omitempty"`
}

// RuntimeHistoryResult is returned by "runtime/history".
type RuntimeHistoryResult struct {
	Entries []ShimEvent `json:"entries"`
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
