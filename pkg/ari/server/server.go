// Package server implements the ARI JSON-RPC service layer.
//
// Service holds the shared dependencies. Three unexported adapter types
// (workspaceAdapter, agentRunAdapter, agentAdapter) wrap *Service and satisfy
// the WorkspaceService, AgentRunService, and AgentService interfaces
// respectively. Use Register to wire all three with a jsonrpc.Server.
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

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	"github.com/zoumo/mass/pkg/agentd"
	"github.com/zoumo/mass/pkg/jsonrpc"
	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
	apishim "github.com/zoumo/mass/pkg/shim/api"
	"github.com/zoumo/mass/pkg/agentd/store"
	"github.com/zoumo/mass/pkg/workspace"
)

// Service holds shared dependencies for all ARI handlers.
// Use Register to wire it with a jsonrpc.Server.
type Service struct {
	manager   *workspace.WorkspaceManager
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
	agents *agentd.AgentRunManager,
	processes *agentd.ProcessManager,
	s *store.Store,
	baseDir string,
	logger *slog.Logger,
) *Service {
	return &Service{
		manager:   manager,
		agents:    agents,
		processes: processes,
		store:     s,
		baseDir:   baseDir,
		logger:    logger.With("component", "ari.server"),
	}
}

// Register wires all three ARI service interfaces with the jsonrpc.Server.
func Register(srv *jsonrpc.Server, svc *Service) {
	RegisterWorkspaceService(srv, &workspaceAdapter{svc})
	RegisterAgentRunService(srv, &agentRunAdapter{svc})
	RegisterAgentService(srv, &agentAdapter{svc})
}

// copyVal returns a pointer to a shallow copy of v.
func copyVal[T any](v T) *T { return &v }

// ────────────────────────────────────────────────────────────────────────────
// Adapter types
// ────────────────────────────────────────────────────────────────────────────

// workspaceAdapter adapts *Service to WorkspaceService.
type workspaceAdapter struct{ *Service }

// agentRunAdapter adapts *Service to AgentRunService.
type agentRunAdapter struct{ *Service }

// agentAdapter adapts *Service to AgentService.
type agentAdapter struct{ *Service }

// ────────────────────────────────────────────────────────────────────────────
// WorkspaceService (workspaceAdapter)
// ────────────────────────────────────────────────────────────────────────────

// Create handles workspace/create.
//
// Creates the workspace record with phase=pending, returns immediately, then
// prepares the workspace directory asynchronously.
func (a *workspaceAdapter) Create(ctx context.Context, ws *pkgariapi.Workspace) (*pkgariapi.Workspace, error) {
	if ws.Metadata.Name == "" {
		return nil, jsonrpc.ErrInvalidParams("name is required")
	}

	a.logger.Info("workspace/create", "workspace", ws.Metadata.Name, "phase", "pending")

	ws.Status.Phase = pkgariapi.WorkspacePhasePending
	if err := a.store.CreateWorkspace(ctx, ws); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return nil, jsonrpc.ErrInvalidParams(fmt.Sprintf("workspace %s already exists", ws.Metadata.Name))
		}
		return nil, jsonrpc.ErrInternal(err.Error())
	}

	result := copyVal(ws.ARIView())

	// Parse source for the Prepare call.
	var src workspace.Source
	if len(ws.Spec.Source) > 0 {
		if err := json.Unmarshal(ws.Spec.Source, &src); err != nil {
			a.logger.Warn("workspace/create: invalid source JSON",
				"workspace", ws.Metadata.Name, "error", err)
		}
	}

	wsSpec := workspace.WorkspaceSpec{
		MassVersion: "0.1.0",
		Metadata:    workspace.WorkspaceMetadata{Name: ws.Metadata.Name},
		Source:      src,
	}
	targetDir := filepath.Join(a.baseDir, "workspaces", ws.Metadata.Name)
	wsName := ws.Metadata.Name

	go func() {
		prepareCtx := context.Background()
		path, err := a.manager.Prepare(prepareCtx, wsSpec, targetDir)
		if err != nil {
			a.logger.Warn("workspace/create: prepare failed",
				"workspace", wsName, "phase", "error", "error", err)
			_ = a.store.UpdateWorkspaceStatus(prepareCtx, wsName, pkgariapi.WorkspaceStatus{
				Phase: pkgariapi.WorkspacePhaseError,
			})
			return
		}
		a.logger.Info("workspace/create: prepared",
			"workspace", wsName, "phase", "ready", "path", path)
		_ = a.store.UpdateWorkspaceStatus(prepareCtx, wsName, pkgariapi.WorkspaceStatus{
			Phase: pkgariapi.WorkspacePhaseReady,
			Path:  path,
		})
	}()

	return result, nil
}

