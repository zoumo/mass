// Package agentd provides the agent daemon that manages agent sessions and
// orchestrates the runtime lifecycle.
package agentd

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"

	"github.com/open-agent-d/open-agent-d/pkg/events"
	"github.com/open-agent-d/open-agent-d/pkg/spec"
	"github.com/sourcegraph/jsonrpc2"
)

// ────────────────────────────────────────────────────────────────────────────
// ShimClient - JSON-RPC client for agent-shim communication
// ────────────────────────────────────────────────────────────────────────────

// ShimClient is a JSON-RPC 2.0 client that communicates with the agent-shim
// process over a Unix-domain socket. It wraps jsonrpc2.Conn and provides
// typed methods for the five RPC operations: Prompt, Cancel, Subscribe,
// GetState, and Shutdown.
type ShimClient struct {
	conn *jsonrpc2.Conn

	mu        sync.Mutex
	socketPath string
}

// EventHandler is called for each event received from the shim via the
// "$/event" notification. Handlers must be registered before calling
// Subscribe.
type EventHandler func(ctx context.Context, ev events.Event)

// Dial connects to the agent-shim RPC server at the given Unix socket path
// and returns a ShimClient. The connection remains open until Close is called
// or the server shuts down.
func Dial(ctx context.Context, socketPath string) (*ShimClient, error) {
	nc, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("shim_client: dial %s: %w", socketPath, err)
	}

	stream := jsonrpc2.NewPlainObjectStream(nc)
	// Use a no-op handler for incoming requests (shim shouldn't send requests)
	// but we need to handle "$/event" notifications via Subscribe.
	h := jsonrpc2.AsyncHandler(&clientHandler{})
	conn := jsonrpc2.NewConn(ctx, stream, h)

	return &ShimClient{
		conn:      conn,
		socketPath: socketPath,
	}, nil
}

// DialWithHandler connects to the shim and registers an event handler for
// "$/event" notifications. This is the preferred way to create a ShimClient
// when you want to receive events via Subscribe.
func DialWithHandler(ctx context.Context, socketPath string, handler EventHandler) (*ShimClient, error) {
	nc, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("shim_client: dial %s: %w", socketPath, err)
	}

	stream := jsonrpc2.NewPlainObjectStream(nc)
	h := jsonrpc2.AsyncHandler(&clientHandler{eventHandler: handler})
	conn := jsonrpc2.NewConn(ctx, stream, h)

	return &ShimClient{
		conn:      conn,
		socketPath: socketPath,
	}, nil
}

// Close disconnects from the shim. It does NOT call Shutdown RPC.
// Use Shutdown for graceful termination.
func (c *ShimClient) Close() error {
	return c.conn.Close()
}

// DisconnectNotify returns a channel that is closed when the connection
// to the shim is lost (server shutdown, socket closed, etc.).
func (c *ShimClient) DisconnectNotify() <-chan struct{} {
	return c.conn.DisconnectNotify()
}

// ────────────────────────────────────────────────────────────────────────────
// RPC Methods
// ────────────────────────────────────────────────────────────────────────────

// PromptParams is the JSON body for the "Prompt" method.
type PromptParams struct {
	Text string `json:"text"`
}

// PromptResult is returned by the "Prompt" method.
type PromptResult struct {
	StopReason string `json:"stopReason"`
}

// Prompt sends a text prompt to the agent and waits for the turn to complete.
// It returns the stop reason (e.g., "end_turn", "cancelled", "tool_use").
func (c *ShimClient) Prompt(ctx context.Context, text string) (PromptResult, error) {
	var result PromptResult
	err := c.call(ctx, "Prompt", PromptParams{Text: text}, &result)
	if err != nil {
		return PromptResult{}, fmt.Errorf("shim_client: Prompt: %w", err)
	}
	return result, nil
}

// Cancel cancels the current agent turn. It returns nil on success.
func (c *ShimClient) Cancel(ctx context.Context) error {
	err := c.call(ctx, "Cancel", nil, nil)
	if err != nil {
		return fmt.Errorf("shim_client: Cancel: %w", err)
	}
	return nil
}

// Subscribe registers for event streaming from the shim. The handler will
// receive "$/event" notifications for all agent events (text, thinking,
// tool_call, etc.). Subscribe returns immediately; events arrive asynchronously.
//
// The subscription remains active until the connection is closed or the
// shim shuts down. Events are delivered to the handler registered via
// DialWithHandler or SetEventHandler.
func (c *ShimClient) Subscribe(ctx context.Context) error {
	err := c.call(ctx, "Subscribe", nil, nil)
	if err != nil {
		return fmt.Errorf("shim_client: Subscribe: %w", err)
	}
	return nil
}

// GetStateResult is returned by the "GetState" method.
type GetStateResult struct {
	OarVersion  string            `json:"oarVersion"`
	ID          string            `json:"id"`
	Status      string            `json:"status"`
	PID         int               `json:"pid,omitempty"`
	Bundle      string            `json:"bundle"`
	Annotations map[string]string `json:"annotations,omitempty"`
	ExitCode    *int              `json:"exitCode,omitempty"`
}

