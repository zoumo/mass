// Package agentd implements the agent daemon that manages agent runtime lifecycle.
// This file defines the ProcessManager for managing agent-run process lifecycle.
package agentd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/zoumo/mass/pkg/agentd/store"
	runapi "github.com/zoumo/mass/pkg/agentrun/api"
	runclient "github.com/zoumo/mass/pkg/agentrun/client"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	spec "github.com/zoumo/mass/pkg/runtime-spec"
	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
	"github.com/zoumo/mass/pkg/watch"
)

// EventHandler is called for each runtime/event_update notification received from the agent-run.
// Handlers must be registered before calling Subscribe.
type EventHandler func(ctx context.Context, update runapi.AgentRunEvent)

// agentKey returns the composite map key for an agent: workspace+"/"+name.
// This matches the bbolt key path convention used by the meta store.
func agentKey(workspace, name string) string {
	return workspace + "/" + name
}

// ────────────────────────────────────────────────────────────────────────────
// ProcessManager - manages agent-run process lifecycle
// ────────────────────────────────────────────────────────────────────────────

// ProcessManager manages the lifecycle of agent-run processes.
// It orchestrates:
//   - Agent status transitions
//   - Runtime entity resolution from DB
//   - Bundle creation (config.json + workspace symlink)
//   - Agent-run process fork/exec (self-fork)
//   - Client connection and event subscription
type ProcessManager struct {
	agents     *AgentRunManager
	store      *store.Store
	socketPath string
	bundleRoot string
	logLevel   string // propagated to agent-run and mesh-mcp child processes
	logFormat  string // propagated to agent-run and mesh-mcp child processes

	// RunBinary overrides the agent-run binary path for testing.
	// When empty (default), forkRun uses os.Executable() (self-fork).
	RunBinary string

	mu        sync.RWMutex
	processes map[string]*RunProcess // agentKey (workspace+"/"+name) -> RunProcess

	// recoveryPhase tracks the daemon-level recovery lifecycle as an atomic
	// int32 so it can be read cheaply without acquiring mu. Guards in ARI
	// handlers check this on every operational request.
	recoveryPhase atomic.Int32

	logger *slog.Logger
}

// RunProcess tracks a running agent-run process and its RPC client.
type RunProcess struct {
	// AgentKey is the composite agent identifier: workspace+"/"+name.
	AgentKey string

	// PID is the OS process ID of the agent-run process.
	PID int

	// BundlePath is the absolute path to the bundle directory.
	BundlePath string

	// StateDir is the absolute path to the agent-run.s state directory.
	StateDir string

	// SocketPath is the absolute path to the agent-run.s RPC socket.
	SocketPath string

	// Client is the connected Client for RPC communication.
	Client *runclient.Client

	// Watcher is the event stream from the agent-run.
	// It wraps a RetryWatcher with auto-reconnect for both freshly-forked and
	// recovered agents.
	Watcher watch.Interface[runapi.AgentRunEvent]

	// Cmd is the exec.Cmd for the agent-run process (for Wait/Kill).
	Cmd *exec.Cmd

	// Events is a channel receiving ordered AgentRunEvents from the agent-run.
	// A default drain goroutine consumes events when no external reader is active.
	Events chan runapi.AgentRunEvent

	// Done is closed when the agent-run process exits and all cleanup is complete.
	Done chan struct{}

	// stopDrain is closed to stop the default drain goroutine when an external
	// consumer takes over reading from Events.
	stopDrain chan struct{}

	// exitErr holds the error returned by cmd.Wait(). Set before Done is closed.
	exitErr error

	// Recovery holds per-agent recovery metadata. Nil for agents that
	// were started normally (not recovered after a daemon restart).
	Recovery *RecoveryInfo
}

// NewProcessManager creates a new ProcessManager.
func NewProcessManager(agents *AgentRunManager, s *store.Store, socketPath, bundleRoot string, logger *slog.Logger, logLevel, logFormat string) *ProcessManager {
	logger = logger.With("component", "mass.process")
	return &ProcessManager{
		agents:     agents,
		store:      s,
		socketPath: socketPath,
		bundleRoot: bundleRoot,
		logLevel:   logLevel,
		logFormat:  logFormat,
		processes:  make(map[string]*RunProcess),
		logger:     logger,
	}
}