// Get handles workspace/get.
func (a *workspaceAdapter) Get(ctx context.Context, name string) (*pkgariapi.Workspace, error) {
	a.logger.Info("workspace/get", "workspace", name)

	ws, err := a.store.GetWorkspace(ctx, name)
	if err != nil {
		return nil, jsonrpc.ErrInternal(err.Error())
	}
	if ws == nil {
		return nil, jsonrpc.ErrInvalidParams(fmt.Sprintf("workspace %s not found", name))
	}

	result := ws.ARIView()
	return &result, nil
}

// List handles workspace/list.
//
// Returns workspaces matching the optional field selector filter.
func (a *workspaceAdapter) List(ctx context.Context, opts pkgariapi.ListOptions) (*pkgariapi.WorkspaceList, error) {
	a.logger.Info("workspace/list")

	var filter *pkgariapi.WorkspaceFilter
	if phase := opts.FieldSelector["phase"]; phase != "" {
		filter = &pkgariapi.WorkspaceFilter{Phase: pkgariapi.WorkspacePhase(phase)}
	}

	all, err := a.store.ListWorkspaces(ctx, filter)
	if err != nil {
		return nil, jsonrpc.ErrInternal(err.Error())
	}
	items := make([]pkgariapi.Workspace, 0, len(all))
	for _, ws := range all {
		items = append(items, ws.ARIView())
	}
	return &pkgariapi.WorkspaceList{Items: items}, nil
}

// Delete handles workspace/delete.
//
// Rejects deletion if the workspace has active agent runs.
func (a *workspaceAdapter) Delete(ctx context.Context, name string) error {
	a.logger.Info("workspace/delete", "workspace", name)

	if err := a.store.DeleteWorkspace(ctx, name); err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			return jsonrpc.ErrInvalidParams(err.Error())
		}
		return &jsonrpc.RPCError{Code: pkgariapi.CodeRecoveryBlocked, Message: err.Error()}
	}
	return nil
}

// Send handles workspace/send.
//
// Routes a message from one agent run to another within a workspace via a
// fire-and-forget ShimClient.Prompt call.
func (a *workspaceAdapter) Send(ctx context.Context, req *pkgariapi.WorkspaceSendParams) (*pkgariapi.WorkspaceSendResult, error) {
	if req.Workspace == "" || req.From == "" || req.To == "" || req.Message == "" {
		return nil, jsonrpc.ErrInvalidParams("workspace, from, to, and message are required")
	}

	a.logger.Info("workspace/send",
		"workspace", req.Workspace, "from", req.From, "to", req.To)

	if a.processes.IsRecovering() {
		a.logger.Warn("workspace/send: recovery blocked",
			"workspace", req.Workspace, "to", req.To)
		return nil, &jsonrpc.RPCError{Code: pkgariapi.CodeRecoveryBlocked, Message: "daemon is recovering agents"}
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
	if agent.Status.State == apiruntime.StatusError {
		a.logger.Warn("workspace/send: target agent in error state",
			"workspace", req.Workspace, "to", req.To)
		return nil, &jsonrpc.RPCError{Code: pkgariapi.CodeRecoveryBlocked, Message: "target agent is in error state"}
	}
	if agent.Status.State != apiruntime.StatusIdle {
		a.logger.Warn("workspace/send: target agent not idle",
			"workspace", req.Workspace, "to", req.To, "state", agent.Status.State)
		return nil, &jsonrpc.RPCError{
			Code:    pkgariapi.CodeRecoveryBlocked,
			Message: fmt.Sprintf("target agent not in idle state: %s", agent.Status.State),
		}
	}

	reserved, err := a.agents.TransitionState(ctx, req.Workspace, req.To, apiruntime.StatusIdle, apiruntime.StatusRunning)
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
			Code:    pkgariapi.CodeRecoveryBlocked,
			Message: fmt.Sprintf("target agent not in idle state: %s", state),
		}
	}

	client, err := a.processes.Connect(ctx, req.Workspace, req.To)
	if err != nil {
		a.logger.Warn("workspace/send: target agent not running",
			"workspace", req.Workspace, "to", req.To, "error", err)
		a.recordPromptDeliveryFailure(req.Workspace, req.To, agent.Status, err, true)
		return nil, &jsonrpc.RPCError{Code: pkgariapi.CodeRecoveryBlocked, Message: "target agent is not running"}
	}

	msg := buildWorkspaceEnvelope(*req) + req.Message
	go func() {
		if _, err := client.Prompt(context.Background(), &apishim.SessionPromptParams{Prompt: msg}); err != nil {
			a.logger.Warn("workspace/send: prompt delivery failed",
				"workspace", req.Workspace, "to", req.To, "error", err)
			a.recordPromptDeliveryFailure(req.Workspace, req.To, agent.Status, err, false)
		}
	}()

	return &pkgariapi.WorkspaceSendResult{Delivered: true}, nil
}

