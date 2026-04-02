// Package rpc implements a JSON-RPC 2.0 server over a Unix-domain socket.
// It wraps runtime.Manager and events.Translator and exposes five methods:
//
//	Prompt    – forward a text prompt to the agent and return the stop reason
//	Cancel    – cancel the current agent turn
//	Subscribe – stream typed agent events as JSON-RPC notifications
//	GetState  – return the persisted spec.State for the agent
//	Shutdown  – stop the agent and close the server
package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"sync"

	"github.com/coder/acp-go-sdk"
	"github.com/open-agent-d/open-agent-d/pkg/events"
	"github.com/open-agent-d/open-agent-d/pkg/runtime"
	"github.com/sourcegraph/jsonrpc2"
)

// ────────────────────────────────────────────────────────────────────────────
// Request / response types
// ────────────────────────────────────────────────────────────────────────────

// PromptParams is the JSON body for the "Prompt" method.
type PromptParams struct {
	// Text is the user-supplied prompt text.
	Text string `json:"text"`
}

// PromptResult is returned by the "Prompt" method.
type PromptResult struct {
	// StopReason mirrors acp.PromptResponse.StopReason.
	StopReason string `json:"stopReason"`
}

// GetHistoryParams is the JSON body for the "GetHistory" method.
type GetHistoryParams struct {
	// FromSeq is the inclusive start offset. 0 returns all entries.
	FromSeq int `json:"fromSeq"`
}