// routeEvent routes a single AgentRunEvent from the event consumer:
//   - runtime_update with Status → DB agent state update (D088)
//   - all other events → runProc.Events channel for external consumers
func (m *ProcessManager) routeEvent(workspace, name string, runProc *RunProcess, ev runapi.AgentRunEvent, logger *slog.Logger) {
	if ev.Type == runapi.EventTypeRuntimeUpdate {
		ru, ok := ev.Payload.(runapi.RuntimeUpdateEvent)
		if !ok || ru.Phase == nil {
			// runtime_update without Status (e.g. metadata-only) → forward to Events.
			select {
			case runProc.Events <- ev:
			default:
			}
			return
		}
		prevStatus := ru.Phase.PreviousPhase
		newStatus := ru.Phase.Phase
		logger.Info("stateChange: updating DB state",
			"prev", prevStatus,
			"new", newStatus)
		updateCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		current, err := m.agents.Get(updateCtx, workspace, name)
		if err != nil {
			logger.Warn("stateChange: failed to read DB state",
				"error", err)
			cancel()
			return
		}
		if current != nil && current.Status.Phase == apiruntime.PhaseStopped && apiruntime.Phase(newStatus) != apiruntime.PhaseStopped {
			logger.Info("stateChange: dropped stale live state after stop",
				"current", current.Status.Phase,
				"new", newStatus)
			cancel()
			return
		}
		if current != nil && current.Status.Phase == apiruntime.PhaseRestarting && apiruntime.Phase(newStatus) != apiruntime.PhaseStopped {
			logger.Info("stateChange: dropped stale live state during restart",
				"current", current.Status.Phase,
				"new", newStatus)
			cancel()
			return
		}
		if err := m.agents.UpdatePhase(updateCtx, workspace, name, apiruntime.Phase(newStatus), ""); err != nil {
			logger.Warn("stateChange: failed to update DB phase",
				"error", err)
		}
		// On transition to idle the ACP handshake is complete and
		// state.json contains the sessionID. Persist it now.
		if apiruntime.Phase(newStatus) == apiruntime.PhaseIdle && runProc.StateDir != "" {
			m.syncSessionInfo(updateCtx, workspace, name, runProc.StateDir, logger)
		}
		cancel()
		return
	}

	// All other events → push to the Events channel for external consumers.
	select {
	case runProc.Events <- ev:
	default:
		// No consumer is draining Events; drop silently to avoid log spam.
	}
}

// startEventConsumer launches a goroutine that reads from the event source and
// routes events:
//   - runtime_update with Status → DB agent state update (D088)
//   - all other events → runProc.Events channel for external consumers
//
// When runProc.WC is non-nil (recovered agents), events are read from WC.Events().
// Otherwise events are read from runProc.Watcher.ResultChan() (freshly-forked agents).
//
// The goroutine exits when the event channel closes (connection lost,
// slow consumer eviction, context canceled, or explicit Stop()). This is the
// single authoritative event consumer — all DB state transitions after bootstrap
// flow through here.
func (m *ProcessManager) startEventConsumer(workspace, name string, runProc *RunProcess) {
	key := agentKey(workspace, name)
	logger := m.logger.With("agent_key", key)

	go func() {
		for ev := range runProc.Watcher.ResultChan() {
			m.routeEvent(workspace, name, runProc, ev, logger)
		}
	}()
}