// ────────────────────────────────────────────────────────────────────────────
// AgentRunService (agentRunAdapter)
// ────────────────────────────────────────────────────────────────────────────

// Create handles agentrun/create.
//
// Validates workspace/name/agent, checks workspace phase, creates the agent
// run record with state=creating, and starts the shim in the background.
// Returns immediately with state="creating".
func (a *agentRunAdapter) Create(ctx context.Context, ar *pkgariapi.AgentRun) (*pkgariapi.AgentRun, error) {
	if ar.Metadata.Workspace == "" || ar.Metadata.Name == "" || ar.Spec.Agent == "" {
		return nil, jsonrpc.ErrInvalidParams("workspace, name, and agent are required")
	}

	if err := a.processes.ValidateAgentSocketPath(ar.Metadata.Workspace, ar.Metadata.Name); err != nil {
		return nil, jsonrpc.ErrInvalidParams(err.Error())
	}

	switch ar.Spec.RestartPolicy {
	case "", pkgariapi.RestartPolicyTryReload, pkgariapi.RestartPolicyAlwaysNew:
		// valid
	default:
		return nil, jsonrpc.ErrInvalidParams(
			fmt.Sprintf("invalid restartPolicy %q: must be one of \"try_reload\", \"always_new\"", ar.Spec.RestartPolicy))
	}

	a.logger.Info("agentrun/create", "workspace", ar.Metadata.Workspace, "name", ar.Metadata.Name)

	ws, err := a.store.GetWorkspace(ctx, ar.Metadata.Workspace)
	if err != nil {
		return nil, jsonrpc.ErrInternal(err.Error())
	}
	if ws == nil {
		return nil, jsonrpc.ErrInvalidParams(fmt.Sprintf("workspace %s not found", ar.Metadata.Workspace))
	}
	if ws.Status.Phase != pkgariapi.WorkspacePhaseReady {
		return nil, &jsonrpc.RPCError{
			Code:    pkgariapi.CodeRecoveryBlocked,
			Message: fmt.Sprintf("workspace %s is not ready (phase=%s)", ar.Metadata.Workspace, ws.Status.Phase),
		}
	}

	ar.Status.State = apiruntime.StatusCreating
	if err := a.agents.Create(ctx, ar); err != nil {
		var alreadyExists *agentd.ErrAgentRunAlreadyExists
		if errors.As(err, &alreadyExists) {
			return nil, &jsonrpc.RPCError{Code: pkgariapi.CodeRecoveryBlocked, Message: err.Error()}
		}
		return nil, jsonrpc.ErrInternal(err.Error())
	}

	a.logger.Info("agentrun/create: agent run created, starting shim",
		"workspace", ar.Metadata.Workspace, "name", ar.Metadata.Name)

	wsName := ar.Metadata.Workspace
	agName := ar.Metadata.Name
	go func() {
		bgCtx := context.Background()
		if _, err := a.processes.Start(bgCtx, wsName, agName); err != nil {
			a.logger.Warn("agentrun/create: shim start failed",
				"workspace", wsName, "name", agName, "error", err)
			_ = a.agents.UpdateStatus(bgCtx, wsName, agName, pkgariapi.AgentRunStatus{
				State:        apiruntime.StatusError,
				ErrorMessage: err.Error(),
			})
		} else {
			a.logger.Info("agentrun/create: shim started",
				"workspace", wsName, "name", agName)
		}
	}()

	return copyVal(ar.ARIView()), nil
}

