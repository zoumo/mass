// Package ari implements the Agent Runtime Interface (ARI) JSON-RPC server.
package ari

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sourcegraph/jsonrpc2"

	"github.com/open-agent-d/open-agent-d/api"
	"github.com/open-agent-d/open-agent-d/api/meta"
	"github.com/open-agent-d/open-agent-d/pkg/agentd"
	"github.com/open-agent-d/open-agent-d/pkg/store"
	"github.com/open-agent-d/open-agent-d/pkg/workspace"
)

// Server is a JSON-RPC 2.0 server that exposes workspace/*, agentrun/*, and
// agent/* methods over a Unix-domain socket.
type Server struct {
	manager    *workspace.WorkspaceManager
	registry   *Registry
	agents     *agentd.AgentRunManager
	processes  *agentd.ProcessManager
	store      *store.Store
	socketPath string
	baseDir    string

	// ln is the active listener; set by Serve, used by Shutdown.
	ln net.Listener

	// mu protects ln and conns.
	mu sync.RWMutex

	// conns tracks all active jsonrpc2 connections so Shutdown can close them.
	conns map[*jsonrpc2.Conn]struct{}

	// shutdownCh is closed when Shutdown is called to signal the accept loop.
	shutdownCh chan struct{}

	logger *slog.Logger
}

