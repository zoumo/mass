// Package agentd implements the agent daemon that manages agent runtime sessions.
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

// ────────────────────────────────────────────────────────────────────────────
// ProcessManager - manages shim process lifecycle
// ────────────────────────────────────────────────────────────────────────────

// ProcessManager manages the lifecycle of agent-shim processes.
// It orchestrates:
//   - Session creation and state transitions
//   - Runtime class resolution
//   - Bundle creation (config.json + workspace symlink)
//   - Shim process fork/exec
//   - ShimClient connection and event subscription
type ProcessManager struct {
	registry *RuntimeClassRegistry
	sessions *SessionManager
	agents   *AgentManager
	store    *meta.Store
	config   Config

	mu        sync.RWMutex
	processes map[string]*ShimProcess // sessionID -> ShimProcess

	// recoveryPhase tracks the daemon-level recovery lifecycle as an atomic
	// int32 so it can be read cheaply without acquiring mu. Guards in ARI
	// handlers check this on every operational request.
	recoveryPhase atomic.Int32

	logger *slog.Logger
}

// ShimProcess tracks a running shim process and its RPC client.
type ShimProcess struct {
	// SessionID is the unique session identifier.
	SessionID string

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

	// Recovery holds per-session recovery metadata. Nil for sessions that
	// were started normally (not recovered after a daemon restart).
	Recovery *RecoveryInfo
}

// NewProcessManager creates a new ProcessManager.
func NewProcessManager(registry *RuntimeClassRegistry, sessions *SessionManager, agents *AgentManager, store *meta.Store, cfg Config) *ProcessManager {
	logger := slog.Default().With("component", "agentd.process")
	return &ProcessManager{
		registry:  registry,
		sessions:  sessions,
		agents:    agents,
		store:     store,
		config:    cfg,
		processes: make(map[string]*ShimProcess),
		logger:    logger,
	}
}