// GetHistoryResult is returned by the "GetHistory" method.
type GetHistoryResult struct {
	// Entries is the slice of log entries from FromSeq onward.
	Entries []events.LogEntry `json:"entries"`
}
// It re-exports the fields of spec.State with JSON tags.
type GetStateResult struct {
	OarVersion  string            `json:"oarVersion"`
	ID          string            `json:"id"`
	Status      string            `json:"status"`
	PID         int               `json:"pid,omitempty"`
	Bundle      string            `json:"bundle"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// EventNotification is the JSON body sent as a "$/event" notification.
type EventNotification struct {
	// Type is the event discriminator (matches Event.eventType()).
	Type string `json:"type"`
	// Payload is the event-specific data, serialised as a JSON object.
	Payload any `json:"payload"`
}

// ────────────────────────────────────────────────────────────────────────────
// Server
// ────────────────────────────────────────────────────────────────────────────

// Server is a JSON-RPC 2.0 server that exposes the agent runtime over a
// Unix-domain socket.
type Server struct {
	mgr     *runtime.Manager
	trans   *events.Translator
	path    string // socket path
	logPath string // path to events.jsonl for GetHistory

	mu       sync.Mutex
	listener net.Listener
	done     chan struct{} // closed by Shutdown
	once     sync.Once    // guards done-close
}

// New creates a Server that listens on socketPath.  Call Serve to begin
// accepting connections.
func New(mgr *runtime.Manager, trans *events.Translator, socketPath, logPath string) *Server {
	return &Server{
		mgr:     mgr,
		trans:   trans,
		path:    socketPath,
		logPath: logPath,
		done:    make(chan struct{}),
	}
}

// Serve creates the Unix socket, enters the accept loop, and blocks until
// the server is shut down.  It is safe to call from a goroutine.
func (s *Server) Serve() error {
	ln, err := net.Listen("unix", s.path)
	if err != nil {
		return fmt.Errorf("rpc: listen %s: %w", s.path, err)
	}
	s.mu.Lock()
	s.listener = ln
	s.mu.Unlock()

	for {
		conn, err := ln.Accept()
		if err != nil {
			// Check whether we shut down intentionally.
			select {
			case <-s.done:
				return nil
			default:
			}
			log.Printf("rpc: accept error: %v", err)
			return fmt.Errorf("rpc: accept: %w", err)
		}
		go s.handleConn(conn)
	}
}

// Shutdown closes the listener, marks the server done, and kills the agent.
// It is idempotent.
func (s *Server) Shutdown(ctx context.Context) error {
	s.once.Do(func() { close(s.done) })

	s.mu.Lock()
	ln := s.listener
	s.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}

	return s.mgr.Kill(ctx)
}

// ────────────────────────────────────────────────────────────────────────────
// Per-connection handler
// ────────────────────────────────────────────────────────────────────────────

// handleConn wraps the net.Conn in a jsonrpc2.Conn and dispatches requests.
func (s *Server) handleConn(nc net.Conn) {
	ctx := context.Background()
	stream := jsonrpc2.NewPlainObjectStream(nc)
	h := jsonrpc2.AsyncHandler(&connHandler{srv: s})
	c := jsonrpc2.NewConn(ctx, stream, h)
	<-c.DisconnectNotify()
}

// connHandler implements jsonrpc2.Handler for a single client connection.
type connHandler struct {
	srv *Server
}

// Handle dispatches incoming JSON-RPC requests to the appropriate method.
func (h *connHandler) Handle(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	// Notifications from the client are not expected; ignore them.
	if req.Notif {
		return
	}

	switch req.Method {
	case "Prompt":
		h.handlePrompt(ctx, conn, req)
	case "Cancel":
		h.handleCancel(ctx, conn, req)
	case "Subscribe":
		h.handleSubscribe(ctx, conn, req)
	case "GetState":
		h.handleGetState(ctx, conn, req)
	case "GetHistory":
		h.handleGetHistory(ctx, conn, req)
	case "Shutdown":
		h.handleShutdown(ctx, conn, req)
	default:
		_ = conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{
			Code:    jsonrpc2.CodeMethodNotFound,
			Message: fmt.Sprintf("unknown method %q", req.Method),
		})
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Method handlers
// ────────────────────────────────────────────────────────────────────────────

// handlePrompt decodes a PromptParams, forwards the text to the agent, and
// replies with PromptResult once the agent turn completes.
func (h *connHandler) handlePrompt(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var p PromptParams
	if err := unmarshalParams(req, &p); err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	resp, err := h.srv.mgr.Prompt(ctx, []acp.ContentBlock{acp.TextBlock(p.Text)})
	if err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError, err.Error())
		return
	}

	_ = conn.Reply(ctx, req.ID, PromptResult{StopReason: string(resp.StopReason)})
}

// handleCancel forwards a cancel request to the agent.
func (h *connHandler) handleCancel(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	if err := h.srv.mgr.Cancel(ctx); err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError, err.Error())
		return
	}
	_ = conn.Reply(ctx, req.ID, nil)
}

// handleSubscribe registers a Translator subscription and pumps events to the
// client as "$/event" notifications until the client disconnects.
// The method returns an empty reply immediately; events arrive asynchronously.
func (h *connHandler) handleSubscribe(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	ch, subID := h.srv.trans.Subscribe()

	// Acknowledge the subscription before streaming events.
	_ = conn.Reply(ctx, req.ID, nil)

	// Stream events in a goroutine; exit when the client disconnects or the
	// subscription channel is closed.
	go func() {
		defer h.srv.trans.Unsubscribe(subID)
		disconnect := conn.DisconnectNotify()
		for {
			select {
			case <-disconnect:
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				notif := toNotification(ev)
				if err := conn.Notify(ctx, "$/event", notif); err != nil {
					// Connection closed; exit cleanly.
					return
				}
			}
		}
	}()
}

// handleGetHistory reads the event log from a given offset and returns all
// entries from that offset onward. fromSeq=0 returns the complete history.
func (h *connHandler) handleGetHistory(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var p GetHistoryParams
	// params is optional — default fromSeq=0
	if req.Params != nil {
		if err := unmarshalParams(req, &p); err != nil {
			replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
			return
		}
	}
	entries, err := events.ReadEventLog(h.srv.logPath, p.FromSeq)
	if err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError, err.Error())
		return
	}
	if entries == nil {
		entries = []events.LogEntry{}
	}
	_ = conn.Reply(ctx, req.ID, GetHistoryResult{Entries: entries})
}

// handleGetState reads the persisted agent state and returns it.
func (h *connHandler) handleGetState(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	st, err := h.srv.mgr.GetState()
	if err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError, err.Error())
		return
	}
	_ = conn.Reply(ctx, req.ID, GetStateResult{
		OarVersion:  st.OarVersion,
		ID:          st.ID,
		Status:      string(st.Status),
		PID:         st.PID,
		Bundle:      st.Bundle,
		Annotations: st.Annotations,
	})
}

// handleShutdown replies OK then shuts down the server and agent.
func (h *connHandler) handleShutdown(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	_ = conn.Reply(ctx, req.ID, nil)
	if err := h.srv.Shutdown(ctx); err != nil {
		log.Printf("rpc: shutdown error: %v", err)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Helpers
// ────────────────────────────────────────────────────────────────────────────

// unmarshalParams decodes req.Params into dst.
func unmarshalParams(req *jsonrpc2.Request, dst any) error {
	if req.Params == nil {
		return fmt.Errorf("missing params")
	}
	return json.Unmarshal(*req.Params, dst)
}

// replyError sends a JSON-RPC error response.
func replyError(ctx context.Context, conn *jsonrpc2.Conn, id jsonrpc2.ID, code int64, msg string) {
	_ = conn.ReplyWithError(ctx, id, &jsonrpc2.Error{Code: code, Message: msg})
}

// toNotification converts a typed events.Event into an EventNotification
// ready to send as a JSON-RPC notification payload.
func toNotification(ev events.Event) EventNotification {
	return EventNotification{
		Type:    eventTypeName(ev),
		Payload: ev,
	}
}

// eventTypeName returns the human-readable type discriminator for ev.
// The events package uses an unexported method, so we use a type switch here.
func eventTypeName(ev events.Event) string {
	switch ev.(type) {
	case events.TextEvent:
		return "text"
	case events.ThinkingEvent:
		return "thinking"
	case events.UserMessageEvent:
		return "user_message"
	case events.ToolCallEvent:
		return "tool_call"
	case events.ToolResultEvent:
		return "tool_result"
	case events.FileWriteEvent:
		return "file_write"
	case events.FileReadEvent:
		return "file_read"
	case events.CommandEvent:
		return "command"
	case events.PlanEvent:
		return "plan"
	case events.TurnStartEvent:
		return "turn_start"
	case events.TurnEndEvent:
		return "turn_end"
	case events.ErrorEvent:
		return "error"
	default:
		return "unknown"
	}
}
