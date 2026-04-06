// Package ari implements the Agent Runtime Interface (ARI) JSON-RPC server.
// This file defines the Server that exposes workspace/* and session/* methods over a Unix socket.
package ari

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/open-agent-d/open-agent-d/pkg/agentd"
	"github.com/open-agent-d/open-agent-d/pkg/meta"
	"github.com/open-agent-d/open-agent-d/pkg/workspace"
	"github.com/sourcegraph/jsonrpc2"
)

// ────────────────────────────────────────────────────────────────────────────
// Server
// ────────────────────────────────────────────────────────────────────────────

// Server is a JSON-RPC 2.0 server that exposes workspace/* and session/* methods over a
// Unix-domain socket.
type Server struct {
	// manager prepares and cleans up workspaces.
	manager *workspace.WorkspaceManager

	// registry tracks workspace metadata.
	registry *Registry

	// sessions manages session lifecycle with state machine validation.
	sessions *agentd.SessionManager

	// processes manages shim process lifecycle.
	processes *agentd.ProcessManager

	// runtimeClasses resolves runtime class names to launch configurations.
	runtimeClasses *agentd.RuntimeClassRegistry

	// config is the agentd configuration.
	config agentd.Config

	// store is the metadata store for persisting workspaces and sessions.
	store *meta.Store

	// baseDir is the root directory for workspace creation.
	// Defaults to os.TempDir() + "/agentd-workspaces" if empty.
	baseDir string

	// path is the Unix socket path.
	path string

	// mu protects listener and done.
	mu sync.Mutex

	// listener is the Unix socket listener.
	listener net.Listener

	// done is closed by Shutdown to signal server termination.
	done chan struct{}

	// once guards done-close to ensure single close.
	once sync.Once
}

// New creates a Server that listens on socketPath.
// If baseDir is empty, defaults to os.TempDir() + "/agentd-workspaces".
// Call Serve to begin accepting connections.
//
// Parameters:
//   - manager: WorkspaceManager for workspace preparation
//   - registry: Registry for workspace metadata tracking
//   - sessions: SessionManager for session lifecycle management
//   - processes: ProcessManager for shim process lifecycle
//   - runtimeClasses: RuntimeClassRegistry for runtime class resolution
//   - config: agentd configuration
//   - store: metadata store for persisting workspaces and sessions
//   - socketPath: Unix socket path for ARI server
//   - baseDir: root directory for workspace creation
func New(manager *workspace.WorkspaceManager, registry *Registry, sessions *agentd.SessionManager, processes *agentd.ProcessManager, runtimeClasses *agentd.RuntimeClassRegistry, config agentd.Config, store *meta.Store, socketPath, baseDir string) *Server {
	if baseDir == "" {
		baseDir = filepath.Join(os.TempDir(), "agentd-workspaces")
	}
	return &Server{
		manager:        manager,
		registry:       registry,
		sessions:       sessions,
		processes:      processes,
		runtimeClasses: runtimeClasses,
		config:         config,
		store:          store,
		baseDir:        baseDir,
		path:           socketPath,
		done:           make(chan struct{}),
	}
}

// Serve creates the Unix socket, enters the accept loop, and blocks until
// the server is shut down. It is safe to call from a goroutine.
func (s *Server) Serve() error {
	// Ensure baseDir exists.
	if err := os.MkdirAll(s.baseDir, 0755); err != nil {
		return fmt.Errorf("ari: create baseDir %s: %w", s.baseDir, err)
	}

	// Create Unix socket listener.
	ln, err := net.Listen("unix", s.path)
	if err != nil {
		return fmt.Errorf("ari: listen %s: %w", s.path, err)
	}
	s.mu.Lock()
	s.listener = ln
	s.mu.Unlock()

	// Accept loop.
	for {
		conn, err := ln.Accept()
		if err != nil {
			// Check whether we shut down intentionally.
			select {
			case <-s.done:
				return nil
			default:
			}
			log.Printf("ari: accept error: %v", err)
			return fmt.Errorf("ari: accept: %w", err)
		}
		go s.handleConn(conn)
	}
}

