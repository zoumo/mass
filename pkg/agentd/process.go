// Package agentd implements the agent daemon that manages agent runtime lifecycle.
// This file defines the ProcessManager for managing shim process lifecycle.
package agentd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zoumo/oar/api"
	apiari "github.com/zoumo/oar/api/ari"
	apiruntime "github.com/zoumo/oar/api/runtime"
	"github.com/zoumo/oar/pkg/events"
	"github.com/zoumo/oar/api/shim"
	"github.com/zoumo/oar/pkg/spec"
	"github.com/zoumo/oar/pkg/store"
)

// EventHandler is called for each shim/event notification received from the shim.
// Handlers must be registered before calling Subscribe.
type EventHandler func(ctx context.Context, update events.ShimEvent)

// agentKey returns the composite map key for an agent: workspace+"/"+name.
// This matches the bbolt key path convention used by the meta store.
func agentKey(workspace, name string) string {
	return workspace + "/" + name
}

// ────────────────────────────────────────────────────────────────────────────
// ProcessManager - manages shim process lifecycle
// ────────────────────────────────────────────────────────────────────────────

// ProcessManager manages the lifecycle of agent-shim processes.
// It orchestrates:
//   - Agent status transitions
//   - Runtime entity resolution from DB
//   - Bundle creation (config.json + workspace symlink)
//   - Shim process fork/exec (self-fork or OAR_SHIM_BINARY override)
//   - ShimClient connection and event subscription
type ProcessManager struct {
	agents     *AgentRunManager
	store      *store.Store
	socketPath string
	bundleRoot string
	logLevel   string // propagated to shim and workspace-mcp child processes
	logFormat  string // propagated to shim and workspace-mcp child processes

	mu        sync.RWMutex
	processes map[string]*ShimProcess // agentKey (workspace+"/"+name) -> ShimProcess

	// recoveryPhase tracks the daemon-level recovery lifecycle as an atomic
	// int32 so it can be read cheaply without acquiring mu. Guards in ARI
	// handlers check this on every operational request.
	recoveryPhase atomic.Int32

	logger *slog.Logger
}

// ShimProcess tracks a running shim process and its RPC client.
type ShimProcess struct {
	// AgentKey is the composite agent identifier: workspace+"/"+name.
	AgentKey string

	// PID is the OS process ID of the shim process.
	PID int

	// BundlePath is the absolute path to the bundle directory.
	BundlePath string

	// StateDir is the absolute path to the shim's state directory.
	StateDir string

	// SocketPath is the absolute path to the shim's RPC socket.
	SocketPath string

	// Client is the connected ShimClient for RPC communication.
	Client *ShimClient

	// Cmd is the exec.Cmd for the shim process (for Wait/Kill).
	Cmd *exec.Cmd

	// Events is a channel receiving ordered ShimEvents from the shim.
	// A default drain goroutine consumes events when no external reader is active.
	Events chan events.ShimEvent

	// Done is closed when the shim process exits and all cleanup is complete.
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
	logger = logger.With("component", "agentd.process")
	return &ProcessManager{
		agents:     agents,
		store:      s,
		socketPath: socketPath,
		bundleRoot: bundleRoot,
		logLevel:   logLevel,
		logFormat:  logFormat,
		processes:  make(map[string]*ShimProcess),
		logger:     logger,
	}
}