// Start creates and starts a shim process for the given session.
// The full workflow:
//  1. Get Session from SessionManager
//  2. Resolve RuntimeClass from registry
//  3. Generate config.json
//  4. Create bundle directory with workspace symlink
//  5. Fork agent-shim process
//  6. Wait for socket to appear
//  7. Connect ShimClient
//  8. Subscribe to events
//  9. Transition session state to "running"
//
// Returns ShimProcess on success, or error on failure.
// On failure, any partial state (bundle dir, process) is cleaned up.
func (m *ProcessManager) Start(ctx context.Context, sessionID string) (*ShimProcess, error) {
	m.logger.Info("starting session", "session_id", sessionID)

	// 1. Get Session from SessionManager.
	session, err := m.sessions.Get(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("process: get session %s: %w", sessionID, err)
	}
	if session == nil {
		return nil, fmt.Errorf("process: session %s does not exist", sessionID)
	}

	// Validate session state - must be "created" to start.
	if session.State != meta.SessionStateCreated {
		return nil, fmt.Errorf("process: session %s is in state %s (must be 'created' to start)", sessionID, session.State)
	}

	// 2. Resolve RuntimeClass from registry.
	runtimeClass, err := m.registry.Get(session.RuntimeClass)
	if err != nil {
		return nil, fmt.Errorf("process: resolve runtime class %s: %w", session.RuntimeClass, err)
	}

	// 3. Generate config.json for this session.
	cfg := m.generateConfig(session, runtimeClass)

	// 4. Create bundle directory with workspace symlink.
	bundlePath, stateDir, socketPath, err := m.createBundle(session, cfg)
	if err != nil {
		return nil, fmt.Errorf("process: create bundle: %w", err)
	}

	// 5. Fork agent-shim process.
	shimProc, err := m.forkShim(ctx, session, runtimeClass, bundlePath, stateDir)
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

	// 7. Connect ShimClient.
	client, err := DialWithHandler(ctx, socketPath, func(ctx context.Context, method string, params json.RawMessage) {
		// Dispatch session/update events to the ShimProcess.Events channel.
		// Non-blocking send to avoid blocking the RPC notification path.
		if method != events.MethodSessionUpdate {
			return
		}
		p, err := ParseSessionUpdate(params)
		if err != nil {
			m.logger.Warn("malformed session/update notification dropped",
				"session_id", sessionID, "error", err)
			return
		}
		ev, ok := p.Event.Payload.(events.Event)
		if !ok {
			m.logger.Warn("session/update payload is not an events.Event — dropped",
				"session_id", sessionID, "type", p.Event.Type)
			return
		}
		select {
		case shimProc.Events <- ev:
		default:
			m.logger.Warn("event channel full, dropping event",
				"session_id", sessionID, "seq", p.Seq)
		}
	})
	if err != nil {
		// Kill shim process and clean up.
		_ = m.killShim(shimProc)
		_ = os.RemoveAll(bundlePath)
		return nil, fmt.Errorf("process: connect shim client: session=%s: %w", sessionID, err)
	}
	shimProc.Client = client

	// 7b. Persist bootstrap config for recovery.
	// Marshal the generated config as the bootstrap config blob.
	bootstrapJSON, err := json.Marshal(cfg)
	if err != nil {
		m.logger.Error("failed to marshal bootstrap config", "session_id", sessionID, "error", err)
		// Non-fatal: session can still run, just won't have recovery data.
	} else {
		if err := m.store.UpdateSessionBootstrap(ctx, sessionID, bootstrapJSON, socketPath, stateDir, shimProc.PID); err != nil {
			m.logger.Error("failed to persist bootstrap config", "session_id", sessionID, "error", err)
			// Non-fatal: session can still run, recovery won't have the data.
		}
	}

	// 8. Subscribe to events (no afterSeq — this is a fresh start).
	if _, err := client.Subscribe(ctx, nil, nil); err != nil {
		// Close client, kill shim, clean up.
		_ = client.Close()
		_ = m.killShim(shimProc)
		_ = os.RemoveAll(bundlePath)
		return nil, fmt.Errorf("process: subscribe events: session=%s: %w", sessionID, err)
	}

	// 9. Transition session state to "running".
	if err := m.sessions.Transition(ctx, sessionID, meta.SessionStateRunning); err != nil {
		// Close client, kill shim, clean up.
		_ = client.Close()
		_ = m.killShim(shimProc)
		_ = os.RemoveAll(bundlePath)
		return nil, fmt.Errorf("process: transition session state: %w", err)
	}

	// Store the ShimProcess.
	m.mu.Lock()
	m.processes[sessionID] = shimProc
	m.mu.Unlock()

	// Start a goroutine to wait for process exit and clean up.
	go m.watchProcess(shimProc)

	m.logger.Info("session started", "session_id", sessionID, "pid", shimProc.PID)

	return shimProc, nil
}

// generateConfig creates the OAR Runtime config.json for this session.
func (m *ProcessManager) generateConfig(session *meta.Session, rc *RuntimeClass) spec.Config {
	// Build environment variables in KEY=VALUE format.
	// Merge runtime class env with any session-specific env.
	env := make([]string, 0, len(rc.Env))
	for key, value := range rc.Env {
		env = append(env, fmt.Sprintf("%s=%s", key, value))
	}

	// Build annotations from session labels.
	annotations := make(map[string]string)
	for k, v := range session.Labels {
		annotations[k] = v
	}
	annotations["runtimeClass"] = rc.Name

	// Build MCP servers list. Inject room MCP server for room sessions.
	var mcpServers []spec.McpServer
	if session.Room != "" {
		// Compute state dir the same way createBundle does, so room-mcp-server
		// can write its log file next to events.jsonl and state.json.
		stateDir := spec.StateDir("/tmp/agentd-shim", session.ID)

		mcpServers = append(mcpServers, spec.McpServer{
			Type:    "stdio",
			Name:    "room-tools",
			Command: resolveRoomMCPBinary(),
			Args:    []string{},
			Env: []spec.EnvVar{
				{Name: "OAR_AGENTD_SOCKET", Value: m.config.Socket},
				{Name: "OAR_ROOM_NAME", Value: session.Room},
				{Name: "OAR_AGENT_ID", Value: session.AgentID},     // agent-level identity
				{Name: "OAR_AGENT_NAME", Value: session.RoomAgent}, // agent name within room
				{Name: "OAR_STATE_DIR", Value: stateDir},
			},
		})
	}

	return spec.Config{
		OarVersion: "0.1.0",
		Metadata: spec.Metadata{
			Name:        session.ID,
			Annotations: annotations,
		},
		AgentRoot: spec.AgentRoot{
			Path: "workspace", // symlink to actual workspace
		},
		AcpAgent: spec.AcpAgent{
			Process: spec.AcpProcess{
				Command: rc.Command,
				Args:    rc.Args,
				Env:     env,
			},
			Session: spec.AcpSession{
				McpServers: mcpServers,
			},
		},
		Permissions: spec.ApproveAll,
	}
}