// Get handles agentrun/get.
//
// Returns detailed agent run info with shim runtime state populated in Status.Shim.
func (a *agentRunAdapter) Get(ctx context.Context, wsName, name string) (*pkgariapi.AgentRun, error) {
	a.logger.Info("agentrun/get", "workspace", wsName, "name", name)

	agent, err := a.store.GetAgentRun(ctx, wsName, name)
	if err != nil {
		return nil, jsonrpc.ErrInternal(err.Error())
	}
	if agent == nil {
		return nil, jsonrpc.ErrInvalidParams(fmt.Sprintf("agent %s/%s not found", wsName, name))
	}

	result := agent.ARIView()

	// Best-effort: populate Shim runtime state if available.
	if rts, err := a.processes.RuntimeStatus(ctx, wsName, name); err == nil {
		st := rts.State
		socketPath := agent.Status.ShimSocketPath
		if p := a.processes.GetProcess(wsName + "/" + name); p != nil {
			socketPath = p.SocketPath
		}
		result.Status.Shim = &pkgariapi.ShimStateInfo{
			Status:     string(st.Status),
			PID:        agent.Status.ShimPID,
			Bundle:     st.Bundle,
			SocketPath: socketPath,
		}
	}

	return &result, nil
}

// List handles agentrun/list.
//
// Returns all agent runs matching the optional workspace/state filter.
func (a *agentRunAdapter) List(ctx context.Context, opts pkgariapi.ListOptions) (*pkgariapi.AgentRunList, error) {
	wsFilter := opts.FieldSelector["workspace"]
	stFilter := opts.FieldSelector["state"]
	a.logger.Info("agentrun/list", "workspace", wsFilter, "state", stFilter)

	filter := &pkgariapi.AgentRunFilter{
		Workspace: wsFilter,
		State:     apiruntime.Status(stFilter),
	}
	agentRuns, err := a.agents.List(ctx, filter)
	if err != nil {
		return nil, jsonrpc.ErrInternal(err.Error())
	}

	items := make([]pkgariapi.AgentRun, 0, len(agentRuns))
	for _, ag := range agentRuns {
		items = append(items, ag.ARIView())
	}
	return &pkgariapi.AgentRunList{Items: items}, nil
}

// Delete handles agentrun/delete.
//
// Validates agent run is in stopped/error state then deletes from DB.
func (a *agentRunAdapter) Delete(ctx context.Context, wsName, name string) error {
	a.logger.Info("agentrun/delete", "workspace", wsName, "name", name)

	if err := a.agents.Delete(ctx, wsName, name); err != nil {
		var notFound *agentd.ErrAgentRunNotFound
		if errors.As(err, &notFound) {
			return jsonrpc.ErrInvalidParams(err.Error())
		}
		var notStopped *agentd.ErrDeleteNotStopped
		if errors.As(err, &notStopped) {
			return &jsonrpc.RPCError{Code: pkgariapi.CodeRecoveryBlocked, Message: err.Error()}
		}
		return jsonrpc.ErrInternal(err.Error())
	}

	// Clean up bundle directory (best effort; DB record already deleted).
	bundlePath := a.processes.BundlePath(wsName, name)
	if err := os.RemoveAll(bundlePath); err != nil {
		a.logger.Warn("agentrun/delete: failed to remove bundle",
			"workspace", wsName, "name", name,
			"bundle", bundlePath, "error", err)
	}
	return nil
}