// buildNotifHandler returns a NotificationHandler that:
//   - routes session/update params to shimProc.Events (async, non-blocking), and
//   - handles runtime/state_change notifications by updating DB agent state (D088).
//
// This is the single authoritative handler used by both Start() and recoverAgent().
// All DB state transitions after bootstrap must flow through here, never via a
// direct UpdateStatus call in the caller.
func (m *ProcessManager) buildNotifHandler(workspace, name string, shimProc *ShimProcess) NotificationHandler {
	key := agentKey(workspace, name)
	logger := m.logger.With("agent_key", key)
	return func(ctx context.Context, method string, params json.RawMessage) {
		if method != api.MethodShimEvent {
			return
		}
		ev, err := ParseShimEvent(params)
		if err != nil {
			logger.Warn("malformed shim/event notification dropped", "error", err)
			return
		}

		// Route by category/type.
		if ev.Category == api.CategoryRuntime && ev.Type == api.EventTypeStateChange {
			// state_change → update DB agent state.
			sc, ok := ev.Content.(events.StateChangeEvent)
			if !ok {
				logger.Warn("stateChange: content type assertion failed", "agent_key", key)
				return
			}
			prevStatus := sc.PreviousStatus
			newStatus := sc.Status
			logger.Info("stateChange: updating DB state",
				"agent_key", key,
				"prev", prevStatus,
				"new", newStatus)
			updateCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			current, err := m.agents.Get(updateCtx, workspace, name)
			if err != nil {
				logger.Warn("stateChange: failed to read DB state",
					"agent_key", key,
					"error", err)
				return
			}
			if current != nil && current.Status.State == api.StatusStopped && api.Status(newStatus) != api.StatusStopped {
				logger.Info("stateChange: dropped stale live state after stop",
					"agent_key", key,
					"current", current.Status.State,
					"new", newStatus)
				return
			}
			if err := m.agents.UpdateStatus(updateCtx, workspace, name, apiari.AgentRunStatus{
				State:          api.Status(newStatus),
				ShimSocketPath: shimProc.SocketPath,
				ShimStateDir:   shimProc.StateDir,
				ShimPID:        shimProc.PID,
			}); err != nil {
				logger.Warn("stateChange: failed to update DB state",
					"agent_key", key,
					"error", err)
			}
			return
		}

		// All other events → push to the Events channel for external consumers.
		select {
		case shimProc.Events <- ev:
		default:
			// No consumer is draining Events; drop silently to avoid log spam.
		}
	}
}

