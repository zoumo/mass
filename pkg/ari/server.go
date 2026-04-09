// Package ari implements the Agent Runtime Interface (ARI) JSON-RPC server.
// This file defines the Server that exposes workspace/* and session/* methods over a Unix socket.
package ari

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/sourcegraph/jsonrpc2"

	"github.com/open-agent-d/open-agent-d/pkg/agentd"
	"github.com/open-agent-d/open-agent-d/pkg/meta"
	"github.com/open-agent-d/open-agent-d/pkg/workspace"
)

// ────────────────────────────────────────────────────────────────────────────
// Server
// ────────────────────────────────────────────────────────────────────────────

// Server is a JSON-RPC 2.0 server that exposes workspace/*, agent/*, and room/* methods over a
// Unix-domain socket.
type Server struct {
	// manager prepares and cleans up workspaces.
	manager *workspace.WorkspaceManager

	// registry tracks workspace metadata.
	registry *Registry

	// sessions manages session lifecycle with state machine validation.
	sessions *agentd.SessionManager

	// agents manages agent lifecycle with domain error types.
	agents *agentd.AgentManager

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
//   - agents: AgentManager for agent lifecycle management
//   - processes: ProcessManager for shim process lifecycle
//   - runtimeClasses: RuntimeClassRegistry for runtime class resolution
//   - config: agentd configuration
//   - store: metadata store for persisting workspaces and sessions
//   - socketPath: Unix socket path for ARI server
//   - baseDir: root directory for workspace creation
func New(manager *workspace.WorkspaceManager, registry *Registry, sessions *agentd.SessionManager, agents *agentd.AgentManager, processes *agentd.ProcessManager, runtimeClasses *agentd.RuntimeClassRegistry, config agentd.Config, store *meta.Store, socketPath, baseDir string) *Server {
	if baseDir == "" {
		baseDir = filepath.Join(os.TempDir(), "agentd-workspaces")
	}
	return &Server{
		manager:        manager,
		registry:       registry,
		sessions:       sessions,
		agents:         agents,
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
	if err := os.MkdirAll(s.baseDir, 0o755); err != nil {
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

	// Agent methods
	case "agent/create":
		h.handleAgentCreate(ctx, conn, req)
	case "agent/prompt":
		h.handleAgentPrompt(ctx, conn, req)
	case "agent/cancel":
		h.handleAgentCancel(ctx, conn, req)
	case "agent/stop":
		h.handleAgentStop(ctx, conn, req)
	case "agent/delete":
		h.handleAgentDelete(ctx, conn, req)
	case "agent/restart":
		h.handleAgentRestart(ctx, conn, req)
	case "agent/list":
		h.handleAgentList(ctx, conn, req)
	case "agent/status":
		h.handleAgentStatus(ctx, conn, req)
	case "agent/attach":
		h.handleAgentAttach(ctx, conn, req)
	case "agent/detach":
		h.handleAgentDetach(ctx, conn, req)

	// Room methods
	case "room/create":
		h.handleRoomCreate(ctx, conn, req)
	case "room/status":
		h.handleRoomStatus(ctx, conn, req)
	case "room/send":
		h.handleRoomSend(ctx, conn, req)
	case "room/delete":
		h.handleRoomDelete(ctx, conn, req)

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
		wsErr := &workspace.WorkspaceError{}
		if errors.As(err, &wsErr) {
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
		// Serialize the Source spec so DB captures the full workspace origin.
		sourceJSON, marshalErr := json.Marshal(p.Spec.Source)
		if marshalErr != nil {
			log.Printf("ari: failed to marshal workspace %s source: %v", workspaceId, marshalErr)
			sourceJSON = json.RawMessage("{}")
		}
		ws := &meta.Workspace{
			ID:       workspaceId,
			Name:     p.Spec.Metadata.Name,
			Path:     workspacePath,
			Source:   sourceJSON,
			Status:   meta.WorkspaceStatusActive,
			RefCount: 0,
		}
		if err := h.srv.store.CreateWorkspace(ctx, ws); err != nil {
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
	// Block cleanup during active recovery phase (fail-closed posture).
	if h.recoveryGuard(ctx, conn, req) {
		return
	}

	var p WorkspaceCleanupParams
	if err := unmarshalParams(req, &p); err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	// Get workspace from registry.
	wsMeta := h.srv.registry.Get(p.WorkspaceId)
	if wsMeta == nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("workspace %q not found", p.WorkspaceId))
		return
	}

	// Gate on DB ref_count (persisted truth that survives restarts) rather
	// than the volatile in-memory registry RefCount.
	if h.srv.store != nil {
		dbWs, err := h.srv.store.GetWorkspace(ctx, p.WorkspaceId)
		if err != nil {
			log.Printf("ari: failed to query DB ref_count for workspace %s, falling back to registry: %v", p.WorkspaceId, err)
		} else if dbWs != nil && dbWs.RefCount > 0 {
			replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError,
				fmt.Sprintf("workspace %q has %d active references", p.WorkspaceId, dbWs.RefCount))
			return
		}
	}

	// Fallback: if store is nil, check the in-memory registry RefCount.
	if h.srv.store == nil && wsMeta.RefCount > 0 {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError,
			fmt.Sprintf("workspace %q has %d active references", p.WorkspaceId, wsMeta.RefCount))
		return
	}

	// Call manager.Cleanup.
	err := h.srv.manager.Cleanup(ctx, wsMeta.Path, wsMeta.Spec)
	if err != nil {
		// Check if error is a WorkspaceError to extract Phase.
		wsErr := &workspace.WorkspaceError{}
		if errors.As(err, &wsErr) {
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

// deliverPromptAsync dispatches a prompt to the shim and returns immediately.
// State transitions (running -> created/error) happen in a background goroutine.
// The caller must have already set the agent state to "running" before calling this.
// A 120 s hard timeout ensures stuck goroutines eventually resolve.
func (h *connHandler) deliverPromptAsync(agentID, sessionID, text string) error {
	// Perform session existence check and auto-start synchronously so that
	// dispatch errors (session not found, connect failure) are surfaced to the
	// caller before we return.
	checkCtx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()

	session, err := h.srv.sessions.Get(checkCtx, sessionID)
	if err != nil {
		return fmt.Errorf("get session failed: %w", err)
	}
	if session == nil {
		return fmt.Errorf("session %q not found", sessionID)
	}

	if session.State == meta.SessionStateCreated {
		startCtx, startCancel := context.WithTimeout(checkCtx, 30*time.Second)
		_, err := h.srv.processes.Start(startCtx, sessionID)
		startCancel()
		if err != nil {
			return fmt.Errorf("start session failed: %w", err)
		}
	}

	connectCtx, connectCancel := context.WithTimeout(checkCtx, 5*time.Second)
	client, err := h.srv.processes.Connect(connectCtx, sessionID)
	connectCancel()
	if err != nil {
		if strings.Contains(err.Error(), "not running") {
			return fmt.Errorf("session %q not running: %w", sessionID, err)
		}
		return fmt.Errorf("connect to session failed: %w", err)
	}

	// Launch the actual prompt call in a background goroutine.
	go func() {
		promptCtx, promptCancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer promptCancel()

		_, err := client.Prompt(promptCtx, text)
		if err != nil {
			log.Printf("ari: deliverPromptAsync: agent %s session %s turn failed: %v", agentID, sessionID, err)
			if updateErr := h.srv.agents.UpdateState(context.Background(), agentID, meta.AgentStateError, err.Error()); updateErr != nil {
				log.Printf("ari: deliverPromptAsync: failed to set agent %s to error: %v", agentID, updateErr)
			}
			return
		}

		log.Printf("ari: deliverPromptAsync: agent %s session %s turn completed", agentID, sessionID)
		if updateErr := h.srv.agents.UpdateState(context.Background(), agentID, meta.AgentStateCreated, ""); updateErr != nil {
			log.Printf("ari: deliverPromptAsync: failed to set agent %s to created: %v", agentID, updateErr)
		}
	}()

	return nil
}

// ────────────────────────────────────────────────────────────────────────────
// Agent handlers
// ────────────────────────────────────────────────────────────────────────────

// agentInfoFromMeta converts a meta.Agent to an AgentInfo wire type.
func agentInfoFromMeta(a *meta.Agent) AgentInfo {
	return AgentInfo{
		AgentId:      a.ID,
		Room:         a.Room,
		Name:         a.Name,
		Description:  a.Description,
		RuntimeClass: a.RuntimeClass,
		WorkspaceId:  a.WorkspaceID,
		State:        string(a.State),
		ErrorMessage: a.ErrorMessage,
		Labels:       a.Labels,
		CreatedAt:    a.CreatedAt,
		UpdatedAt:    a.UpdatedAt,
	}
}

// linkedSessionForAgent returns the first session linked to agentID, or nil if none exists.
func (h *connHandler) linkedSessionForAgent(ctx context.Context, agentID string) (*meta.Session, error) {
	sessions, err := h.srv.store.ListSessions(ctx, &meta.SessionFilter{AgentID: agentID})
	if err != nil {
		return nil, err
	}
	if len(sessions) == 0 {
		return nil, nil
	}
	return sessions[0], nil
}

// handleAgentCreate creates a new agent in "creating" state and returns immediately.
// Workflow:
//  1. Unmarshal AgentCreateParams
//  2. Validate room exists; validate workspace exists
//  3. Generate agentId (UUID)
//  4. Create meta.Agent via agents.Create (state: "creating")
//  5. Reply immediately with state:"creating"
//  6. Launch background goroutine (90s timeout, context.Background()) that:
//     a. Generates sessionId
//     b. Creates meta.Session linked to agentId
//     c. AcquireWorkspace using sessionId (FK to sessions)
//     d. Acquire in-memory registry ref using sessionId
//     e. Calls processes.Start
//     f. On success: UpdateState to "created"; on failure: UpdateState to "error"
func (h *connHandler) handleAgentCreate(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var p AgentCreateParams
	if err := unmarshalParams(req, &p); err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	if p.Room == "" {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, "room is required")
		return
	}
	if p.Name == "" {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, "name is required")
		return
	}
	if p.RuntimeClass == "" {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, "runtimeClass is required")
		return
	}
	if p.WorkspaceId == "" {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, "workspaceId is required")
		return
	}

	// Validate room exists.
	room, err := h.srv.store.GetRoom(ctx, p.Room)
	if err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError,
			fmt.Sprintf("failed to check room: %s", err.Error()))
		return
	}
	if room == nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("room %q does not exist; call room/create first", p.Room))
		return
	}

	// Validate workspace exists.
	ws, err := h.srv.store.GetWorkspace(ctx, p.WorkspaceId)
	if err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError,
			fmt.Sprintf("failed to check workspace: %s", err.Error()))
		return
	}
	if ws == nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("workspace %q does not exist; call workspace/prepare first", p.WorkspaceId))
		return
	}

	// Generate agent ID only; session ID is generated inside the goroutine.
	agentId := uuid.New().String()

	// Create agent in "creating" state.
	agent := &meta.Agent{
		ID:           agentId,
		Room:         p.Room,
		Name:         p.Name,
		Description:  p.Description,
		RuntimeClass: p.RuntimeClass,
		WorkspaceID:  p.WorkspaceId,
		SystemPrompt: p.SystemPrompt,
		Labels:       p.Labels,
		State:        meta.AgentStateCreating,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	if err := h.srv.agents.Create(ctx, agent); err != nil {
		errAgentAlreadyExists := &agentd.ErrAgentAlreadyExists{}
		if errors.As(err, &errAgentAlreadyExists) {
			replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
		} else {
			replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError,
				fmt.Sprintf("create agent failed: %s", err.Error()))
		}
		return
	}

	log.Printf("ari: agent/create registered agent %s (room=%s name=%s) — bootstrapping in background",
		agentId, p.Room, p.Name)

	// Reply immediately with state "creating" before launching the background bootstrap.
	_ = conn.Reply(ctx, req.ID, AgentCreateResult{
		AgentId: agentId,
		State:   string(meta.AgentStateCreating),
	})

	// Bootstrap the agent session in a background goroutine.
	// Captures p (params) and agentId by value; uses context.Background() because
	// the request ctx is dead after conn.Reply returns.
	pCopy := p
	go func() {
		bgCtx, bgCancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer bgCancel()

		sessionId := uuid.New().String()

		// Create linked session (SessionStateCreated required by ProcessManager.Start).
		session := &meta.Session{
			ID:           sessionId,
			AgentID:      agentId,
			WorkspaceID:  pCopy.WorkspaceId,
			RuntimeClass: pCopy.RuntimeClass,
			Room:         pCopy.Room,
			RoomAgent:    pCopy.Name,
			Labels:       pCopy.Labels,
			State:        meta.SessionStateCreated,
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		}
		if err := h.srv.sessions.Create(bgCtx, session); err != nil {
			slog.Error("ari: agent bootstrap: failed to create session",
				"agentId", agentId, "error", err)
			_ = h.srv.agents.UpdateState(bgCtx, agentId, meta.AgentStateError, err.Error())
			return
		}

		// Acquire workspace ref (FK uses sessionId).
		if err := h.srv.store.AcquireWorkspace(bgCtx, pCopy.WorkspaceId, sessionId); err != nil {
			slog.Error("ari: agent bootstrap: failed to acquire workspace",
				"agentId", agentId, "sessionId", sessionId, "error", err)
			// Clean up session row before marking error.
			_ = h.srv.sessions.Delete(bgCtx, sessionId)
			_ = h.srv.agents.UpdateState(bgCtx, agentId, meta.AgentStateError, err.Error())
			return
		}
		h.srv.registry.Acquire(pCopy.WorkspaceId, sessionId)

		// Start the process.
		if _, err := h.srv.processes.Start(bgCtx, sessionId); err != nil {
			slog.Error("ari: agent bootstrap: failed to start process",
				"agentId", agentId, "sessionId", sessionId, "error", err)
			// Release acquired refs before marking error.
			h.srv.registry.Release(pCopy.WorkspaceId, sessionId)
			_ = h.srv.sessions.Delete(bgCtx, sessionId)
			_ = h.srv.agents.UpdateState(bgCtx, agentId, meta.AgentStateError, err.Error())
			return
		}

		// Transition agent to "created" — session is running and ready for prompts.
		if err := h.srv.agents.UpdateState(bgCtx, agentId, meta.AgentStateCreated, ""); err != nil {
			slog.Error("ari: agent bootstrap: failed to update agent state to created",
				"agentId", agentId, "sessionId", sessionId, "error", err)
			return
		}

		slog.Info("ari: agent bootstrap complete",
			"agentId", agentId, "sessionId", sessionId)
	}()
}

