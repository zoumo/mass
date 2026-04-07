// Package runtime implements the OAR agent process lifecycle.
// It forks/execs the ACP agent, performs the ACP initialize+session/new
// handshake, persists state.json through lifecycle transitions, and exposes
// Kill/Delete/GetState operations.
package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/acp-go-sdk"
	"github.com/open-agent-d/open-agent-d/pkg/spec"
)

// Manager manages the lifecycle of a single ACP agent process.
type Manager struct {
	cfg        spec.Config
	bundleDir  string
	stateDir   string

	mu          sync.Mutex
	cmd         *exec.Cmd
	conn        *acp.ClientSideConnection
	sessionID   acp.SessionId
	events      chan acp.SessionNotification
	terminalMgr *TerminalManager // manages terminal operations
}

// New creates a new Manager. It does not start the agent process.
func New(cfg spec.Config, bundleDir, stateDir string) *Manager {
	return &Manager{
		cfg:       cfg,
		bundleDir: bundleDir,
		stateDir:  stateDir,
		events:    make(chan acp.SessionNotification, 64),
	}
}

// Create starts the agent process and performs the ACP handshake.
// It writes state.json at each lifecycle transition:
//   - creating: before fork/exec
//   - created: after successful handshake
//   - stopped: if the process exits unexpectedly (written by background goroutine)
func (m *Manager) Create(ctx context.Context) error {
	// (a) Resolve the agent root: join bundle dir with agentRoot.path, follow symlinks.
	// The resolved path is used as cmd.Dir and as the ACP session/new cwd parameter,
	// so the agent process sees a canonical absolute path from os.Getwd().
	workDir, err := spec.ResolveAgentRoot(m.bundleDir, m.cfg)
	if err != nil {
		return fmt.Errorf("runtime: %w", err)
	}

	// (b) Write creating state.
	if err := spec.WriteState(m.stateDir, spec.State{
		OarVersion:  m.cfg.OarVersion,
		ID:          m.cfg.Metadata.Name,
		Status:      spec.StatusCreating,
		Bundle:      m.bundleDir,
		Annotations: m.cfg.Metadata.Annotations,
	}); err != nil {
		return fmt.Errorf("runtime: write creating state: %w", err)
	}

	// (c) Build exec.Cmd.
	proc := m.cfg.AcpAgent.Process
	//nolint:gosec // command comes from trusted config
	cmd := exec.CommandContext(ctx, proc.Command, proc.Args...)
	// Merge environment: start with the parent process env, then apply
	// config overrides on top. This ensures the child always has a sane
	// baseline (PATH, HOME, etc.) while allowing config.json to inject or
	// override specific variables (e.g. ANTHROPIC_API_KEY, PI_ACP_PI_COMMAND).
	cmd.Env = mergeEnv(os.Environ(), proc.Env)
	// Set the working directory to the resolved agent root so the agent process
	// starts in the right directory. Using the canonical path (symlinks resolved)
	// ensures os.Getwd() in the child returns the same path we pass to ACP session/new.
	cmd.Dir = workDir
	cmd.Stderr = os.Stderr

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("runtime: stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("runtime: stdout pipe: %w", err)
	}

	// (d) Start the process.
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("runtime: start agent: %w", err)
	}
	m.cmd = cmd

	// (e) Initialize TerminalManager for terminal operations.
	// Uses the resolved workDir (agentRoot) and merged environment.
	m.terminalMgr = NewTerminalManager(workDir, cmd.Env, m.cfg.Permissions)

	// (f) Build acpClient with Manager reference and TerminalManager.
	client := &acpClient{mgr: m, terminalMgr: m.terminalMgr}

	// (g) Create client-side ACP connection.
	// stdinPipe is the writer to the agent's stdin (our peerInput).
	// stdoutPipe is the reader from the agent's stdout (our peerOutput).
	conn := acp.NewClientSideConnection(client, stdinPipe, stdoutPipe)
	m.conn = conn

	// On any error below, kill the process.
	var handshakeErr error
	defer func() {
		if handshakeErr != nil {
			_ = cmd.Process.Kill()
		}
	}()

	// (h) ACP Initialize handshake.
	_, handshakeErr = conn.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
		ClientCapabilities: acp.ClientCapabilities{
			Fs: acp.FileSystemCapability{
				ReadTextFile:  true,
				WriteTextFile: true,
			},
		},
	})
	if handshakeErr != nil {
		return fmt.Errorf("runtime: acp initialize: %w", handshakeErr)
	}

	// (i) ACP session/new.
	sessionResp, err := conn.NewSession(ctx, acp.NewSessionRequest{
		Cwd:        workDir,
		McpServers: convertMcpServers(m.cfg.AcpAgent.Session.McpServers),
	})
	if err != nil {
		handshakeErr = err
		return fmt.Errorf("runtime: acp session/new: %w", err)
	}
	m.sessionID = sessionResp.SessionId

	// (i2) If a systemPrompt is configured, send it as the first prompt so the
	// agent can establish its role/identity before any task prompt arrives.
	// This is the current workaround for ACP v0.6.3 NewSessionRequest not
	// supporting a systemPrompt field natively.
	// The prompt is sent silently during Create; its events are intentionally
	// discarded (no subscribers yet) and its turn outcome is not persisted to
	// LastTurn so callers see a clean slate on the first real task prompt.
	if m.cfg.AcpAgent.SystemPrompt != "" {
		_, err = conn.Prompt(ctx, acp.PromptRequest{
			SessionId: m.sessionID,
			Prompt:    []acp.ContentBlock{acp.TextBlock(m.cfg.AcpAgent.SystemPrompt)},
		})
		if err != nil {
			handshakeErr = err
			return fmt.Errorf("runtime: acp systemPrompt seed: %w", err)
		}
	}

	// (j) Write created state.
	if err := spec.WriteState(m.stateDir, spec.State{
		OarVersion:  m.cfg.OarVersion,
		ID:          m.cfg.Metadata.Name,
		Status:      spec.StatusCreated,
		PID:         cmd.Process.Pid,
		Bundle:      m.bundleDir,
		Annotations: m.cfg.Metadata.Annotations,
	}); err != nil {
		handshakeErr = err
		return fmt.Errorf("runtime: write created state: %w", err)
	}

	// (k) Background goroutine: wait for process exit and write stopped state.
	// This is started AFTER the handshake completes to avoid interfering with
	// pipe reads during the handshake (per Go's exec.Wait documentation).
	go func() {
		_ = cmd.Wait()
		_ = spec.WriteState(m.stateDir, spec.State{
			OarVersion:  m.cfg.OarVersion,
			ID:          m.cfg.Metadata.Name,
			Status:      spec.StatusStopped,
			Bundle:      m.bundleDir,
			Annotations: m.cfg.Metadata.Annotations,
		})
	}()

	return nil
}