// resolveRoomMCPBinary finds the room-mcp-server binary using the same
// priority pattern as the shim binary resolver:
//  1. OAR_ROOM_MCP_BINARY env var (test override)
//  2. ./bin/room-mcp-server relative to cwd (development)
//  3. PATH lookup (production)
func resolveRoomMCPBinary() string {
	if envPath := os.Getenv("OAR_ROOM_MCP_BINARY"); envPath != "" {
		return envPath
	}
	if cwd, err := os.Getwd(); err == nil {
		builtPath := filepath.Join(cwd, "bin", "room-mcp-server")
		if _, err := os.Stat(builtPath); err == nil {
			return builtPath
		}
	}
	return "room-mcp-server" // PATH lookup
}

// createBundle creates the bundle directory and writes config.json.
// Also creates the workspace symlink (agentRoot.path -> actual workspace).
// Returns bundlePath, stateDir, socketPath.
func (m *ProcessManager) createBundle(session *meta.Session, cfg spec.Config) (string, string, string, error) {
	// Bundle directory: <WorkspaceRoot>/<sessionID>
	bundlePath := filepath.Join(m.config.WorkspaceRoot, session.ID)

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

	// Get workspace path from session.WorkspaceID.
	// We need to look up the workspace to get its path.
	workspace, err := m.store.GetWorkspace(context.Background(), session.WorkspaceID)
	if err != nil {
		_ = os.RemoveAll(bundlePath)
		return "", "", "", fmt.Errorf("get workspace %s: %w", session.WorkspaceID, err)
	}
	if workspace == nil {
		_ = os.RemoveAll(bundlePath)
		return "", "", "", fmt.Errorf("workspace %s does not exist", session.WorkspaceID)
	}

	// Create workspace symlink: bundle/workspace -> workspace.Path.
	workspaceLink := filepath.Join(bundlePath, cfg.AgentRoot.Path)
	// Remove any existing symlink/file first.
	_ = os.Remove(workspaceLink)
	if err := os.Symlink(workspace.Path, workspaceLink); err != nil {
		_ = os.RemoveAll(bundlePath)
		return "", "", "", fmt.Errorf("symlink workspace %s -> %s: %w", workspaceLink, workspace.Path, err)
	}

	// State directory: /run/agentd/shim/<sessionID> (or similar)
	// Use a temp directory for tests, production uses /run/agentd/shim.
	stateDir := spec.StateDir("/run/agentd/shim", session.ID)
	if m.config.Socket != "" {
		// If Socket is set, derive state dir from socket path pattern.
		// For testing, use /tmp for shorter paths (macOS has ~107 char limit for Unix sockets).
		stateDir = spec.StateDir("/tmp/agentd-shim", session.ID)
	}

	// Socket path.
	socketPath := spec.ShimSocketPath(stateDir)

	m.logger.Debug("bundle created", "bundle_path", bundlePath, "state_dir", stateDir, "socket_path", socketPath)

	return bundlePath, stateDir, socketPath, nil
}