// Prompt handles agentrun/prompt.
//
// Validates agent run state == idle, transitions to running, connects to shim,
// and fires the prompt in the background. Returns Accepted:true immediately.
func (a *agentRunAdapter) Prompt(ctx context.Context, req *pkgariapi.AgentRunPromptParams) (*pkgariapi.AgentRunPromptResult, error) {
	if req.Workspace == "" || req.Name == "" || req.Prompt == "" {
		return nil, jsonrpc.ErrInvalidParams("workspace, name, and prompt are required")
	}

	a.logger.Info("agentrun/prompt", "workspace", req.Workspace, "name", req.Name)

	if a.processes.IsRecovering() {
		a.logger.Warn("agentrun/prompt: recovery blocked",
			"workspace", req.Workspace, "name", req.Name)
		return nil, &jsonrpc.RPCError{Code: pkgariapi.CodeRecoveryBlocked, Message: "daemon is recovering agents"}
	}

	agent, err := a.store.GetAgentRun(ctx, req.Workspace, req.Name)
	if err != nil {
		return nil, jsonrpc.ErrInternal(err.Error())
	}
	if agent == nil {
		return nil, jsonrpc.ErrInvalidParams(fmt.Sprintf("agent %s/%s not found", req.Workspace, req.Name))
	}

	if agent.Status.State != apiruntime.StatusIdle {
		a.logger.Warn("agentrun/prompt: agent not in idle state",
			"workspace", req.Workspace, "name", req.Name, "state", agent.Status.State)
		return nil, &jsonrpc.RPCError{
			Code:    pkgariapi.CodeRecoveryBlocked,
			Message: fmt.Sprintf("agent not in idle state: %s", agent.Status.State),
		}
	}

	reserved, err := a.agents.TransitionState(ctx, req.Workspace, req.Name, apiruntime.StatusIdle, apiruntime.StatusRunning)
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
			Code:    pkgariapi.CodeRecoveryBlocked,
			Message: fmt.Sprintf("agent not in idle state: %s", state),
		}
	}

	client, err := a.processes.Connect(ctx, req.Workspace, req.Name)
	if err != nil {
		a.logger.Warn("agentrun/prompt: agent not running",
			"workspace", req.Workspace, "name", req.Name, "error", err)
		a.recordPromptDeliveryFailure(req.Workspace, req.Name, agent.Status, err, true)
		return nil, &jsonrpc.RPCError{Code: pkgariapi.CodeRecoveryBlocked, Message: "agent not running"}
	}

	prompt := req.Prompt
	go func() {
		if _, err := client.Prompt(context.Background(), &apishim.SessionPromptParams{Prompt: prompt}); err != nil {
			a.logger.Warn("agentrun/prompt: prompt delivery failed",
				"workspace", req.Workspace, "name", req.Name, "error", err)
			a.recordPromptDeliveryFailure(req.Workspace, req.Name, agent.Status, err, false)
		}
	}()

	a.logger.Info("agentrun/prompt: dispatched",
		"workspace", req.Workspace, "name", req.Name)
	return &pkgariapi.AgentRunPromptResult{Accepted: true}, nil
}

// Cancel handles agentrun/cancel.
//
// Connects to the running shim and calls Cancel.
func (a *agentRunAdapter) Cancel(ctx context.Context, wsName, name string) error {
	a.logger.Info("agentrun/cancel", "workspace", wsName, "name", name)

	agent, err := a.store.GetAgentRun(ctx, wsName, name)
	if err != nil {
		return jsonrpc.ErrInternal(err.Error())
	}
	if agent == nil {
		return jsonrpc.ErrInvalidParams(fmt.Sprintf("agent %s/%s not found", wsName, name))
	}

	client, err := a.processes.Connect(ctx, wsName, name)
	if err != nil {
		return &jsonrpc.RPCError{Code: pkgariapi.CodeRecoveryBlocked, Message: "agent not running"}
	}

	if err := client.Cancel(ctx); err != nil {
		return jsonrpc.ErrInternal(err.Error())
	}
	return nil
}

// Stop handles agentrun/stop.
//
// Calls processes.Stop which sends runtime/stop to the shim and waits.
func (a *agentRunAdapter) Stop(ctx context.Context, wsName, name string) error {
	a.logger.Info("agentrun/stop", "workspace", wsName, "name", name)

	if err := a.processes.Stop(ctx, wsName, name); err != nil {
		return jsonrpc.ErrInternal(err.Error())
	}
	return nil
}