// Start creates and starts an agent-run process for the given agent.
// The full workflow:
//  1. Get AgentRun from AgentRunManager
//  2. Resolve Agent definition from DB store via GetAgent
//  3. Generate config.json
//  4. Create bundle directory with workspace symlink
//  5. Fork agent-run process (self-fork)
//  6. Wait for socket to appear
//  7. Connect Client with the unified notification handler (D088)
//  8. Subscribe to events
//
// After Subscribe, the agent-run emits runtime/state_change creating→idle once the
// ACP handshake completes; the notification handler updates DB state
// asynchronously per D088 — callers must not assume StatusRunning immediately.
//
// Returns RunProcess on success, or error on failure.
// On failure, any partial state (bundle dir, process) is cleaned up.
func (m *ProcessManager) Start(ctx context.Context, workspace, name string) (*RunProcess, error) {
	key := agentKey(workspace, name)
	m.logger.Info("starting agent", "agent_key", key)

	// 1. Get AgentRun from AgentRunManager.
	agent, err := m.agents.Get(ctx, workspace, name)
	if err != nil {
		return nil, fmt.Errorf("process: get agent %s: %w", key, err)
	}
	if agent == nil {
		return nil, fmt.Errorf("process: agent %s does not exist", key)
	}

	// Validate agent status - must be "creating" to start.
	if agent.Status.Phase != apiruntime.PhaseCreating {
		return nil, fmt.Errorf("process: agent %s is in phase %s (must be 'creating' to start)", key, agent.Status.Phase)
	}

	// 2. Resolve Agent definition from DB.
	agentDef, err := m.store.GetAgent(ctx, agent.Spec.Agent)
	if err != nil {
		return nil, fmt.Errorf("process: get agent definition %s: %w", agent.Spec.Agent, err)
	}
	if agentDef == nil {
		return nil, fmt.Errorf("process: agent definition %s not found", agent.Spec.Agent)
	}

	// From this point the runtime bootstrap has started. The socket may not
	// exist yet, so observers should see creating while Start waits for it.
	if err := m.agents.UpdatePhase(ctx, workspace, name, apiruntime.PhaseCreating, ""); err != nil {
		return nil, fmt.Errorf("process: mark agent creating: %w", err)
	}
	agent.Status.Phase = apiruntime.PhaseCreating

	// 2b. Fetch workspace for feature gate checks (best-effort).
	ws, _ := m.store.GetWorkspace(ctx, workspace)

	// 3. Generate config.json for this agent run.
	cfg := m.generateConfig(agent, agentDef, ws)

	// 4. Create bundle directory with workspace symlink.
	bundlePath, stateDir, socketPath, err := m.createBundle(agent, cfg)
	if err != nil {
		return nil, fmt.Errorf("process: create bundle: %w", err)
	}
	if err := spec.ValidateRunSocketPath(socketPath); err != nil {
		return nil, err
	}

	// 5. Fork agent-run process.
	runProc, err := m.forkRun(agent, bundlePath, stateDir)
	if err != nil {
		return nil, fmt.Errorf("process: fork run: %w", err)
	}

	// Set paths on RunProcess.
	runProc.BundlePath = bundlePath
	runProc.StateDir = stateDir
	runProc.SocketPath = socketPath

	// Close Done as soon as the OS process exits so waitForSocket can fail fast
	// on early crash. Full cleanup (map removal, DB update) runs in the
	// watchProcess goroutine started after successful bootstrap.
	go func() {
		runProc.exitErr = runProc.Cmd.Wait()
		close(runProc.Done)
	}()

	// 6. Wait for socket to appear (poll with timeout).
	// Use Agent definition's startupTimeoutSeconds if configured.
	waitCtx := ctx
	if agentDef.Spec.StartupTimeoutSeconds != nil {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, time.Duration(*agentDef.Spec.StartupTimeoutSeconds)*time.Second)
		defer cancel()
	}
	if err := m.waitForSocket(waitCtx, socketPath, runProc); err != nil {
		// Kill agent-run process; leave bundle intact (preserved until agent/delete).
		m.killRun(runProc)
		return nil, fmt.Errorf("process: wait for socket: %w", err)
	}

	// 7. Connect Client (plain Dial, no global handler).
	// Event routing is handled by the Watcher + startEventConsumer goroutine.
	client, err := runclient.Dial(ctx, socketPath)
	if err != nil {
		// Kill agent-run process; leave bundle intact (preserved until agent/delete).
		m.killRun(runProc)
		return nil, fmt.Errorf("process: connect agent-run client: agent=%s: %w", key, err)
	}
	runProc.Client = client

	// 7b. Persist agent-run socket/state/pid for recovery.
	if err := m.store.UpdateAgentRunStatus(ctx, workspace, name, pkgariapi.AgentRunStatus{
		Phase:      apiruntime.PhaseCreating, // keep creating until we transition below
		SocketPath: socketPath,
		StateDir:   stateDir,
		PID:        runProc.PID,
	}); err != nil {
		m.logger.Error("failed to persist run info", "agent_key", key, "error", err)
		// Non-fatal: agent can still run.
	}

	// 8. Watch events with reconnect/replay recovery.
	watcher := watch.NewRetryWatcher(
		ctx,
		runclient.NewWatchFunc(socketPath),
		-1,
		func(ev runapi.AgentRunEvent) int { return ev.Seq },
		1024,
	)
	runProc.Watcher = watcher

	// Start the event consumer goroutine that routes Watcher events to
	// DB state updates (state_change) and runProc.Events (session events).
	m.startEventConsumer(workspace, name, runProc)

	// 8b. Bootstrap state from agent-run's current runtime status.
	// The agent-run.s Create() may have already transitioned creating→idle before
	// the Subscribe call, causing the stateChange notification to be missed
	// (SetStateChangeHook in the agent-run is registered after Create() returns,
	// so the hook is nil during the initial transition). Reading runtime/status
	// here ensures the DB reflects the actual state even when the notification
	// was dropped.
	if statusResult, statusErr := client.Status(ctx); statusErr == nil {
		if statusResult.State.Phase != apiruntime.PhaseCreating {
			updateCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := m.agents.UpdatePhase(updateCtx, workspace, name, statusResult.State.Phase, ""); err != nil {
				m.logger.Warn("bootstrap state sync failed",
					"agent_key", key, "phase", statusResult.State.Phase, "error", err)
			} else {
				m.logger.Info("bootstrap phase synced from agent-run",
					"agent_key", key, "phase", statusResult.State.Phase)
				if statusResult.State.Phase == apiruntime.PhaseIdle {
					m.syncSessionInfo(updateCtx, workspace, name, stateDir, m.logger.With("agent_key", key))
				}
			}
		}
	}

	// Store the RunProcess.
	m.mu.Lock()
	m.processes[key] = runProc
	m.mu.Unlock()

	// Start a goroutine to wait for process exit and clean up.
	go m.watchProcess(workspace, name, runProc)

	m.logger.Info("agent started", "agent_key", key, "pid", runProc.PID)

	return runProc, nil
}

