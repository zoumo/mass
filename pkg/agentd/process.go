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

	"github.com/open-agent-d/open-agent-d/pkg/events"
	"github.com/open-agent-d/open-agent-d/pkg/meta"
	"github.com/open-agent-d/open-agent-d/pkg/spec"
)

// EventHandler is called for each event received from the shim via
// session/update notifications. Handlers must be registered before calling
// Subscribe.
type EventHandler func(ctx context.Context, ev events.Event)

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
//   - Runtime class resolution
//   - Bundle creation (config.json + workspace symlink)
//   - Shim process fork/exec
//   - ShimClient connection and event subscription
type ProcessManager struct {
	registry *RuntimeClassRegistry
	agents   *AgentManager
	store    *meta.Store
	config   Config

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

	// Events is a channel receiving events from the shim.
	// Events are delivered after Subscribe is called.
	Events chan events.Event

	// Done is closed when the shim process exits.
	Done chan struct{}

	// Recovery holds per-agent recovery metadata. Nil for agents that
	// were started normally (not recovered after a daemon restart).
	Recovery *RecoveryInfo
}

// NewProcessManager creates a new ProcessManager.
func NewProcessManager(registry *RuntimeClassRegistry, agents *AgentManager, store *meta.Store, cfg Config) *ProcessManager {
	logger := slog.Default().With("component", "agentd.process")
	return &ProcessManager{
		registry:  registry,
		agents:    agents,
		store:     store,
		config:    cfg,
		processes: make(map[string]*ShimProcess),
		logger:    logger,
	}
}

// buildNotifHandler returns a NotificationHandler that:
//   - routes session/update events to shimProc.Events (async, non-blocking), and
//   - handles runtime/stateChange notifications by updating DB agent state (D088).
//
// This is the single authoritative handler used by both Start() and recoverAgent().
// All DB state transitions after bootstrap must flow through here, never via a
// direct UpdateStatus call in the caller.
func (m *ProcessManager) buildNotifHandler(workspace, name string, shimProc *ShimProcess) NotificationHandler {
	key := agentKey(workspace, name)
	logger := m.logger.With("agent_key", key)
	return func(ctx context.Context, method string, params json.RawMessage) {
		switch method {
		case events.MethodSessionUpdate:
			p, err := ParseSessionUpdate(params)
			if err != nil {
				logger.Warn("malformed session/update notification dropped", "error", err)
				return
			}
			ev, ok := p.Event.Payload.(events.Event)
			if !ok {
				logger.Warn("session/update payload is not an events.Event — dropped",
					"type", p.Event.Type)
				return
			}
			select {
			case shimProc.Events <- ev:
			default:
				logger.Warn("event channel full, dropping event", "seq", p.Seq)
			}

		case events.MethodRuntimeStateChange:
			p, err := ParseRuntimeStateChange(params)
			if err != nil {
				logger.Warn("stateChange: malformed notification dropped", "error", err)
				return
			}
			prevStatus := p.PreviousStatus
			newStatus := p.Status
			logger.Info("stateChange: updating DB state",
				"agent_key", key,
				"prev", prevStatus,
				"new", newStatus)
			updateCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := m.agents.UpdateStatus(updateCtx, workspace, name, meta.AgentStatus{
				State:          spec.Status(newStatus),
				ShimSocketPath: shimProc.SocketPath,
				ShimStateDir:   shimProc.StateDir,
				ShimPID:        shimProc.PID,
			}); err != nil {
				logger.Warn("stateChange: failed to update DB state",
					"agent_key", key,
					"error", err)
			}
		}
	}
}