// New creates a Server with the provided dependencies.
// Call Serve to begin accepting connections.
func New(
	manager *workspace.WorkspaceManager,
	registry *Registry,
	agents *agentd.AgentRunManager,
	processes *agentd.ProcessManager,
	s *store.Store,
	socketPath, baseDir string,
	logger *slog.Logger,
) *Server {
	return &Server{
		manager:    manager,
		registry:   registry,
		agents:     agents,
		processes:  processes,
		store:      s,
		socketPath: socketPath,
		baseDir:    baseDir,
		conns:      make(map[*jsonrpc2.Conn]struct{}),
		shutdownCh: make(chan struct{}),
		logger:     logger.With("component", "ari.server"),
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Server lifecycle
// ────────────────────────────────────────────────────────────────────────────

// Serve starts the JSON-RPC server and blocks until Shutdown is called or an
// accept error occurs.
//
// Convention K014: remove the existing socket file before Listen() so a
// previous daemon crash does not block the new process.
func (s *Server) Serve() error {
	// K014: remove stale socket file from a previous crash.
	_ = os.Remove(s.socketPath)

	ln, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("ari: listen %s: %w", s.socketPath, err)
	}

	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()

	s.logger.Info("ari server listening", "socket", s.socketPath)

	for {
		nc, err := ln.Accept()
		if err != nil {
			select {
			case <-s.shutdownCh:
				return nil
			default:
				s.logger.Error("ari: accept error", "error", err)
				return fmt.Errorf("ari: accept: %w", err)
			}
		}
		go s.handleConn(nc)
	}
}

// handleConn wraps a raw net.Conn in a jsonrpc2.Conn, registers it, and
// waits for the connection to close.
func (s *Server) handleConn(nc net.Conn) {
	ctx := context.Background()
	stream := jsonrpc2.NewPlainObjectStream(nc)
	conn := jsonrpc2.NewConn(ctx, stream, jsonrpc2.AsyncHandler(s))

	s.mu.Lock()
	s.conns[conn] = struct{}{}
	s.mu.Unlock()

	<-conn.DisconnectNotify()

	s.mu.Lock()
	delete(s.conns, conn)
	s.mu.Unlock()
}

// Shutdown gracefully stops the server: closes the listener and all active
// connections.
func (s *Server) Shutdown(_ context.Context) error {
	// Signal the accept loop to exit.
	select {
	case <-s.shutdownCh:
		// already shut down
	default:
		close(s.shutdownCh)
	}

	// Close the listener.
	s.mu.RLock()
	ln := s.ln
	s.mu.RUnlock()
	if ln != nil {
		_ = ln.Close()
	}

	// Snapshot and close all active connections.
	s.mu.RLock()
	active := make([]*jsonrpc2.Conn, 0, len(s.conns))
	for c := range s.conns {
		active = append(active, c)
	}
	s.mu.RUnlock()

	for _, c := range active {
		_ = c.Close()
	}

	return nil
}

// ────────────────────────────────────────────────────────────────────────────
// jsonrpc2.Handler dispatch
// ────────────────────────────────────────────────────────────────────────────

// Handle implements jsonrpc2.Handler. It dispatches each incoming request to
// the appropriate workspace/*, agentrun/*, or agent/* handler function.
// Unknown methods return a JSON-RPC -32601 (MethodNotFound) error.
func (s *Server) Handle(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	if req.Notif {
		return
	}

	switch req.Method {
	// workspace/*
	case "workspace/create":
		s.handleWorkspaceCreate(ctx, conn, req)
	case "workspace/status":
		s.handleWorkspaceStatus(ctx, conn, req)
	case "workspace/list":
		s.handleWorkspaceList(ctx, conn, req)
	case "workspace/delete":
		s.handleWorkspaceDelete(ctx, conn, req)
	case "workspace/send":
		s.handleWorkspaceSend(ctx, conn, req)

	// agentrun/* — running agent instance methods
	case "agentrun/create":
		s.handleAgentRunCreate(ctx, conn, req)
	case "agentrun/prompt":
		s.handleAgentRunPrompt(ctx, conn, req)
	case "agentrun/cancel":
		s.handleAgentRunCancel(ctx, conn, req)
	case "agentrun/stop":
		s.handleAgentRunStop(ctx, conn, req)
	case "agentrun/delete":
		s.handleAgentRunDelete(ctx, conn, req)
	case "agentrun/restart":
		s.handleAgentRunRestart(ctx, conn, req)
	case "agentrun/list":
		s.handleAgentRunList(ctx, conn, req)
	case "agentrun/status":
		s.handleAgentRunStatus(ctx, conn, req)
	case "agentrun/attach":
		s.handleAgentRunAttach(ctx, conn, req)

	// agent/* — agent template (configuration) CRUD methods
	case "agent/set":
		s.handleAgentSet(ctx, conn, req)
	case "agent/get":
		s.handleAgentGet(ctx, conn, req)
	case "agent/list":
		s.handleAgentList(ctx, conn, req)
	case "agent/delete":
		s.handleAgentDelete(ctx, conn, req)

	default:
		s.replyErr(ctx, conn, req, jsonrpc2.CodeMethodNotFound,
			fmt.Sprintf("unknown method %q", req.Method))
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Reply helpers
// ────────────────────────────────────────────────────────────────────────────

func (s *Server) replyOK(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request, result any) {
	_ = conn.Reply(ctx, req.ID, result)
}

func (s *Server) replyErr(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request, code int64, msg string) {
	_ = conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{Code: code, Message: msg})
}

// ────────────────────────────────────────────────────────────────────────────
// workspace/* handlers
// ────────────────────────────────────────────────────────────────────────────

// handleWorkspaceCreate handles the workspace/create method.
//
// Observability: INFO on entry (name, phase:pending); INFO/WARN in goroutine
// on prepare success (phase:ready, path) or failure (phase:error).
func (s *Server) handleWorkspaceCreate(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var params WorkspaceCreateParams
	if err := unmarshalParams(req, &params); err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}
	if params.Name == "" {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams, "name is required")
		return
	}

	s.logger.Info("workspace/create", "workspace", params.Name, "phase", "pending")

	// Create workspace record in the store.
	ws := &meta.Workspace{
		Metadata: meta.ObjectMeta{
			Name:   params.Name,
			Labels: params.Labels,
		},
		Spec: meta.WorkspaceSpec{
			Source: params.Source,
		},
		Status: meta.WorkspaceStatus{
			Phase: meta.WorkspacePhasePending,
		},
	}
	if err := s.store.CreateWorkspace(ctx, ws); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams,
				fmt.Sprintf("workspace %s already exists", params.Name))
			return
		}
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInternalError, err.Error())
		return
	}

	// Reply with pending immediately, then prepare asynchronously.
	s.replyOK(ctx, conn, req, WorkspaceCreateResult{
		Name:  params.Name,
		Phase: string(meta.WorkspacePhasePending),
	})

	// Build the workspace spec for Prepare.
	var src workspace.Source
	if len(params.Source) > 0 {
		if err := json.Unmarshal(params.Source, &src); err != nil {
			// Log the error; the prepare goroutine will fail and mark error state.
			s.logger.Warn("workspace/create: invalid source JSON",
				"workspace", params.Name, "error", err)
		}
	}

	wsSpec := workspace.WorkspaceSpec{
		OarVersion: "0.1.0",
		Metadata:   workspace.WorkspaceMetadata{Name: params.Name},
		Source:     src,
	}
	targetDir := filepath.Join(s.baseDir, "workspaces", params.Name)

	// Capture params for the goroutine.
	wsName := params.Name

	go func() {
		prepareCtx := context.Background()

		path, err := s.manager.Prepare(prepareCtx, wsSpec, targetDir)
		if err != nil {
			s.logger.Warn("workspace/create: prepare failed",
				"workspace", wsName, "phase", "error", "error", err)
			_ = s.store.UpdateWorkspaceStatus(prepareCtx, wsName, meta.WorkspaceStatus{
				Phase: meta.WorkspacePhaseError,
			})
			return
		}

		s.logger.Info("workspace/create: prepared",
			"workspace", wsName, "phase", "ready", "path", path)
		_ = s.store.UpdateWorkspaceStatus(prepareCtx, wsName, meta.WorkspaceStatus{
			Phase: meta.WorkspacePhaseReady,
			Path:  path,
		})
		s.registry.Add(wsName, wsName, path, wsSpec)
	}()
}

