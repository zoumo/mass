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

	"github.com/open-agent-d/open-agent-d/pkg/agentd"
	"github.com/open-agent-d/open-agent-d/pkg/meta"
	"github.com/open-agent-d/open-agent-d/pkg/spec"
	"github.com/open-agent-d/open-agent-d/pkg/workspace"
)

// Server is a JSON-RPC 2.0 server that exposes workspace/* and agent/* methods
// over a Unix-domain socket.
type Server struct {
	manager        *workspace.WorkspaceManager
	registry       *Registry
	agents         *agentd.AgentManager
	processes      *agentd.ProcessManager
	runtimeClasses *agentd.RuntimeClassRegistry
	config         agentd.Config
	store          *meta.Store
	socketPath     string
	baseDir        string

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
	agents *agentd.AgentManager,
	processes *agentd.ProcessManager,
	runtimeClasses *agentd.RuntimeClassRegistry,
	config agentd.Config,
	store *meta.Store,
	socketPath, baseDir string,
) *Server {
	return &Server{
		manager:        manager,
		registry:       registry,
		agents:         agents,
		processes:      processes,
		runtimeClasses: runtimeClasses,
		config:         config,
		store:          store,
		socketPath:     socketPath,
		baseDir:        baseDir,
		conns:          make(map[*jsonrpc2.Conn]struct{}),
		shutdownCh:     make(chan struct{}),
		logger:         slog.Default().With("component", "ari.server"),
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
// the appropriate workspace/* or agent/* handler function.
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

	// agent/*
	case "agent/create":
		s.handleAgentCreate(ctx, conn, req)
	case "agent/prompt":
		s.handleAgentPrompt(ctx, conn, req)
	case "agent/cancel":
		s.handleAgentCancel(ctx, conn, req)
	case "agent/stop":
		s.handleAgentStop(ctx, conn, req)
	case "agent/delete":
		s.handleAgentDelete(ctx, conn, req)
	case "agent/restart":
		s.handleAgentRestart(ctx, conn, req)
	case "agent/list":
		s.handleAgentList(ctx, conn, req)
	case "agent/status":
		s.handleAgentStatus(ctx, conn, req)
	case "agent/attach":
		s.handleAgentAttach(ctx, conn, req)

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
		s.replyOK(ctx, conn, req, WorkspaceStatusResult{
			Name:  wm.Name,
			Phase: wm.Status,
			Path:  wm.Path,
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

	s.replyOK(ctx, conn, req, WorkspaceStatusResult{
		Name:  ws.Metadata.Name,
		Phase: string(ws.Status.Phase),
		Path:  ws.Status.Path,
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
// Rejects deletion if the workspace has active agents (enforced by the store).
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
// Routes a message from one agent to another within a workspace via a
// fire-and-forget ShimClient.Prompt call. Rejects when:
//   - recovery is active (CodeRecoveryBlocked)
//   - target agent not found (-32602)
//   - target agent is in error state (-32001)
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

	// Load target agent from store.
	agent, err := s.store.GetAgent(ctx, params.Workspace, params.To)
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
	if agent.Status.State == spec.StatusError {
		s.logger.Warn("workspace/send: target agent in error state",
			"workspace", params.Workspace, "to", params.To)
		s.replyErr(ctx, conn, req, CodeRecoveryBlocked, "target agent is in error state")
		return
	}

	// Connect to the target shim.
	client, err := s.processes.Connect(ctx, params.Workspace, params.To)
	if err != nil {
		s.logger.Warn("workspace/send: target agent not running",
			"workspace", params.Workspace, "to", params.To, "error", err)
		s.replyErr(ctx, conn, req, CodeRecoveryBlocked, "target agent is not running")
		return
	}

	// Fire-and-forget: send prompt without blocking the caller.
	msg := params.Message
	go func() {
		if _, err := client.Prompt(context.Background(), msg); err != nil {
			s.logger.Warn("workspace/send: prompt delivery failed",
				"workspace", params.Workspace, "to", params.To, "error", err)
		}
	}()

	s.replyOK(ctx, conn, req, WorkspaceSendResult{Delivered: true})
}

// ────────────────────────────────────────────────────────────────────────────
// agent/* handlers
// ────────────────────────────────────────────────────────────────────────────

// handleAgentCreate handles the agent/create method.
//
// Validates Workspace/Name/RuntimeClass, checks workspace phase, creates the
// agent record with state=creating, and starts the shim in a background goroutine.
// Returns AgentCreateResult immediately (state is always "creating").
//
// Observability: INFO on creation; INFO/WARN in goroutine on Start success/failure.
func (s *Server) handleAgentCreate(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var params AgentCreateParams
	if err := unmarshalParams(req, &params); err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}
	if params.Workspace == "" || params.Name == "" || params.RuntimeClass == "" {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams,
			"workspace, name, and runtimeClass are required")
		return
	}

	s.logger.Info("agent/create", "workspace", params.Workspace, "name", params.Name)

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

	// Create agent record in DB.
	agent := &meta.Agent{
		Metadata: meta.ObjectMeta{
			Name:      params.Name,
			Workspace: params.Workspace,
			Labels:    params.Labels,
		},
		Spec: meta.AgentSpec{
			RuntimeClass:  params.RuntimeClass,
			RestartPolicy: params.RestartPolicy,
			SystemPrompt:  params.SystemPrompt,
		},
		Status: meta.AgentStatus{
			State: spec.StatusCreating,
		},
	}
	if err := s.agents.Create(ctx, agent); err != nil {
		var alreadyExists *agentd.ErrAgentAlreadyExists
		if errors.As(err, &alreadyExists) {
			s.replyErr(ctx, conn, req, CodeRecoveryBlocked, err.Error())
			return
		}
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInternalError, err.Error())
		return
	}

	s.logger.Info("agent/create: agent created, starting shim",
		"workspace", params.Workspace, "name", params.Name)

	// Start shim in background.
	wsName := params.Workspace
	agName := params.Name
	go func() {
		bgCtx := context.Background()
		if _, err := s.processes.Start(bgCtx, wsName, agName); err != nil {
			s.logger.Warn("agent/create: shim start failed",
				"workspace", wsName, "name", agName, "error", err)
			_ = s.agents.UpdateStatus(bgCtx, wsName, agName, meta.AgentStatus{
				State:        spec.StatusError,
				ErrorMessage: err.Error(),
			})
		} else {
			s.logger.Info("agent/create: shim started",
				"workspace", wsName, "name", agName)
		}
	}()

	s.replyOK(ctx, conn, req, AgentCreateResult{
		Workspace: params.Workspace,
		Name:      params.Name,
		State:     string(spec.StatusCreating),
	})
}

// handleAgentPrompt handles the agent/prompt method.
//
// Validates agent state == idle, connects to shim, fires prompt in background.
// Returns AgentPromptResult{Accepted: true} immediately.
//
// Observability: INFO on dispatch; WARN on rejection (bad state, not running).
func (s *Server) handleAgentPrompt(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var params AgentPromptParams
	if err := unmarshalParams(req, &params); err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	s.logger.Info("agent/prompt", "workspace", params.Workspace, "name", params.Name)

	// Recovery guard.
	if s.processes.IsRecovering() {
		s.logger.Warn("agent/prompt: recovery blocked",
			"workspace", params.Workspace, "name", params.Name)
		s.replyErr(ctx, conn, req, CodeRecoveryBlocked, "daemon is recovering agents")
		return
	}

	// Load agent from DB.
	agent, err := s.store.GetAgent(ctx, params.Workspace, params.Name)
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
	if agent.Status.State != spec.StatusIdle {
		s.logger.Warn("agent/prompt: agent not in idle state",
			"workspace", params.Workspace, "name", params.Name, "state", agent.Status.State)
		s.replyErr(ctx, conn, req, CodeRecoveryBlocked,
			fmt.Sprintf("agent not in idle state: %s", agent.Status.State))
		return
	}

	// Connect to shim.
	client, err := s.processes.Connect(ctx, params.Workspace, params.Name)
	if err != nil {
		s.logger.Warn("agent/prompt: agent not running",
			"workspace", params.Workspace, "name", params.Name, "error", err)
		s.replyErr(ctx, conn, req, CodeRecoveryBlocked, "agent not running")
		return
	}

	// Fire-and-forget prompt.
	prompt := params.Prompt
	go func() {
		if _, err := client.Prompt(context.Background(), prompt); err != nil {
			s.logger.Warn("agent/prompt: prompt delivery failed",
				"workspace", params.Workspace, "name", params.Name, "error", err)
		}
	}()

	s.logger.Info("agent/prompt: dispatched",
		"workspace", params.Workspace, "name", params.Name)
	s.replyOK(ctx, conn, req, AgentPromptResult{Accepted: true})
}

// handleAgentCancel handles the agent/cancel method.
//
// Connects to the running shim and calls Cancel. Returns empty result.
func (s *Server) handleAgentCancel(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var params AgentCancelParams
	if err := unmarshalParams(req, &params); err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	s.logger.Info("agent/cancel", "workspace", params.Workspace, "name", params.Name)

	// Load agent.
	agent, err := s.store.GetAgent(ctx, params.Workspace, params.Name)
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

// handleAgentStop handles the agent/stop method.
//
// Calls processes.Stop which sends runtime/stop to the shim and waits.
// Returns empty result.
func (s *Server) handleAgentStop(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var params AgentStopParams
	if err := unmarshalParams(req, &params); err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	s.logger.Info("agent/stop", "workspace", params.Workspace, "name", params.Name)

	if err := s.processes.Stop(ctx, params.Workspace, params.Name); err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInternalError, err.Error())
		return
	}

	s.replyOK(ctx, conn, req, struct{}{})
}

// handleAgentDelete handles the agent/delete method.
//
// Validates agent is in stopped/error state, then deletes from DB.
// Maps ErrDeleteNotStopped → -32001, ErrAgentNotFound → -32602.
func (s *Server) handleAgentDelete(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var params AgentDeleteParams
	if err := unmarshalParams(req, &params); err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	s.logger.Info("agent/delete", "workspace", params.Workspace, "name", params.Name)

	if err := s.agents.Delete(ctx, params.Workspace, params.Name); err != nil {
		var notFound *agentd.ErrAgentNotFound
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

	s.replyOK(ctx, conn, req, struct{}{})
}

// handleAgentRestart handles the agent/restart method.
//
// Validates agent is stopped/error, updates state to creating, starts shim.
// Returns AgentRestartResult immediately (state="creating").
func (s *Server) handleAgentRestart(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var params AgentRestartParams
	if err := unmarshalParams(req, &params); err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	s.logger.Info("agent/restart", "workspace", params.Workspace, "name", params.Name)

	// Load agent.
	agent, err := s.store.GetAgent(ctx, params.Workspace, params.Name)
	if err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInternalError, err.Error())
		return
	}
	if agent == nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("agent %s/%s not found", params.Workspace, params.Name))
		return
	}

	if agent.Status.State != spec.StatusStopped && agent.Status.State != spec.StatusError {
		s.replyErr(ctx, conn, req, CodeRecoveryBlocked,
			fmt.Sprintf("agent not in stopped/error state: %s", agent.Status.State))
		return
	}

	// Transition to creating.
	if err := s.agents.UpdateStatus(ctx, params.Workspace, params.Name, meta.AgentStatus{
		State: spec.StatusCreating,
	}); err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInternalError, err.Error())
		return
	}

	// Start shim in background.
	wsName := params.Workspace
	agName := params.Name
	go func() {
		bgCtx := context.Background()
		if _, err := s.processes.Start(bgCtx, wsName, agName); err != nil {
			s.logger.Warn("agent/restart: shim start failed",
				"workspace", wsName, "name", agName, "error", err)
			_ = s.agents.UpdateStatus(bgCtx, wsName, agName, meta.AgentStatus{
				State:        spec.StatusError,
				ErrorMessage: err.Error(),
			})
		}
	}()

	s.replyOK(ctx, conn, req, AgentRestartResult{
		Workspace: params.Workspace,
		Name:      params.Name,
		State:     string(spec.StatusCreating),
	})
}

