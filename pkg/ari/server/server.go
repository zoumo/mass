// Package server implements the ARI JSON-RPC service layer.
//
// Service holds the shared dependencies. Three unexported adapter types
// (workspaceAdapter, agentRunAdapter, agentAdapter) wrap *Service and satisfy
// the apiari.WorkspaceService, apiari.AgentRunService, and apiari.AgentService
// interfaces respectively. Use Register to wire all three with a jsonrpc.Server.
//
// NOTE: A single Go struct cannot implement all three interfaces because
// WorkspaceService.List(ctx) and AgentService.List(ctx) have the same method
// signature but different return types. The adapter pattern keeps shared deps
// in one place while satisfying each interface independently.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/zoumo/oar/api"
	apiari "github.com/zoumo/oar/api/ari"
	"github.com/zoumo/oar/pkg/agentd"
	"github.com/zoumo/oar/pkg/jsonrpc"
	"github.com/zoumo/oar/pkg/store"
	"github.com/zoumo/oar/pkg/workspace"

	ariregistry "github.com/zoumo/oar/pkg/ari"
)

// Service holds shared dependencies for all ARI handlers.
// Use Register to wire it with a jsonrpc.Server.
type Service struct {
	manager   *workspace.WorkspaceManager
	registry  *ariregistry.Registry
	agents    *agentd.AgentRunManager
	processes *agentd.ProcessManager
	store     *store.Store
	baseDir   string
	logger    *slog.Logger
}

// New creates a Service with the provided dependencies.
// Call Register to attach it to a jsonrpc.Server.
func New(
	manager *workspace.WorkspaceManager,
	registry *ariregistry.Registry,
	agents *agentd.AgentRunManager,
	processes *agentd.ProcessManager,
	s *store.Store,
	baseDir string,
	logger *slog.Logger,
) *Service {
	return &Service{
		manager:   manager,
		registry:  registry,
		agents:    agents,
		processes: processes,
		store:     s,
		baseDir:   baseDir,
		logger:    logger.With("component", "ari.server"),
	}
}

// Register wires all three ARI service interfaces with the jsonrpc.Server.
func Register(srv *jsonrpc.Server, svc *Service) {
	apiari.RegisterWorkspaceService(srv, &workspaceAdapter{svc})
	apiari.RegisterAgentRunService(srv, &agentRunAdapter{svc})
	apiari.RegisterAgentService(srv, &agentAdapter{svc})
}

// ────────────────────────────────────────────────────────────────────────────
// Adapter types
// ────────────────────────────────────────────────────────────────────────────

// workspaceAdapter adapts *Service to apiari.WorkspaceService.
type workspaceAdapter struct{ *Service }

// agentRunAdapter adapts *Service to apiari.AgentRunService.
type agentRunAdapter struct{ *Service }

// agentAdapter adapts *Service to apiari.AgentService.
type agentAdapter struct{ *Service }

// ────────────────────────────────────────────────────────────────────────────
// WorkspaceService (workspaceAdapter)
// ────────────────────────────────────────────────────────────────────────────