// handleWorkspaceStatus handles the workspace/status method.
//
// Lookup order: in-memory registry (fast path for ready workspaces), then DB
// fallback for workspaces still in pending/error phase.
func (s *Server) handleWorkspaceStatus(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var params WorkspaceStatusParams
	if err := unmarshalParams(req, &params); err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	s.logger.Info("workspace/status", "workspace", params.Name)

	// Fast path: registry contains ready workspaces.
	if wm := s.registry.Get(params.Name); wm != nil {
		members := s.listWorkspaceMembers(ctx, params.Name)
		s.replyOK(ctx, conn, req, WorkspaceStatusResult{
			Name:    wm.Name,
			Phase:   wm.Status,
			Path:    wm.Path,
			Members: members,
		})
		return
	}

	// Fallback: DB lookup for pending/error workspaces.
	ws, err := s.store.GetWorkspace(ctx, params.Name)
	if err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInternalError, err.Error())
		return
	}
	if ws == nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("workspace %s not found", params.Name))
		return
	}

	members := s.listWorkspaceMembers(ctx, params.Name)
	s.replyOK(ctx, conn, req, WorkspaceStatusResult{
		Name:    ws.Metadata.Name,
		Phase:   string(ws.Status.Phase),
		Path:    ws.Status.Path,
		Members: members,
	})
}

// handleWorkspaceList handles the workspace/list method.
//
// Returns all workspaces currently in the in-memory registry (i.e., ready
// workspaces). Pending workspaces can be queried individually via
// workspace/status.
func (s *Server) handleWorkspaceList(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	s.logger.Info("workspace/list")

	metas := s.registry.List()
	infos := make([]WorkspaceInfo, 0, len(metas))
	for _, m := range metas {
		infos = append(infos, WorkspaceInfo{
			Name:  m.Name,
			Phase: m.Status,
			Path:  m.Path,
		})
	}

	s.replyOK(ctx, conn, req, WorkspaceListResult{Workspaces: infos})
}

// handleWorkspaceDelete handles the workspace/delete method.
//
// Rejects deletion if the workspace has active agent runs (enforced by the store).
func (s *Server) handleWorkspaceDelete(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var params WorkspaceDeleteParams
	if err := unmarshalParams(req, &params); err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	s.logger.Info("workspace/delete", "workspace", params.Name)

	if err := s.store.DeleteWorkspace(ctx, params.Name); err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams, err.Error())
			return
		}
		// "has N agent(s)" or other domain error.
		s.replyErr(ctx, conn, req, CodeRecoveryBlocked, err.Error())
		return
	}

	s.registry.Remove(params.Name)
	s.replyOK(ctx, conn, req, struct{}{})
}

// handleWorkspaceSend handles the workspace/send method.
//
// Routes a message from one agent run to another within a workspace via a
// fire-and-forget ShimClient.Prompt call. Rejects when:
//   - recovery is active (CodeRecoveryBlocked)
//   - target agent run not found (-32602)
//   - target agent run is in error state (-32001)
//   - target shim is not running (-32001)
//
// Observability: INFO on dispatch (workspace, from, to); WARN on each
// rejection path with reason.
func (s *Server) handleWorkspaceSend(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var params WorkspaceSendParams
	if err := unmarshalParams(req, &params); err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}
	if params.Workspace == "" || params.From == "" || params.To == "" || params.Message == "" {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams,
			"workspace, from, to, and message are required")
		return
	}

	s.logger.Info("workspace/send",
		"workspace", params.Workspace, "from", params.From, "to", params.To)

	// Recovery guard.
	if s.processes.IsRecovering() {
		s.logger.Warn("workspace/send: recovery blocked",
			"workspace", params.Workspace, "to", params.To)
		s.replyErr(ctx, conn, req, CodeRecoveryBlocked, "daemon is recovering agents")
		return
	}

	// Load target agent run from store.
	agent, err := s.store.GetAgentRun(ctx, params.Workspace, params.To)
	if err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInternalError, err.Error())
		return
	}
	if agent == nil {
		s.logger.Warn("workspace/send: target agent not found",
			"workspace", params.Workspace, "to", params.To)
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("agent %s/%s not found", params.Workspace, params.To))
		return
	}
	if agent.Status.State == api.StatusError {
		s.logger.Warn("workspace/send: target agent in error state",
			"workspace", params.Workspace, "to", params.To)
		s.replyErr(ctx, conn, req, CodeRecoveryBlocked, "target agent is in error state")
		return
	}
	if agent.Status.State != api.StatusIdle {
		s.logger.Warn("workspace/send: target agent not idle",
			"workspace", params.Workspace, "to", params.To, "state", agent.Status.State)
		s.replyErr(ctx, conn, req, CodeRecoveryBlocked,
			fmt.Sprintf("target agent not in idle state: %s", agent.Status.State))
		return
	}

	reserved, err := s.agents.TransitionState(ctx, params.Workspace, params.To, api.StatusIdle, api.StatusRunning)
	if err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInternalError, err.Error())
		return
	}
	if !reserved {
		current, getErr := s.store.GetAgentRun(ctx, params.Workspace, params.To)
		if getErr != nil {
			s.replyErr(ctx, conn, req, jsonrpc2.CodeInternalError, getErr.Error())
			return
		}
		state := "<missing>"
		if current != nil {
			state = string(current.Status.State)
		}
		s.replyErr(ctx, conn, req, CodeRecoveryBlocked,
			fmt.Sprintf("target agent not in idle state: %s", state))
		return
	}

	// Connect to the target shim.
	client, err := s.processes.Connect(ctx, params.Workspace, params.To)
	if err != nil {
		s.logger.Warn("workspace/send: target agent not running",
			"workspace", params.Workspace, "to", params.To, "error", err)
		s.recordPromptDeliveryFailure(params.Workspace, params.To, agent.Status, err, true)
		s.replyErr(ctx, conn, req, CodeRecoveryBlocked, "target agent is not running")
		return
	}

	// Fire-and-forget: send prompt without blocking the caller.
	msg := buildWorkspaceEnvelope(params) + params.Message
	go func() {
		if _, err := client.Prompt(context.Background(), msg); err != nil {
			s.logger.Warn("workspace/send: prompt delivery failed",
				"workspace", params.Workspace, "to", params.To, "error", err)
			s.recordPromptDeliveryFailure(params.Workspace, params.To, agent.Status, err, false)
		}
	}()

	s.replyOK(ctx, conn, req, WorkspaceSendResult{Delivered: true})
}