// Start creates and starts a shim process for the given agent.
// The full workflow:
//  1. Get AgentRun from AgentRunManager
//  2. Resolve Agent definition from DB store via GetAgent
//  3. Generate config.json
//  4. Create bundle directory with workspace symlink
//  5. Fork agent-shim process (self-fork or OAR_SHIM_BINARY override)
//  6. Wait for socket to appear
//  7. Connect ShimClient with the unified notification handler (D088)
//  8. Subscribe to events
//
// After Subscribe, the shim emits runtime/state_change creating→idle once the
// ACP handshake completes; the notification handler updates DB state
// asynchronously per D088 — callers must not assume StatusRunning immediately.
//
// Returns ShimProcess on success, or error on failure.
// On failure, any partial state (bundle dir, process) is cleaned up.
func (m *ProcessManager) Start(ctx context.Context, workspace, name string) (*ShimProcess, error) {
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
	if agent.Status.State != api.StatusCreating {
		return nil, fmt.Errorf("process: agent %s is in state %s (must be 'creating' to start)", key, agent.Status.State)
	}

	// 2. Resolve Agent definition from DB.
	agentDef, err := m.store.GetAgent(ctx, agent.Spec.Agent)
	if err != nil {
		return nil, fmt.Errorf("process: get agent definition %s: %w", agent.Spec.Agent, err)
	}
	if agentDef == nil {
		return nil, fmt.Errorf("process: agent definition %s not found", agent.Spec.Agent)
	}

	// 3. Generate config.json for this agent run.
	cfg := m.generateConfig(agent, agentDef)

	// 4. Create bundle directory with workspace symlink.
	bundlePath, stateDir, socketPath, err := m.createBundle(agent, cfg)
	if err != nil {
		return nil, fmt.Errorf("process: create bundle: %w", err)
	}
	if err := spec.ValidateShimSocketPath(socketPath); err != nil {
		return nil, err
	}

	// 5. Fork agent-shim process.
	shimProc, err := m.forkShim(agent, bundlePath, stateDir)
	if err != nil {
		return nil, fmt.Errorf("process: fork shim: %w", err)
	}

	// Set paths on ShimProcess.
	shimProc.BundlePath = bundlePath
	shimProc.StateDir = stateDir
	shimProc.SocketPath = socketPath

	// Close Done as soon as the OS process exits so waitForSocket can fail fast
	// on early crash. Full cleanup (map removal, DB update) runs in the
	// watchProcess goroutine started after successful bootstrap.
	go func() {
		shimProc.exitErr = shimProc.Cmd.Wait()
		close(shimProc.Done)
	}()

	// 6. Wait for socket to appear (poll with timeout).
	if err := m.waitForSocket(ctx, socketPath, shimProc); err != nil {
		// Kill shim process; leave bundle intact (preserved until agent/delete).
		_ = m.killShim(shimProc)
		return nil, fmt.Errorf("process: wait for socket: %w", err)
	}

	// 7. Connect ShimClient with the unified notification handler.
	// Routes session/update → shimProc.Events and runtime/state_change → DB (D088).
	client, err := DialWithHandler(ctx, socketPath, m.buildNotifHandler(workspace, name, shimProc))
	if err != nil {
		// Kill shim process; leave bundle intact (preserved until agent/delete).
		_ = m.killShim(shimProc)
		return nil, fmt.Errorf("process: connect shim client: agent=%s: %w", key, err)
	}
	shimProc.Client = client

	// 7b. Persist bootstrap config + shim socket/state/pid for recovery.
	bootstrapJSON, err := json.Marshal(cfg)
	if err != nil {
		m.logger.Error("failed to marshal bootstrap config", "agent_key", key, "error", err)
		// Non-fatal: agent can still run, just won't have recovery data.
	} else {
		if err := m.store.UpdateAgentRunStatus(ctx, workspace, name, apiari.AgentRunStatus{
			State:           api.StatusCreating, // keep creating until we transition below
			ShimSocketPath:  socketPath,
			ShimStateDir:    stateDir,
			ShimPID:         shimProc.PID,
			BootstrapConfig: bootstrapJSON,
		}); err != nil {
			m.logger.Error("failed to persist bootstrap config", "agent_key", key, "error", err)
			// Non-fatal: agent can still run.
		}
	}

	// 8. Subscribe to events (no afterSeq — this is a fresh start).
	if _, err := client.Subscribe(ctx, nil, nil); err != nil {
		// Close client, kill shim; leave bundle intact (preserved until agent/delete).
		_ = client.Close()
		_ = m.killShim(shimProc)
		return nil, fmt.Errorf("process: subscribe events: agent=%s: %w", key, err)
	}

	// 8b. Bootstrap state from shim's current runtime status.
	// The shim's Create() may have already transitioned creating→idle before
	// the Subscribe call, causing the stateChange notification to be missed
	// (SetStateChangeHook in the shim is registered after Create() returns,
	// so the hook is nil during the initial transition). Reading runtime/status
	// here ensures the DB reflects the actual state even when the notification
	// was dropped.
	if statusResult, statusErr := client.Status(ctx); statusErr == nil {
		if statusResult.State.Status != api.StatusCreating {
			updateCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := m.agents.UpdateStatus(updateCtx, workspace, name, apiari.AgentRunStatus{
				State:          statusResult.State.Status,
				ShimSocketPath: socketPath,
				ShimStateDir:   stateDir,
				ShimPID:        shimProc.PID,
			}); err != nil {
				m.logger.Warn("bootstrap state sync failed",
					"agent_key", key, "state", statusResult.State.Status, "error", err)
			} else {
				m.logger.Info("bootstrap state synced from shim",
					"agent_key", key, "state", statusResult.State.Status)
			}
		}
	}

	// Store the ShimProcess.
	m.mu.Lock()
	m.processes[key] = shimProc
	m.mu.Unlock()

	// Start a goroutine to wait for process exit and clean up.
	go m.watchProcess(workspace, name, shimProc)

	m.logger.Info("agent started", "agent_key", key, "pid", shimProc.PID)

	return shimProc, nil
}