// Create handles workspace/create.
//
// Creates the workspace record with phase=pending, returns immediately, then
// prepares the workspace directory asynchronously.
//
// Observability: INFO on entry (name, phase:pending); INFO/WARN in goroutine
// on prepare success (phase:ready, path) or failure (phase:error).
func (a *workspaceAdapter) Create(ctx context.Context, req *apiari.WorkspaceCreateParams) (*apiari.WorkspaceCreateResult, error) {
	if req.Name == "" {
		return nil, jsonrpc.ErrInvalidParams("name is required")
	}

	a.logger.Info("workspace/create", "workspace", req.Name, "phase", "pending")

	ws := &apiari.Workspace{
		Metadata: apiari.ObjectMeta{
			Name:   req.Name,
			Labels: req.Labels,
		},
		Spec: apiari.WorkspaceSpec{
			Source: req.Source,
		},
		Status: apiari.WorkspaceStatus{
			Phase: apiari.WorkspacePhasePending,
		},
	}
	if err := a.store.CreateWorkspace(ctx, ws); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return nil, jsonrpc.ErrInvalidParams(fmt.Sprintf("workspace %s already exists", req.Name))
		}
		return nil, jsonrpc.ErrInternal(err.Error())
	}

	result := &apiari.WorkspaceCreateResult{Workspace: ws.ARIView()}

	// Parse source for the Prepare call.
	var src workspace.Source
	if len(req.Source) > 0 {
		if err := json.Unmarshal(req.Source, &src); err != nil {
			a.logger.Warn("workspace/create: invalid source JSON",
				"workspace", req.Name, "error", err)
		}
	}

	wsSpec := workspace.WorkspaceSpec{
		OarVersion: "0.1.0",
		Metadata:   workspace.WorkspaceMetadata{Name: req.Name},
		Source:     src,
	}
	targetDir := filepath.Join(a.baseDir, "workspaces", req.Name)
	wsName := req.Name

	go func() {
		prepareCtx := context.Background()
		path, err := a.manager.Prepare(prepareCtx, wsSpec, targetDir)
		if err != nil {
			a.logger.Warn("workspace/create: prepare failed",
				"workspace", wsName, "phase", "error", "error", err)
			_ = a.store.UpdateWorkspaceStatus(prepareCtx, wsName, apiari.WorkspaceStatus{
				Phase: apiari.WorkspacePhaseError,
			})
			return
		}
		a.logger.Info("workspace/create: prepared",
			"workspace", wsName, "phase", "ready", "path", path)
		_ = a.store.UpdateWorkspaceStatus(prepareCtx, wsName, apiari.WorkspaceStatus{
			Phase: apiari.WorkspacePhaseReady,
			Path:  path,
		})
		a.registry.Add(wsName, wsName, path, wsSpec)
	}()

	return result, nil
}

// Status handles workspace/status.
//
// Fast path: in-memory registry for ready workspaces. Fallback: DB for
// workspaces still in pending/error phase.
func (a *workspaceAdapter) Status(ctx context.Context, req *apiari.WorkspaceStatusParams) (*apiari.WorkspaceStatusResult, error) {
	a.logger.Info("workspace/status", "workspace", req.Name)

	if wm := a.registry.Get(req.Name); wm != nil {
		members := a.listWorkspaceMembers(ctx, req.Name)
		wsObj := apiari.Workspace{
			Metadata: apiari.ObjectMeta{Name: wm.Name},
			Status:   apiari.WorkspaceStatus{Phase: apiari.WorkspacePhase(wm.Status), Path: wm.Path},
		}
		return &apiari.WorkspaceStatusResult{
			Workspace: wsObj.ARIView(),
			Members:   members,
		}, nil
	}

	ws, err := a.store.GetWorkspace(ctx, req.Name)
	if err != nil {
		return nil, jsonrpc.ErrInternal(err.Error())
	}
	if ws == nil {
		return nil, jsonrpc.ErrInvalidParams(fmt.Sprintf("workspace %s not found", req.Name))
	}

	members := a.listWorkspaceMembers(ctx, req.Name)
	return &apiari.WorkspaceStatusResult{
		Workspace: ws.ARIView(),
		Members:   members,
	}, nil
}

// List handles workspace/list.
//
// Returns all workspaces in the in-memory registry (ready workspaces only).
func (a *workspaceAdapter) List(ctx context.Context) (*apiari.WorkspaceListResult, error) {
	a.logger.Info("workspace/list")

	metas := a.registry.List()
	workspaces := make([]apiari.Workspace, 0, len(metas))
	for _, m := range metas {
		workspaces = append(workspaces, apiari.Workspace{
			Metadata: apiari.ObjectMeta{Name: m.Name},
			Status:   apiari.WorkspaceStatus{Phase: apiari.WorkspacePhase(m.Status), Path: m.Path},
		})
	}
	return &apiari.WorkspaceListResult{Workspaces: workspaces}, nil
}

// Delete handles workspace/delete.
//
// Rejects deletion if the workspace has active agent runs.
func (a *workspaceAdapter) Delete(ctx context.Context, req *apiari.WorkspaceDeleteParams) error {
	a.logger.Info("workspace/delete", "workspace", req.Name)

	if err := a.store.DeleteWorkspace(ctx, req.Name); err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			return jsonrpc.ErrInvalidParams(err.Error())
		}
		return &jsonrpc.RPCError{Code: apiari.CodeRecoveryBlocked, Message: err.Error()}
	}
	a.registry.Remove(req.Name)
	return nil
}