// handleAgentPrompt dispatches a prompt to an agent asynchronously.
//
// The prompt is fired in a background goroutine; the RPC returns immediately
// with {accepted: true} once the dispatch is confirmed. The agent state
// machine follows the actor model:
//
//	created/idle  →  running  (on successful dispatch)
//	running       →  created  (background goroutine, turn completed)
//	running       →  error    (background goroutine, turn failed / timed out)
//
// Callers MUST poll agent/status to detect turn completion. The agent-shim
// does not support prompt queuing: if the agent is already running, the call
// is rejected with CodeInvalidParams. The caller must issue agent/cancel first.
func (h *connHandler) handleAgentPrompt(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var p AgentPromptParams
	if err := unmarshalParams(req, &p); err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	if p.AgentId == "" {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, "agentId is required")
		return
	}

	// Fail-closed: refuse operational actions while daemon is recovering.
	if h.recoveryGuard(ctx, conn, req) {
		return
	}

	// Get agent.
	agent, err := h.srv.agents.Get(ctx, p.AgentId)
	if err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError,
			fmt.Sprintf("get agent failed: %s", err.Error()))
		return
	}
	if agent == nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("agent %q not found", p.AgentId))
		return
	}

	// Guard: reject prompts while the agent is still being provisioned.
	if agent.State == meta.AgentStateCreating {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
			"agent is still being provisioned; poll agent/status until state is 'created'")
		return
	}
	if agent.State == meta.AgentStateError {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
			"agent is in error state; restart or delete it before sending a prompt")
		return
	}
	if agent.State == meta.AgentStateStopped {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
			"agent is stopped; restart it before sending a prompt")
		return
	}

	// Reject concurrent prompts — the shim has no prompt queuing.
	// Caller must issue agent/cancel to interrupt the current turn first.
	if agent.State == meta.AgentStateRunning {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
			"agent is already processing a prompt; cancel it first via agent/cancel")
		return
	}

	// Find linked session.
	session, err := h.linkedSessionForAgent(ctx, p.AgentId)
	if err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError,
			fmt.Sprintf("failed to find linked session: %s", err.Error()))
		return
	}
	if session == nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("no linked session found for agent %q", p.AgentId))
		return
	}

	// Transition agent to "running" BEFORE dispatch.
	if updateErr := h.srv.agents.UpdateState(ctx, p.AgentId, meta.AgentStateRunning, ""); updateErr != nil {
		log.Printf("ari: agent/prompt: warning: failed to update agent %s state to running: %v", p.AgentId, updateErr)
	}

	// Dispatch prompt asynchronously. If dispatch itself fails (session not
	// reachable), revert agent state to error and surface the error to the caller.
	if err := h.deliverPromptAsync(p.AgentId, session.ID, p.Prompt); err != nil {
		if updateErr := h.srv.agents.UpdateState(ctx, p.AgentId, meta.AgentStateError, err.Error()); updateErr != nil {
			log.Printf("ari: agent/prompt: warning: failed to update agent %s state to error: %v", p.AgentId, updateErr)
		}
		errMsg := err.Error()
		if strings.Contains(errMsg, "not found") || strings.Contains(errMsg, "not running") || strings.Contains(errMsg, "get session failed") {
			replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, errMsg)
		} else {
			replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError, errMsg)
		}
		return
	}

	// Prompt accepted — return immediately. Background goroutine handles
	// running -> created/error transition when the turn completes.
	_ = conn.Reply(ctx, req.ID, AgentPromptResult{Accepted: true})
}