// Restart handles agentrun/restart.
//
// Accepts any agent state. Stops the existing shim (if running) then starts a
// new one. Returns immediately with state="creating".
func (a *agentRunAdapter) Restart(ctx context.Context, wsName, name string) (*pkgariapi.AgentRun, error) {
	a.logger.Info("agentrun/restart", "workspace", wsName, "name", name)

	agent, err := a.store.GetAgentRun(ctx, wsName, name)
	if err != nil {
		return nil, jsonrpc.ErrInternal(err.Error())
	}
	if agent == nil {
		return nil, jsonrpc.ErrInvalidParams(fmt.Sprintf("agent %s/%s not found", wsName, name))
	}

	// Agents in terminal states have no active shim.
	needsStop := agent.Status.State != apiruntime.StatusStopped && agent.Status.State != apiruntime.StatusError

	if err := a.agents.UpdateStatus(ctx, wsName, name, pkgariapi.AgentRunStatus{
		State: apiruntime.StatusCreating,
	}); err != nil {
		return nil, jsonrpc.ErrInternal(err.Error())
	}

	go func() {
		bgCtx := context.Background()
		if needsStop {
			if err := a.processes.Stop(bgCtx, wsName, name); err != nil {
				a.logger.Warn("agentrun/restart: pre-stop failed, continuing",
					"workspace", wsName, "name", name, "error", err)
			}
			// Stop() transitions state to "stopped"; re-set to "creating" for Start().
			if err := a.agents.UpdateStatus(bgCtx, wsName, name, pkgariapi.AgentRunStatus{
				State: apiruntime.StatusCreating,
			}); err != nil {
				a.logger.Warn("agentrun/restart: failed to re-transition to creating",
					"workspace", wsName, "name", name, "error", err)
				return
			}
		}
		if _, err := a.processes.Start(bgCtx, wsName, name); err != nil {
			a.logger.Warn("agentrun/restart: shim start failed",
				"workspace", wsName, "name", name, "error", err)
			_ = a.agents.UpdateStatus(bgCtx, wsName, name, pkgariapi.AgentRunStatus{
				State:        apiruntime.StatusError,
				ErrorMessage: err.Error(),
			})
		}
	}()

	// Read back updated agent state for the response.
	agentUpdated, err2 := a.store.GetAgentRun(ctx, wsName, name)
	if err2 != nil || agentUpdated == nil {
		agentUpdated = &pkgariapi.AgentRun{
			Metadata: pkgariapi.ObjectMeta{Workspace: wsName, Name: name},
			Status:   pkgariapi.AgentRunStatus{State: apiruntime.StatusCreating},
		}
	}
	return copyVal(agentUpdated.ARIView()), nil
}

// ────────────────────────────────────────────────────────────────────────────
// AgentService (agentAdapter)
// ────────────────────────────────────────────────────────────────────────────

// Create handles agent/create.
//
// Creates a new agent definition. Returns error if the agent already exists.
func (a *agentAdapter) Create(ctx context.Context, agent *pkgariapi.Agent) (*pkgariapi.Agent, error) {
	if agent.Metadata.Name == "" {
		return nil, jsonrpc.ErrInvalidParams("name is required")
	}
	if agent.Spec.Command == "" {
		return nil, jsonrpc.ErrInvalidParams("command is required")
	}

	a.logger.Info("agent/create", "name", agent.Metadata.Name, "command", agent.Spec.Command)

	// Check if the agent already exists.
	existing, err := a.store.GetAgent(ctx, agent.Metadata.Name)
	if err != nil {
		return nil, jsonrpc.ErrInternal(err.Error())
	}
	if existing != nil {
		return nil, jsonrpc.ErrInvalidParams(fmt.Sprintf("agent %s already exists", agent.Metadata.Name))
	}

	if err := a.store.SetAgent(ctx, agent); err != nil {
		return nil, jsonrpc.ErrInternal(err.Error())
	}

	// Read back to get server-assigned timestamps.
	stored, err := a.store.GetAgent(ctx, agent.Metadata.Name)
	if err != nil || stored == nil {
		return agent, nil
	}
	return stored, nil
}