// Send handles workspace/send.
//
// Routes a message from one agent run to another within a workspace via a
// fire-and-forget ShimClient.Prompt call.
//
// Observability: INFO on dispatch (workspace, from, to); WARN on each
// rejection path with reason.
func (a *workspaceAdapter) Send(ctx context.Context, req *apiari.WorkspaceSendParams) (*apiari.WorkspaceSendResult, error) {
	if req.Workspace == "" || req.From == "" || req.To == "" || req.Message == "" {
		return nil, jsonrpc.ErrInvalidParams("workspace, from, to, and message are required")
	}

	a.logger.Info("workspace/send",
		"workspace", req.Workspace, "from", req.From, "to", req.To)

	if a.processes.IsRecovering() {
		a.logger.Warn("workspace/send: recovery blocked",
			"workspace", req.Workspace, "to", req.To)
		return nil, &jsonrpc.RPCError{Code: apiari.CodeRecoveryBlocked, Message: "daemon is recovering agents"}
	}

	agent, err := a.store.GetAgentRun(ctx, req.Workspace, req.To)
	if err != nil {
		return nil, jsonrpc.ErrInternal(err.Error())
	}
	if agent == nil {
		a.logger.Warn("workspace/send: target agent not found",
			"workspace", req.Workspace, "to", req.To)
		return nil, jsonrpc.ErrInvalidParams(fmt.Sprintf("agent %s/%s not found", req.Workspace, req.To))
	}
	if agent.Status.State == api.StatusError {
		a.logger.Warn("workspace/send: target agent in error state",
			"workspace", req.Workspace, "to", req.To)
		return nil, &jsonrpc.RPCError{Code: apiari.CodeRecoveryBlocked, Message: "target agent is in error state"}
	}
	if agent.Status.State != api.StatusIdle {
		a.logger.Warn("workspace/send: target agent not idle",
			"workspace", req.Workspace, "to", req.To, "state", agent.Status.State)
		return nil, &jsonrpc.RPCError{
			Code:    apiari.CodeRecoveryBlocked,
			Message: fmt.Sprintf("target agent not in idle state: %s", agent.Status.State),
		}
	}

	reserved, err := a.agents.TransitionState(ctx, req.Workspace, req.To, api.StatusIdle, api.StatusRunning)
	if err != nil {
		return nil, jsonrpc.ErrInternal(err.Error())
	}
	if !reserved {
		current, getErr := a.store.GetAgentRun(ctx, req.Workspace, req.To)
		if getErr != nil {
			return nil, jsonrpc.ErrInternal(getErr.Error())
		}
		state := "<missing>"
		if current != nil {
			state = string(current.Status.State)
		}
		return nil, &jsonrpc.RPCError{
			Code:    apiari.CodeRecoveryBlocked,
			Message: fmt.Sprintf("target agent not in idle state: %s", state),
		}
	}

	client, err := a.processes.Connect(ctx, req.Workspace, req.To)
	if err != nil {
		a.logger.Warn("workspace/send: target agent not running",
			"workspace", req.Workspace, "to", req.To, "error", err)
		a.recordPromptDeliveryFailure(req.Workspace, req.To, agent.Status, err, true)
		return nil, &jsonrpc.RPCError{Code: apiari.CodeRecoveryBlocked, Message: "target agent is not running"}
	}

	msg := buildWorkspaceEnvelope(*req) + req.Message
	go func() {
		if _, err := client.Prompt(context.Background(), msg); err != nil {
			a.logger.Warn("workspace/send: prompt delivery failed",
				"workspace", req.Workspace, "to", req.To, "error", err)
			a.recordPromptDeliveryFailure(req.Workspace, req.To, agent.Status, err, false)
		}
	}()

	return &apiari.WorkspaceSendResult{Delivered: true}, nil
}

// ────────────────────────────────────────────────────────────────────────────
// AgentRunService (agentRunAdapter)
// ────────────────────────────────────────────────────────────────────────────