// generateConfig creates the MASS Runtime config.json for this agent.
func (m *ProcessManager) generateConfig(agent *pkgariapi.AgentRun, agentDef *pkgariapi.Agent, ws *pkgariapi.Workspace) apiruntime.Config {
	// Build environment variables in KEY=VALUE format from the Agent definition.
	env := make([]string, 0, len(agentDef.Spec.Env))
	for _, ev := range agentDef.Spec.Env {
		env = append(env, fmt.Sprintf("%s=%s", ev.Name, ev.Value))
	}

	// Build annotations from agent labels.
	annotations := make(map[string]string)
	maps.Copy(annotations, agent.Metadata.Labels)
	annotations["agent"] = agentDef.Metadata.Name

	// Compute the bundle/state directory (same formula as createBundle) so we
	// can pass --log-path to the workspace-mesh server before the directory
	// is actually created.
	stateDir := filepath.Join(m.bundleRoot, agent.Metadata.Workspace, agent.Metadata.Name)

	// Conditional MCP injection based on WorkspaceMesh feature.
	var mcpServers []apiruntime.McpServer
	if featureEnabled(ws, FeatureWorkspaceMesh) {
		mcpBinary, mcpArgs := m.workspaceMcpCommand(agent.Metadata.Workspace, agent.Metadata.Name, stateDir)
		workspaceMcp := apiruntime.McpServer{
			Type:    "stdio",
			Name:    pkgariapi.WorkspaceMeshName,
			Command: mcpBinary,
			Args:    mcpArgs,
		}
		mcpServers = mergeMcpServers([]apiruntime.McpServer{workspaceMcp}, agent.Spec.McpServers)
	} else {
		mcpServers = agent.Spec.McpServers
	}

	// ClientProtocol from AgentSpec, default to ACP.
	protocol := agentDef.Spec.ClientProtocol
	if protocol == "" {
		protocol = apiruntime.ClientProtocolACP
	}

	// Permissions from AgentRunSpec, default to ApproveAll.
	permissions := agent.Spec.Permissions
	if permissions == "" {
		permissions = apiruntime.ApproveAll
	}

	// System prompt assembly (order matters):
	// 1. Identity — who you are, which workspace, workspace path
	// 2. Workspace Mesh MCP usage (feature-gated)
	// 3. Agent Task protocol (feature-gated)
	// 4. User-supplied system prompt
	// 5. Workflow file reference
	workspacePath := ""
	if ws != nil {
		workspacePath = ws.Status.Path
	}
	var systemPrompt string
	systemPrompt = appendPromptSection(systemPrompt, identityPrompt(agent.Metadata.Workspace, agent.Metadata.Name, workspacePath))
	if featureEnabled(ws, FeatureWorkspaceMesh) {
		systemPrompt = appendPromptSection(systemPrompt, workspaceMeshMCPPrompt())
	}
	if featureEnabled(ws, FeatureAgentTask) {
		systemPrompt = appendPromptSection(systemPrompt, agentTaskPrompt())
	}
	if agent.Spec.SystemPrompt != "" {
		systemPrompt = appendPromptSection(systemPrompt, agent.Spec.SystemPrompt)
	}
	if agent.Spec.WorkflowFile != "" {
		if _, err := os.Stat(agent.Spec.WorkflowFile); err == nil {
			systemPrompt = appendPromptSection(systemPrompt, workflowPrompt(agent.Spec.WorkflowFile))
		}
	}

	return apiruntime.Config{
		MassVersion: "0.1.0",
		Metadata: apiruntime.Metadata{
			Name:        agent.Metadata.Name,
			Annotations: annotations,
		},
		AgentRoot: apiruntime.AgentRoot{
			Path: "workspace", // symlink to actual workspace
		},
		ClientProtocol: protocol,
		Process: apiruntime.Process{
			Command: agentDef.Spec.Command,
			Args:    agentDef.Spec.Args,
			Env:     env,
		},
		Session: apiruntime.Session{
			SystemPrompt: systemPrompt,
			Permissions:  permissions,
			McpServers:   mcpServers,
		},
	}
}

// mergeMcpServers merges base and override MCP server lists, deduplicating by Name.
// Entries in overrides with the same Name as a base entry replace the base entry.
// Order: base entries (possibly replaced) first, then new override entries.
func mergeMcpServers(base, overrides []apiruntime.McpServer) []apiruntime.McpServer {
	if len(overrides) == 0 {
		return base
	}
	overrideMap := make(map[string]apiruntime.McpServer, len(overrides))
	for _, s := range overrides {
		if s.Name != "" {
			overrideMap[s.Name] = s
		}
	}
	// Replace base entries that have an override.
	result := make([]apiruntime.McpServer, 0, len(base)+len(overrides))
	seen := make(map[string]bool, len(base))
	for _, s := range base {
		if ov, ok := overrideMap[s.Name]; ok {
			result = append(result, ov)
			seen[s.Name] = true
		} else {
			result = append(result, s)
			if s.Name != "" {
				seen[s.Name] = true
			}
		}
	}
	// Append new override entries not already in base.
	for _, s := range overrides {
		if s.Name == "" || !seen[s.Name] {
			result = append(result, s)
		}
	}
	return result
}

// workspaceMcpCommand returns the command and args for the workspace MCP server.
// Uses self-fork: os.Executable() + mesh subcommand (same pattern as agent-run).
func (m *ProcessManager) workspaceMcpCommand(workspace, agent, logDir string) (string, []string) {
	self, err := os.Executable()
	if err != nil {
		m.logger.Error("os.Executable failed for workspace mesh subcommand, falling back to PATH", "error", err)
		self = "mass"
	}
	args := []string{
		"mesh-mcp",
		"--socket", m.socketPath,
		"--workspace", workspace,
		"--agent", agent,
		"--log-path", logDir,
		"--log-level", m.logLevel,
		"--log-format", m.logFormat,
	}
	return self, args
}