// Update handles agent/update.
//
// Updates an existing agent definition. Returns error if the agent does not exist.
func (a *agentAdapter) Update(ctx context.Context, agent *pkgariapi.Agent) (*pkgariapi.Agent, error) {
	if agent.Metadata.Name == "" {
		return nil, jsonrpc.ErrInvalidParams("name is required")
	}
	if agent.Spec.Command == "" {
		return nil, jsonrpc.ErrInvalidParams("command is required")
	}

	a.logger.Info("agent/update", "name", agent.Metadata.Name, "command", agent.Spec.Command)

	// Check if the agent exists.
	existing, err := a.store.GetAgent(ctx, agent.Metadata.Name)
	if err != nil {
		return nil, jsonrpc.ErrInternal(err.Error())
	}
	if existing == nil {
		return nil, jsonrpc.ErrInvalidParams(fmt.Sprintf("agent %s not found", agent.Metadata.Name))
	}

	if err := a.store.SetAgent(ctx, agent); err != nil {
		return nil, jsonrpc.ErrInternal(err.Error())
	}

	// Read back to get server-assigned timestamps.
	stored, err := a.store.GetAgent(ctx, agent.Metadata.Name)
	if err != nil || stored == nil {
		return agent, nil
	}
	return stored, nil
}

// Get handles agent/get.
//
// Returns the agent definition or InvalidParams if not found.
func (a *agentAdapter) Get(ctx context.Context, name string) (*pkgariapi.Agent, error) {
	a.logger.Info("agent/get", "name", name)

	ag, err := a.store.GetAgent(ctx, name)
	if err != nil {
		return nil, jsonrpc.ErrInternal(err.Error())
	}
	if ag == nil {
		return nil, jsonrpc.ErrInvalidParams(fmt.Sprintf("agent %s not found", name))
	}
	return ag, nil
}

// List handles agent/list.
//
// Returns all agent definition objects stored in the metadata DB.
func (a *agentAdapter) List(ctx context.Context, opts pkgariapi.ListOptions) (*pkgariapi.AgentList, error) {
	a.logger.Info("agent/list")

	ags, err := a.store.ListAgents(ctx)
	if err != nil {
		return nil, jsonrpc.ErrInternal(err.Error())
	}

	items := make([]pkgariapi.Agent, 0, len(ags))
	for _, ag := range ags {
		items = append(items, *ag)
	}
	return &pkgariapi.AgentList{Items: items}, nil
}

// Delete handles agent/delete.
// No-op if the agent definition does not exist.
func (a *agentAdapter) Delete(ctx context.Context, name string) error {
	a.logger.Info("agent/delete", "name", name)

	if err := a.store.DeleteAgent(ctx, name); err != nil {
		return jsonrpc.ErrInternal(err.Error())
	}
	return nil
}

// ────────────────────────────────────────────────────────────────────────────
// Shared helpers (on *Service so both adapter families can call them)
// ────────────────────────────────────────────────────────────────────────────

// recordPromptDeliveryFailure updates agent run status after a failed prompt
// delivery, consulting the runtime status to get the authoritative state.
func (s *Service) recordPromptDeliveryFailure(wsName, name string, fallback pkgariapi.AgentRunStatus, cause error, markErrorWhenRuntimeUnavailable bool) {
	ctx := context.Background()
	current, err := s.store.GetAgentRun(ctx, wsName, name)
	if err != nil {
		s.logger.Warn("prompt failure: current state lookup failed",
			"workspace", wsName, "name", name, "error", err)
	}
	if current != nil && current.Status.State == apiruntime.StatusStopped {
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
	if current != nil && current.Status.State == apiruntime.StatusStopped {
		s.logger.Info("prompt failure: ignored after stop",
			"workspace", wsName, "name", name, "error", cause)
		return
	}

	_ = s.agents.UpdateStatus(ctx, wsName, name, pkgariapi.AgentRunStatus{
		State:          apiruntime.StatusError,
		ShimSocketPath: fallback.ShimSocketPath,
		ShimStateDir:   fallback.ShimStateDir,
		ShimPID:        fallback.ShimPID,
		ErrorMessage:   cause.Error(),
	})
}

// buildWorkspaceEnvelope constructs the envelope header prepended to every
// workspace message before delivery.
func buildWorkspaceEnvelope(p pkgariapi.WorkspaceSendParams) string {
	if p.NeedsReply {
		return "[workspace-message from=" + p.From + " reply-to=" + p.From + " reply-requested=true]\n\n"
	}
	return "[workspace-message from=" + p.From + "]\n\n"
}