// Start creates and starts a shim process for the given agent.
// The full workflow:
//  1. Get Agent from AgentManager
//  2. Resolve RuntimeClass from registry
//  3. Generate config.json
//  4. Create bundle directory with workspace symlink
//  5. Fork agent-shim process
//  6. Wait for socket to appear
//  7. Connect ShimClient with the unified notification handler (D088)
//  8. Subscribe to events
//
// After Subscribe, the shim emits runtime/stateChange creating→idle once the
// ACP handshake completes; the notification handler updates DB state
// asynchronously per D088 — callers must not assume StatusRunning immediately.
//
// Returns ShimProcess on success, or error on failure.
// On failure, any partial state (bundle dir, process) is cleaned up.
func (m *ProcessManager) Start(ctx context.Context, workspace, name string) (*ShimProcess, error) {
	key := agentKey(workspace, name)
	m.logger.Info("starting agent", "agent_key", key)

	// 1. Get Agent from AgentManager.
	agent, err := m.agents.Get(ctx, workspace, name)
	if err != nil {
		return nil, fmt.Errorf("process: get agent %s: %w", key, err)
	}
	if agent == nil {
		return nil, fmt.Errorf("process: agent %s does not exist", key)
	}

	// Validate agent status - must be "creating" to start.
	if agent.Status.State != spec.StatusCreating {
		return nil, fmt.Errorf("process: agent %s is in state %s (must be 'creating' to start)", key, agent.Status.State)
	}

	// 2. Resolve RuntimeClass from registry.
	runtimeClass, err := m.registry.Get(agent.Spec.RuntimeClass)
	if err != nil {
		return nil, fmt.Errorf("process: resolve runtime class %s: %w", agent.Spec.RuntimeClass, err)
	}

	// 3. Generate config.json for this agent.
	cfg := m.generateConfig(agent, runtimeClass)

	// 4. Create bundle directory with workspace symlink.
	bundlePath, stateDir, socketPath, err := m.createBundle(agent, cfg)
	if err != nil {
		return nil, fmt.Errorf("process: create bundle: %w", err)
	}

	// 5. Fork agent-shim process.
	shimProc, err := m.forkShim(agent, bundlePath, stateDir)
	if err != nil {
		// Clean up bundle directory on fork failure.
		_ = os.RemoveAll(bundlePath)
		return nil, fmt.Errorf("process: fork shim: %w", err)
	}

	// Set paths on ShimProcess.
	shimProc.BundlePath = bundlePath
	shimProc.StateDir = stateDir
	shimProc.SocketPath = socketPath

	// 6. Wait for socket to appear (poll with timeout).
	if err := m.waitForSocket(ctx, socketPath); err != nil {
		// Kill shim process and clean up.
		_ = m.killShim(shimProc)
		_ = os.RemoveAll(bundlePath)
		return nil, fmt.Errorf("process: wait for socket: %w", err)
	}

	// 7. Connect ShimClient with the unified notification handler.
	// Routes session/update → shimProc.Events and runtime/stateChange → DB (D088).
	client, err := DialWithHandler(ctx, socketPath, m.buildNotifHandler(workspace, name, shimProc))
	if err != nil {
		// Kill shim process and clean up.
		_ = m.killShim(shimProc)
		_ = os.RemoveAll(bundlePath)
		return nil, fmt.Errorf("process: connect shim client: agent=%s: %w", key, err)
	}
	shimProc.Client = client

	// 7b. Persist bootstrap config + shim socket/state/pid for recovery.
	bootstrapJSON, err := json.Marshal(cfg)
	if err != nil {
		m.logger.Error("failed to marshal bootstrap config", "agent_key", key, "error", err)
		// Non-fatal: agent can still run, just won't have recovery data.
	} else {
		if err := m.store.UpdateAgentStatus(ctx, workspace, name, meta.AgentStatus{
			State:           spec.StatusCreating, // keep creating until we transition below
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
		// Close client, kill shim, clean up.
		_ = client.Close()
		_ = m.killShim(shimProc)
		_ = os.RemoveAll(bundlePath)
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
		if statusResult.State.Status != spec.StatusCreating {
			updateCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := m.agents.UpdateStatus(updateCtx, workspace, name, meta.AgentStatus{
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
func (m *ProcessManager) generateConfig(agent *meta.Agent, rc *RuntimeClass) spec.Config {
	// Build environment variables in KEY=VALUE format.
	// Merge runtime class env with any agent-specific env.
	env := make([]string, 0, len(rc.Env))
	for key, value := range rc.Env {
		env = append(env, fmt.Sprintf("%s=%s", key, value))
	}

	// Build annotations from agent labels.
	annotations := make(map[string]string)
	for k, v := range agent.Metadata.Labels {
		annotations[k] = v
	}
	annotations["runtimeClass"] = rc.Name

	return spec.Config{
		OarVersion: "0.1.0",
		Metadata: spec.Metadata{
			Name:        agent.Metadata.Name,
			Annotations: annotations,
		},
		AgentRoot: spec.AgentRoot{
			Path: "workspace", // symlink to actual workspace
		},
		AcpAgent: spec.AcpAgent{
			SystemPrompt: agent.Spec.SystemPrompt,
			Process: spec.AcpProcess{
				Command: rc.Command,
				Args:    rc.Args,
				Env:     env,
			},
			Session: spec.AcpSession{},
		},
		Permissions: spec.ApproveAll,
	}
}

// createBundle creates the bundle directory and writes config.json.
// Also creates the workspace symlink (agentRoot.path -> actual workspace).
// Returns bundlePath, stateDir, socketPath.
func (m *ProcessManager) createBundle(agent *meta.Agent, cfg spec.Config) (string, string, string, error) {
	// Bundle directory: <WorkspaceRoot>/<workspace>/<name>
	dirFragment := agent.Metadata.Workspace + "-" + agent.Metadata.Name
	bundlePath := filepath.Join(m.config.WorkspaceRoot, dirFragment)

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

	// State directory.
	// Use /tmp for shorter paths (macOS has ~107 char limit for Unix sockets).
	stateDir := spec.StateDir("/tmp/agentd-shim", dirFragment)
	if m.config.Socket == "" {
		stateDir = spec.StateDir("/run/agentd/shim", dirFragment)
	}

	// Socket path.
	socketPath := spec.ShimSocketPath(stateDir)

	m.logger.Debug("bundle created", "bundle_path", bundlePath, "state_dir", stateDir, "socket_path", socketPath)

	return bundlePath, stateDir, socketPath, nil
}

// forkShim forks the agent-shim process.
// Note: We intentionally do NOT use exec.CommandContext here because the shim
// process should run independently of the request context that initiated Start.
// Using CommandContext would kill the shim when the request context is canceled.
// The shim process lifecycle is managed by ProcessManager.Stop and watchProcess.
func (m *ProcessManager) forkShim(agent *meta.Agent, bundlePath, stateDir string) (*ShimProcess, error) {
	// Find the agent-shim binary.
	// Priority:
	//  1. OAR_SHIM_BINARY env var (test override)
	//  2. ./bin/agent-shim relative to current working directory (development)
	//  3. PATH lookup (production)
	var shimBinary string
	if envPath := os.Getenv("OAR_SHIM_BINARY"); envPath != "" {
		shimBinary = envPath
	} else if cwd, err := os.Getwd(); err == nil {
		builtPath := filepath.Join(cwd, "bin", "agent-shim")
		if _, err := os.Stat(builtPath); err == nil {
			shimBinary = builtPath
		}
	}
	if shimBinary == "" {
		shimBinary = "agent-shim" // PATH lookup
	}

	// Create state directory before starting shim.
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir state dir %s: %w", stateDir, err)
	}

	// Remove any stale socket file from a previous run so the new shim can bind.
	_ = os.Remove(spec.ShimSocketPath(stateDir))

	key := agentKey(agent.Metadata.Workspace, agent.Metadata.Name)

	// Build command arguments.
	// Pass filepath.Base(stateDir) as --id so the shim computes the same
	// stateDir as agentd expects. stateDir uses workspace-name (hyphenated),
	// while agentKey uses workspace/name (slash); mismatching them causes the
	// socket path to diverge and waitForSocket to time out.
	args := []string{
		"--bundle", bundlePath,
		"--id", filepath.Base(stateDir),
		"--state-dir", filepath.Dir(stateDir), // parent dir, shim adds /<id>
		"--permissions", "approve-all",
	}

	// Log the command for debugging.
	m.logger.Info("forking shim process", "shim_binary", shimBinary, "args", args, "state_dir", stateDir, "bundle_path", bundlePath)

	// Create exec.Cmd WITHOUT tying to the request context.
	cmd := exec.Command(shimBinary, args...)
	// In test mode, pipe stderr to os.Stderr for debugging.
	if m.config.Socket != "" {
		cmd.Stderr = os.Stderr
	} else {
		cmd.Stderr = nil // discard stderr for production
	}
	cmd.Stdout = nil // discard stdout (shim logs to stderr via slog)

	// Start the process.
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start shim process: %w", err)
	}

	m.logger.Debug("shim forked", "agent_key", key, "pid", cmd.Process.Pid)

	return &ShimProcess{
		AgentKey: key,
		PID:      cmd.Process.Pid,
		Cmd:      cmd,
		Events:   make(chan events.Event, 1024), // buffered for async delivery
		Done:     make(chan struct{}),
	}, nil
}

// waitForSocket waits for the shim's RPC socket to appear.
// Polls with a 20s timeout — real CLI runtimes (gsd-pi, claude-code) need
// npm resolution + ACP handshake which can take 10-20s.
func (m *ProcessManager) waitForSocket(ctx context.Context, socketPath string) error {
	timeout := 20 * time.Second

	deadline := time.Now().Add(timeout)
	for {
		// Check if socket exists.
		if _, err := os.Stat(socketPath); err == nil {
			// Socket exists, try to connect to verify it's ready.
			conn, err := net.Dial("unix", socketPath)
			if err == nil {
				_ = conn.Close()
				return nil
			}
		}

		// Check if timeout expired.
		if time.Now().After(deadline) {
			return fmt.Errorf("socket %s not ready after %v", socketPath, timeout)
		}

		// Check if context canceled.
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Wait a bit before polling again.
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
func (m *ProcessManager) watchProcess(workspace, name string, shimProc *ShimProcess) {
	// Wait for process to exit.
	err := shimProc.Cmd.Wait()
	key := shimProc.AgentKey

	m.logger.Info("shim process exited", "agent_key", key, "error", err)

	// Close the Events channel first.
	close(shimProc.Events)

	// Remove from processes map.
	m.mu.Lock()
	delete(m.processes, key)
	m.mu.Unlock()

	// Transition agent to "stopped" (best effort).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = m.agents.UpdateStatus(ctx, workspace, name, meta.AgentStatus{State: spec.StatusStopped})

	// Clean up bundle directory (best effort).
	_ = os.RemoveAll(shimProc.BundlePath)

	// Close the Done channel LAST to signal all cleanup is complete.
	close(shimProc.Done)
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
		if agent.Status.State != spec.StatusStopped {
			if err := m.agents.UpdateStatus(ctx, workspace, name, meta.AgentStatus{State: spec.StatusStopped}); err != nil {
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

	// Clean up bundle directory if it still exists.
	if shimProc.BundlePath != "" {
		_ = os.RemoveAll(shimProc.BundlePath)
	}

	m.logger.Info("agent stopped", "agent_key", key)
	return nil
}

// State returns the current runtime state of the shim for the given agent.
// Returns an error if the agent is not running or the response is malformed.
func (m *ProcessManager) State(ctx context.Context, workspace, name string) (spec.State, error) {
	key := agentKey(workspace, name)
	m.mu.RLock()
	shimProc, exists := m.processes[key]
	m.mu.RUnlock()

	if !exists {
		return spec.State{}, fmt.Errorf("process: agent %s is not running", key)
	}

	if shimProc.Client == nil {
		return spec.State{}, fmt.Errorf("process: agent %s has no client connection", key)
	}

	status, err := shimProc.Client.Status(ctx)
	if err != nil {
		return spec.State{}, fmt.Errorf("process: runtime/status for agent %s: %w", key, err)
	}

	return status.State, nil
}

// RuntimeStatus returns the full runtime/status result including recovery
// metadata for the given agent.
func (m *ProcessManager) RuntimeStatus(ctx context.Context, workspace, name string) (RuntimeStatusResult, error) {
	key := agentKey(workspace, name)
	m.mu.RLock()
	shimProc, exists := m.processes[key]
	m.mu.RUnlock()

	if !exists {
		return RuntimeStatusResult{}, fmt.Errorf("process: agent %s is not running", key)
	}

	if shimProc.Client == nil {
		return RuntimeStatusResult{}, fmt.Errorf("process: agent %s has no client connection", key)
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