// handleAgentCancel cancels the current agent turn.
// Finds the linked session and connects to shim cancel.
func (h *connHandler) handleAgentCancel(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var p AgentCancelParams
	if err := unmarshalParams(req, &p); err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	// Fail-closed: refuse operational actions while daemon is recovering.
	if h.recoveryGuard(ctx, conn, req) {
		return
	}

	// Get agent.
	agent, err := h.srv.agents.Get(ctx, p.AgentId)
	if err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError,
			fmt.Sprintf("get agent failed: %s", err.Error()))
		return
	}
	if agent == nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("agent %q not found", p.AgentId))
		return
	}

	// Find linked session.
	session, err := h.linkedSessionForAgent(ctx, p.AgentId)
	if err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError,
			fmt.Sprintf("failed to find linked session: %s", err.Error()))
		return
	}
	if agent.State == meta.AgentStateError {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("agent %q is in error state; restart or delete it before canceling", p.AgentId))
		return
	}
	if session == nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("no linked session found for agent %q", p.AgentId))
		return
	}

	// Connect to shim and cancel.
	client, err := h.srv.processes.Connect(ctx, session.ID)
	if err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("agent %q not running: %s", p.AgentId, err.Error()))
		return
	}

	if err := client.Cancel(ctx); err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError,
			fmt.Sprintf("cancel failed: %s", err.Error()))
		return
	}

	log.Printf("ari: agent/cancel completed for agent %s (session %s)", p.AgentId, session.ID)
	_ = conn.Reply(ctx, req.ID, nil)
}