// Create handles agentrun/create.
//
// Validates workspace/name/agent, checks workspace phase, creates the agent
// run record with state=creating, and starts the shim in the background.
// Returns immediately with state="creating".
//
// Observability: INFO on creation; INFO/WARN in goroutine on Start success/failure.
func (a *agentRunAdapter) Create(ctx context.Context, req *apiari.AgentRunCreateParams) (*apiari.AgentRunCreateResult, error) {
	if req.Workspace == "" || req.Name == "" || req.Agent == "" {
		return nil, jsonrpc.ErrInvalidParams("workspace, name, and agent are required")
	}

	if err := a.processes.ValidateAgentSocketPath(req.Workspace, req.Name); err != nil {
		return nil, jsonrpc.ErrInvalidParams(err.Error())
	}

	switch req.RestartPolicy {
	case "", apiari.RestartPolicyTryReload, apiari.RestartPolicyAlwaysNew:
		// valid
	default:
		return nil, jsonrpc.ErrInvalidParams(
			fmt.Sprintf("invalid restartPolicy %q: must be one of \"try_reload\", \"always_new\"", req.RestartPolicy))
	}

	a.logger.Info("agentrun/create", "workspace", req.Workspace, "name", req.Name)

	ws, err := a.store.GetWorkspace(ctx, req.Workspace)
	if err != nil {
		return nil, jsonrpc.ErrInternal(err.Error())
	}
	if ws == nil {
		return nil, jsonrpc.ErrInvalidParams(fmt.Sprintf("workspace %s not found", req.Workspace))
	}
	if ws.Status.Phase != apiari.WorkspacePhaseReady {
		return nil, &jsonrpc.RPCError{
			Code:    apiari.CodeRecoveryBlocked,
			Message: fmt.Sprintf("workspace %s is not ready (phase=%s)", req.Workspace, ws.Status.Phase),
		}
	}

	agentRun := &apiari.AgentRun{
		Metadata: apiari.ObjectMeta{
			Name:      req.Name,
			Workspace: req.Workspace,
			Labels:    req.Labels,
		},
		Spec: apiari.AgentRunSpec{
			Agent:         req.Agent,
			RestartPolicy: req.RestartPolicy,
			SystemPrompt:  req.SystemPrompt,
		},
		Status: apiari.AgentRunStatus{
			State: api.StatusCreating,
		},
	}
	if err := a.agents.Create(ctx, agentRun); err != nil {
		var alreadyExists *agentd.ErrAgentRunAlreadyExists
		if errors.As(err, &alreadyExists) {
			return nil, &jsonrpc.RPCError{Code: apiari.CodeRecoveryBlocked, Message: err.Error()}
		}
		return nil, jsonrpc.ErrInternal(err.Error())
	}

	a.logger.Info("agentrun/create: agent run created, starting shim",
		"workspace", req.Workspace, "name", req.Name)

	wsName := req.Workspace
	agName := req.Name
	go func() {
		bgCtx := context.Background()
		if _, err := a.processes.Start(bgCtx, wsName, agName); err != nil {
			a.logger.Warn("agentrun/create: shim start failed",
				"workspace", wsName, "name", agName, "error", err)
			_ = a.agents.UpdateStatus(bgCtx, wsName, agName, apiari.AgentRunStatus{
				State:        api.StatusError,
				ErrorMessage: err.Error(),
			})
		} else {
			a.logger.Info("agentrun/create: shim started",
				"workspace", wsName, "name", agName)
		}
	}()

	return &apiari.AgentRunCreateResult{AgentRun: agentRun.ARIView()}, nil
}