// buildWorkspaceEnvelope constructs the envelope header prepended to every
// workspace message before delivery. It encodes the sender identity and, when
// NeedsReply is set, the reply-to address and reply-requested flag so the
// receiving agent knows it is expected to respond.
func buildWorkspaceEnvelope(p WorkspaceSendParams) string {
	if p.NeedsReply {
		return "[workspace-message from=" + p.From + " reply-to=" + p.From + " reply-requested=true]\n\n"
	}
	return "[workspace-message from=" + p.From + "]\n\n"
}

func (s *Server) recordPromptDeliveryFailure(workspace, name string, fallback meta.AgentRunStatus, cause error, markErrorWhenRuntimeUnavailable bool) {
	ctx := context.Background()
	current, err := s.store.GetAgentRun(ctx, workspace, name)
	if err != nil {
		s.logger.Warn("prompt failure: current state lookup failed",
			"workspace", workspace, "name", name, "error", err)
	}
	if current != nil && current.Status.State == api.StatusStopped {
		s.logger.Info("prompt failure: ignored after stop",
			"workspace", workspace, "name", name, "error", cause)
		return
	}

	if rts, statusErr := s.processes.RuntimeStatus(ctx, workspace, name); statusErr == nil {
		status := fallback
		if current != nil {
			status = current.Status
		}
		status.State = rts.State.Status
		status.ErrorMessage = cause.Error()
		_ = s.agents.UpdateStatus(ctx, workspace, name, status)
		return
	}
	if !markErrorWhenRuntimeUnavailable {
		s.logger.Info("prompt failure: runtime unavailable, leaving terminal state to process watcher",
			"workspace", workspace, "name", name, "error", cause)
		return
	}

	current, err = s.store.GetAgentRun(ctx, workspace, name)
	if err != nil {
		s.logger.Warn("prompt failure: current state lookup failed",
			"workspace", workspace, "name", name, "error", err)
	}
	if current != nil && current.Status.State == api.StatusStopped {
		s.logger.Info("prompt failure: ignored after stop",
			"workspace", workspace, "name", name, "error", cause)
		return
	}

	_ = s.agents.UpdateStatus(ctx, workspace, name, meta.AgentRunStatus{
		State:          api.StatusError,
		ShimSocketPath: fallback.ShimSocketPath,
		ShimStateDir:   fallback.ShimStateDir,
		ShimPID:        fallback.ShimPID,
		ErrorMessage:   cause.Error(),
	})
}

// ────────────────────────────────────────────────────────────────────────────
// agentrun/* handlers
// ────────────────────────────────────────────────────────────────────────────