// handleAgentList handles the agent/list method.
//
// Returns all agents matching the optional workspace/state filter.
func (s *Server) handleAgentList(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var params AgentListParams
	if req.Params != nil {
		if err := unmarshalParams(req, &params); err != nil {
			s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams, err.Error())
			return
		}
	}

	s.logger.Info("agent/list", "workspace", params.Workspace, "state", params.State)

	filter := &meta.AgentFilter{
		Workspace: params.Workspace,
		State:     spec.Status(params.State),
	}

	agents, err := s.agents.List(ctx, filter)
	if err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInternalError, err.Error())
		return
	}

	infos := make([]AgentInfo, 0, len(agents))
	for _, ag := range agents {
		infos = append(infos, agentToInfo(ag))
	}

	s.replyOK(ctx, conn, req, AgentListResult{Agents: infos})
}

// handleAgentStatus handles the agent/status method.
//
// Returns detailed agent info plus optional shim runtime state.
func (s *Server) handleAgentStatus(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var params AgentStatusParams
	if err := unmarshalParams(req, &params); err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	s.logger.Info("agent/status", "workspace", params.Workspace, "name", params.Name)

	agent, err := s.store.GetAgent(ctx, params.Workspace, params.Name)
	if err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInternalError, err.Error())
		return
	}
	if agent == nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("agent %s/%s not found", params.Workspace, params.Name))
		return
	}

	result := AgentStatusResult{Agent: agentToInfo(agent)}

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