// handleAgentStop stops a running agent.
// Finds the linked session, calls processes.Stop, then updates agent state to stopped.
func (h *connHandler) handleAgentStop(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var p AgentStopParams
	if err := unmarshalParams(req, &p); err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	// Find linked session.
	session, err := h.linkedSessionForAgent(ctx, p.AgentId)
	if err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError,
			fmt.Sprintf("failed to find linked session: %s", err.Error()))
		return
	}
	if session != nil {
		log.Printf("ari: agent/stop stopping session %s for agent %s", session.ID, p.AgentId)
		if err := h.srv.processes.Stop(ctx, session.ID); err != nil {
			replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError,
				fmt.Sprintf("stop agent failed: %s", err.Error()))
			return
		}
	}

	// Update agent state to stopped.
	if err := h.srv.agents.UpdateState(ctx, p.AgentId, meta.AgentStateStopped, ""); err != nil {
		errAgentNotFound := &agentd.ErrAgentNotFound{}
		if errors.As(err, &errAgentNotFound) {
			replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
		} else {
			replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError,
				fmt.Sprintf("update agent state failed: %s", err.Error()))
		}
		return
	}

	log.Printf("ari: agent/stop completed for agent %s", p.AgentId)
	_ = conn.Reply(ctx, req.ID, nil)
}

// deleteAgentWithCleanup performs full agent deletion including linked session
// and workspace registry cleanup. Used by both handleAgentDelete and handleRoomDelete.
//
// Workflow:
//  1. Find linked session BEFORE deleting agent (ON DELETE SET NULL on sessions.agent_id)
//  2. Call agents.Delete (enforces stopped precondition)
//  3. Delete linked session via sessions.Delete
//  4. Release in-memory registry ref
func (h *connHandler) deleteAgentWithCleanup(ctx context.Context, agentID string) error {
	// Find the linked session BEFORE deleting the agent, because agents.Delete
	// triggers ON DELETE SET NULL on sessions.agent_id, making the session
	// unfindable afterwards via AgentID filter.
	session, err := h.linkedSessionForAgent(ctx, agentID)
	if err != nil {
		log.Printf("ari: deleteAgentWithCleanup warning: failed to find linked session for agent %s: %v", agentID, err)
	}

	// agents.Delete enforces stopped precondition and not-found check.
	if err := h.srv.agents.Delete(ctx, agentID); err != nil {
		return err
	}

	// Delete the linked session (releases workspace_refs FK chain via store.DeleteSession).
	if session != nil {
		if err := h.srv.sessions.Delete(ctx, session.ID); err != nil {
			log.Printf("ari: deleteAgentWithCleanup warning: failed to delete session %s for agent %s: %v",
				session.ID, agentID, err)
		}
		// Release in-memory registry ref.
		h.srv.registry.Release(session.WorkspaceID, session.ID)
	}

	return nil
}