// generateConfig creates the OAR Runtime config.json for this agent.
func (m *ProcessManager) generateConfig(agent *apiari.AgentRun, agentDef *apiari.Agent) apiruntime.Config {
	// Build environment variables in KEY=VALUE format from the Agent definition.
	env := make([]string, 0, len(agentDef.Spec.Env))
	for _, ev := range agentDef.Spec.Env {
		env = append(env, fmt.Sprintf("%s=%s", ev.Name, ev.Value))
	}

	// Build annotations from agent labels.
	annotations := make(map[string]string)
	for k, v := range agent.Metadata.Labels {
		annotations[k] = v
	}
	annotations["agent"] = agentDef.Metadata.Name

	// Compute the bundle/state directory (same formula as createBundle) so we
	// can pass OAR_STATE_DIR to the workspace-mcp-server before the directory
	// is actually created.
	stateDir := filepath.Join(m.bundleRoot, agent.Metadata.Workspace+"-"+agent.Metadata.Name)

	mcpBinary, mcpArgs := m.workspaceMcpCommand()
	workspaceMcp := apiruntime.McpServer{
		Type:    "stdio",
		Name:    "workspace",
		Command: mcpBinary,
		Args:    mcpArgs,
		Env: []api.EnvVar{
			{Name: "OAR_AGENTD_SOCKET", Value: m.socketPath},
			{Name: "OAR_WORKSPACE_NAME", Value: agent.Metadata.Workspace},
			{Name: "OAR_AGENT_NAME", Value: agent.Metadata.Name},
			{Name: "OAR_STATE_DIR", Value: stateDir},
			{Name: "OAR_LOG_LEVEL", Value: m.logLevel},
			{Name: "OAR_LOG_FORMAT", Value: m.logFormat},
		},
	}

	return apiruntime.Config{
		OarVersion: "0.1.0",
		Metadata: apiruntime.Metadata{
			Name:        agent.Metadata.Name,
			Annotations: annotations,
		},
		AgentRoot: apiruntime.AgentRoot{
			Path: "workspace", // symlink to actual workspace
		},
		AcpAgent: apiruntime.AcpAgent{
			SystemPrompt: agent.Spec.SystemPrompt,
			Process: apiruntime.AcpProcess{
				Command: agentDef.Spec.Command,
				Args:    agentDef.Spec.Args,
				Env:     env,
			},
			Session: apiruntime.AcpSession{
				McpServers: []apiruntime.McpServer{workspaceMcp},
			},
		},
		Permissions: apiruntime.ApproveAll,
	}
}

// workspaceMcpCommand returns the command and args for the workspace MCP server.
// Uses self-fork: os.Executable() + "workspace-mcp" subcommand (same pattern as shim).
func (m *ProcessManager) workspaceMcpCommand() (string, []string) {
	self, err := os.Executable()
	if err != nil {
		m.logger.Error("os.Executable failed for workspace-mcp, falling back to PATH", "error", err)
		return "agentd", []string{"workspace-mcp"}
	}
	return self, []string{"workspace-mcp"}
}

// createBundle creates the bundle directory and writes config.json.
// Also creates the workspace symlink (agentRoot.path -> actual workspace).
// Returns bundlePath, stateDir, socketPath.
func (m *ProcessManager) createBundle(agent *apiari.AgentRun, cfg apiruntime.Config) (string, string, string, error) {
	// Bundle directory: <bundleRoot>/<workspace>-<name>
	dirFragment := agent.Metadata.Workspace + "-" + agent.Metadata.Name
	bundlePath := filepath.Join(m.bundleRoot, dirFragment)

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
	workspace, err := m.store.GetWorkspace(context.Background(), wsName)
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

	// State directory is co-located with the bundle directory.
	// All shim runtime files (agent-shim.sock, state.json, events.jsonl) live
	// inside the bundle so the entire agent lifecycle is in one place.
	stateDir := bundlePath

	// Socket path.
	socketPath := spec.ShimSocketPath(stateDir)

	m.logger.Debug("bundle created", "bundle_path", bundlePath, "state_dir", stateDir, "socket_path", socketPath)

	return bundlePath, stateDir, socketPath, nil
}