// Shutdown closes the listener and marks the server done.
// It is idempotent.
func (s *Server) Shutdown(ctx context.Context) error {
	s.once.Do(func() { close(s.done) })

	s.mu.Lock()
	ln := s.listener
	s.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}

	return nil
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
	// Workspace methods
	case "workspace/prepare":
		h.handleWorkspacePrepare(ctx, conn, req)
	case "workspace/list":
		h.handleWorkspaceList(ctx, conn, req)
	case "workspace/cleanup":
		h.handleWorkspaceCleanup(ctx, conn, req)

	// Session methods
	case "session/new":
		h.handleSessionNew(ctx, conn, req)
	case "session/prompt":
		h.handleSessionPrompt(ctx, conn, req)
	case "session/cancel":
		h.handleSessionCancel(ctx, conn, req)
	case "session/stop":
		h.handleSessionStop(ctx, conn, req)
	case "session/remove":
		h.handleSessionRemove(ctx, conn, req)
	case "session/list":
		h.handleSessionList(ctx, conn, req)
	case "session/status":
		h.handleSessionStatus(ctx, conn, req)
	case "session/attach":
		h.handleSessionAttach(ctx, conn, req)
	case "session/detach":
		h.handleSessionDetach(ctx, conn, req)

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

// handleWorkspacePrepare prepares a workspace from a WorkspaceSpec.
// Workflow:
//  1. Unmarshal WorkspacePrepareParams
//  2. Generate UUID for workspaceId
//  3. Generate targetDir under baseDir using UUID
//  4. Call manager.Prepare
//  5. Add to registry
//  6. Return WorkspacePrepareResult
//
// Error handling:
//   - Invalid params: returns InvalidParams error
//   - Prepare failure: returns InvalidParams with WorkspaceError Phase
//   - Prepare timeout: returns InternalError
func (h *connHandler) handleWorkspacePrepare(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var p WorkspacePrepareParams
	if err := unmarshalParams(req, &p); err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	// Generate UUID for workspaceId.
	workspaceId := uuid.New().String()

	// Generate targetDir under baseDir using UUID.
	targetDir := filepath.Join(h.srv.baseDir, workspaceId)

	// Call manager.Prepare.
	workspacePath, err := h.srv.manager.Prepare(ctx, p.Spec, targetDir)
	if err != nil {
		// Check if error is a WorkspaceError to extract Phase.
		if wsErr, ok := err.(*workspace.WorkspaceError); ok {
			replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
				fmt.Sprintf("%s failed: %s", wsErr.Phase, wsErr.Message))
			return
		}
		// Generic error: return InternalError.
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError, err.Error())
		return
	}

	// Add to registry.
	h.srv.registry.Add(workspaceId, p.Spec.Metadata.Name, workspacePath, p.Spec)

	// Persist to database for foreign key constraint support.
	if h.srv.store != nil {
		workspace := &meta.Workspace{
			ID:       workspaceId,
			Name:     p.Spec.Metadata.Name,
			Path:     workspacePath,
			Status:   meta.WorkspaceStatusActive,
			RefCount: 0,
		}
		if err := h.srv.store.CreateWorkspace(ctx, workspace); err != nil {
			// Log the error but don't fail - registry is the source of truth for workspace/* methods.
			log.Printf("ari: failed to persist workspace %s to database: %v", workspaceId, err)
		}
	}

	// Return result.
	_ = conn.Reply(ctx, req.ID, WorkspacePrepareResult{
		WorkspaceId: workspaceId,
		Path:        workspacePath,
		Status:      "ready",
	})
}

// handleWorkspaceList returns all registered workspaces.
// Workflow:
//  1. Call registry.List()
//  2. Convert WorkspaceMeta to WorkspaceInfo
//  3. Return WorkspaceListResult
func (h *connHandler) handleWorkspaceList(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	// Params are optional (empty struct). Accept nil params.
	if req.Params != nil {
		var p WorkspaceListParams
		if err := unmarshalParams(req, &p); err != nil {
			replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
			return
		}
	}

	// Get all workspaces from registry.
	list := h.srv.registry.List()

	// Convert to WorkspaceInfo.
	workspaces := make([]WorkspaceInfo, 0, len(list))
	for _, meta := range list {
		workspaces = append(workspaces, WorkspaceInfo{
			WorkspaceId: meta.Id,
			Name:        meta.Name,
			Path:        meta.Path,
			Status:      meta.Status,
			Refs:        meta.Refs,
		})
	}

	// Return result.
	_ = conn.Reply(ctx, req.ID, WorkspaceListResult{Workspaces: workspaces})
}