// handleAgentDelete deletes an agent (must be stopped first).
func (h *connHandler) handleAgentDelete(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var p AgentDeleteParams
	if err := unmarshalParams(req, &p); err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	if err := h.deleteAgentWithCleanup(ctx, p.AgentId); err != nil {
		{
			var errCase0 *agentd.ErrAgentNotFound
			var errCase1 *agentd.ErrDeleteNotStopped
			switch {
			case errors.As(err, &errCase0):
				replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
			case errors.As(err, &errCase1):
				replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
			default:
				replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError, err.Error())
			}
		}
		return
	}

	log.Printf("ari: agent/delete completed for agent %s", p.AgentId)
	_ = conn.Reply(ctx, req.ID, nil)
}

// handleAgentRestart stops the current session and re-bootstraps the agent.
// Workflow:
//  1. Unmarshal AgentRestartParams
//  2. Get agent — validate exists (404 on nil)
//  3. Validate state is stopped or error — CodeInvalidParams otherwise
//  4. Find linked session via linkedSessionForAgent (may be nil)
//  5. Generate new sessionId
//  6. Transition agent to "creating" synchronously (blocks concurrent prompts)
//  7. Reply immediately: AgentRestartResult{AgentId, State:"creating"}
//  8. Launch background goroutine (context.Background(), 90s timeout):
//     a. Delete old session + release registry ref (if old session exists)
//     b. Get agent (fresh copy for WorkspaceID etc.)
//     c. Create new session linked to agentId
//     d. AcquireWorkspace + registry.Acquire
//     e. processes.Start
//     f. On success: UpdateState → "created"
//     g. On failure: UpdateState → "error" + cleanup new session
func (h *connHandler) handleAgentRestart(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var p AgentRestartParams
	if err := unmarshalParams(req, &p); err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}
	if p.AgentId == "" {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, "agentId is required")
		return
	}

	// Get agent — validate exists.
	agent, err := h.srv.agents.Get(ctx, p.AgentId)
	if err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError,
			fmt.Sprintf("get agent failed: %s", err.Error()))
		return
	}
	if agent == nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("agent %q not found", p.AgentId))
		return
	}

	// Only stopped or error agents may be restarted.
	if agent.State != meta.AgentStateStopped && agent.State != meta.AgentStateError {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("agent %q is in state %q; only stopped or error agents may be restarted", p.AgentId, agent.State))
		return
	}

	// Find the linked session BEFORE transitioning state.
	// oldSession may be nil (e.g. agent is in error state before session was created).
	oldSession, err := h.linkedSessionForAgent(ctx, p.AgentId)
	if err != nil {
		log.Printf("ari: agent/restart warning: failed to find linked session for agent %s: %v", p.AgentId, err)
	}

	// Generate the new session ID before Reply so we don't need it in the goroutine prologue.
	newSessionId := uuid.New().String()

	// Transition agent to "creating" synchronously — blocks any concurrent prompt calls.
	if err := h.srv.agents.UpdateState(ctx, p.AgentId, meta.AgentStateCreating, ""); err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError,
			fmt.Sprintf("failed to transition agent to creating: %s", err.Error()))
		return
	}

	log.Printf("ari: agent/restart: agent %s transitioning to creating (newSession=%s)", p.AgentId, newSessionId)

	// Reply immediately with state "creating".
	_ = conn.Reply(ctx, req.ID, AgentRestartResult{
		AgentId: p.AgentId,
		State:   string(meta.AgentStateCreating),
	})

	// Re-bootstrap in background. Captures local variables by value.
	agentId := p.AgentId
	go func() {
		bgCtx, bgCancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer bgCancel()

		// Step a: tear down old session if it exists.
		if oldSession != nil {
			if err := h.srv.sessions.Delete(bgCtx, oldSession.ID); err != nil {
				slog.Error("ari: agent restart: failed to delete old session",
					"agentId", agentId, "oldSessionId", oldSession.ID, "error", err)
				// Non-fatal: proceed — orphaned session rows are recoverable.
			}
			h.srv.registry.Release(oldSession.WorkspaceID, oldSession.ID)
		}

		// Step b: re-fetch agent for a fresh copy of WorkspaceID, Room, Name, etc.
		freshAgent, err := h.srv.agents.Get(bgCtx, agentId)
		if err != nil || freshAgent == nil {
			slog.Error("ari: agent restart: failed to get fresh agent",
				"agentId", agentId, "error", err)
			_ = h.srv.agents.UpdateState(bgCtx, agentId, meta.AgentStateError,
				"restart: failed to get agent after state transition")
			return
		}

		// Step c: create new session.
		session := &meta.Session{
			ID:           newSessionId,
			AgentID:      agentId,
			WorkspaceID:  freshAgent.WorkspaceID,
			RuntimeClass: freshAgent.RuntimeClass,
			Room:         freshAgent.Room,
			RoomAgent:    freshAgent.Name,
			Labels:       freshAgent.Labels,
			State:        meta.SessionStateCreated,
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		}
		if err := h.srv.sessions.Create(bgCtx, session); err != nil {
			slog.Error("ari: agent restart: failed to create new session",
				"agentId", agentId, "newSessionId", newSessionId, "error", err)
			_ = h.srv.agents.UpdateState(bgCtx, agentId, meta.AgentStateError, err.Error())
			return
		}

		// Step d: acquire workspace ref and in-memory registry ref.
		if err := h.srv.store.AcquireWorkspace(bgCtx, freshAgent.WorkspaceID, newSessionId); err != nil {
			slog.Error("ari: agent restart: failed to acquire workspace",
				"agentId", agentId, "newSessionId", newSessionId, "error", err)
			_ = h.srv.sessions.Delete(bgCtx, newSessionId)
			_ = h.srv.agents.UpdateState(bgCtx, agentId, meta.AgentStateError, err.Error())
			return
		}
		h.srv.registry.Acquire(freshAgent.WorkspaceID, newSessionId)

		// Step e: start the process.
		if _, err := h.srv.processes.Start(bgCtx, newSessionId); err != nil {
			slog.Error("ari: agent restart: failed to start process",
				"agentId", agentId, "newSessionId", newSessionId, "error", err)
			h.srv.registry.Release(freshAgent.WorkspaceID, newSessionId)
			_ = h.srv.sessions.Delete(bgCtx, newSessionId)
			_ = h.srv.agents.UpdateState(bgCtx, agentId, meta.AgentStateError, err.Error())
			return
		}

		// Step f: transition to "created".
		if err := h.srv.agents.UpdateState(bgCtx, agentId, meta.AgentStateCreated, ""); err != nil {
			slog.Error("ari: agent restart: failed to update agent state to created",
				"agentId", agentId, "newSessionId", newSessionId, "error", err)
			return
		}

		oldSessionId := ""
		if oldSession != nil {
			oldSessionId = oldSession.ID
		}
		slog.Info("ari: agent restart complete",
			"agentId", agentId,
			"oldSessionId", oldSessionId,
			"newSessionId", newSessionId)
	}()
}