// forkShim forks the agent-shim process using self-fork or OAR_SHIM_BINARY override.
// Self-fork: uses os.Executable() to re-invoke the daemon with "shim" as the first arg.
// Override: if OAR_SHIM_BINARY is set, that binary is used instead.
//
// Note: We intentionally do NOT use exec.CommandContext here because the shim
// process should run independently of the request context that initiated Start.
// Using CommandContext would kill the shim when the request context is canceled.
// The shim process lifecycle is managed by ProcessManager.Stop and watchProcess.
func (m *ProcessManager) forkShim(agent *apiari.AgentRun, bundlePath, stateDir string) (*ShimProcess, error) {
	var shimBinary string
	var usingOverride bool

	if envPath := os.Getenv("OAR_SHIM_BINARY"); envPath != "" {
		shimBinary = envPath
		usingOverride = true
	} else {
		// Self-fork: use the current executable.
		self, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("os.Executable: %w", err)
		}
		shimBinary = self
		usingOverride = false
	}

	if usingOverride {
		m.logger.Info("forkShim: using OAR_SHIM_BINARY override", "shim_binary", shimBinary)
	} else {
		m.logger.Info("forkShim: using self-fork", "shim_binary", shimBinary)
	}

	// Create state directory before starting shim.
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir state dir %s: %w", stateDir, err)
	}

	// Remove any stale socket file from a previous run so the new shim can bind.
	_ = os.Remove(spec.ShimSocketPath(stateDir))

	key := agentKey(agent.Metadata.Workspace, agent.Metadata.Name)

	// Build command arguments.
	// stateDir == bundlePath, so --id is the bundle directory name and
	// --state-dir is bundleRoot. The shim computes: stateDir = parent/<id>,
	// which resolves back to bundlePath.
	// Prepend "shim" as the first arg so the process knows to behave as a shim.
	args := []string{
		"shim",
		"--bundle", bundlePath,
		"--id", filepath.Base(bundlePath),
		"--state-dir", filepath.Dir(bundlePath), // bundleRoot; shim appends /<id>
		"--permissions", string(apiruntime.ApproveAll),
	}

	// Log the command for debugging.
	m.logger.Info("forking shim process",
		"shim_binary", shimBinary,
		"args", args,
		"state_dir", stateDir,
		"bundle_path", bundlePath)

	// Create exec.Cmd WITHOUT tying to the request context.
	cmd := exec.Command(shimBinary, args...)
	cmd.Env = append(os.Environ(),
		"OAR_LOG_LEVEL="+m.logLevel,
		"OAR_LOG_FORMAT="+m.logFormat,
	)
	cmd.Stderr = os.Stderr // always pipe stderr for debugging
	cmd.Stdout = nil       // discard stdout (shim logs to stderr via slog)

	// Start the process.
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start shim process: %w", err)
	}

	m.logger.Debug("shim forked", "agent_key", key, "pid", cmd.Process.Pid)

	sp := &ShimProcess{
		AgentKey:  key,
		PID:       cmd.Process.Pid,
		Cmd:       cmd,
		Events:    make(chan events.ShimEvent, 1024), // buffered for async delivery
		Done:      make(chan struct{}),
		stopDrain: make(chan struct{}),
	}
	go sp.drainEvents()
	return sp, nil
}