// handleAgentRunCreate handles the agentrun/create method.
//
// Validates Workspace/Name/Agent, checks workspace phase, creates the
// agent run record with state=creating, and starts the shim in a background goroutine.
// Returns AgentRunCreateResult immediately (state is always "creating").
//
// Observability: INFO on creation; INFO/WARN in goroutine on Start success/failure.
func (s *Server) handleAgentRunCreate(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var params AgentRunCreateParams
	if err := unmarshalParams(req, &params); err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}
	if params.Workspace == "" || params.Name == "" || params.Agent == "" {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams,
			"workspace, name, and agent are required")
		return
	}

	// Early socket-path length guard — fail before any DB write (D111).
	if err := s.processes.ValidateAgentSocketPath(params.Workspace, params.Name); err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	s.logger.Info("agentrun/create", "workspace", params.Workspace, "name", params.Name)

	// Load workspace from DB — must exist and be ready.
	ws, err := s.store.GetWorkspace(ctx, params.Workspace)
	if err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInternalError, err.Error())
		return
	}
	if ws == nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("workspace %s not found", params.Workspace))
		return
	}
	if ws.Status.Phase != meta.WorkspacePhaseReady {
		s.replyErr(ctx, conn, req, CodeRecoveryBlocked,
			fmt.Sprintf("workspace %s is not ready (phase=%s)", params.Workspace, ws.Status.Phase))
		return
	}

	// Create agent run record in DB.
	agent := &meta.AgentRun{
		Metadata: meta.ObjectMeta{
			Name:      params.Name,
			Workspace: params.Workspace,
			Labels:    params.Labels,
		},
		Spec: meta.AgentRunSpec{
			Agent:         params.Agent,
			RestartPolicy: params.RestartPolicy,
			SystemPrompt:  params.SystemPrompt,
		},
		Status: meta.AgentRunStatus{
			State: api.StatusCreating,
		},
	}
	if err := s.agents.Create(ctx, agent); err != nil {
		var alreadyExists *agentd.ErrAgentRunAlreadyExists
		if errors.As(err, &alreadyExists) {
			s.replyErr(ctx, conn, req, CodeRecoveryBlocked, err.Error())
			return
		}
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInternalError, err.Error())
		return
	}

	s.logger.Info("agentrun/create: agent run created, starting shim",
		"workspace", params.Workspace, "name", params.Name)

	// Start shim in background.
	wsName := params.Workspace
	agName := params.Name
	go func() {
		bgCtx := context.Background()
		if _, err := s.processes.Start(bgCtx, wsName, agName); err != nil {
			s.logger.Warn("agentrun/create: shim start failed",
				"workspace", wsName, "name", agName, "error", err)
			_ = s.agents.UpdateStatus(bgCtx, wsName, agName, meta.AgentRunStatus{
				State:        api.StatusError,
				ErrorMessage: err.Error(),
			})
		} else {
			s.logger.Info("agentrun/create: shim started",
				"workspace", wsName, "name", agName)
		}
	}()

	s.replyOK(ctx, conn, req, AgentRunCreateResult{
		Workspace: params.Workspace,
		Name:      params.Name,
		State:     string(api.StatusCreating),
	})
}

// handleAgentRunPrompt handles the agentrun/prompt method.
//
// Validates agent run state == idle, connects to shim, fires prompt in background.
// Returns AgentRunPromptResult{Accepted: true} immediately.
//
// Observability: INFO on dispatch; WARN on rejection (bad state, not running).
func (s *Server) handleAgentRunPrompt(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var params AgentRunPromptParams
	if err := unmarshalParams(req, &params); err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}
	if params.Workspace == "" || params.Name == "" || params.Prompt == "" {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams,
			"workspace, name, and prompt are required")
		return
	}

	s.logger.Info("agentrun/prompt", "workspace", params.Workspace, "name", params.Name)

	// Recovery guard.
	if s.processes.IsRecovering() {
		s.logger.Warn("agentrun/prompt: recovery blocked",
			"workspace", params.Workspace, "name", params.Name)
		s.replyErr(ctx, conn, req, CodeRecoveryBlocked, "daemon is recovering agents")
		return
	}

	// Load agent run from DB.
	agent, err := s.store.GetAgentRun(ctx, params.Workspace, params.Name)
	if err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInternalError, err.Error())
		return
	}
	if agent == nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("agent %s/%s not found", params.Workspace, params.Name))
		return
	}

	// Validate state is idle.
	if agent.Status.State != api.StatusIdle {
		s.logger.Warn("agentrun/prompt: agent not in idle state",
			"workspace", params.Workspace, "name", params.Name, "state", agent.Status.State)
		s.replyErr(ctx, conn, req, CodeRecoveryBlocked,
			fmt.Sprintf("agent not in idle state: %s", agent.Status.State))
		return
	}

	// Reserve the agent run before acknowledging the prompt. The shim remains
	// the post-bootstrap state authority; this write is an admission lock so a
	// second prompt/stop/delete cannot observe stale idle while delivery is
	// still queued in the goroutine below.
	reserved, err := s.agents.TransitionState(ctx, params.Workspace, params.Name, api.StatusIdle, api.StatusRunning)
	if err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInternalError, err.Error())
		return
	}
	if !reserved {
		current, getErr := s.store.GetAgentRun(ctx, params.Workspace, params.Name)
		if getErr != nil {
			s.replyErr(ctx, conn, req, jsonrpc2.CodeInternalError, getErr.Error())
			return
		}
		state := "<missing>"
		if current != nil {
			state = string(current.Status.State)
		}
		s.replyErr(ctx, conn, req, CodeRecoveryBlocked,
			fmt.Sprintf("agent not in idle state: %s", state))
		return
	}

	// Connect to shim.
	client, err := s.processes.Connect(ctx, params.Workspace, params.Name)
	if err != nil {
		s.logger.Warn("agentrun/prompt: agent not running",
			"workspace", params.Workspace, "name", params.Name, "error", err)
		s.recordPromptDeliveryFailure(params.Workspace, params.Name, agent.Status, err, true)
		s.replyErr(ctx, conn, req, CodeRecoveryBlocked, "agent not running")
		return
	}

	// Fire-and-forget prompt.
	prompt := params.Prompt
	go func() {
		if _, err := client.Prompt(context.Background(), prompt); err != nil {
			s.logger.Warn("agentrun/prompt: prompt delivery failed",
				"workspace", params.Workspace, "name", params.Name, "error", err)
			s.recordPromptDeliveryFailure(params.Workspace, params.Name, agent.Status, err, false)
		}
	}()

	s.logger.Info("agentrun/prompt: dispatched",
		"workspace", params.Workspace, "name", params.Name)
	s.replyOK(ctx, conn, req, AgentRunPromptResult{Accepted: true})
}