// handleAgentList returns all agents matching optional filters.
// Workflow: unmarshal params → build AgentFilter → call store.ListAgents →
// convert to []AgentInfo → return AgentListResult.
func (h *connHandler) handleAgentList(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var p AgentListParams
	if req.Params != nil {
		if err := unmarshalParams(req, &p); err != nil {
			replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
			return
		}
	}

	filter := &meta.AgentFilter{}
	if p.State != "" {
		filter.State = meta.AgentState(p.State)
	}
	if p.Room != "" {
		filter.Room = p.Room
	}

	agents, err := h.srv.agents.List(ctx, filter)
	if err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError,
			fmt.Sprintf("list agents failed: %s", err.Error()))
		return
	}

	infos := make([]AgentInfo, 0, len(agents))
	for _, a := range agents {
		infos = append(infos, agentInfoFromMeta(a))
	}

	_ = conn.Reply(ctx, req.ID, AgentListResult{Agents: infos})
}

// handleAgentStatus returns agent info and optional shim runtime state.
// Workflow: get agent → find linked session → get shim state → return AgentStatusResult.
func (h *connHandler) handleAgentStatus(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var p AgentStatusParams
	if err := unmarshalParams(req, &p); err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	agent, err := h.srv.agents.Get(ctx, p.AgentId)
	if err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError,
			fmt.Sprintf("get agent failed: %s", err.Error()))
		return
	}
	if agent == nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("agent %q not found", p.AgentId))
		return
	}

	// Find linked session.
	session, err := h.linkedSessionForAgent(ctx, p.AgentId)
	if err != nil {
		log.Printf("ari: agent/status: failed to find linked session for agent %s: %v", p.AgentId, err)
	}

	// Get shim state if session is running.
	var shimState *ShimStateInfo
	if session != nil && session.State == meta.SessionStateRunning {
		state, err := h.srv.processes.State(ctx, session.ID)
		if err == nil {
			shimState = &ShimStateInfo{
				Status:   string(state.Status),
				PID:      state.PID,
				Bundle:   state.Bundle,
				ExitCode: state.ExitCode,
			}
		}
	}

	// Surface recovery metadata when available.
	var recoveryInfo *AgentRecoveryInfo
	if session != nil {
		if shimProc := h.srv.processes.GetProcess(session.ID); shimProc != nil && shimProc.Recovery != nil {
			ri := shimProc.Recovery
			recoveryInfo = &AgentRecoveryInfo{
				Recovered:   ri.Recovered,
				RecoveredAt: ri.RecoveredAt,
				Outcome:     string(ri.Outcome),
			}
		}
	}

	_ = conn.Reply(ctx, req.ID, AgentStatusResult{
		Agent:     agentInfoFromMeta(agent),
		ShimState: shimState,
		Recovery:  recoveryInfo,
	})
}

// handleAgentAttach returns the shim RPC socket path for the agent's linked session.
func (h *connHandler) handleAgentAttach(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var p AgentAttachParams
	if err := unmarshalParams(req, &p); err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	// Find linked session.
	session, err := h.linkedSessionForAgent(ctx, p.AgentId)
	if err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError,
			fmt.Sprintf("failed to find linked session: %s", err.Error()))
		return
	}
	if session == nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("no linked session found for agent %q", p.AgentId))
		return
	}

	shimProc := h.srv.processes.GetProcess(session.ID)
	if shimProc == nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("agent %q not running", p.AgentId))
		return
	}

	_ = conn.Reply(ctx, req.ID, AgentAttachResult{SocketPath: shimProc.SocketPath})
}

// handleAgentDetach detaches from an agent (placeholder — no clear semantics).
func (h *connHandler) handleAgentDetach(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var p AgentDetachParams
	if err := unmarshalParams(req, &p); err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}
	_ = conn.Reply(ctx, req.ID, nil)
}

// ────────────────────────────────────────────────────────────────────────────
// Room handlers
// ────────────────────────────────────────────────────────────────────────────