// handleWorkspaceCleanup cleans up a workspace.
// Workflow:
//  1. Unmarshal WorkspaceCleanupParams
//  2. Get workspace from registry
//  3. Check RefCount > 0 → return error
//  4. Call manager.Cleanup
//  5. Remove from registry
//  6. Return nil result
//
// Error handling:
//   - Invalid params: returns InvalidParams error
//   - RefCount > 0: returns InternalError
//   - Cleanup failure: returns InternalError with WorkspaceError Phase
//   - Cleanup timeout: returns InternalError
func (h *connHandler) handleWorkspaceCleanup(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var p WorkspaceCleanupParams
	if err := unmarshalParams(req, &p); err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	// Get workspace from registry.
	meta := h.srv.registry.Get(p.WorkspaceId)
	if meta == nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("workspace %q not found", p.WorkspaceId))
		return
	}

	// Check RefCount > 0.
	if meta.RefCount > 0 {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError,
			fmt.Sprintf("workspace %q has %d active references", p.WorkspaceId, meta.RefCount))
		return
	}

	// Call manager.Cleanup.
	err := h.srv.manager.Cleanup(ctx, meta.Path, meta.Spec)
	if err != nil {
		// Check if error is a WorkspaceError to extract Phase.
		if wsErr, ok := err.(*workspace.WorkspaceError); ok {
			replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError,
				fmt.Sprintf("%s failed: %s", wsErr.Phase, wsErr.Message))
			return
		}
		// Generic error: return InternalError.
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError, err.Error())
		return
	}

	// Remove from registry.
	h.srv.registry.Remove(p.WorkspaceId)

	// Delete from database.
	if h.srv.store != nil {
		if _, err := h.srv.store.DeleteWorkspace(ctx, p.WorkspaceId); err != nil {
			// Log the error but don't fail - cleanup already succeeded.
			log.Printf("ari: failed to delete workspace %s from database: %v", p.WorkspaceId, err)
		}
	}

	// Return success (nil result).
	_ = conn.Reply(ctx, req.ID, nil)
}

// ────────────────────────────────────────────────────────────────────────────
// Session handlers
// ────────────────────────────────────────────────────────────────────────────

// handleSessionNew creates a new session in the "created" state.
// Workflow:
//  1. Unmarshal SessionNewParams
//  2. Generate UUID for sessionId
//  3. Create new meta.Session with State="created"
//  4. Call sessions.Create
//  5. Return SessionNewResult
//
// Error handling:
//   - Invalid params: returns InvalidParams error
//   - Create failure: returns InternalError
func (h *connHandler) handleSessionNew(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var p SessionNewParams
	if err := unmarshalParams(req, &p); err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	// Generate UUID for sessionId.
	sessionId := uuid.New().String()

	// Create new meta.Session with State="created".
	session := &meta.Session{
		ID:          sessionId,
		WorkspaceID: p.WorkspaceId,
		RuntimeClass: p.RuntimeClass,
		Room:        p.Room,
		RoomAgent:   p.RoomAgent,
		Labels:      p.Labels,
		State:       meta.SessionStateCreated,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	log.Printf("ari: session/new creating session %s for workspace %s", sessionId, p.WorkspaceId)

	// Call sessions.Create.
	if err := h.srv.sessions.Create(ctx, session); err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError,
			fmt.Sprintf("create session failed: %s", err.Error()))
		return
	}

	// Return result.
	_ = conn.Reply(ctx, req.ID, SessionNewResult{
		SessionId: sessionId,
		State:     string(meta.SessionStateCreated),
	})
}

// handleSessionPrompt sends a prompt to an agent session.
// Workflow:
//  1. Unmarshal SessionPromptParams
//  2. Get session from sessions.Get
//  3. If state=="created", call processes.Start (auto-start)
//  4. Call processes.Connect to get ShimClient
//  5. Call client.Prompt
//  6. Return SessionPromptResult
//
// Error handling:
//   - Invalid params (session not found): returns InvalidParams
//   - Start failure: returns InternalError "start session failed"
//   - Connect failure: returns InternalError
//   - Prompt failure: returns InternalError "prompt failed"
//   - Prompt timeout (30s): returns InternalError
func (h *connHandler) handleSessionPrompt(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var p SessionPromptParams
	if err := unmarshalParams(req, &p); err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	// Get session from sessions.Get.
	session, err := h.srv.sessions.Get(ctx, p.SessionId)
	if err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("get session failed: %s", err.Error()))
		return
	}
	if session == nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("session %q not found", p.SessionId))
		return
	}

	// If state=="created", call processes.Start (auto-start).
	if session.State == meta.SessionStateCreated {
		log.Printf("ari: session/prompt auto-starting session %s", p.SessionId)

		// Use a timeout context for start operation (10s per failure modes).
		startCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, err := h.srv.processes.Start(startCtx, p.SessionId)
		cancel()

		if err != nil {
			replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError,
				fmt.Sprintf("start session failed: %s", err.Error()))
			return
		}
	}

	// Call processes.Connect to get ShimClient.
	// Use a timeout context for connect (part of start flow).
	connectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	client, err := h.srv.processes.Connect(connectCtx, p.SessionId)
	cancel()

	if err != nil {
		// Check if error is because session is not running (InvalidParams)
		// vs some other error (InternalError).
		if strings.Contains(err.Error(), "not running") {
			replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
				fmt.Sprintf("session %q not running: %s", p.SessionId, err.Error()))
		} else {
			replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError,
				fmt.Sprintf("connect to session failed: %s", err.Error()))
		}
		return
	}

	// Call client.Prompt with 30s timeout.
	promptCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	result, err := client.Prompt(promptCtx, p.Text)
	cancel()

	if err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError,
			fmt.Sprintf("prompt failed: %s", err.Error()))
		return
	}

	log.Printf("ari: session/prompt completed for session %s, stopReason=%s", p.SessionId, result.StopReason)

	// Return result.
	_ = conn.Reply(ctx, req.ID, SessionPromptResult{
		StopReason: result.StopReason,
	})
}