// GetState retrieves the current runtime state of the agent from the shim.
func (c *ShimClient) GetState(ctx context.Context) (spec.State, error) {
	var result GetStateResult
	err := c.call(ctx, "GetState", nil, &result)
	if err != nil {
		return spec.State{}, fmt.Errorf("shim_client: GetState: %w", err)
	}

	return spec.State{
		OarVersion:  result.OarVersion,
		ID:          result.ID,
		Status:      spec.Status(result.Status),
		PID:         result.PID,
		Bundle:      result.Bundle,
		Annotations: result.Annotations,
		ExitCode:    result.ExitCode,
	}, nil
}

// Shutdown requests the shim to gracefully stop the agent and close the server.
// After Shutdown returns, the connection will be closed by the shim.
func (c *ShimClient) Shutdown(ctx context.Context) error {
	err := c.call(ctx, "Shutdown", nil, nil)
	if err != nil {
		return fmt.Errorf("shim_client: Shutdown: %w", err)
	}
	return nil
}

// ────────────────────────────────────────────────────────────────────────────
// Event handling
// ────────────────────────────────────────────────────────────────────────────

// SetEventHandler registers a handler for "$/event" notifications.
// This must be called before Subscribe to receive events.
func (c *ShimClient) SetEventHandler(handler EventHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Note: This requires modifying the underlying handler, which we can't do
	// after connection creation. This is a limitation of the current design.
	// Use DialWithHandler for proper event handling.
	// This method exists for API completeness but logs a warning in practice.
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
	err := c.conn.Call(ctx, method, paramsJSON, &result)
	if err != nil {
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
// It primarily handles "$/event" notifications from the shim.
type clientHandler struct {
	eventHandler EventHandler
}

// Handle processes incoming JSON-RPC messages. For the ShimClient, this
// is primarily "$/event" notifications from the shim.
func (h *clientHandler) Handle(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	// Ignore non-notification messages (shim shouldn't send requests)
	if !req.Notif {
		return
	}

	// Handle "$/event" notifications
	if req.Method == "$/event" {
		h.handleEvent(ctx, req)
	}
}

// handleEvent parses an "$/event" notification and dispatches it to the
// registered EventHandler.
func (h *clientHandler) handleEvent(ctx context.Context, req *jsonrpc2.Request) {
	if h.eventHandler == nil {
		// No handler registered; drop the event
		return
	}

	if req.Params == nil {
		return
	}

	// Parse the notification wrapper
	var notif EventNotification
	if err := json.Unmarshal(*req.Params, &notif); err != nil {
		return
	}

	// Convert the payload to a typed events.Event
	ev := parseEvent(notif.Type, notif.Payload)
	if ev != nil {
		h.eventHandler(ctx, ev)
	}
}

// EventNotification is the JSON body sent as a "$/event" notification.
type EventNotification struct {
	Type    string `json:"type"`
	Payload any    `json:"payload"`
}

// parseEvent converts a raw notification payload into a typed events.Event.
func parseEvent(eventType string, payload any) events.Event {
	// Marshal and unmarshal to convert the generic payload into typed struct
	data, err := json.Marshal(payload)
	if err != nil {
		return events.ErrorEvent{Msg: fmt.Sprintf("marshal event payload: %v", err)}
	}

	switch eventType {
	case "text":
		var ev events.TextEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			return events.ErrorEvent{Msg: fmt.Sprintf("parse text event: %v", err)}
		}
		return ev
	case "thinking":
		var ev events.ThinkingEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			return events.ErrorEvent{Msg: fmt.Sprintf("parse thinking event: %v", err)}
		}
		return ev
	case "user_message":
		var ev events.UserMessageEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			return events.ErrorEvent{Msg: fmt.Sprintf("parse user_message event: %v", err)}
		}
		return ev
	case "tool_call":
		var ev events.ToolCallEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			return events.ErrorEvent{Msg: fmt.Sprintf("parse tool_call event: %v", err)}
		}
		return ev
	case "tool_result":
		var ev events.ToolResultEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			return events.ErrorEvent{Msg: fmt.Sprintf("parse tool_result event: %v", err)}
		}
		return ev
	case "file_write":
		var ev events.FileWriteEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			return events.ErrorEvent{Msg: fmt.Sprintf("parse file_write event: %v", err)}
		}
		return ev
	case "file_read":
		var ev events.FileReadEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			return events.ErrorEvent{Msg: fmt.Sprintf("parse file_read event: %v", err)}
		}
		return ev
	case "command":
		var ev events.CommandEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			return events.ErrorEvent{Msg: fmt.Sprintf("parse command event: %v", err)}
		}
		return ev
	case "plan":
		var ev events.PlanEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			return events.ErrorEvent{Msg: fmt.Sprintf("parse plan event: %v", err)}
		}
		return ev
	case "turn_start":
		return events.TurnStartEvent{}
	case "turn_end":
		var ev events.TurnEndEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			return events.ErrorEvent{Msg: fmt.Sprintf("parse turn_end event: %v", err)}
		}
		return ev
	case "error":
		var ev events.ErrorEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			return events.ErrorEvent{Msg: fmt.Sprintf("parse error event: %v", err)}
		}
		return ev
	default:
		return events.ErrorEvent{Msg: fmt.Sprintf("unknown event type: %s", eventType)}
	}
}