// handleAgentRunCancel handles the agentrun/cancel method.
//
// Connects to the running shim and calls Cancel. Returns empty result.
func (s *Server) handleAgentRunCancel(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var params AgentRunCancelParams
	if err := unmarshalParams(req, &params); err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	s.logger.Info("agentrun/cancel", "workspace", params.Workspace, "name", params.Name)

	// Load agent run.
	agent, err := s.store.GetAgentRun(ctx, params.Workspace, params.Name)
	if err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInternalError, err.Error())
		return
	}
	if agent == nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("agent %s/%s not found", params.Workspace, params.Name))
		return
	}

	client, err := s.processes.Connect(ctx, params.Workspace, params.Name)
	if err != nil {
		s.replyErr(ctx, conn, req, CodeRecoveryBlocked, "agent not running")
		return
	}

	if err := client.Cancel(ctx); err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInternalError, err.Error())
		return
	}

	s.replyOK(ctx, conn, req, struct{}{})
}

// handleAgentRunStop handles the agentrun/stop method.
//
// Calls processes.Stop which sends runtime/stop to the shim and waits.
// Returns empty result.
func (s *Server) handleAgentRunStop(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var params AgentRunStopParams
	if err := unmarshalParams(req, &params); err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	s.logger.Info("agentrun/stop", "workspace", params.Workspace, "name", params.Name)

	if err := s.processes.Stop(ctx, params.Workspace, params.Name); err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInternalError, err.Error())
		return
	}

	s.replyOK(ctx, conn, req, struct{}{})
}

// handleAgentRunDelete handles the agentrun/delete method.
//
// Validates agent run is in stopped/error state, then deletes from DB.
// Maps ErrDeleteNotStopped → -32001, ErrAgentRunNotFound → -32602.
func (s *Server) handleAgentRunDelete(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var params AgentRunDeleteParams
	if err := unmarshalParams(req, &params); err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	s.logger.Info("agentrun/delete", "workspace", params.Workspace, "name", params.Name)

	if err := s.agents.Delete(ctx, params.Workspace, params.Name); err != nil {
		var notFound *agentd.ErrAgentRunNotFound
		if errors.As(err, &notFound) {
			s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams, err.Error())
			return
		}
		var notStopped *agentd.ErrDeleteNotStopped
		if errors.As(err, &notStopped) {
			s.replyErr(ctx, conn, req, CodeRecoveryBlocked, err.Error())
			return
		}
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInternalError, err.Error())
		return
	}

	// Clean up bundle directory (best effort; DB record already deleted).
	bundlePath := s.processes.BundlePath(params.Workspace, params.Name)
	if err := os.RemoveAll(bundlePath); err != nil {
		s.logger.Warn("agentrun/delete: failed to remove bundle",
			"workspace", params.Workspace, "name", params.Name,
			"bundle", bundlePath, "error", err)
	}

	s.replyOK(ctx, conn, req, struct{}{})
}

// handleAgentRunRestart handles the agentrun/restart method.
//
// Accepts any agent state. For agents with an active shim (creating/idle/running),
// the shim is stopped first in the background before a new one is started.
// Returns AgentRunRestartResult immediately (state="creating").
func (s *Server) handleAgentRunRestart(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var params AgentRunRestartParams
	if err := unmarshalParams(req, &params); err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	s.logger.Info("agentrun/restart", "workspace", params.Workspace, "name", params.Name)

	// Load agent run.
	agent, err := s.store.GetAgentRun(ctx, params.Workspace, params.Name)
	if err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInternalError, err.Error())
		return
	}
	if agent == nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("agent %s/%s not found", params.Workspace, params.Name))
		return
	}

	// Agents in terminal states (stopped/error) have no active shim; others do.
	needsStop := agent.Status.State != api.StatusStopped && agent.Status.State != api.StatusError

	// Transition to creating.
	if err := s.agents.UpdateStatus(ctx, params.Workspace, params.Name, meta.AgentRunStatus{
		State: api.StatusCreating,
	}); err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInternalError, err.Error())
		return
	}

	// Start shim in background, stopping any existing shim first if needed.
	wsName := params.Workspace
	agName := params.Name
	go func() {
		bgCtx := context.Background()
		if needsStop {
			if err := s.processes.Stop(bgCtx, wsName, agName); err != nil {
				s.logger.Warn("agentrun/restart: pre-stop failed, continuing",
					"workspace", wsName, "name", agName, "error", err)
			}
			// Stop() transitions state to "stopped"; re-set to "creating" for Start().
			if err := s.agents.UpdateStatus(bgCtx, wsName, agName, meta.AgentRunStatus{
				State: api.StatusCreating,
			}); err != nil {
				s.logger.Warn("agentrun/restart: failed to re-transition to creating",
					"workspace", wsName, "name", agName, "error", err)
				return
			}
		}
		if _, err := s.processes.Start(bgCtx, wsName, agName); err != nil {
			s.logger.Warn("agentrun/restart: shim start failed",
				"workspace", wsName, "name", agName, "error", err)
			_ = s.agents.UpdateStatus(bgCtx, wsName, agName, meta.AgentRunStatus{
				State:        api.StatusError,
				ErrorMessage: err.Error(),
			})
		}
	}()

	s.replyOK(ctx, conn, req, AgentRunRestartResult{
		Workspace: params.Workspace,
		Name:      params.Name,
		State:     string(api.StatusCreating),
	})
}

