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

// StateChange describes an externally visible runtime lifecycle transition.
type StateChange struct {
	SessionID      string
	PreviousStatus spec.Status
	Status         spec.Status
	PID            int
	Reason         string
}

// StateChangeHook is invoked after a lifecycle transition has been persisted.
type StateChangeHook func(StateChange)

// Manager manages the lifecycle of a single ACP agent process.
type Manager struct {
	cfg       spec.Config
	bundleDir string
	stateDir  string

	mu              sync.Mutex
	cmd             *exec.Cmd
	conn            *acp.ClientSideConnection
	sessionID       acp.SessionId
	events          chan acp.SessionNotification
	terminalMgr     *TerminalManager
	stateChangeHook StateChangeHook
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

// SetStateChangeHook registers a best-effort observer for persisted lifecycle transitions.
func (m *Manager) SetStateChangeHook(hook StateChangeHook) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stateChangeHook = hook
}

// Create starts the agent process and performs the ACP handshake.
// It writes state.json at each lifecycle transition:
//   - creating: before fork/exec
//   - created: after successful handshake
//   - stopped: if the process exits unexpectedly (written by background goroutine)
func (m *Manager) Create(ctx context.Context) error {
	workDir, err := spec.ResolveAgentRoot(m.bundleDir, m.cfg)
	if err != nil {
		return fmt.Errorf("runtime: %w", err)
	}

	if err := m.writeState(spec.State{
		OarVersion:  m.cfg.OarVersion,
		ID:          m.cfg.Metadata.Name,
		Status:      spec.StatusCreating,
		Bundle:      m.bundleDir,
		Annotations: m.cfg.Metadata.Annotations,
	}, "bootstrap-started"); err != nil {
		return fmt.Errorf("runtime: write creating state: %w", err)
	}

	proc := m.cfg.AcpAgent.Process
	//nolint:gosec // command comes from trusted config
	cmd := exec.CommandContext(ctx, proc.Command, proc.Args...)
	cmd.Env = mergeEnv(os.Environ(), proc.Env)
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

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("runtime: start agent: %w", err)
	}
	m.cmd = cmd
	m.terminalMgr = NewTerminalManager(workDir, cmd.Env, m.cfg.Permissions)

	client := &acpClient{mgr: m, terminalMgr: m.terminalMgr}
	conn := acp.NewClientSideConnection(client, stdinPipe, stdoutPipe)
	m.conn = conn

	var handshakeErr error
	defer func() {
		if handshakeErr != nil {
			_ = cmd.Process.Kill()
		}
	}()

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

	sessionResp, err := conn.NewSession(ctx, acp.NewSessionRequest{
		Cwd:        workDir,
		McpServers: convertMcpServers(m.cfg.AcpAgent.Session.McpServers),
	})
	if err != nil {
		handshakeErr = err
		return fmt.Errorf("runtime: acp session/new: %w", err)
	}
	m.sessionID = sessionResp.SessionId

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

	if err := m.writeState(spec.State{
		OarVersion:  m.cfg.OarVersion,
		ID:          m.cfg.Metadata.Name,
		Status:      spec.StatusCreated,
		PID:         cmd.Process.Pid,
		Bundle:      m.bundleDir,
		Annotations: m.cfg.Metadata.Annotations,
	}, "bootstrap-complete"); err != nil {
		handshakeErr = err
		return fmt.Errorf("runtime: write created state: %w", err)
	}

	go func() {
		_ = cmd.Wait()
		_ = m.writeState(spec.State{
			OarVersion:  m.cfg.OarVersion,
			ID:          m.cfg.Metadata.Name,
			Status:      spec.StatusStopped,
			Bundle:      m.bundleDir,
			Annotations: m.cfg.Metadata.Annotations,
		}, "process-exited")
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

	return m.writeState(spec.State{
		OarVersion:  m.cfg.OarVersion,
		ID:          m.cfg.Metadata.Name,
		Status:      spec.StatusStopped,
		Bundle:      m.bundleDir,
		Annotations: m.cfg.Metadata.Annotations,
	}, "runtime-stop")
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

	if st, readErr := spec.ReadState(m.stateDir); readErr == nil {
		st.Status = spec.StatusRunning
		_ = m.writeState(st, "prompt-started")
	}

	resp, err := conn.Prompt(ctx, acp.PromptRequest{
		SessionId: sessionID,
		Prompt:    prompt,
	})

	lt := &spec.LastTurn{CompletedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err != nil {
		lt.Error = err.Error()
	} else {
		lt.StopReason = string(resp.StopReason)
	}
	if st, readErr := spec.ReadState(m.stateDir); readErr == nil {
		st.Status = spec.StatusCreated
		st.LastTurn = lt
		reason := "prompt-completed"
		if err != nil {
			reason = "prompt-failed"
		}
		_ = m.writeState(st, reason)
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

	if err := conn.Cancel(ctx, acp.CancelNotification{SessionId: sessionID}); err != nil {
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
	return make(chan struct{})
}

func (m *Manager) writeState(state spec.State, reason string) error {
	previous, prevErr := spec.ReadState(m.stateDir)
	if err := spec.WriteState(m.stateDir, state); err != nil {
		return err
	}
	if prevErr == nil && previous.Status != state.Status {
		m.emitStateChange(previous, state, reason)
	}
	return nil
}

func (m *Manager) emitStateChange(previous, current spec.State, reason string) {
	m.mu.Lock()
	hook := m.stateChangeHook
	m.mu.Unlock()
	if hook == nil {
		return
	}
	hook(StateChange{
		SessionID:      current.ID,
		PreviousStatus: previous.Status,
		Status:         current.Status,
		PID:            current.PID,
		Reason:         reason,
	})
}

// convertMcpServers maps spec.McpServer slice to acp.McpServer slice.
// spec.McpServer.Type is "http" or "sse"; both map to the acp union variants.
func convertMcpServers(servers []spec.McpServer) []acp.McpServer {
	result := make([]acp.McpServer, 0, len(servers))
	for _, s := range servers {
		switch s.Type {
		case "stdio":
			env := make([]acp.EnvVariable, len(s.Env))
			for i, e := range s.Env {
				env[i] = acp.EnvVariable{Name: e.Name, Value: e.Value}
			}
			result = append(result, acp.McpServer{Stdio: &acp.McpServerStdio{
				Name:    s.Name,
				Command: s.Command,
				Args:    s.Args,
				Env:     env,
			}})
		case "sse":
			result = append(result, acp.McpServer{Sse: &acp.McpServerSse{Url: s.URL, Type: s.Type}})
		default:
			result = append(result, acp.McpServer{Http: &acp.McpServerHttp{Url: s.URL, Type: s.Type}})
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