// forkShim forks the agent-shim process.
// Note: We intentionally do NOT use exec.CommandContext here because the shim
// process should run independently of the request context that initiated Start.
// Using CommandContext would kill the shim when the request context is canceled
// (which happens immediately after Start returns in session/prompt auto-start).
// The shim process lifecycle is managed by ProcessManager.Stop and watchProcess.
func (m *ProcessManager) forkShim(ctx context.Context, session *meta.Session, rc *RuntimeClass, bundlePath, stateDir string) (*ShimProcess, error) {
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

	// Build command arguments.
	args := []string{
		"--bundle", bundlePath,
		"--id", session.ID,
		"--state-dir", filepath.Dir(stateDir), // parent dir, shim adds /<id>
		"--permissions", "approve-all",
	}

	// Log the command for debugging.
	m.logger.Info("forking shim process", "shim_binary", shimBinary, "args", args, "state_dir", stateDir, "bundle_path", bundlePath)

	// Create exec.Cmd WITHOUT tying to the request context.
	// Using exec.Command (not CommandContext) ensures the shim process
	// continues running after Start returns, independent of the request lifecycle.
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

	m.logger.Debug("shim forked", "session_id", session.ID, "pid", cmd.Process.Pid)

	return &ShimProcess{
		SessionID: session.ID,
		PID:       cmd.Process.Pid,
		Cmd:       cmd,
		Events:    make(chan events.Event, 1024), // buffered for async delivery
		Done:      make(chan struct{}),
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
			// Use net.Dial for Unix sockets (os.OpenFile doesn't work on socket files).
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

	// Wait for process to exit.
	// Use a short timeout to avoid hanging.
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
		// Process exited.
		return err
	}

	return nil
}

// watchProcess waits for the shim process to exit and cleans up.
func (m *ProcessManager) watchProcess(shimProc *ShimProcess) {
	// Wait for process to exit.
	err := shimProc.Cmd.Wait()

	m.logger.Info("shim process exited", "session_id", shimProc.SessionID, "error", err)

	// Close the Events channel first.
	close(shimProc.Events)

	// Remove from processes map.
	m.mu.Lock()
	delete(m.processes, shimProc.SessionID)
	m.mu.Unlock()

	// Transition session to "stopped" (best effort).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = m.sessions.Transition(ctx, shimProc.SessionID, meta.SessionStateStopped)

	// Clean up bundle directory (best effort).
	_ = os.RemoveAll(shimProc.BundlePath)

	// Close the Done channel LAST to signal all cleanup is complete.
	close(shimProc.Done)
}

// Stop gracefully stops a running shim process for the given session.
// The workflow:
//  1. Get ShimProcess from processes map
//  2. Call ShimClient.Shutdown RPC to request graceful shutdown
//  3. Wait for process to exit (with timeout)
//  4. If timeout, kill the process
//  5. Remove bundle directory
//  6. Transition session to "stopped"
//
// Returns error if the session is not running or shutdown fails.
func (m *ProcessManager) Stop(ctx context.Context, sessionID string) error {
	m.logger.Info("stopping session", "session_id", sessionID)

	// Get ShimProcess from processes map.
	m.mu.RLock()
	shimProc, exists := m.processes[sessionID]
	m.mu.RUnlock()

	if !exists {
		// Session is not running - check if it exists at all.
		session, err := m.sessions.Get(ctx, sessionID)
		if err != nil {
			return fmt.Errorf("process: get session %s: %w", sessionID, err)
		}
		if session == nil {
			return fmt.Errorf("process: session %s does not exist", sessionID)
		}
		// Session exists but is not running - transition to stopped if needed.
		if session.State != meta.SessionStateStopped {
			if err := m.sessions.Transition(ctx, sessionID, meta.SessionStateStopped); err != nil {
				return fmt.Errorf("process: transition session %s to stopped: %w", sessionID, err)
			}
		}
		return nil
	}

	// Call runtime/stop RPC to request graceful shutdown.
	if shimProc.Client != nil {
		stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if err := shimProc.Client.Stop(stopCtx); err != nil {
			m.logger.Warn("runtime/stop RPC failed, will kill process", "session_id", sessionID, "error", err)
		}
		cancel()
	}

	// Wait for process to exit.
	select {
	case <-shimProc.Done:
		// Process exited gracefully.
		m.logger.Info("shim process exited gracefully", "session_id", sessionID)
	case <-time.After(10 * time.Second):
		// Timeout - kill the process.
		m.logger.Warn("shim process did not exit in time, killing", "session_id", sessionID)
		if err := m.killShim(shimProc); err != nil {
			m.logger.Error("failed to kill shim process", "session_id", sessionID, "error", err)
		}
		// Wait for watchProcess to clean up.
		<-shimProc.Done
	}

	// Clean up bundle directory if it still exists.
	if shimProc.BundlePath != "" {
		_ = os.RemoveAll(shimProc.BundlePath)
	}

	m.logger.Info("session stopped", "session_id", sessionID)
	return nil
}