// drainEvents is the default consumer that discards events from the Events
// channel. It runs until stopDrain or Done is closed, preventing the channel
// from filling up when no external consumer is reading.
func (sp *ShimProcess) drainEvents() {
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
func (sp *ShimProcess) StopDrain() {
	select {
	case <-sp.stopDrain:
		// already stopped
	default:
		close(sp.stopDrain)
	}
}

// waitForSocket waits for the shim's RPC socket to appear.
// Polls with a 90s timeout — real CLI runtimes (gsd-pi, claude-code) need
// bunx package resolution + process startup which can take 30-60s on cold cache.
// Returns early if the shim process exits before the socket appears.
func (m *ProcessManager) waitForSocket(ctx context.Context, socketPath string, shimProc *ShimProcess) error {
	timeout := 90 * time.Second

	deadline := time.Now().Add(timeout)
	for {
		// Check if socket exists and is connectable.
		if _, err := os.Stat(socketPath); err == nil {
			conn, err := net.Dial("unix", socketPath)
			if err == nil {
				_ = conn.Close()
				return nil
			}
		}

		// Fail fast if the shim process has already exited.
		select {
		case <-shimProc.Done:
			return fmt.Errorf("shim process exited before socket appeared at %s", socketPath)
		default:
		}

		// Check if timeout expired.
		if time.Now().After(deadline) {
			return fmt.Errorf("socket %s not ready after %v", socketPath, timeout)
		}

		// Check if context canceled.
		if ctx.Err() != nil {
			return ctx.Err()
		}

		time.Sleep(50 * time.Millisecond)
	}
}

// killShim kills the shim process if it's still running.
func (m *ProcessManager) killShim(shimProc *ShimProcess) error {
	if shimProc.Cmd == nil || shimProc.Cmd.Process == nil {
		return nil
	}

	// Try graceful shutdown first (SIGTERM).
	if err := shimProc.Cmd.Process.Signal(os.Interrupt); err != nil {
		// Process might already be dead.
		if err.Error() == "os: process already finished" {
			return nil
		}
		// Fall back to SIGKILL.
		_ = shimProc.Cmd.Process.Kill()
	}

	// Wait for process to exit with a short timeout.
	done := make(chan error, 1)
	go func() {
		done <- shimProc.Cmd.Wait()
	}()

	select {
	case <-time.After(2 * time.Second):
		// Timeout, force kill.
		_ = shimProc.Cmd.Process.Kill()
		<-done // Drain the channel.
	case err := <-done:
		return err
	}

	return nil
}

// watchProcess waits for the shim process to exit and cleans up.
// The process exit itself is detected by the lightweight goroutine started in
// Start(), which calls cmd.Wait() and closes shimProc.Done. This goroutine
// waits on Done and then performs full cleanup.
func (m *ProcessManager) watchProcess(workspace, name string, shimProc *ShimProcess) {
	// Wait for the lightweight goroutine to signal process exit.
	<-shimProc.Done
	key := shimProc.AgentKey

	m.logger.Info("shim process exited", "agent_key", key, "error", shimProc.exitErr)

	// Remove from processes map.
	m.mu.Lock()
	delete(m.processes, key)
	m.mu.Unlock()

	// Transition agent to "stopped" (best effort).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = m.agents.UpdateStatus(ctx, workspace, name, apiari.AgentRunStatus{State: api.StatusStopped})

	// Bundle directory is intentionally NOT cleaned up here.
	// It must persist until the agent is explicitly deleted via agent/delete.
}

// Stop gracefully stops a running shim process for the given agent.
// The workflow:
//  1. Get ShimProcess from processes map
//  2. Call ShimClient.Stop RPC to request graceful shutdown
//  3. Wait for process to exit (with timeout)
//  4. If timeout, kill the process
//  5. Remove bundle directory
//  6. Transition agent to "stopped"
//
// Returns error if the agent is not running or shutdown fails.
func (m *ProcessManager) Stop(ctx context.Context, workspace, name string) error {
	key := agentKey(workspace, name)
	m.logger.Info("stopping agent", "agent_key", key)

	// Get ShimProcess from processes map.
	m.mu.RLock()
	shimProc, exists := m.processes[key]
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
		if agent.Status.State != api.StatusStopped {
			if err := m.agents.UpdateStatus(ctx, workspace, name, apiari.AgentRunStatus{State: api.StatusStopped}); err != nil {
				return fmt.Errorf("process: transition agent %s to stopped: %w", key, err)
			}
		}
		return nil
	}

	// Call runtime/stop RPC to request graceful shutdown.
	if shimProc.Client != nil {
		stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if err := shimProc.Client.Stop(stopCtx); err != nil {
			m.logger.Warn("runtime/stop RPC failed, will kill process", "agent_key", key, "error", err)
		}
		cancel()
	}

	// Wait for process to exit.
	select {
	case <-shimProc.Done:
		m.logger.Info("shim process exited gracefully", "agent_key", key)
	case <-time.After(10 * time.Second):
		m.logger.Warn("shim process did not exit in time, killing", "agent_key", key)
		if err := m.killShim(shimProc); err != nil {
			m.logger.Error("failed to kill shim process", "agent_key", key, "error", err)
		}
		// Wait for watchProcess to clean up.
		<-shimProc.Done
	}

	// Bundle directory is intentionally NOT cleaned up here.
	// It must persist until the agent is explicitly deleted via agent/delete.

	m.logger.Info("agent stopped", "agent_key", key)
	return nil
}

