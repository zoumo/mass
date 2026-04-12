// Package rpc implements a JSON-RPC 2.0 server over a Unix-domain socket.
// It wraps runtime.Manager and events.Translator and exposes the clean-break
// shim surface:
//
//	session/prompt
//	session/cancel
//	session/subscribe
//	runtime/status
//	runtime/history
//	runtime/stop
package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"sync"

	acp "github.com/coder/acp-go-sdk"
	"github.com/sourcegraph/jsonrpc2"

	"github.com/open-agent-d/open-agent-d/pkg/events"
	"github.com/open-agent-d/open-agent-d/pkg/runtime"
	"github.com/open-agent-d/open-agent-d/pkg/shimapi"
)

// Server is a JSON-RPC 2.0 server that exposes the agent runtime over a
// Unix-domain socket.
type Server struct {
	mgr     *runtime.Manager
	trans   *events.Translator
	path    string
	logPath string
	logger  *slog.Logger

	mu       sync.Mutex
	listener net.Listener
	done     chan struct{}
	once     sync.Once
}

// New creates a Server that listens on socketPath. Call Serve to begin accepting connections.
func New(mgr *runtime.Manager, trans *events.Translator, socketPath, logPath string, logger *slog.Logger) *Server {
	return &Server{mgr: mgr, trans: trans, path: socketPath, logPath: logPath, logger: logger, done: make(chan struct{})}
}

// Serve creates the Unix socket, enters the accept loop, and blocks until the server is shut down.
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
			select {
			case <-s.done:
				return nil
			default:
			}
			s.logger.Error("accept error", "error", err)
			return fmt.Errorf("rpc: accept: %w", err)
		}
		go s.handleConn(conn)
	}
}

// Shutdown closes the listener, marks the server done, and kills the agent.
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

func (s *Server) handleConn(nc net.Conn) {
	ctx := context.Background()
	stream := jsonrpc2.NewPlainObjectStream(nc)
	h := jsonrpc2.AsyncHandler(&connHandler{srv: s})
	c := jsonrpc2.NewConn(ctx, stream, h)
	<-c.DisconnectNotify()
}

type connHandler struct {
	srv *Server
}

func (h *connHandler) Handle(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	if req.Notif {
		return
	}

	switch req.Method {
	case "session/prompt":
		h.handlePrompt(ctx, conn, req)
	case "session/cancel":
		h.handleCancel(ctx, conn, req)
	case "session/subscribe":
		h.handleSubscribe(ctx, conn, req)
	case "runtime/status":
		h.handleStatus(ctx, conn, req)
	case "runtime/history":
		h.handleHistory(ctx, conn, req)
	case "runtime/stop":
		h.handleStop(ctx, conn, req)
	default:
		_ = conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{
			Code:    jsonrpc2.CodeMethodNotFound,
			Message: fmt.Sprintf("unknown method %q", req.Method),
		})
	}
}

func (h *connHandler) handlePrompt(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var p shimapi.SessionPromptParams
	if err := unmarshalParams(req, &p); err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}
	if p.Prompt == "" {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, "missing prompt")
		return
	}

	h.srv.trans.NotifyTurnStart()
	h.srv.trans.NotifyUserPrompt(p.Prompt)
	resp, err := h.srv.mgr.Prompt(ctx, []acp.ContentBlock{acp.TextBlock(p.Prompt)})
	stopReason := "error"
	if err == nil {
		stopReason = string(resp.StopReason)
	}
	h.srv.trans.NotifyTurnEnd(acp.StopReason(stopReason))

	if err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError, err.Error())
		return
	}
	_ = conn.Reply(ctx, req.ID, shimapi.SessionPromptResult{StopReason: string(resp.StopReason)})
}

func (h *connHandler) handleCancel(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	if err := h.srv.mgr.Cancel(ctx); err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError, err.Error())
		return
	}
	_ = conn.Reply(ctx, req.ID, nil)
}