// Prompt handles agentrun/prompt.
//
// Validates agent run state == idle, transitions to running, connects to shim,
// and fires the prompt in the background. Returns Accepted:true immediately.
//
// Observability: INFO on dispatch; WARN on rejection (bad state, not running).
func (a *agentRunAdapter) Prompt(ctx context.Context, req *apiari.AgentRunPromptParams) (*apiari.AgentRunPromptResult, error) {
	if req.Workspace == "" || req.Name == "" || req.Prompt == "" {
		return nil, jsonrpc.ErrInvalidParams("workspace, name, and prompt are required")
	}

	a.logger.Info("agentrun/prompt", "workspace", req.Workspace, "name", req.Name)

	if a.processes.IsRecovering() {
		a.logger.Warn("agentrun/prompt: recovery blocked",
			"workspace", req.Workspace, "name", req.Name)
		return nil, &jsonrpc.RPCError{Code: apiari.CodeRecoveryBlocked, Message: "daemon is recovering agents"}
	}

	agent, err := a.store.GetAgentRun(ctx, req.Workspace, req.Name)
	if err != nil {
		return nil, jsonrpc.ErrInternal(err.Error())
	}
	if agent == nil {
		return nil, jsonrpc.ErrInvalidParams(fmt.Sprintf("agent %s/%s not found", req.Workspace, req.Name))
	}

	if agent.Status.State != api.StatusIdle {
		a.logger.Warn("agentrun/prompt: agent not in idle state",
			"workspace", req.Workspace, "name", req.Name, "state", agent.Status.State)
		return nil, &jsonrpc.RPCError{
			Code:    apiari.CodeRecoveryBlocked,
			Message: fmt.Sprintf("agent not in idle state: %s", agent.Status.State),
		}
	}

	// Reserve the agent run before acknowledging the prompt so a second
	// prompt/stop/delete cannot observe stale idle while delivery is queued.
	reserved, err := a.agents.TransitionState(ctx, req.Workspace, req.Name, api.StatusIdle, api.StatusRunning)
	if err != nil {
		return nil, jsonrpc.ErrInternal(err.Error())
	}
	if !reserved {
		current, getErr := a.store.GetAgentRun(ctx, req.Workspace, req.Name)
		if getErr != nil {
			return nil, jsonrpc.ErrInternal(getErr.Error())
		}
		state := "<missing>"
		if current != nil {
			state = string(current.Status.State)
		}
		return nil, &jsonrpc.RPCError{
			Code:    apiari.CodeRecoveryBlocked,
			Message: fmt.Sprintf("agent not in idle state: %s", state),
		}
	}

	client, err := a.processes.Connect(ctx, req.Workspace, req.Name)
	if err != nil {
		a.logger.Warn("agentrun/prompt: agent not running",
			"workspace", req.Workspace, "name", req.Name, "error", err)
		a.recordPromptDeliveryFailure(req.Workspace, req.Name, agent.Status, err, true)
		return nil, &jsonrpc.RPCError{Code: apiari.CodeRecoveryBlocked, Message: "agent not running"}
	}

	prompt := req.Prompt
	go func() {
		if _, err := client.Prompt(context.Background(), prompt); err != nil {
			a.logger.Warn("agentrun/prompt: prompt delivery failed",
				"workspace", req.Workspace, "name", req.Name, "error", err)
			a.recordPromptDeliveryFailure(req.Workspace, req.Name, agent.Status, err, false)
		}
	}()

	a.logger.Info("agentrun/prompt: dispatched",
		"workspace", req.Workspace, "name", req.Name)
	return &apiari.AgentRunPromptResult{Accepted: true}, nil
}

// Cancel handles agentrun/cancel.
//
// Connects to the running shim and calls Cancel.
func (a *agentRunAdapter) Cancel(ctx context.Context, req *apiari.AgentRunCancelParams) error {
	a.logger.Info("agentrun/cancel", "workspace", req.Workspace, "name", req.Name)

	agent, err := a.store.GetAgentRun(ctx, req.Workspace, req.Name)
	if err != nil {
		return jsonrpc.ErrInternal(err.Error())
	}
	if agent == nil {
		return jsonrpc.ErrInvalidParams(fmt.Sprintf("agent %s/%s not found", req.Workspace, req.Name))
	}

	client, err := a.processes.Connect(ctx, req.Workspace, req.Name)
	if err != nil {
		return &jsonrpc.RPCError{Code: apiari.CodeRecoveryBlocked, Message: "agent not running"}
	}

	if err := client.Cancel(ctx); err != nil {
		return jsonrpc.ErrInternal(err.Error())
	}
	return nil
}

// Stop handles agentrun/stop.
//
// Calls processes.Stop which sends runtime/stop to the shim and waits.
func (a *agentRunAdapter) Stop(ctx context.Context, req *apiari.AgentRunStopParams) error {
	a.logger.Info("agentrun/stop", "workspace", req.Workspace, "name", req.Name)

	if err := a.processes.Stop(ctx, req.Workspace, req.Name); err != nil {
		return jsonrpc.ErrInternal(err.Error())
	}
	return nil
}

// Delete handles agentrun/delete.
//
// Validates agent run is in stopped/error state then deletes from DB.
// Maps ErrDeleteNotStopped → -32001, ErrAgentRunNotFound → -32602.
func (a *agentRunAdapter) Delete(ctx context.Context, req *apiari.AgentRunDeleteParams) error {
	a.logger.Info("agentrun/delete", "workspace", req.Workspace, "name", req.Name)

	if err := a.agents.Delete(ctx, req.Workspace, req.Name); err != nil {
		var notFound *agentd.ErrAgentRunNotFound
		if errors.As(err, &notFound) {
			return jsonrpc.ErrInvalidParams(err.Error())
		}
		var notStopped *agentd.ErrDeleteNotStopped
		if errors.As(err, &notStopped) {
			return &jsonrpc.RPCError{Code: apiari.CodeRecoveryBlocked, Message: err.Error()}
		}
		return jsonrpc.ErrInternal(err.Error())
	}

	// Clean up bundle directory (best effort; DB record already deleted).
	bundlePath := a.processes.BundlePath(req.Workspace, req.Name)
	if err := os.RemoveAll(bundlePath); err != nil {
		a.logger.Warn("agentrun/delete: failed to remove bundle",
			"workspace", req.Workspace, "name", req.Name,
			"bundle", bundlePath, "error", err)
	}
	return nil
}