// State returns the current runtime state of the shim for the given agent.
// Returns an error if the agent is not running or the response is malformed.
func (m *ProcessManager) State(ctx context.Context, workspace, name string) (apiruntime.State, error) {
	key := agentKey(workspace, name)
	m.mu.RLock()
	shimProc, exists := m.processes[key]
	m.mu.RUnlock()

	if !exists {
		return apiruntime.State{}, fmt.Errorf("process: agent %s is not running", key)
	}

	if shimProc.Client == nil {
		return apiruntime.State{}, fmt.Errorf("process: agent %s has no client connection", key)
	}

	status, err := shimProc.Client.Status(ctx)
	if err != nil {
		return apiruntime.State{}, fmt.Errorf("process: runtime/status for agent %s: %w", key, err)
	}

	return status.State, nil
}

// RuntimeStatus returns the full runtime/status result including recovery
// metadata for the given agent.
func (m *ProcessManager) RuntimeStatus(ctx context.Context, workspace, name string) (shim.RuntimeStatusResult, error) {
	key := agentKey(workspace, name)
	m.mu.RLock()
	shimProc, exists := m.processes[key]
	m.mu.RUnlock()

	if !exists {
		return shim.RuntimeStatusResult{}, fmt.Errorf("process: agent %s is not running", key)
	}

	if shimProc.Client == nil {
		return shim.RuntimeStatusResult{}, fmt.Errorf("process: agent %s has no client connection", key)
	}

	return shimProc.Client.Status(ctx)
}

// Connect returns the ShimClient for direct RPC access to the shim process.
// Returns error if the agent is not running.
func (m *ProcessManager) Connect(ctx context.Context, workspace, name string) (*ShimClient, error) {
	key := agentKey(workspace, name)
	m.mu.RLock()
	shimProc, exists := m.processes[key]
	m.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("process: agent %s is not running", key)
	}

	if shimProc.Client == nil {
		return nil, fmt.Errorf("process: agent %s has no client connection", key)
	}

	return shimProc.Client, nil
}

// GetProcess returns the ShimProcess for the given agent key (workspace+"/"+name).
// Returns nil if the agent is not running.
func (m *ProcessManager) GetProcess(agentKey string) *ShimProcess {
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

// InjectProcess inserts a pre-built ShimProcess into the processes map under
// the given key. Used in tests to inject a mock shim without calling Start().
func (m *ProcessManager) InjectProcess(key string, proc *ShimProcess) {
	m.mu.Lock()
	m.processes[key] = proc
	m.mu.Unlock()
}

// BundlePath returns the expected bundle directory path for the given agent.
// This path is deterministic and can be computed even when the shim is not running,
// allowing callers (e.g. agent/delete) to clean up the bundle after the process exits.
func (m *ProcessManager) BundlePath(workspace, name string) string {
	dirFragment := workspace + "-" + name
	return filepath.Join(m.bundleRoot, dirFragment)
}

// ValidateAgentSocketPath checks whether the would-be Unix socket path for the
// given agent would exceed the OS limit. The path is computed the same way
// createBundle does it — bundleRoot/<workspace>-<name>/agent-shim.sock — but
// without creating any files or directories.
//
// Call this before writing any DB records (e.g. in handleAgentCreate) so that
// a -32602 error is returned before any side effects.
func (m *ProcessManager) ValidateAgentSocketPath(workspace, name string) error {
	sockPath := filepath.Join(m.bundleRoot, workspace+"-"+name, "agent-shim.sock")
	return spec.ValidateShimSocketPath(sockPath)
}

// SetAgentRecoveryInfo sets the recovery metadata on a running agent's
// ShimProcess. Returns false if the agent is not in the processes map.
func (m *ProcessManager) SetAgentRecoveryInfo(key string, info *RecoveryInfo) bool {
	m.mu.RLock()
	shimProc, exists := m.processes[key]
	m.mu.RUnlock()

	if !exists {
		return false
	}

	shimProc.Recovery = info
	return true
}
