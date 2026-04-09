// Package agentd provides the agent daemon that manages agent sessions and
// orchestrates the runtime lifecycle.
package agentd

import (
	"context"
	"encoding/json"
	"fmt"
	"net"

	"github.com/sourcegraph/jsonrpc2"

	"github.com/open-agent-d/open-agent-d/pkg/events"
	"github.com/open-agent-d/open-agent-d/pkg/spec"
)

// ────────────────────────────────────────────────────────────────────────────
// ShimClient - JSON-RPC client for agent-shim communication (clean-break surface)
//
// Methods exposed:
//   session/prompt    — send a prompt and wait for the turn to complete
//   session/cancel    — cancel the current agent turn
//   session/load      — restore a prior ACP session (tryReload restart policy)
//   session/subscribe — register for live session/update and runtime/stateChange notifications
//   runtime/status    — get current runtime state plus recovery.lastSeq metadata
//   runtime/history   — get replayable event history from a given sequence number
//   runtime/stop      — request graceful shim shutdown
// ────────────────────────────────────────────────────────────────────────────

// ShimClient is a JSON-RPC 2.0 client that communicates with the agent-shim
// process over a Unix-domain socket. It wraps jsonrpc2.Conn and provides
// typed methods for the six RPC operations in the clean-break surface.
type ShimClient struct {
	conn       *jsonrpc2.Conn
	socketPath string
}

// NotificationHandler is called for each live notification received from the
// shim after Subscribe. The method is one of events.MethodSessionUpdate or
// events.MethodRuntimeStateChange. Raw params are passed verbatim.
type NotificationHandler func(ctx context.Context, method string, params json.RawMessage)

// Dial connects to the agent-shim RPC server at the given Unix socket path
// and returns a ShimClient. The connection remains open until Close is called
// or the server shuts down.
func Dial(ctx context.Context, socketPath string) (*ShimClient, error) {
	return dialInternal(ctx, socketPath, nil)
}

// DialWithHandler connects to the shim and registers a notification handler
// for session/update and runtime/stateChange notifications. This is the
// preferred constructor when you need live event delivery after Subscribe.
func DialWithHandler(ctx context.Context, socketPath string, handler NotificationHandler) (*ShimClient, error) {
	return dialInternal(ctx, socketPath, handler)
}

func dialInternal(ctx context.Context, socketPath string, handler NotificationHandler) (*ShimClient, error) {
	nc, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("shim_client: dial %s: %w", socketPath, err)
	}

	stream := jsonrpc2.NewPlainObjectStream(nc)
	h := jsonrpc2.AsyncHandler(&clientHandler{notifHandler: handler})
	conn := jsonrpc2.NewConn(ctx, stream, h)

	return &ShimClient{
		conn:       conn,
		socketPath: socketPath,
	}, nil
}

// Close disconnects from the shim. It does NOT call runtime/stop.
// Use Stop for graceful termination.
func (c *ShimClient) Close() error {
	return c.conn.Close()
}

// DisconnectNotify returns a channel that is closed when the connection
// to the shim is lost (server shutdown, socket closed, etc.).
func (c *ShimClient) DisconnectNotify() <-chan struct{} {
	return c.conn.DisconnectNotify()
}

// ────────────────────────────────────────────────────────────────────────────
// session/* RPC Methods
// ────────────────────────────────────────────────────────────────────────────

// SessionPromptParams is the JSON body for the "session/prompt" method.
type SessionPromptParams struct {
	Prompt string `json:"prompt"`
}

// SessionPromptResult is returned by the "session/prompt" method.
type SessionPromptResult struct {
	StopReason string `json:"stopReason"`
}

// Prompt sends a text prompt to the agent and waits for the turn to complete.
// Returns the stop reason (e.g., "end_turn", "canceled", "tool_use").
func (c *ShimClient) Prompt(ctx context.Context, text string) (SessionPromptResult, error) {
	var result SessionPromptResult
	if err := c.call(ctx, "session/prompt", SessionPromptParams{Prompt: text}, &result); err != nil {
		return SessionPromptResult{}, fmt.Errorf("shim_client: session/prompt: session=%s: %w", c.socketPath, err)
	}
	return result, nil
}