// Restart handles agentrun/restart.
//
// Accepts any agent state. Stops the existing shim (if running) then starts a
// new one. Returns immediately with state="creating".
func (a *agentRunAdapter) Restart(ctx context.Context, req *apiari.AgentRunRestartParams) (*apiari.AgentRunRestartResult, error) {
	a.logger.Info("agentrun/restart", "workspace", req.Workspace, "name", req.Name)

	agent, err := a.store.GetAgentRun(ctx, req.Workspace, req.Name)
	if err != nil {
		return nil, jsonrpc.ErrInternal(err.Error())
	}
	if agent == nil {
		return nil, jsonrpc.ErrInvalidParams(fmt.Sprintf("agent %s/%s not found", req.Workspace, req.Name))
	}

	// Agents in terminal states have no active shim.
	needsStop := agent.Status.State != api.StatusStopped && agent.Status.State != api.StatusError

	if err := a.agents.UpdateStatus(ctx, req.Workspace, req.Name, apiari.AgentRunStatus{
		State: api.StatusCreating,
	}); err != nil {
		return nil, jsonrpc.ErrInternal(err.Error())
	}

	wsName := req.Workspace
	agName := req.Name
	go func() {
		bgCtx := context.Background()
		if needsStop {
			if err := a.processes.Stop(bgCtx, wsName, agName); err != nil {
				a.logger.Warn("agentrun/restart: pre-stop failed, continuing",
					"workspace", wsName, "name", agName, "error", err)
			}
			// Stop() transitions state to "stopped"; re-set to "creating" for Start().
			if err := a.agents.UpdateStatus(bgCtx, wsName, agName, apiari.AgentRunStatus{
				State: api.StatusCreating,
			}); err != nil {
				a.logger.Warn("agentrun/restart: failed to re-transition to creating",
					"workspace", wsName, "name", agName, "error", err)
				return
			}
		}
		if _, err := a.processes.Start(bgCtx, wsName, agName); err != nil {
			a.logger.Warn("agentrun/restart: shim start failed",
				"workspace", wsName, "name", agName, "error", err)
			_ = a.agents.UpdateStatus(bgCtx, wsName, agName, apiari.AgentRunStatus{
				State:        api.StatusError,
				ErrorMessage: err.Error(),
			})
		}
	}()

	// Read back updated agent state for the response.
	agentUpdated, err2 := a.store.GetAgentRun(ctx, req.Workspace, req.Name)
	if err2 != nil || agentUpdated == nil {
		agentUpdated = &apiari.AgentRun{
			Metadata: apiari.ObjectMeta{Workspace: req.Workspace, Name: req.Name},
			Status:   apiari.AgentRunStatus{State: api.StatusCreating},
		}
	}
	return &apiari.AgentRunRestartResult{AgentRun: agentUpdated.ARIView()}, nil
}

// List handles agentrun/list.
//
// Returns all agent runs matching the optional workspace/state filter.
func (a *agentRunAdapter) List(ctx context.Context, req *apiari.AgentRunListParams) (*apiari.AgentRunListResult, error) {
	a.logger.Info("agentrun/list", "workspace", req.Workspace, "state", req.State)

	filter := &apiari.AgentRunFilter{
		Workspace: req.Workspace,
		State:     api.Status(req.State),
	}
	agentRuns, err := a.agents.List(ctx, filter)
	if err != nil {
		return nil, jsonrpc.ErrInternal(err.Error())
	}

	runs := make([]apiari.AgentRun, 0, len(agentRuns))
	for _, ag := range agentRuns {
		runs = append(runs, ag.ARIView())
	}
	return &apiari.AgentRunListResult{AgentRuns: runs}, nil
}