// handleRoomCreate creates a new room.
// Workflow:
//  1. Unmarshal RoomCreateParams
//  2. Validate name non-empty
//  3. Map communication mode string to CommunicationMode constant (default "mesh")
//  4. Call store.CreateRoom
//  5. Return RoomCreateResult
//
// Error handling:
//   - Invalid params (missing name, invalid mode): returns InvalidParams
//   - Room already exists: returns InvalidParams
//   - Store failure: returns InternalError
func (h *connHandler) handleRoomCreate(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var p RoomCreateParams
	if err := unmarshalParams(req, &p); err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	if p.Name == "" {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, "room name is required")
		return
	}

	// Resolve communication mode — default to mesh.
	mode := meta.CommunicationModeMesh
	if p.Communication != nil && p.Communication.Mode != "" {
		switch p.Communication.Mode {
		case "mesh":
			mode = meta.CommunicationModeMesh
		case "star":
			mode = meta.CommunicationModeStar
		case "isolated":
			mode = meta.CommunicationModeIsolated
		default:
			replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
				fmt.Sprintf("invalid communication mode %q; must be mesh, star, or isolated", p.Communication.Mode))
			return
		}
	}

	room := &meta.Room{
		Name:              p.Name,
		Labels:            p.Labels,
		CommunicationMode: mode,
	}

	if err := h.srv.store.CreateRoom(ctx, room); err != nil {
		// Room already exists surfaces as an application error, not internal.
		if strings.Contains(err.Error(), "already exists") {
			replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
		} else {
			replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError, err.Error())
		}
		return
	}

	log.Printf("ari: room/create created room %s (mode=%s)", room.Name, room.CommunicationMode)

	_ = conn.Reply(ctx, req.ID, RoomCreateResult{
		Name:              room.Name,
		CommunicationMode: string(room.CommunicationMode),
		CreatedAt:         room.CreatedAt.Format(time.RFC3339),
	})
}

// handleRoomStatus returns room metadata and realized member list.
// Workflow:
//  1. Unmarshal RoomStatusParams
//  2. Call store.GetRoom
//  3. Call agents.List with Room filter to get members
//  4. Build RoomMember list from matching agents
//  5. Return RoomStatusResult
//
// Error handling:
//   - Invalid params (missing name): returns InvalidParams
//   - Room not found: returns InvalidParams
//   - Store failure: returns InternalError
func (h *connHandler) handleRoomStatus(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var p RoomStatusParams
	if err := unmarshalParams(req, &p); err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	if p.Name == "" {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, "room name is required")
		return
	}

	room, err := h.srv.store.GetRoom(ctx, p.Name)
	if err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError, err.Error())
		return
	}
	if room == nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("room %q not found", p.Name))
		return
	}

	// List agents in this room.
	roomAgents, err := h.srv.agents.List(ctx, &meta.AgentFilter{Room: p.Name})
	if err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError,
			fmt.Sprintf("failed to list room members: %s", err.Error()))
		return
	}

	members := make([]RoomMember, 0, len(roomAgents))
	for _, a := range roomAgents {
		members = append(members, RoomMember{
			AgentName:    a.Name,
			Description:  a.Description,
			RuntimeClass: a.RuntimeClass,
			AgentState:   string(a.State),
		})
	}

	_ = conn.Reply(ctx, req.ID, RoomStatusResult{
		Name:              room.Name,
		Labels:            room.Labels,
		CommunicationMode: string(room.CommunicationMode),
		Members:           members,
		CreatedAt:         room.CreatedAt.Format(time.RFC3339),
		UpdatedAt:         room.UpdatedAt.Format(time.RFC3339),
	})
}

// handleRoomSend routes a message from one agent to another within a room (async).
//
// The message is dispatched to the target agent's session asynchronously; the
// RPC returns {delivered: true} immediately once the dispatch is confirmed. The
// target agent processes the message in the background (actor model).
//
// Workflow:
//  1. Unmarshal RoomSendParams
//  2. Validate required fields (room, targetAgent, message)
//  3. Call store.GetRoom — return InvalidParams if room not found
//  4. Call store.GetAgentByRoomName — return InvalidParams if agent not found
//  5. Guard: agent.State == stopped/creating/error/running → return InvalidParams
//  6. Call store.ListSessions(AgentID) — find linked session ID
//  7. If no linked session: return InvalidParams
//  8. Format attributed message: [room:<roomName> from:<senderAgent>] <message>
//  9. Transition agent to "running"
//
// 10. Call deliverPromptAsync (fire-and-forget dispatch)
// 11. If dispatch fails: revert agent to error, return InternalError
// 12. Return RoomSendResult{Delivered: true} immediately
//
// Error handling:
//   - Invalid params (missing fields, room/agent not found): returns InvalidParams
//   - Target agent already running: returns InvalidParams (shim has no prompt queue)
//   - Dispatch failure: returns InternalError "prompt failed: <err>"
//   - Store failure: returns InternalError
func (h *connHandler) handleRoomSend(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var p RoomSendParams
	if err := unmarshalParams(req, &p); err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	// Validate required fields.
	if p.Room == "" {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, "room is required")
		return
	}
	if p.TargetAgent == "" {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, "targetAgent is required")
		return
	}
	if p.Message == "" {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, "message is required")
		return
	}

	// Verify room exists.
	room, err := h.srv.store.GetRoom(ctx, p.Room)
	if err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError, err.Error())
		return
	}
	if room == nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("room %q not found", p.Room))
		return
	}

	// Find the agent by (room, name) — authoritative lookup via the agents table.
	agent, err := h.srv.store.GetAgentByRoomName(ctx, p.Room, p.TargetAgent)
	if err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError,
			fmt.Sprintf("failed to look up agent: %s", err.Error()))
		return
	}
	if agent == nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("target agent %q not found in room %q", p.TargetAgent, p.Room))
		return
	}

	// Guard: reject delivery if agent is stopped, in error, or still being created.
	if agent.State == meta.AgentStateStopped {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("target agent %q is stopped", p.TargetAgent))
		return
	}
	if agent.State == meta.AgentStateCreating {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("target agent %q is still being created", p.TargetAgent))
		return
	}
	if agent.State == meta.AgentStateError {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("target agent %q is in error state; restart or delete it first", p.TargetAgent))
		return
	}

	// Reject concurrent prompts — the shim has no prompt queuing.
	// The sending agent should retry after the target's current turn completes.
	if agent.State == meta.AgentStateRunning {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("target agent %q is busy processing another prompt; cancel its current turn or try again later", p.TargetAgent))
		return
	}

	// Find the linked session ID.
	linkedSessions, err := h.srv.store.ListSessions(ctx, &meta.SessionFilter{AgentID: agent.ID})
	if err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError,
			fmt.Sprintf("failed to find linked session: %s", err.Error()))
		return
	}

	var targetSessionID string
	for _, s := range linkedSessions {
		targetSessionID = s.ID
		break
	}

	if targetSessionID == "" {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("target agent %q not found in room %q", p.TargetAgent, p.Room))
		return
	}

	// Format attributed message.
	attributedMsg := fmt.Sprintf("[room:%s from:%s] %s", p.Room, p.SenderAgent, p.Message)

	log.Printf("ari: room/send dispatching message from %s to %s in room %s (session %s)",
		p.SenderAgent, p.TargetAgent, p.Room, targetSessionID)

	// Transition agent to "running" BEFORE dispatch.
	if updateErr := h.srv.agents.UpdateState(ctx, agent.ID, meta.AgentStateRunning, ""); updateErr != nil {
		log.Printf("ari: room/send: failed to update agent %s state to running: %v", agent.ID, updateErr)
	}

	// Dispatch asynchronously. If dispatch fails, revert to error.
	if err := h.deliverPromptAsync(agent.ID, targetSessionID, attributedMsg); err != nil {
		if updateErr := h.srv.agents.UpdateState(ctx, agent.ID, meta.AgentStateError, err.Error()); updateErr != nil {
			log.Printf("ari: room/send: failed to update agent %s state to error: %v", agent.ID, updateErr)
		}
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError,
			fmt.Sprintf("prompt failed: %s", err.Error()))
		return
	}

	// Dispatch accepted — return immediately. Background goroutine handles
	// running -> created/error transition when the target's turn completes.
	_ = conn.Reply(ctx, req.ID, RoomSendResult{Delivered: true})
}