// handleAgentRunList handles the agentrun/list method.
//
// Returns all agent runs matching the optional workspace/state filter.
func (s *Server) handleAgentRunList(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var params AgentRunListParams
	if req.Params != nil {
		if err := unmarshalParams(req, &params); err != nil {
			s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams, err.Error())
			return
		}
	}

	s.logger.Info("agentrun/list", "workspace", params.Workspace, "state", params.State)

	filter := &meta.AgentRunFilter{
		Workspace: params.Workspace,
		State:     api.Status(params.State),
	}

	agents, err := s.agents.List(ctx, filter)
	if err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInternalError, err.Error())
		return
	}

	infos := make([]AgentRunInfo, 0, len(agents))
	for _, ag := range agents {
		infos = append(infos, agentRunToInfo(ag))
	}

	s.replyOK(ctx, conn, req, AgentRunListResult{AgentRuns: infos})
}

// handleAgentRunStatus handles the agentrun/status method.
//
// Returns detailed agent run info plus optional shim runtime state.
func (s *Server) handleAgentRunStatus(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var params AgentRunStatusParams
	if err := unmarshalParams(req, &params); err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	s.logger.Info("agentrun/status", "workspace", params.Workspace, "name", params.Name)

	agent, err := s.store.GetAgentRun(ctx, params.Workspace, params.Name)
	if err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInternalError, err.Error())
		return
	}
	if agent == nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("agent %s/%s not found", params.Workspace, params.Name))
		return
	}

	result := AgentRunStatusResult{AgentRun: agentRunToInfo(agent)}

	// Best-effort: fetch shim runtime state if available.
	if rts, err := s.processes.RuntimeStatus(ctx, params.Workspace, params.Name); err == nil {
		st := rts.State
		result.ShimState = &ShimStateInfo{
			Status: string(st.Status),
			PID:    agent.Status.ShimPID,
			Bundle: st.Bundle,
		}
	}

	s.replyOK(ctx, conn, req, result)
}

// handleAgentRunAttach handles the agentrun/attach method.
//
// Returns the shim's Unix socket path so the caller can connect directly.
// Agent run must be in idle or running state.
func (s *Server) handleAgentRunAttach(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var params AgentRunAttachParams
	if err := unmarshalParams(req, &params); err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	s.logger.Info("agentrun/attach", "workspace", params.Workspace, "name", params.Name)

	agent, err := s.store.GetAgentRun(ctx, params.Workspace, params.Name)
	if err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInternalError, err.Error())
		return
	}
	if agent == nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("agent %s/%s not found", params.Workspace, params.Name))
		return
	}

	if agent.Status.State != api.StatusIdle && agent.Status.State != api.StatusRunning {
		s.replyErr(ctx, conn, req, CodeRecoveryBlocked,
			fmt.Sprintf("agent not in idle/running state: %s", agent.Status.State))
		return
	}

	// Try in-memory process map first; fall back to DB-stored socket path.
	proc, err := s.processes.Connect(ctx, params.Workspace, params.Name)
	if err == nil {
		_ = proc // we only needed connect to confirm; get socket from process
	}
	socketPath := agent.Status.ShimSocketPath
	if p := s.processes.GetProcess(params.Workspace + "/" + params.Name); p != nil {
		socketPath = p.SocketPath
	}

	s.replyOK(ctx, conn, req, AgentRunAttachResult{SocketPath: socketPath})
}

// ────────────────────────────────────────────────────────────────────────────
// agentrun helper
// ────────────────────────────────────────────────────────────────────────────