// State returns the current runtime state of the shim process for the given
// session. It calls runtime/status RPC and extracts the state field plus
// recovery metadata. Returns an error if the session is not running or the
// response is malformed.
func (m *ProcessManager) State(ctx context.Context, sessionID string) (spec.State, error) {
	m.mu.RLock()
	shimProc, exists := m.processes[sessionID]
	m.mu.RUnlock()

	if !exists {
		return spec.State{}, fmt.Errorf("process: session %s is not running", sessionID)
	}

	if shimProc.Client == nil {
		return spec.State{}, fmt.Errorf("process: session %s has no client connection", sessionID)
	}

	status, err := shimProc.Client.Status(ctx)
	if err != nil {
		return spec.State{}, fmt.Errorf("process: runtime/status for session %s: %w", sessionID, err)
	}

	return status.State, nil
}

// RuntimeStatus returns the full runtime/status result including recovery
// metadata (lastSeq) for the given session. Use this instead of State when
// the caller needs recovery information for replay or reconnect decisions.
func (m *ProcessManager) RuntimeStatus(ctx context.Context, sessionID string) (RuntimeStatusResult, error) {
	m.mu.RLock()
	shimProc, exists := m.processes[sessionID]
	m.mu.RUnlock()

	if !exists {
		return RuntimeStatusResult{}, fmt.Errorf("process: session %s is not running", sessionID)
	}

	if shimProc.Client == nil {
		return RuntimeStatusResult{}, fmt.Errorf("process: session %s has no client connection", sessionID)
	}

	return shimProc.Client.Status(ctx)
}

// Connect returns the ShimClient for direct RPC access to the shim process.
// Returns error if the session is not running.
func (m *ProcessManager) Connect(ctx context.Context, sessionID string) (*ShimClient, error) {
	m.mu.RLock()
	shimProc, exists := m.processes[sessionID]
	m.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("process: session %s is not running", sessionID)
	}

	if shimProc.Client == nil {
		return nil, fmt.Errorf("process: session %s has no client connection", sessionID)
	}

	return shimProc.Client, nil
}

// GetProcess returns the ShimProcess for the given session.
// This is useful for accessing process details like PID, events channel, etc.
// Returns nil if the session is not running.
func (m *ProcessManager) GetProcess(sessionID string) *ShimProcess {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.processes[sessionID]
}

// ListProcesses returns a list of all running session IDs.
func (m *ProcessManager) ListProcesses() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	ids := make([]string, 0, len(m.processes))
	for id := range m.processes {
		ids = append(ids, id)
	}
	return ids
}

// ────────────────────────────────────────────────────────────────────────────
// Recovery posture — daemon-level phase tracking
// ────────────────────────────────────────────────────────────────────────────

// SetRecoveryPhase atomically sets the daemon-level recovery phase.
// Called by RecoverSessions at the start (RecoveryPhaseRecovering) and end
// (RecoveryPhaseComplete) of the recovery pass.
func (m *ProcessManager) SetRecoveryPhase(phase RecoveryPhase) {
	m.recoveryPhase.Store(int32(phase))
	m.logger.Info("recovery phase changed", "phase", phase.String())
}

// GetRecoveryPhase atomically reads the current daemon-level recovery phase.
func (m *ProcessManager) GetRecoveryPhase() RecoveryPhase {
	return RecoveryPhase(m.recoveryPhase.Load())
}

// IsRecovering returns true when the daemon is actively recovering sessions.
// ARI handlers use this to implement the fail-closed posture: operational
// actions (prompt, cancel) are refused while recovery is in progress.
func (m *ProcessManager) IsRecovering() bool {
	return m.GetRecoveryPhase() == RecoveryPhaseRecovering
}

// SetSessionRecoveryInfo sets the recovery metadata on a running session's
// ShimProcess. Returns false if the session is not in the processes map.
func (m *ProcessManager) SetSessionRecoveryInfo(sessionID string, info *RecoveryInfo) bool {
	m.mu.RLock()
	shimProc, exists := m.processes[sessionID]
	m.mu.RUnlock()

	if !exists {
		return false
	}

	shimProc.Recovery = info
	return true
}