// Kill sends SIGTERM to the agent process, waits up to 5 seconds for it to
// exit, then sends SIGKILL. It writes stopped state on completion.
func (m *Manager) Kill(ctx context.Context) error {
	m.mu.Lock()
	cmd := m.cmd
	m.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return fmt.Errorf("runtime: agent process not started")
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		// Process may have already exited; try SIGKILL.
		_ = cmd.Process.Kill()
	}

	done := m.done()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}

	return spec.WriteState(m.stateDir, spec.State{
		OarVersion:  m.cfg.OarVersion,
		ID:          m.cfg.Metadata.Name,
		Status:      spec.StatusStopped,
		Bundle:      m.bundleDir,
		Annotations: m.cfg.Metadata.Annotations,
	})
}

// Delete removes the agent state directory. The agent must be stopped first.
func (m *Manager) Delete() error {
	s, err := spec.ReadState(m.stateDir)
	if err != nil {
		return fmt.Errorf("runtime: read state for delete: %w", err)
	}
	if s.Status != spec.StatusStopped {
		return fmt.Errorf("runtime: cannot delete agent in status %q (must be stopped)", s.Status)
	}
	return spec.DeleteState(m.stateDir)
}

// GetState returns the current persisted state of the agent.
func (m *Manager) GetState() (spec.State, error) {
	return spec.ReadState(m.stateDir)
}