// agentRunToInfo converts a meta.AgentRun to an AgentRunInfo wire type.
// Note: no agentId field — identity is (workspace, name).
func agentRunToInfo(ag *meta.AgentRun) AgentRunInfo {
	return AgentRunInfo{
		Workspace:    ag.Metadata.Workspace,
		Name:         ag.Metadata.Name,
		Agent:        ag.Spec.Agent,
		State:        string(ag.Status.State),
		ErrorMessage: ag.Status.ErrorMessage,
		Labels:       ag.Metadata.Labels,
		CreatedAt:    ag.Metadata.CreatedAt,
	}
}

// listWorkspaceMembers returns AgentRunInfo for all agent runs in the given workspace.
// Returns nil (not an error) if the query fails.
func (s *Server) listWorkspaceMembers(ctx context.Context, wsName string) []AgentRunInfo {
	agents, err := s.agents.List(ctx, &meta.AgentRunFilter{Workspace: wsName})
	if err != nil {
		s.logger.Error("listWorkspaceMembers: list agent runs failed", "workspace", wsName, "err", err)
		return nil
	}
	infos := make([]AgentRunInfo, 0, len(agents))
	for _, ag := range agents {
		infos = append(infos, agentRunToInfo(ag))
	}
	return infos
}

// ────────────────────────────────────────────────────────────────────────────
// agent/* handlers (Agent definition CRUD)
// ────────────────────────────────────────────────────────────────────────────

// handleAgentSet handles the agent/set method.
// Creates or updates an agent definition entity in the metadata store.
//
// Observability: INFO on success with name and command logged.
func (s *Server) handleAgentSet(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var params AgentSetParams
	if err := unmarshalParams(req, &params); err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}
	if params.Name == "" {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams, "name is required")
		return
	}
	if params.Command == "" {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams, "command is required")
		return
	}

	s.logger.Info("agent/set", "name", params.Name, "command", params.Command)

	ag := &meta.Agent{
		Metadata: meta.ObjectMeta{Name: params.Name},
		Spec: meta.AgentSpec{
			Command:               params.Command,
			Args:                  params.Args,
			Env:                   params.Env,
			StartupTimeoutSeconds: params.StartupTimeoutSeconds,
		},
	}
	if err := s.store.SetAgent(ctx, ag); err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInternalError, err.Error())
		return
	}

	// Read back to get server-assigned timestamps.
	stored, err := s.store.GetAgent(ctx, params.Name)
	if err != nil || stored == nil {
		s.replyOK(ctx, conn, req, agentToInfo(ag))
		return
	}
	s.replyOK(ctx, conn, req, agentToInfo(stored))
}

// handleAgentGet handles the agent/get method.
//
// Returns the agent definition info or -32602 (InvalidParams) if not found.
func (s *Server) handleAgentGet(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var params AgentGetParams
	if err := unmarshalParams(req, &params); err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}
	if params.Name == "" {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams, "name is required")
		return
	}

	s.logger.Info("agent/get", "name", params.Name)

	ag, err := s.store.GetAgent(ctx, params.Name)
	if err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInternalError, err.Error())
		return
	}
	if ag == nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("agent %s not found", params.Name))
		return
	}

	s.replyOK(ctx, conn, req, AgentGetResult{Agent: agentToInfo(ag)})
}

// handleAgentList handles the agent/list method.
//
// Returns all agent definition info objects stored in the metadata DB.
func (s *Server) handleAgentList(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	s.logger.Info("agent/list")

	ags, err := s.store.ListAgents(ctx)
	if err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInternalError, err.Error())
		return
	}

	infos := make([]AgentInfo, 0, len(ags))
	for _, ag := range ags {
		infos = append(infos, agentToInfo(ag))
	}

	s.replyOK(ctx, conn, req, AgentListResult{Agents: infos})
}

// handleAgentDelete handles the agent/delete method.
// No-op if the agent definition does not exist.
//
// Observability: INFO on delete with name logged.
func (s *Server) handleAgentDelete(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var params AgentDeleteParams
	if err := unmarshalParams(req, &params); err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}
	if params.Name == "" {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams, "name is required")
		return
	}

	s.logger.Info("agent/delete", "name", params.Name)

	if err := s.store.DeleteAgent(ctx, params.Name); err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInternalError, err.Error())
		return
	}

	s.replyOK(ctx, conn, req, struct{}{})
}

// agentToInfo converts a meta.Agent to an AgentInfo wire type.
func agentToInfo(ag *meta.Agent) AgentInfo {
	return AgentInfo{
		Name:                  ag.Metadata.Name,
		Command:               ag.Spec.Command,
		Args:                  ag.Spec.Args,
		Env:                   ag.Spec.Env,
		StartupTimeoutSeconds: ag.Spec.StartupTimeoutSeconds,
		CreatedAt:             ag.Metadata.CreatedAt,
		UpdatedAt:             ag.Metadata.UpdatedAt,
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Internal helpers
// ────────────────────────────────────────────────────────────────────────────

// unmarshalParams decodes JSON-RPC request params into dst.
func unmarshalParams(req *jsonrpc2.Request, dst any) error {
	if req.Params == nil {
		return fmt.Errorf("missing params")
	}
	return json.Unmarshal(*req.Params, dst)
}