// handleSessionCancel cancels the current agent turn.
// Workflow:
//  1. Unmarshal SessionCancelParams
//  2. Call processes.Connect to get ShimClient
//  3. Call client.Cancel
//  4. Return nil result
//
// Error handling:
//   - Invalid params (session not found/not running): returns InvalidParams
//   - Connect failure: returns InvalidError
//   - Cancel failure: returns InternalError
func (h *connHandler) handleSessionCancel(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var p SessionCancelParams
	if err := unmarshalParams(req, &p); err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	// Call processes.Connect to get ShimClient.
	client, err := h.srv.processes.Connect(ctx, p.SessionId)
	if err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("session %q not running: %s", p.SessionId, err.Error()))
		return
	}

	// Call client.Cancel.
	if err := client.Cancel(ctx); err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError,
			fmt.Sprintf("cancel failed: %s", err.Error()))
		return
	}

	log.Printf("ari: session/cancel completed for session %s", p.SessionId)

	// Return success (nil result).
	_ = conn.Reply(ctx, req.ID, nil)
}

// handleSessionStop stops a running session.
// Workflow:
//  1. Unmarshal SessionStopParams
//  2. Call processes.Stop
//  3. Return nil result
//
// Error handling:
//   - Invalid params (session not found): returns InvalidParams
//   - Stop failure: returns InternalError
//   - Stop timeout: returns InternalError
func (h *connHandler) handleSessionStop(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var p SessionStopParams
	if err := unmarshalParams(req, &p); err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	log.Printf("ari: session/stop stopping session %s", p.SessionId)

	// Call processes.Stop.
	if err := h.srv.processes.Stop(ctx, p.SessionId); err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError,
			fmt.Sprintf("stop session failed: %s", err.Error()))
		return
	}

	log.Printf("ari: session/stop completed for session %s", p.SessionId)

	// Return success (nil result).
	_ = conn.Reply(ctx, req.ID, nil)
}

// handleSessionRemove removes a session from the registry.
// Workflow:
//  1. Unmarshal SessionRemoveParams
//  2. Call sessions.Delete
//  3. Handle ErrDeleteProtected by replying InvalidParams
//  4. Return nil result
//
// Error handling:
//   - Invalid params (session not found): returns InvalidParams
//   - ErrDeleteProtected (running/paused:warm): returns InvalidParams with error message
//   - Delete failure: returns InternalError
func (h *connHandler) handleSessionRemove(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var p SessionRemoveParams
	if err := unmarshalParams(req, &p); err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	log.Printf("ari: session/remove removing session %s", p.SessionId)

	// Call sessions.Delete.
	err := h.srv.sessions.Delete(ctx, p.SessionId)
	if err != nil {
		// Check if error is ErrDeleteProtected (running/paused:warm session).
		if _, ok := err.(*agentd.ErrDeleteProtected); ok {
			replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
			return
		}
		// Generic error: return InvalidParams for "not found" or InternalError for other.
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	log.Printf("ari: session/remove completed for session %s", p.SessionId)

	// Return success (nil result).
	_ = conn.Reply(ctx, req.ID, nil)
}

// handleSessionList returns all sessions matching optional label filter.
// Workflow:
//  1. Unmarshal SessionListParams (optional)
//  2. Build SessionFilter from params
//  3. Call sessions.List with filter
//  4. Convert meta.Session to SessionInfo
//  5. Return SessionListResult
//
// Error handling:
//   - Invalid params: returns InvalidParams error
//   - List failure: returns InternalError
func (h *connHandler) handleSessionList(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	// Params are optional. Accept nil params.
	var p SessionListParams
	if req.Params != nil {
		if err := unmarshalParams(req, &p); err != nil {
			replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
			return
		}
	}

	// Build SessionFilter from params.
	// Note: meta.SessionFilter doesn't support label filtering.
	// Label filtering can be done client-side if needed.
	var filter *meta.SessionFilter
	// For now, return all sessions (no filter support for labels).
	// Future enhancement: add label filtering to meta.SessionFilter.

	// Call sessions.List with filter (nil returns all sessions).
	sessions, err := h.srv.sessions.List(ctx, filter)
	if err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError,
			fmt.Sprintf("list sessions failed: %s", err.Error()))
		return
	}

	// Convert meta.Session to SessionInfo.
	sessionInfos := make([]SessionInfo, 0, len(sessions))
	for _, s := range sessions {
		sessionInfos = append(sessionInfos, SessionInfo{
			Id:          s.ID,
			WorkspaceId: s.WorkspaceID,
			RuntimeClass: s.RuntimeClass,
			State:       string(s.State),
			Room:        s.Room,
			RoomAgent:   s.RoomAgent,
			Labels:      s.Labels,
			CreatedAt:   s.CreatedAt.Format(time.RFC3339),
			UpdatedAt:   s.UpdatedAt.Format(time.RFC3339),
		})
	}

	// Return result.
	_ = conn.Reply(ctx, req.ID, SessionListResult{Sessions: sessionInfos})
}