// Cancel cancels the current agent turn. Returns nil on success.
func (c *ShimClient) Cancel(ctx context.Context) error {
	if err := c.call(ctx, "session/cancel", nil, nil); err != nil {
		return fmt.Errorf("shim_client: session/cancel: session=%s: %w", c.socketPath, err)
	}
	return nil
}

// SessionLoadParams is the JSON body for the "session/load" RPC method.
type SessionLoadParams struct {
	SessionID string `json:"sessionId"`
}

// Load sends session/load to the shim with the given ACP sessionId.
// Returns nil on success; returns error if the shim rejects the call (e.g.
// runtime does not support session/load) so the caller can fall back.
func (c *ShimClient) Load(ctx context.Context, sessionID string) error {
	if err := c.call(ctx, "session/load", SessionLoadParams{SessionID: sessionID}, nil); err != nil {
		return fmt.Errorf("shim_client: session/load: session=%s: %w", c.socketPath, err)
	}
	return nil
}

// SessionSubscribeParams is the JSON body for the "session/subscribe" method.
type SessionSubscribeParams struct {
	AfterSeq *int `json:"afterSeq,omitempty"`
	FromSeq  *int `json:"fromSeq,omitempty"`
}

// SessionSubscribeResult is returned by "session/subscribe".
type SessionSubscribeResult struct {
	NextSeq int               `json:"nextSeq"`
	Entries []events.Envelope `json:"entries,omitempty"`
}

// Subscribe registers for live session/update and runtime/stateChange
// notifications. Notifications are dispatched to the handler registered via
// DialWithHandler. Subscribe returns immediately; events arrive asynchronously.
//
// afterSeq, if non-nil, filters out notifications with seq <= afterSeq so
// clients that have replayed history can resume from the right point.
//
// fromSeq, if non-nil, requests atomic backfill: the server reads the event
// log from fromSeq under the same lock that registers the subscription,
// returning backfill entries in the result alongside the live subscription.
// This eliminates the gap between a separate History + Subscribe call pair.
func (c *ShimClient) Subscribe(ctx context.Context, afterSeq, fromSeq *int) (SessionSubscribeResult, error) {
	params := SessionSubscribeParams{AfterSeq: afterSeq, FromSeq: fromSeq}
	var result SessionSubscribeResult
	if err := c.call(ctx, "session/subscribe", params, &result); err != nil {
		return SessionSubscribeResult{}, fmt.Errorf("shim_client: session/subscribe: session=%s: %w", c.socketPath, err)
	}
	return result, nil
}

// ────────────────────────────────────────────────────────────────────────────
// runtime/* RPC Methods
// ────────────────────────────────────────────────────────────────────────────

// RuntimeStatusRecovery holds recovery metadata from the shim's durable log.
type RuntimeStatusRecovery struct {
	LastSeq int `json:"lastSeq"`
}

// RuntimeStatusResult is returned by "runtime/status".
type RuntimeStatusResult struct {
	State    spec.State            `json:"state"`
	Recovery RuntimeStatusRecovery `json:"recovery"`
}

// Status retrieves the current runtime state and recovery metadata.
// The recovery.lastSeq field indicates how many events have been durably
// committed to the log — clients use this to resume subscriptions cleanly.
func (c *ShimClient) Status(ctx context.Context) (RuntimeStatusResult, error) {
	var result RuntimeStatusResult
	if err := c.call(ctx, "runtime/status", nil, &result); err != nil {
		return RuntimeStatusResult{}, fmt.Errorf("shim_client: runtime/status: session=%s: %w", c.socketPath, err)
	}
	return result, nil
}

// RuntimeHistoryParams is the JSON body for the "runtime/history" method.
type RuntimeHistoryParams struct {
	FromSeq *int `json:"fromSeq,omitempty"`
}

// RuntimeHistoryResult is returned by "runtime/history".
type RuntimeHistoryResult struct {
	Entries []events.Envelope `json:"entries"`
}