// Status handles agentrun/status.
//
// Returns detailed agent run info plus optional shim runtime state.
func (a *agentRunAdapter) Status(ctx context.Context, req *apiari.AgentRunStatusParams) (*apiari.AgentRunStatusResult, error) {
	a.logger.Info("agentrun/status", "workspace", req.Workspace, "name", req.Name)

	agent, err := a.store.GetAgentRun(ctx, req.Workspace, req.Name)
	if err != nil {
		return nil, jsonrpc.ErrInternal(err.Error())
	}
	if agent == nil {
		return nil, jsonrpc.ErrInvalidParams(fmt.Sprintf("agent %s/%s not found", req.Workspace, req.Name))
	}

	result := &apiari.AgentRunStatusResult{AgentRun: agent.ARIView()}

	// Best-effort: fetch shim runtime state if available.
	if rts, err := a.processes.RuntimeStatus(ctx, req.Workspace, req.Name); err == nil {
		st := rts.State
		result.ShimState = &apiari.ShimStateInfo{
			Status: string(st.Status),
			PID:    agent.Status.ShimPID,
			Bundle: st.Bundle,
		}
	}
	return result, nil
}

// Attach handles agentrun/attach.
//
// Returns the shim's Unix socket path so the caller can connect directly.
// Agent run must be in idle or running state.
func (a *agentRunAdapter) Attach(ctx context.Context, req *apiari.AgentRunAttachParams) (*apiari.AgentRunAttachResult, error) {
	a.logger.Info("agentrun/attach", "workspace", req.Workspace, "name", req.Name)

	agent, err := a.store.GetAgentRun(ctx, req.Workspace, req.Name)
	if err != nil {
		return nil, jsonrpc.ErrInternal(err.Error())
	}
	if agent == nil {
		return nil, jsonrpc.ErrInvalidParams(fmt.Sprintf("agent %s/%s not found", req.Workspace, req.Name))
	}
	if agent.Status.State != api.StatusIdle && agent.Status.State != api.StatusRunning {
		return nil, &jsonrpc.RPCError{
			Code:    apiari.CodeRecoveryBlocked,
			Message: fmt.Sprintf("agent not in idle/running state: %s", agent.Status.State),
		}
	}

	// Try in-memory process map first; fall back to DB-stored socket path.
	socketPath := agent.Status.ShimSocketPath
	if p := a.processes.GetProcess(req.Workspace + "/" + req.Name); p != nil {
		socketPath = p.SocketPath
	}

	return &apiari.AgentRunAttachResult{SocketPath: socketPath}, nil
}

// ────────────────────────────────────────────────────────────────────────────
// AgentService (agentAdapter)
// ────────────────────────────────────────────────────────────────────────────

// Set handles agent/set.
//
// Creates or updates an agent definition in the metadata store.
//
// Observability: INFO on success with name and command logged.
func (a *agentAdapter) Set(ctx context.Context, req *apiari.AgentSetParams) (*apiari.AgentSetResult, error) {
	if req.Name == "" {
		return nil, jsonrpc.ErrInvalidParams("name is required")
	}
	if req.Command == "" {
		return nil, jsonrpc.ErrInvalidParams("command is required")
	}

	a.logger.Info("agent/set", "name", req.Name, "command", req.Command)

	ag := &apiari.Agent{
		Metadata: apiari.ObjectMeta{Name: req.Name},
		Spec: apiari.AgentSpec{
			Command:               req.Command,
			Args:                  req.Args,
			Env:                   req.Env,
			StartupTimeoutSeconds: req.StartupTimeoutSeconds,
		},
	}
	if err := a.store.SetAgent(ctx, ag); err != nil {
		return nil, jsonrpc.ErrInternal(err.Error())
	}

	// Read back to get server-assigned timestamps.
	stored, err := a.store.GetAgent(ctx, req.Name)
	if err != nil || stored == nil {
		return &apiari.AgentSetResult{Agent: *ag}, nil
	}
	return &apiari.AgentSetResult{Agent: *stored}, nil
}

// Get handles agent/get.
//
// Returns the agent definition info or InvalidParams if not found.
func (a *agentAdapter) Get(ctx context.Context, req *apiari.AgentGetParams) (*apiari.AgentGetResult, error) {
	if req.Name == "" {
		return nil, jsonrpc.ErrInvalidParams("name is required")
	}

	a.logger.Info("agent/get", "name", req.Name)

	ag, err := a.store.GetAgent(ctx, req.Name)
	if err != nil {
		return nil, jsonrpc.ErrInternal(err.Error())
	}
	if ag == nil {
		return nil, jsonrpc.ErrInvalidParams(fmt.Sprintf("agent %s not found", req.Name))
	}
	return &apiari.AgentGetResult{Agent: *ag}, nil
}