// handleAgentAttach handles the agent/attach method.
//
// Returns the shim's Unix socket path so the caller can connect directly.
// Agent must be in idle or running state.
func (s *Server) handleAgentAttach(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var params AgentAttachParams
	if err := unmarshalParams(req, &params); err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams, err.Error())
		return
	}

	s.logger.Info("agent/attach", "workspace", params.Workspace, "name", params.Name)

	agent, err := s.store.GetAgent(ctx, params.Workspace, params.Name)
	if err != nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInternalError, err.Error())
		return
	}
	if agent == nil {
		s.replyErr(ctx, conn, req, jsonrpc2.CodeInvalidParams,
			fmt.Sprintf("agent %s/%s not found", params.Workspace, params.Name))
		return
	}

	if agent.Status.State != spec.StatusIdle && agent.Status.State != spec.StatusRunning {
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

	s.replyOK(ctx, conn, req, AgentAttachResult{SocketPath: socketPath})
}

// ────────────────────────────────────────────────────────────────────────────
// agent helper
// ────────────────────────────────────────────────────────────────────────────

// agentToInfo converts a meta.Agent to an AgentInfo wire type.
// Note: no agentId field — identity is (workspace, name).
func agentToInfo(ag *meta.Agent) AgentInfo {
	return AgentInfo{
		Workspace:    ag.Metadata.Workspace,
		Name:         ag.Metadata.Name,
		RuntimeClass: ag.Spec.RuntimeClass,
		State:        string(ag.Status.State),
		ErrorMessage: ag.Status.ErrorMessage,
		Labels:       ag.Metadata.Labels,
		CreatedAt:    ag.Metadata.CreatedAt,
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