// createBundle creates the bundle directory and writes config.json.
// Also creates the workspace symlink (agentRoot.path -> actual workspace).
// Returns bundlePath, stateDir, socketPath.
func (m *ProcessManager) createBundle(agent *pkgariapi.AgentRun, cfg apiruntime.Config) (string, string, string, error) {
	// Bundle directory: <bundleRoot>/<workspace>/<name>
	bundlePath := filepath.Join(m.bundleRoot, agent.Metadata.Workspace, agent.Metadata.Name)

	// Create bundle directory.
	if err := os.MkdirAll(bundlePath, 0o755); err != nil {
		return "", "", "", fmt.Errorf("mkdir bundle %s: %w", bundlePath, err)
	}

	// Write config.json.
	cfgData, err := json.Marshal(cfg)
	if err != nil {
		_ = os.RemoveAll(bundlePath)
		return "", "", "", fmt.Errorf("marshal config.json: %w", err)
	}
	cfgPath := filepath.Join(bundlePath, "config.json")
	if err := os.WriteFile(cfgPath, cfgData, 0o644); err != nil {
		_ = os.RemoveAll(bundlePath)
		return "", "", "", fmt.Errorf("write config.json: %w", err)
	}

	// Look up the workspace to get its filesystem path.
	wsName := agent.Metadata.Workspace
	wsCtx, wsCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer wsCancel()
	workspace, err := m.store.GetWorkspace(wsCtx, wsName)
	if err != nil {
		_ = os.RemoveAll(bundlePath)
		return "", "", "", fmt.Errorf("get workspace %s: %w", wsName, err)
	}
	if workspace == nil {
		_ = os.RemoveAll(bundlePath)
		return "", "", "", fmt.Errorf("workspace %s does not exist", wsName)
	}

	// Create workspace symlink: bundle/workspace -> workspace.Status.Path
	workspaceLink := filepath.Join(bundlePath, cfg.AgentRoot.Path)
	// Remove any existing symlink/file first.
	_ = os.Remove(workspaceLink)
	if err := os.Symlink(workspace.Status.Path, workspaceLink); err != nil {
		_ = os.RemoveAll(bundlePath)
		return "", "", "", fmt.Errorf("symlink workspace %s -> %s: %w", workspaceLink, workspace.Status.Path, err)
	}

	// Copy workflow file into bundle and update spec to point to the copy.
	if agent.Spec.WorkflowFile != "" {
		src := agent.Spec.WorkflowFile
		dst := filepath.Join(bundlePath, "workflow.md")
		data, err := os.ReadFile(src)
		if err != nil {
			_ = os.RemoveAll(bundlePath)
			return "", "", "", fmt.Errorf("read workflow file %s: %w", src, err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			_ = os.RemoveAll(bundlePath)
			return "", "", "", fmt.Errorf("write workflow.md: %w", err)
		}
		agent.Spec.WorkflowFile = dst
	}

	// State directory is co-located with the bundle directory.
	stateDir := bundlePath

	// Socket path.
	socketPath := spec.RunSocketPath(stateDir)

	m.logger.Debug("bundle created", "bundle_path", bundlePath, "state_dir", stateDir, "socket_path", socketPath)

	return bundlePath, stateDir, socketPath, nil
}

// forkRun forks the agent-run process via self-fork.
// Uses os.Executable() to re-invoke the daemon with "run" as the first arg.
//
// Note: We intentionally do NOT use exec.CommandContext here because the agent-run
// process should run independently of the request context that initiated Start.
// Using CommandContext would kill the agent-run when the request context is canceled.
// The agent-run process lifecycle is managed by ProcessManager.Stop and watchProcess.
func (m *ProcessManager) forkRun(agent *pkgariapi.AgentRun, bundlePath, stateDir string) (*RunProcess, error) {
	runBinary := m.RunBinary
	if runBinary == "" {
		var err error
		runBinary, err = os.Executable()
		if err != nil {
			return nil, fmt.Errorf("os.Executable: %w", err)
		}
	}
	m.logger.Info("forkRun: using self-fork", "run_binary", runBinary)

	// Create state directory before starting agent-run.
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir state dir %s: %w", stateDir, err)
	}

	// Remove any stale socket file from a previous run so the new agent-run can bind.
	_ = os.Remove(spec.RunSocketPath(stateDir))

	key := agentKey(agent.Metadata.Workspace, agent.Metadata.Name)

	// Build command arguments.
	// stateDir == bundlePath, so --id is the bundle directory name and
	// --state-dir is bundleRoot. The agent-run computes: stateDir = parent/<id>,
	// which resolves back to bundlePath.
	// Prepend "run" as the first arg so the process knows to behave as an agent-run.
	args := []string{
		"run",
		"--bundle", bundlePath,
		"--permissions", string(apiruntime.ApproveAll),
		"--log-level", m.logLevel,
		"--log-format", m.logFormat,
		"--log-path", bundlePath,
	}

	// Log the command for debugging.
	m.logger.Info("forking agent-run process",
		"run_binary", runBinary,
		"args", args,
		"state_dir", stateDir,
		"bundle_path", bundlePath)

	// Create exec.Cmd WITHOUT tying to the request context.
	cmd := exec.Command(runBinary, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
	cmd.Stderr = os.Stderr // capture panics/early errors; structured logs go to --log-path
	cmd.Stdout = nil

	// Start the process.
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start agent-run process: %w", err)
	}

	m.logger.Debug("agent-run forked", "agent_key", key, "pid", cmd.Process.Pid)

	sp := &RunProcess{
		AgentKey:  key,
		PID:       cmd.Process.Pid,
		Cmd:       cmd,
		Events:    make(chan runapi.AgentRunEvent, 1024), // buffered for async delivery
		Done:      make(chan struct{}),
		stopDrain: make(chan struct{}),
	}
	go sp.drainEvents()
	return sp, nil
}