// handleSessionStatus returns session info and optional shim runtime state.
// Workflow:
//  1. Unmarshal SessionStatusParams
//  2. Get session from sessions.Get
//  3. If state=="running", call processes.State for shim state
//  4. Return SessionStatusResult
//
// Error handling:
//   - Invalid params (session not found): returns InvalidParams
//   - Get failure: returns InvalidParams
//   - State failure (session not running): returns result without shimState
func (h *connHandler) handleSessionStatus(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var p SessionStatusParams
	if err := unmarshalParams(req, &p); err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	// Get session from sessions.Get.
	session, err := h.srv.sessions.Get(ctx, p.SessionId)
	if err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("get session failed: %s", err.Error()))
		return
	}
	if session == nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("session %q not found", p.SessionId))
		return
	}

	// Build SessionInfo.
	sessionInfo := SessionInfo{
		Id:          session.ID,
		WorkspaceId: session.WorkspaceID,
		RuntimeClass: session.RuntimeClass,
		State:       string(session.State),
		Room:        session.Room,
		RoomAgent:   session.RoomAgent,
		Labels:      session.Labels,
		CreatedAt:   session.CreatedAt.Format(time.RFC3339),
		UpdatedAt:   session.UpdatedAt.Format(time.RFC3339),
	}

	// If state=="running", call processes.State for shim state.
	var shimState *ShimStateInfo
	if session.State == meta.SessionStateRunning {
		state, err := h.srv.processes.State(ctx, p.SessionId)
		if err == nil {
			shimState = &ShimStateInfo{
				Status:   string(state.Status),
				PID:      state.PID,
				Bundle:   state.Bundle,
				ExitCode: state.ExitCode,
			}
		}
	}

	// Return result.
	_ = conn.Reply(ctx, req.ID, SessionStatusResult{
		Session:   sessionInfo,
		ShimState: shimState,
	})
}

// handleSessionAttach returns the shim RPC socket path for direct communication.
// Workflow:
//  1. Unmarshal SessionAttachParams
//  2. Call processes.GetProcess to get ShimProcess
//  3. Return SessionAttachResult with shimProc.SocketPath
//
// Error handling:
//   - Invalid params (session not found): returns InvalidParams
//   - Session not running: returns InvalidParams
func (h *connHandler) handleSessionAttach(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var p SessionAttachParams
	if err := unmarshalParams(req, &p); err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	// Call processes.GetProcess to get ShimProcess.
	shimProc := h.srv.processes.GetProcess(p.SessionId)
	if shimProc == nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("session %q not running", p.SessionId))
		return
	}

	// Return result with socket path.
	_ = conn.Reply(ctx, req.ID, SessionAttachResult{
		SocketPath: shimProc.SocketPath,
	})
}

// handleSessionDetach detaches from a session (placeholder).
// Per research doc, detach has no clear semantics - just acknowledge.
// Workflow:
//  1. Unmarshal SessionDetachParams
//  2. Return nil result (placeholder)
//
// Error handling:
//   - Invalid params: returns InvalidParams error
func (h *connHandler) handleSessionDetach(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var p SessionDetachParams
	if err := unmarshalParams(req, &p); err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	// Placeholder - no clear semantics per research doc.
	// Just acknowledge with nil result.
	_ = conn.Reply(ctx, req.ID, nil)
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