// Prompt sends a user prompt to the agent and blocks until the agent
// returns a PromptResponse. Session notifications emitted by the agent
// during the turn are forwarded to the Events channel.
// On completion (success or error), LastTurn is persisted to state.json.
func (m *Manager) Prompt(ctx context.Context, prompt []acp.ContentBlock) (acp.PromptResponse, error) {
	m.mu.Lock()
	conn := m.conn
	sessionID := m.sessionID
	m.mu.Unlock()

	if conn == nil {
		return acp.PromptResponse{}, fmt.Errorf("runtime: agent not started")
	}

	// Write running state before forwarding the prompt, as specified in the
	// OAR Runtime Spec lifecycle (created → running while prompt is processing).
	if st, readErr := spec.ReadState(m.stateDir); readErr == nil {
		st.Status = spec.StatusRunning
		_ = spec.WriteState(m.stateDir, st)
	}

	resp, err := conn.Prompt(ctx, acp.PromptRequest{
		SessionId: sessionID,
		Prompt:    prompt,
	})

	// Persist last turn outcome regardless of success or failure.
	lt := &spec.LastTurn{CompletedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err != nil {
		lt.Error = err.Error()
	} else {
		lt.StopReason = string(resp.StopReason)
	}
	// Best-effort: read current state, patch LastTurn, restore to created, write back.
	if st, readErr := spec.ReadState(m.stateDir); readErr == nil {
		st.Status = spec.StatusCreated // transition back: running → created
		st.LastTurn = lt
		_ = spec.WriteState(m.stateDir, st)
	}

	if err != nil {
		return acp.PromptResponse{}, fmt.Errorf("runtime: prompt: %w", err)
	}
	return resp, nil
}

// Cancel sends a cancel notification to the agent for the current session.
func (m *Manager) Cancel(ctx context.Context) error {
	m.mu.Lock()
	conn := m.conn
	sessionID := m.sessionID
	m.mu.Unlock()

	if conn == nil {
		return fmt.Errorf("runtime: agent not started")
	}

	if err := conn.Cancel(ctx, acp.CancelNotification{
		SessionId: sessionID,
	}); err != nil {
		return fmt.Errorf("runtime: cancel: %w", err)
	}
	return nil
}

// Events returns the channel on which the Manager delivers session
// notifications from the agent. The channel is buffered (64) and is
// never closed; callers should drain it after Prompt returns.
func (m *Manager) Events() <-chan acp.SessionNotification {
	return m.events
}

// done returns a channel that closes when the ACP connection is closed.
// Returns a never-closing channel if the connection has not been established.
func (m *Manager) done() <-chan struct{} {
	m.mu.Lock()
	conn := m.conn
	m.mu.Unlock()
	if conn != nil {
		return conn.Done()
	}
	return make(chan struct{}) // never closes
}

// convertMcpServers maps spec.McpServer slice to acp.McpServer slice.
// spec.McpServer.Type is "http" or "sse"; both map to the acp union variants.
func convertMcpServers(servers []spec.McpServer) []acp.McpServer {
	result := make([]acp.McpServer, 0, len(servers))
	for _, s := range servers {
		switch s.Type {
		case "sse":
			result = append(result, acp.McpServer{
				Sse: &acp.McpServerSse{
					Url:  s.URL,
					Type: s.Type,
				},
			})
		default: // "http" and anything else
			result = append(result, acp.McpServer{
				Http: &acp.McpServerHttp{
					Url:  s.URL,
					Type: s.Type,
				},
			})
		}
	}
	return result
}

// mergeEnv merges base environment with overrides. Keys in overrides take
// precedence over base. Both slices use "KEY=VALUE" format.
func mergeEnv(base, overrides []string) []string {
	merged := make(map[string]string, len(base)+len(overrides))
	for _, e := range base {
		k, v, _ := strings.Cut(e, "=")
		merged[k] = v
	}
	for _, e := range overrides {
		k, v, _ := strings.Cut(e, "=")
		merged[k] = v
	}
	result := make([]string, 0, len(merged))
	for k, v := range merged {
		result = append(result, k+"="+v)
	}
	return result
}