// drainEvents is the default consumer that discards events from the Events
// channel. It runs until stopDrain or Done is closed, preventing the channel
// from filling up when no external consumer is reading.
func (sp *RunProcess) drainEvents() {
	for {
		select {
		case <-sp.stopDrain:
			return
		case <-sp.Done:
			return
		case _, ok := <-sp.Events:
			if !ok {
				return
			}
		}
	}
}

// StopDrain stops the default drain goroutine so an external consumer can
// take over reading from Events without racing.
func (sp *RunProcess) StopDrain() {
	select {
	case <-sp.stopDrain:
		// already stopped
	default:
		close(sp.stopDrain)
	}
}

// defaultStartupTimeout is the fallback timeout when Agent definition does not
// specify startupTimeoutSeconds. Real CLI runtimes (gsd-pi, claude-code) need
// bunx package resolution + process startup which can take 30-60s on cold cache.
const defaultStartupTimeout = 90 * time.Second

// waitForSocket waits for the agent-run RPC socket to appear.
// Uses the context deadline if present (set from Agent.Spec.StartupTimeoutSeconds),
// otherwise falls back to defaultStartupTimeout.
// Returns early if the agent-run process exits before the socket appears.
func (m *ProcessManager) waitForSocket(ctx context.Context, socketPath string, runProc *RunProcess) error {
	start := time.Now()
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		deadline = start.Add(defaultStartupTimeout)
	}
	for {
		// Check if socket exists and is connectable.
		if _, err := os.Stat(socketPath); err == nil {
			conn, err := net.Dial("unix", socketPath)
			if err == nil {
				_ = conn.Close()
				return nil
			}
		}

		// Fail fast if the agent-run process has already exited.
		select {
		case <-runProc.Done:
			return fmt.Errorf("agent-run process exited before socket appeared at %s", socketPath)
		default:
		}

		// Check if timeout expired.
		if time.Now().After(deadline) {
			return fmt.Errorf("socket %s not ready after %v", socketPath, time.Since(start).Truncate(time.Second))
		}

		// Check if context canceled.
		if ctx.Err() != nil {
			return ctx.Err()
		}

		time.Sleep(50 * time.Millisecond)
	}
}

// killRun kills the agent-run process if it's still running.
func (m *ProcessManager) killRun(runProc *RunProcess) {
	// For recovered processes (no Cmd), fall back to killing by PID directly.
	if runProc.Cmd == nil || runProc.Cmd.Process == nil {
		if runProc.PID <= 0 {
			return
		}
		proc, err := os.FindProcess(runProc.PID)
		if err != nil {
			return
		}
		_ = proc.Signal(os.Interrupt)
		time.Sleep(2 * time.Second)
		_ = proc.Kill()
		return
	}

	// Try graceful shutdown first (SIGTERM).
	if err := runProc.Cmd.Process.Signal(os.Interrupt); err != nil {
		// Process might already be dead.
		if err.Error() == "os: process already finished" {
			return
		}
		// Fall back to SIGKILL.
		_ = runProc.Cmd.Process.Kill()
	}

	// Wait via Done channel (set by the goroutine in Start that owns Cmd.Wait).
	// Calling Cmd.Wait() a second time is not safe in Go.
	select {
	case <-runProc.Done:
	case <-time.After(2 * time.Second):
		_ = runProc.Cmd.Process.Kill()
		<-runProc.Done
	}
}

// watchProcess waits for the agent-run process to exit and cleans up.
// The process exit itself is detected by the lightweight goroutine started in
// Start(), which calls cmd.Wait() and closes runProc.Done. This goroutine
// waits on Done and then performs full cleanup.
func (m *ProcessManager) watchProcess(workspace, name string, runProc *RunProcess) {
	// Wait for the lightweight goroutine to signal process exit.
	<-runProc.Done
	key := runProc.AgentKey

	m.logger.Info("agent-run process exited", "agent_key", key, "error", runProc.exitErr)

	if runProc.Watcher != nil {
		runProc.Watcher.Stop()
	}
	if runProc.Client != nil {
		_ = runProc.Client.Close()
	}

	// Remove from processes map.
	m.mu.Lock()
	delete(m.processes, key)
	m.mu.Unlock()

	// Transition agent to "stopped" (best effort).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = m.agents.UpdateStatus(ctx, workspace, name, pkgariapi.AgentRunStatus{Phase: apiruntime.PhaseStopped})

	// Bundle directory is intentionally NOT cleaned up here.
	// It must persist until the agent is explicitly deleted via agent/delete.
}

