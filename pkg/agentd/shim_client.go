// Package agentd provides the agent daemon that manages agent sessions and
// orchestrates the runtime lifecycle.
package agentd

import (
	"context"
	"encoding/json"
	"fmt"
	"net"

	"github.com/sourcegraph/jsonrpc2"

	"github.com/zoumo/oar/api"
	"github.com/zoumo/oar/pkg/events"
	"github.com/zoumo/oar/api/shim"
)

// ────────────────────────────────────────────────────────────────────────────
// ShimClient - JSON-RPC client for agent-shim communication (clean-break surface)
//
// Methods exposed:
//   session/prompt    — send a prompt and wait for the turn to complete
//   session/cancel    — cancel the current agent turn
//   session/load      — restore a prior ACP session (try_reload restart policy)
//   session/subscribe — register for live session/update and runtime/state_change notifications
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
// shim after Subscribe. The method is api.MethodShimEvent. Raw params are
// passed verbatim.
type NotificationHandler func(ctx context.Context, method string, params json.RawMessage)

// Dial connects to the agent-shim RPC server at the given Unix socket path
// and returns a ShimClient. The connection remains open until Close is called
// or the server shuts down.
func Dial(ctx context.Context, socketPath string) (*ShimClient, error) {
	return dialInternal(ctx, socketPath, nil)
}

// DialWithHandler connects to the shim and registers a notification handler
// for session/update and runtime/state_change notifications. This is the
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
	// Keep shim notifications serialized on the client side. The shim already
	// assigns monotonically increasing seq values; dispatching each inbound
	// notification in its own goroutine can reorder delivery into agentd.
	conn := jsonrpc2.NewConn(ctx, stream, &clientHandler{notifHandler: handler})

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

// Prompt sends a text prompt to the agent and waits for the turn to complete.
// Returns the stop reason (e.g., "end_turn", "canceled", "tool_use").
func (c *ShimClient) Prompt(ctx context.Context, text string) (shim.SessionPromptResult, error) {
	var result shim.SessionPromptResult
	if err := c.call(ctx, api.MethodSessionPrompt, shim.SessionPromptParams{Prompt: text}, &result); err != nil {
		return shim.SessionPromptResult{}, fmt.Errorf("shim_client: session/prompt: session=%s: %w", c.socketPath, err)
	}
	return result, nil
}

// Cancel cancels the current agent turn. Returns nil on success.
func (c *ShimClient) Cancel(ctx context.Context) error {
	if err := c.call(ctx, api.MethodSessionCancel, nil, nil); err != nil {
		return fmt.Errorf("shim_client: session/cancel: session=%s: %w", c.socketPath, err)
	}
	return nil
}

// Load sends session/load to the shim with the given ACP sessionId.
// Returns nil on success; returns error if the shim rejects the call (e.g.
// runtime does not support session/load) so the caller can fall back.
func (c *ShimClient) Load(ctx context.Context, sessionID string) error {
	if err := c.call(ctx, api.MethodSessionLoad, shim.SessionLoadParams{SessionID: sessionID}, nil); err != nil {
		return fmt.Errorf("shim_client: session/load: session=%s: %w", c.socketPath, err)
	}
	return nil
}

// Subscribe registers for live session/update and runtime/state_change
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
func (c *ShimClient) Subscribe(ctx context.Context, afterSeq, fromSeq *int) (shim.SessionSubscribeResult, error) {
	params := shim.SessionSubscribeParams{AfterSeq: afterSeq, FromSeq: fromSeq}
	var result shim.SessionSubscribeResult
	if err := c.call(ctx, api.MethodSessionSubscribe, params, &result); err != nil {
		return shim.SessionSubscribeResult{}, fmt.Errorf("shim_client: session/subscribe: session=%s: %w", c.socketPath, err)
	}
	return result, nil
}

// ────────────────────────────────────────────────────────────────────────────
// runtime/* RPC Methods
// ────────────────────────────────────────────────────────────────────────────

// Status retrieves the current runtime state and recovery metadata.
// The recovery.lastSeq field indicates how many events have been durably
// committed to the log — clients use this to resume subscriptions cleanly.
func (c *ShimClient) Status(ctx context.Context) (shim.RuntimeStatusResult, error) {
	var result shim.RuntimeStatusResult
	if err := c.call(ctx, api.MethodRuntimeStatus, nil, &result); err != nil {
		return shim.RuntimeStatusResult{}, fmt.Errorf("shim_client: runtime/status: session=%s: %w", c.socketPath, err)
	}
	return result, nil
}

// History retrieves replayable event history starting from fromSeq (inclusive).
// Returns parse failure when the response is malformed — callers must not
// treat partial results as valid history.
func (c *ShimClient) History(ctx context.Context, fromSeq *int) (shim.RuntimeHistoryResult, error) {
	params := shim.RuntimeHistoryParams{FromSeq: fromSeq}
	var result shim.RuntimeHistoryResult
	if err := c.call(ctx, api.MethodRuntimeHistory, params, &result); err != nil {
		return shim.RuntimeHistoryResult{}, fmt.Errorf("shim_client: runtime/history: session=%s: %w", c.socketPath, err)
	}
	return result, nil
}

// Stop requests the shim to gracefully stop the agent and close the server.
// After Stop returns, the connection will be closed by the shim.
func (c *ShimClient) Stop(ctx context.Context) error {
	if err := c.call(ctx, api.MethodRuntimeStop, nil, nil); err != nil {
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
// It dispatches session/update and runtime/state_change notifications to the
// registered NotificationHandler.
type clientHandler struct {
	notifHandler NotificationHandler
}

// Handle processes incoming JSON-RPC messages. For the ShimClient, this is
// exclusively inbound notifications (shim/event).
// Any inbound requests (unexpected from a shim) are ignored.
func (h *clientHandler) Handle(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	if !req.Notif {
		return
	}

	// Only forward the unified shim/event notification to the handler.
	if req.Method != api.MethodShimEvent {
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

// ParseShimEvent parses a shim/event notification params into a typed ShimEvent.
// Returns an error if the payload is malformed.
func ParseShimEvent(params json.RawMessage) (events.ShimEvent, error) {
	var ev events.ShimEvent
	if err := json.Unmarshal(params, &ev); err != nil {
		return events.ShimEvent{}, fmt.Errorf("shim_client: parse shim/event: %w", err)
	}
	return ev, nil
}