func (h *connHandler) handleSubscribe(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var p shimapi.SessionSubscribeParams
	if req.Params != nil {
		if err := unmarshalParams(req, &p); err != nil {
			replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
			return
		}
	}
	if p.AfterSeq != nil && *p.AfterSeq < 0 {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, "afterSeq must be >= 0")
		return
	}
	if p.FromSeq != nil && *p.FromSeq < 0 {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, "fromSeq must be >= 0")
		return
	}

	// Atomic path: when fromSeq is present, use SubscribeFromSeq to read
	// history and register the subscription under a single lock hold.
	if p.FromSeq != nil {
		entries, ch, subID, nextSeq, err := h.srv.trans.SubscribeFromSeq(h.srv.logPath, *p.FromSeq)
		if err != nil {
			replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError, err.Error())
			return
		}

		_ = conn.Reply(ctx, req.ID, shimapi.SessionSubscribeResult{NextSeq: nextSeq, Entries: entries})

		go func() {
			defer h.srv.trans.Unsubscribe(subID)
			disconnect := conn.DisconnectNotify()
			for {
				select {
				case <-disconnect:
					return
				case env, ok := <-ch:
					if !ok {
						return
					}
					if err := conn.Notify(ctx, env.Method, env.Params); err != nil {
						return
					}
				}
			}
		}()
		return
	}

	// Legacy path: subscribe without backfill.
	ch, subID, nextSeq := h.srv.trans.Subscribe()
	floor := nextSeq - 1
	if p.AfterSeq != nil {
		floor = *p.AfterSeq
	}

	_ = conn.Reply(ctx, req.ID, shimapi.SessionSubscribeResult{NextSeq: nextSeq})

	go func() {
		defer h.srv.trans.Unsubscribe(subID)
		disconnect := conn.DisconnectNotify()
		for {
			select {
			case <-disconnect:
				return
			case env, ok := <-ch:
				if !ok {
					return
				}
				seq, err := env.Seq()
				if err != nil || seq <= floor {
					continue
				}
				if err := conn.Notify(ctx, env.Method, env.Params); err != nil {
					return
				}
			}
		}
	}()
}

func (h *connHandler) handleHistory(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var p shimapi.RuntimeHistoryParams
	if req.Params != nil {
		if err := unmarshalParams(req, &p); err != nil {
			replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
			return
		}
	}
	fromSeq := 0
	if p.FromSeq != nil {
		fromSeq = *p.FromSeq
	}
	if fromSeq < 0 {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, "fromSeq must be >= 0")
		return
	}

	entries, err := events.ReadEventLog(h.srv.logPath, fromSeq)
	if err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError, err.Error())
		return
	}
	if entries == nil {
		entries = []events.Envelope{}
	}
	_ = conn.Reply(ctx, req.ID, shimapi.RuntimeHistoryResult{Entries: entries})
}

func (h *connHandler) handleStatus(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	st, err := h.srv.mgr.GetState()
	if err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError, err.Error())
		return
	}
	_ = conn.Reply(ctx, req.ID, shimapi.RuntimeStatusResult{
		State: st,
		Recovery: shimapi.RuntimeStatusRecovery{
			LastSeq: h.srv.trans.LastSeq(),
		},
	})
}

func (h *connHandler) handleStop(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	_ = conn.Reply(ctx, req.ID, nil)
	go func() {
		if err := h.srv.Shutdown(context.Background()); err != nil {
			h.srv.logger.Error("stop error", "error", err)
		}
	}()
}

func unmarshalParams(req *jsonrpc2.Request, dst any) error {
	if req.Params == nil {
		return fmt.Errorf("missing params")
	}
	return json.Unmarshal(*req.Params, dst)
}

func replyError(ctx context.Context, conn *jsonrpc2.Conn, id jsonrpc2.ID, code int64, msg string) {
	_ = conn.ReplyWithError(ctx, id, &jsonrpc2.Error{Code: code, Message: msg})
}