// Stop gracefully stops a running agent-run process for the given agent.
// The workflow:
//  1. Get RunProcess from processes map
//  2. Call Client.Stop RPC to request graceful shutdown
//  3. Wait for process to exit (with timeout)
//  4. If timeout, kill the process
//  5. Remove bundle directory
//  6. Transition agent to "stopped"
//
// Returns error if the agent is not running or shutdown fails.
func (m *ProcessManager) Stop(ctx context.Context, workspace, name string) error {
	key := agentKey(workspace, name)
	m.logger.Info("stopping agent", "agent_key", key)

	// Get RunProcess from processes map.
	m.mu.RLock()
	runProc, exists := m.processes[key]
	m.mu.RUnlock()

	if !exists {
		// Agent is not running — check if it exists at all.
		agent, err := m.agents.Get(ctx, workspace, name)
		if err != nil {
			return fmt.Errorf("process: get agent %s: %w", key, err)
		}
		if agent == nil {
			return fmt.Errorf("process: agent %s does not exist", key)
		}
		// Agent exists but is not running — transition to stopped if needed.
		if agent.Status.Phase != apiruntime.PhaseStopped {
			if err := m.agents.UpdateStatus(ctx, workspace, name, pkgariapi.AgentRunStatus{Phase: apiruntime.PhaseStopped}); err != nil {
				return fmt.Errorf("process: transition agent %s to stopped: %w", key, err)
			}
		}
		return nil
	}

	// Call runtime/stop RPC to request graceful shutdown.
	if runProc.Client != nil {
		stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if err := runProc.Client.Stop(stopCtx); err != nil {
			m.logger.Warn("runtime/stop RPC failed, will kill process", "agent_key", key, "error", err)
		}
		cancel()
	}

	// Wait for process to exit.
	select {
	case <-runProc.Done:
		m.logger.Info("agent-run process exited gracefully", "agent_key", key)
	case <-time.After(10 * time.Second):
		m.logger.Warn("agent-run process did not exit in time, killing", "agent_key", key)
		m.killRun(runProc)
		// Wait for watchProcess to clean up.
		<-runProc.Done
	}

	// Bundle directory is intentionally NOT cleaned up here.
	// It must persist until the agent is explicitly deleted via agent/delete.

	m.logger.Info("agent stopped", "agent_key", key)
	return nil
}

// State returns the current runtime state of the agent-run for the given agent.
// Returns an error if the agent is not running or the response is malformed.
func (m *ProcessManager) State(ctx context.Context, workspace, name string) (apiruntime.State, error) {
	key := agentKey(workspace, name)
	m.mu.RLock()
	runProc, exists := m.processes[key]
	m.mu.RUnlock()

	if !exists {
		return apiruntime.State{}, fmt.Errorf("process: agent %s is not running", key)
	}

	if runProc.Client == nil {
		return apiruntime.State{}, fmt.Errorf("process: agent %s has no client connection", key)
	}

	statusResult, err := runProc.Client.Status(ctx)
	if err != nil {
		return apiruntime.State{}, fmt.Errorf("process: runtime/status for agent %s: %w", key, err)
	}

	return statusResult.State, nil
}

// RuntimePhase returns the full runtime/status result including recovery
// metadata for the given agent.
func (m *ProcessManager) RuntimePhase(ctx context.Context, workspace, name string) (runapi.RuntimePhaseResult, error) {
	key := agentKey(workspace, name)
	m.mu.RLock()
	runProc, exists := m.processes[key]
	m.mu.RUnlock()

	if !exists {
		return runapi.RuntimePhaseResult{}, fmt.Errorf("process: agent %s is not running", key)
	}

	if runProc.Client == nil {
		return runapi.RuntimePhaseResult{}, fmt.Errorf("process: agent %s has no client connection", key)
	}

	result, err := runProc.Client.Status(ctx)
	if err != nil {
		return runapi.RuntimePhaseResult{}, err
	}
	return *result, nil
}

// Connect returns the Client for direct RPC access to the agent-run process.
// Returns error if the agent is not running.
func (m *ProcessManager) Connect(ctx context.Context, workspace, name string) (*runclient.Client, error) {
	key := agentKey(workspace, name)
	m.mu.RLock()
	runProc, exists := m.processes[key]
	m.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("process: agent %s is not running", key)
	}

	if runProc.Client == nil {
		return nil, fmt.Errorf("process: agent %s has no client connection", key)
	}

	return runProc.Client, nil
}

// GetProcess returns the RunProcess for the given agent key (workspace+"/"+name).
// Returns nil if the agent is not running.
func (m *ProcessManager) GetProcess(agentKey string) *RunProcess {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.processes[agentKey]
}