// History retrieves replayable event history starting from fromSeq (inclusive).
// Returns parse failure when the response is malformed — callers must not
// treat partial results as valid history.
func (c *ShimClient) History(ctx context.Context, fromSeq *int) (RuntimeHistoryResult, error) {
	params := RuntimeHistoryParams{FromSeq: fromSeq}
	var result RuntimeHistoryResult
	if err := c.call(ctx, "runtime/history", params, &result); err != nil {
		return RuntimeHistoryResult{}, fmt.Errorf("shim_client: runtime/history: session=%s: %w", c.socketPath, err)
	}
	return result, nil
}

// Stop requests the shim to gracefully stop the agent and close the server.
// After Stop returns, the connection will be closed by the shim.
func (c *ShimClient) Stop(ctx context.Context) error {
	if err := c.call(ctx, "runtime/stop", nil, nil); err != nil {
		return fmt.Errorf("shim_client: runtime/stop: session=%s: %w", c.socketPath, err)
	}
	return nil
}

// ────────────────────────────────────────────────────────────────────────────
// Helpers
// ────────────────────────────────────────────────────────────────────────────

// call is a helper that performs a JSON-RPC call with params and unmarshals
// the result into resultPtr (if non-nil).
func (c *ShimClient) call(ctx context.Context, method string, params, resultPtr any) error {
	var paramsJSON *json.RawMessage
	if params != nil {
		p, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("marshal params: %w", err)
		}
		paramsJSON = (*json.RawMessage)(&p)
	}

	var result json.RawMessage
	if err := c.conn.Call(ctx, method, paramsJSON, &result); err != nil {
		return err
	}

	if resultPtr != nil && len(result) > 0 {
		if err := json.Unmarshal(result, resultPtr); err != nil {
			return fmt.Errorf("unmarshal result: %w", err)
		}
	}

	return nil
}

// ────────────────────────────────────────────────────────────────────────────
// clientHandler - handles incoming notifications from shim
// ────────────────────────────────────────────────────────────────────────────

// clientHandler implements jsonrpc2.Handler for the ShimClient side.
// It dispatches session/update and runtime/stateChange notifications to the
// registered NotificationHandler.
type clientHandler struct {
	notifHandler NotificationHandler
}

// Handle processes incoming JSON-RPC messages. For the ShimClient, this is
// exclusively inbound notifications (session/update, runtime/stateChange).
// Any inbound requests (unexpected from a shim) are ignored.
func (h *clientHandler) Handle(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	if !req.Notif {
		return
	}

	// Only forward recognized notification methods to the handler.
	switch req.Method {
	case events.MethodSessionUpdate, events.MethodRuntimeStateChange:
		// Valid clean-break notification.
	default:
		// Unknown method — reject silently; don't dispatch to handler.
		return
	}

	if h.notifHandler == nil || req.Params == nil {
		return
	}

	h.notifHandler(ctx, req.Method, *req.Params)
}

// ────────────────────────────────────────────────────────────────────────────
// Notification parsing helpers
// ────────────────────────────────────────────────────────────────────────────

// ParseSessionUpdate parses a session/update notification params into a
// typed SessionUpdateParams. Returns an error if the payload is malformed.
func ParseSessionUpdate(params json.RawMessage) (events.SessionUpdateParams, error) {
	var p events.SessionUpdateParams
	if err := json.Unmarshal(params, &p); err != nil {
		return events.SessionUpdateParams{}, fmt.Errorf("shim_client: parse session/update: %w", err)
	}
	return p, nil
}

// ParseRuntimeStateChange parses a runtime/stateChange notification params
// into a typed RuntimeStateChangeParams. Returns an error if the payload is
// malformed.
func ParseRuntimeStateChange(params json.RawMessage) (events.RuntimeStateChangeParams, error) {
	var p events.RuntimeStateChangeParams
	if err := json.Unmarshal(params, &p); err != nil {
		return events.RuntimeStateChangeParams{}, fmt.Errorf("shim_client: parse runtime/stateChange: %w", err)
	}
	return p, nil
}