// handleRoomDelete deletes a room.
// Refuses deletion when non-stopped sessions are associated with the room.
// Workflow:
//  1. Unmarshal RoomDeleteParams
//  2. Call store.ListSessions with Room filter
//  3. Check no non-stopped sessions exist
//  4. Call store.DeleteRoom
//  5. Return nil result
//
// Error handling:
//   - Invalid params (missing name): returns InvalidParams
//   - Active members: returns InvalidParams
//   - Room not found: returns InvalidParams
//   - Store failure: returns InternalError
func (h *connHandler) handleRoomDelete(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var p RoomDeleteParams
	if err := unmarshalParams(req, &p); err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	if p.Name == "" {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, "room name is required")
		return
	}

	// Check for non-stopped sessions in this room.
	sessions, err := h.srv.store.ListSessions(ctx, &meta.SessionFilter{Room: p.Name})
	if err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError,
			fmt.Sprintf("failed to check room members: %s", err.Error()))
		return
	}

	var active []string
	for _, s := range sessions {
		if s.State != meta.SessionStateStopped {
			active = append(active, s.ID)
		}
	}
	if len(active) > 0 {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("room %q has %d active member(s); stop sessions before deleting", p.Name, len(active)))
		return
	}

	// Also check for agents in non-stopped states (covers the async "creating" window
	// when a session may not yet exist in the DB but bootstrap is in progress).
	agents, err := h.srv.agents.List(ctx, &meta.AgentFilter{Room: p.Name})
	if err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError,
			fmt.Sprintf("failed to list room agents: %s", err.Error()))
		return
	}
	var activeAgents []string
	for _, a := range agents {
		if a.State != meta.AgentStateStopped && a.State != meta.AgentStateError {
			activeAgents = append(activeAgents, a.ID)
		}
	}
	if len(activeAgents) > 0 {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("room %q has %d active member(s); stop or fail active agents before deleting", p.Name, len(activeAgents)))
		return
	}

	// Delete all stopped/error agents in this room with full cleanup (session + workspace refs).
	// This is safe because we confirmed all agents are in stopped or error state above.
	agents, err = h.srv.agents.List(ctx, &meta.AgentFilter{Room: p.Name})
	if err != nil {
		replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError,
			fmt.Sprintf("failed to list room agents: %s", err.Error()))
		return
	}
	for _, a := range agents {
		if err := h.deleteAgentWithCleanup(ctx, a.ID); err != nil {
			log.Printf("ari: room/delete warning: failed to delete agent %s in room %s: %v", a.ID, p.Name, err)
		}
	}

	if err := h.srv.store.DeleteRoom(ctx, p.Name); err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
		} else {
			replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError, err.Error())
		}
		return
	}

	log.Printf("ari: room/delete deleted room %s", p.Name)

	_ = conn.Reply(ctx, req.ID, nil)
}

// ────────────────────────────────────────────────────────────────────────────
// Helpers
// ────────────────────────────────────────────────────────────────────────────

// recoveryGuard checks whether the daemon is actively recovering sessions.
// If so, it replies with a CodeRecoveryBlocked JSON-RPC error and returns
// true. Callers should return early when the guard fires.
//
// Guarded methods (operational, mutating): workspace/cleanup,
// session/prompt, session/cancel, agent/prompt, agent/cancel.
//
// NOT guarded (read-only or safety-critical): session/status, session/list,
// session/attach, session/detach, session/stop, agent/status, agent/list,
// agent/attach, agent/detach, agent/stop, agent/delete.
func (h *connHandler) recoveryGuard(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) bool {
	if h.srv.processes.IsRecovering() {
		_ = conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{
			Code:    CodeRecoveryBlocked,
			Message: "daemon is recovering sessions, operational actions are blocked",
		})
		return true
	}
	return false
}

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