// ListProcesses returns a list of all running agent keys (workspace+"/"+name).
func (m *ProcessManager) ListProcesses() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	keys := make([]string, 0, len(m.processes))
	for k := range m.processes {
		keys = append(keys, k)
	}
	return keys
}

// ────────────────────────────────────────────────────────────────────────────
// Recovery posture — daemon-level phase tracking
// ────────────────────────────────────────────────────────────────────────────

// SetRecoveryPhase atomically sets the daemon-level recovery phase.
func (m *ProcessManager) SetRecoveryPhase(phase RecoveryPhase) {
	m.recoveryPhase.Store(int32(phase))
	m.logger.Info("recovery phase changed", "phase", phase.String())
}

// GetRecoveryPhase atomically reads the current daemon-level recovery phase.
func (m *ProcessManager) GetRecoveryPhase() RecoveryPhase {
	return RecoveryPhase(m.recoveryPhase.Load())
}

// IsRecovering returns true when the daemon is actively recovering agents.
func (m *ProcessManager) IsRecovering() bool {
	return m.GetRecoveryPhase() == RecoveryPhaseRecovering
}

// InjectProcess inserts a pre-built RunProcess into the processes map under
// the given key. Used in tests to inject a mock agent-run without calling Start().
func (m *ProcessManager) InjectProcess(key string, proc *RunProcess) {
	m.mu.Lock()
	m.processes[key] = proc
	m.mu.Unlock()
}

// BundlePath returns the expected bundle directory path for the given agent.
// This path is deterministic and can be computed even when the agent-run is not running,
// allowing callers (e.g. agent/delete) to clean up the bundle after the process exits.
func (m *ProcessManager) BundlePath(workspace, name string) string {
	return filepath.Join(m.bundleRoot, workspace, name)
}

// ValidateAgentSocketPath checks whether the would-be Unix socket path for the
// given agent would exceed the OS limit. The path is computed the same way
// createBundle does it — bundleRoot/<workspace>-<name>/agent-run.sock — but
// without creating any files or directories.
//
// Call this before writing any DB records (e.g. in handleAgentCreate) so that
// a -32602 error is returned before any side effects.
func (m *ProcessManager) ValidateAgentSocketPath(workspace, name string) error {
	sockPath := filepath.Join(m.bundleRoot, workspace, name, "agent-run.sock")
	return spec.ValidateRunSocketPath(sockPath)
}

// SetAgentRecoveryInfo sets the recovery metadata on a running agent's
// RunProcess. Returns false if the agent is not in the processes map.
func (m *ProcessManager) SetAgentRecoveryInfo(key string, info *RecoveryInfo) bool {
	m.mu.RLock()
	runProc, exists := m.processes[key]
	m.mu.RUnlock()

	if !exists {
		return false
	}

	runProc.Recovery = info
	return true
}

// syncSessionInfo reads state.json from stateDir and persists SessionID and
// EventPath into the agent's DB record. Best-effort: logs and returns on error.
func (m *ProcessManager) syncSessionInfo(ctx context.Context, workspace, name, stateDir string, logger *slog.Logger) {
	state, err := spec.ReadState(stateDir)
	if err != nil {
		logger.Info("syncSessionInfo: could not read state.json, skipping",
			"workspace", workspace, "name", name, "error", err)
		return
	}
	if state.SessionID == "" {
		return
	}
	eventPath := spec.SessionEventLogPath(stateDir, state.SessionID)
	if err := m.agents.UpdateSessionInfo(ctx, workspace, name, state.SessionID, eventPath); err != nil {
		logger.Warn("syncSessionInfo: failed to persist session info",
			"workspace", workspace, "name", name, "error", err)
	}
}

// appendPromptSection appends a section to the system prompt with double newline separator.
func appendPromptSection(base, section string) string {
	if base == "" {
		return section
	}
	return base + "\n---\n" + section
}

func identityPrompt(workspaceName, agentName, workspacePath string) string {
	path := workspacePath
	if path == "" {
		path = "(unknown)"
	}
	return fmt.Sprintf(`<identity>
You are %s, an agent in workspace %q. Path: %s
</identity>`, agentName, workspaceName, path)
}

func workspaceMeshMCPPrompt() string {
	return fmt.Sprintf(`<%s>
Use workspace-mesh MCP to collaborate with other agents.
</%s>`,
		pkgariapi.WorkspaceMeshName,
		pkgariapi.WorkspaceMeshName,
	)
}

func workflowPrompt(workflowPath string) string {
	return fmt.Sprintf(`<workflow>
Follow workflow instructions in file %s.
</workflow>`, workflowPath)
}

// agentTaskPrompt returns the AgentTask feature system prompt snippet.
func agentTaskPrompt() string {
	return `<agent-task-protocol>
You may receive a task file path. Read the JSON file and treat all fields in "request" as your input and instructions.

When done, report the result by running:
massctl agentrun task done --file {task-path} --reason {reason} --response '{json}'

- {task-path}: the path passed to you
- {reason}: outcome summary (e.g. success, failed, needs_human)
- {json}: any JSON object describing the result
</agent-task-protocol>`
}