// List handles agent/list.
//
// Returns all agent definition objects stored in the metadata DB.
func (a *agentAdapter) List(ctx context.Context) (*apiari.AgentListResult, error) {
	a.logger.Info("agent/list")

	ags, err := a.store.ListAgents(ctx)
	if err != nil {
		return nil, jsonrpc.ErrInternal(err.Error())
	}

	agents := make([]apiari.Agent, 0, len(ags))
	for _, ag := range ags {
		agents = append(agents, *ag)
	}
	return &apiari.AgentListResult{Agents: agents}, nil
}

// Delete handles agent/delete.
// No-op if the agent definition does not exist.
//
// Observability: INFO on delete with name logged.
func (a *agentAdapter) Delete(ctx context.Context, req *apiari.AgentDeleteParams) error {
	if req.Name == "" {
		return jsonrpc.ErrInvalidParams("name is required")
	}

	a.logger.Info("agent/delete", "name", req.Name)

	if err := a.store.DeleteAgent(ctx, req.Name); err != nil {
		return jsonrpc.ErrInternal(err.Error())
	}
	return nil
}

// ────────────────────────────────────────────────────────────────────────────
// Shared helpers (on *Service so both adapter families can call them)
// ────────────────────────────────────────────────────────────────────────────

// listWorkspaceMembers returns all AgentRun domain objects for the given workspace.
// Returns nil (not an error) if the query fails.
func (s *Service) listWorkspaceMembers(ctx context.Context, wsName string) []apiari.AgentRun {
	agentRuns, err := s.agents.List(ctx, &apiari.AgentRunFilter{Workspace: wsName})
	if err != nil {
		s.logger.Error("listWorkspaceMembers: list agent runs failed",
			"workspace", wsName, "err", err)
		return nil
	}
	runs := make([]apiari.AgentRun, 0, len(agentRuns))
	for _, ag := range agentRuns {
		runs = append(runs, ag.ARIView())
	}
	return runs
}

// recordPromptDeliveryFailure updates agent run status after a failed prompt
// delivery, consulting the runtime status to get the authoritative state.
func (s *Service) recordPromptDeliveryFailure(wsName, name string, fallback apiari.AgentRunStatus, cause error, markErrorWhenRuntimeUnavailable bool) {
	ctx := context.Background()
	current, err := s.store.GetAgentRun(ctx, wsName, name)
	if err != nil {
		s.logger.Warn("prompt failure: current state lookup failed",
			"workspace", wsName, "name", name, "error", err)
	}
	if current != nil && current.Status.State == api.StatusStopped {
		s.logger.Info("prompt failure: ignored after stop",
			"workspace", wsName, "name", name, "error", cause)
		return
	}

	if rts, statusErr := s.processes.RuntimeStatus(ctx, wsName, name); statusErr == nil {
		status := fallback
		if current != nil {
			status = current.Status
		}
		status.State = rts.State.Status
		status.ErrorMessage = cause.Error()
		_ = s.agents.UpdateStatus(ctx, wsName, name, status)
		return
	}
	if !markErrorWhenRuntimeUnavailable {
		s.logger.Info("prompt failure: runtime unavailable, leaving terminal state to process watcher",
			"workspace", wsName, "name", name, "error", cause)
		return
	}

	current, err = s.store.GetAgentRun(ctx, wsName, name)
	if err != nil {
		s.logger.Warn("prompt failure: current state lookup failed",
			"workspace", wsName, "name", name, "error", err)
	}
	if current != nil && current.Status.State == api.StatusStopped {
		s.logger.Info("prompt failure: ignored after stop",
			"workspace", wsName, "name", name, "error", cause)
		return
	}

	_ = s.agents.UpdateStatus(ctx, wsName, name, apiari.AgentRunStatus{
		State:          api.StatusError,
		ShimSocketPath: fallback.ShimSocketPath,
		ShimStateDir:   fallback.ShimStateDir,
		ShimPID:        fallback.ShimPID,
		ErrorMessage:   cause.Error(),
	})
}

// buildWorkspaceEnvelope constructs the envelope header prepended to every
// workspace message before delivery. It encodes sender identity and, when
// NeedsReply is set, the reply-to address so the receiving agent knows it is
// expected to respond.
func buildWorkspaceEnvelope(p apiari.WorkspaceSendParams) string {
	if p.NeedsReply {
		return "[workspace-message from=" + p.From + " reply-to=" + p.From + " reply-requested=true]\n\n"
	}
	return "[workspace-message from=" + p.From + "]\n\n"
}
