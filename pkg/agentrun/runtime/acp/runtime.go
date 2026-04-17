// Package acp implements the MASS agent process lifecycle.
// It forks/execs the ACP agent, performs the ACP initialize+session/new
// handshake, persists state.json through lifecycle transitions, and exposes
// Kill/Delete/GetState operations.
package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/acp-go-sdk"

	spec "github.com/zoumo/mass/pkg/runtime-spec"
	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
)

// StateChange describes an externally visible runtime lifecycle transition.
type StateChange struct {
	SessionID      string
	PreviousStatus apiruntime.Status
	Status         apiruntime.Status
	PID            int
	Reason         string
	SessionChanged []string
}

// StateChangeHook is invoked after a lifecycle transition has been persisted.
type StateChangeHook func(StateChange)

// Manager manages the lifecycle of a single ACP agent process.
type Manager struct {
	cfg       apiruntime.Config
	bundleDir string
	stateDir  string
	logger    *slog.Logger

	mu              sync.Mutex
	cmd             *exec.Cmd
	conn            *acp.ClientSideConnection
	sessionID       acp.SessionId
	events          chan acp.SessionNotification
	models          *acp.SessionModelState // from session/new response
	stateChangeHook StateChangeHook
	eventCountsFn   func() map[string]int
}

// New creates a new Manager. It does not start the agent process.
func New(cfg apiruntime.Config, bundleDir, stateDir string, logger *slog.Logger) *Manager {
	return &Manager{
		cfg:       cfg,
		bundleDir: bundleDir,
		stateDir:  stateDir,
		logger:    logger,
		events:    make(chan acp.SessionNotification, 1024),
	}
}

// SetStateChangeHook registers a best-effort observer for persisted lifecycle transitions.
func (m *Manager) SetStateChangeHook(hook StateChangeHook) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stateChangeHook = hook
}

// SetEventCountsFn registers a function that returns cumulative event counts.
// The function is called during every state write to flush counts into state.json.
func (m *Manager) SetEventCountsFn(fn func() map[string]int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.eventCountsFn = fn
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

	if err := m.writeState(func(s *apiruntime.State) {
		s.MassVersion = m.cfg.MassVersion
		s.ID = m.cfg.Metadata.Name
		s.Status = apiruntime.StatusCreating
		s.Bundle = m.bundleDir
		s.Annotations = m.cfg.Metadata.Annotations
	}, "bootstrap-started"); err != nil {
		return fmt.Errorf("runtime: write creating state: %w", err)
	}

	proc := m.cfg.Process
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

	client := &acpClient{mgr: m}
	conn := acp.NewClientSideConnection(client, stdinPipe, stdoutPipe)
	m.conn = conn

	var initResp acp.InitializeResponse
	var handshakeErr error
	defer func() {
		if handshakeErr != nil {
			_ = cmd.Process.Kill()
			_ = m.writeState(func(s *apiruntime.State) {
				s.MassVersion = m.cfg.MassVersion
				s.ID = m.cfg.Metadata.Name
				s.Status = apiruntime.StatusStopped
				s.Bundle = m.bundleDir
				s.Annotations = m.cfg.Metadata.Annotations
			}, "bootstrap-failed")
		}
	}()

	initResp, handshakeErr = conn.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion:    acp.ProtocolVersionNumber,
		ClientCapabilities: acp.ClientCapabilities{},
	})
	if handshakeErr != nil {
		return fmt.Errorf("runtime: acp initialize: %w", handshakeErr)
	}

	mcpServers := convertMcpServers(m.cfg.Session.McpServers)
	newSessionReq := acp.NewSessionRequest{
		Cwd:        workDir,
		McpServers: mcpServers,
	}
	if debugJSON, err := json.MarshalIndent(newSessionReq, "", "  "); err == nil {
		m.logger.Debug("acp session/new request", "body", string(debugJSON))
	}

	sessionResp, err := conn.NewSession(ctx, newSessionReq)
	if err != nil {
		handshakeErr = err
		return fmt.Errorf("runtime: acp session/new: %w", err)
	}
	m.mu.Lock()
	m.sessionID = sessionResp.SessionId
	m.models = sessionResp.Models
	m.mu.Unlock()

	if m.cfg.Session.SystemPrompt != "" {
		_, err = conn.Prompt(ctx, acp.PromptRequest{
			SessionId: m.sessionID,
			Prompt:    []acp.ContentBlock{acp.TextBlock(m.cfg.Session.SystemPrompt)},
		})
		if err != nil {
			handshakeErr = err
			return fmt.Errorf("runtime: acp systemPrompt seed: %w", err)
		}
	}

	if err := m.writeState(func(s *apiruntime.State) {
		s.MassVersion = m.cfg.MassVersion
		s.ID = m.cfg.Metadata.Name
		s.Status = apiruntime.StatusIdle
		s.PID = cmd.Process.Pid
		s.Bundle = m.bundleDir
		s.Annotations = m.cfg.Metadata.Annotations
		s.Session = convertInitializeToSession(initResp)
		if sessionResp.Models != nil {
			s.Session.Models = convertModels(sessionResp.Models)
		}
	}, "bootstrap-complete"); err != nil {
		handshakeErr = err
		return fmt.Errorf("runtime: write created state: %w", err)
	}

	go func() {
		_ = cmd.Wait()
		_ = m.writeState(func(s *apiruntime.State) {
			s.MassVersion = m.cfg.MassVersion
			s.ID = m.cfg.Metadata.Name
			s.Status = apiruntime.StatusStopped
			s.Bundle = m.bundleDir
			s.Annotations = m.cfg.Metadata.Annotations
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

	return m.writeState(func(s *apiruntime.State) {
		s.MassVersion = m.cfg.MassVersion
		s.ID = m.cfg.Metadata.Name
		s.Status = apiruntime.StatusStopped
		s.Bundle = m.bundleDir
		s.Annotations = m.cfg.Metadata.Annotations
	}, "runtime-stop")
}

// Delete removes the agent state directory. The agent must be stopped first.
func (m *Manager) Delete() error {
	s, err := spec.ReadState(m.stateDir)
	if err != nil {
		return fmt.Errorf("runtime: read state for delete: %w", err)
	}
	if s.Status != apiruntime.StatusStopped {
		return fmt.Errorf("runtime: cannot delete agent in status %q (must be stopped)", s.Status)
	}
	return spec.DeleteState(m.stateDir)
}

// GetState returns the current persisted state of the agent.
func (m *Manager) GetState() (apiruntime.State, error) {
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

	_ = m.writeState(func(s *apiruntime.State) {
		s.Status = apiruntime.StatusRunning
	}, "prompt-started")

	resp, err := conn.Prompt(ctx, acp.PromptRequest{
		SessionId: sessionID,
		Prompt:    prompt,
	})

	{
		reason := "prompt-completed"
		if err != nil {
			reason = "prompt-failed"
		}
		_ = m.writeState(func(s *apiruntime.State) {
			s.Status = apiruntime.StatusIdle
		}, reason)
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

// SetModel switches the agent to a different model via ACP session/set_model.
func (m *Manager) SetModel(ctx context.Context, modelID string) error {
	m.mu.Lock()
	conn := m.conn
	sessionID := m.sessionID
	m.mu.Unlock()

	if conn == nil {
		return fmt.Errorf("runtime: agent not started")
	}

	_, err := conn.UnstableSetSessionModel(ctx, acp.UnstableSetSessionModelRequest{
		SessionId: sessionID,
		ModelId:   acp.UnstableModelId(modelID),
	})
	if err != nil {
		return fmt.Errorf("runtime: set model: %w", err)
	}

	// Update in-memory models state.
	m.mu.Lock()
	if m.models != nil {
		m.models.CurrentModelId = acp.ModelId(modelID) //nolint:gosec // ModelId is string
	}
	m.mu.Unlock()

	// Persist updated currentModelId to state.json.
	_ = m.writeState(func(s *apiruntime.State) {
		if s.Session != nil && s.Session.Models != nil {
			s.Session.Models.CurrentModelId = modelID
		}
	}, "set-model")

	return nil
}

// Events returns the channel on which the Manager delivers session
// notifications from the agent. The channel is buffered (64) and is
// never closed; callers should drain it after Prompt returns.
func (m *Manager) Events() <-chan acp.SessionNotification {
	return m.events
}

// SessionID returns the ACP session ID obtained during the session/new handshake.
// Returns empty string if the session has not been created yet.
func (m *Manager) SessionID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return string(m.sessionID)
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

func (m *Manager) writeState(apply func(*apiruntime.State), reason string) error {
	m.mu.Lock()

	previous, prevErr := spec.ReadState(m.stateDir)
	if prevErr != nil && !errors.Is(prevErr, os.ErrNotExist) {
		m.mu.Unlock()
		return fmt.Errorf("runtime: read state for %s: %w", reason, prevErr)
	}

	state := previous // zero value if prevErr was NotExist
	apply(&state)
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)

	if m.eventCountsFn != nil {
		state.EventCounts = m.eventCountsFn()
	}

	if err := spec.WriteState(m.stateDir, state); err != nil {
		m.mu.Unlock()
		return err
	}

	statusChanged := prevErr == nil && previous.Status != state.Status
	hook := m.stateChangeHook
	change := StateChange{
		SessionID:      state.ID,
		PreviousStatus: previous.Status,
		Status:         state.Status,
		PID:            state.PID,
		Reason:         reason,
	}

	m.mu.Unlock()

	if statusChanged && hook != nil {
		hook(change)
	}
	return nil
}

// UpdateSessionMetadata performs a read-modify-write on state.json to update
// session metadata fields. It always emits a metadata-only state_change event
// (PreviousStatus == Status, SessionChanged populated) regardless of status
// transitions.
//
// The apply function receives the full State and should mutate only session
// fields — the caller is responsible for ensuring the mutation is correct.
//
// Lock order: m.mu is acquired for the read-modify-write cycle, then released
// before calling the stateChangeHook (D120).
func (m *Manager) UpdateSessionMetadata(changed []string, reason string, apply func(*apiruntime.State)) error {
	m.mu.Lock()

	state, err := spec.ReadState(m.stateDir)
	if err != nil {
		m.mu.Unlock()
		m.logger.Error("UpdateSessionMeta read state failed",
			"reason", reason, "changed", changed, "error", err)
		return fmt.Errorf("runtime: read state for %s: %w", reason, err)
	}

	if state.Session == nil {
		state.Session = &apiruntime.SessionState{}
	}

	apply(&state)
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)

	if m.eventCountsFn != nil {
		state.EventCounts = m.eventCountsFn()
	}

	if err := spec.WriteState(m.stateDir, state); err != nil {
		m.mu.Unlock()
		m.logger.Error("UpdateSessionMeta write state failed",
			"reason", reason, "changed", changed, "error", err)
		return fmt.Errorf("runtime: write state for %s: %w", reason, err)
	}

	hook := m.stateChangeHook
	change := StateChange{
		SessionID:      state.ID,
		PreviousStatus: state.Status,
		Status:         state.Status,
		PID:            state.PID,
		Reason:         reason,
		SessionChanged: changed,
	}

	m.mu.Unlock()

	if hook != nil {
		hook(change)
	}

	return nil
}

// convertInitializeToSession maps an ACP InitializeResponse to the runtime-spec
// SessionState so that agent identity and capabilities are captured in state.json
// at bootstrap-complete.
func convertInitializeToSession(resp acp.InitializeResponse) *apiruntime.SessionState {
	session := &apiruntime.SessionState{
		Capabilities: &apiruntime.AgentCapabilities{
			LoadSession: resp.AgentCapabilities.LoadSession,
			McpCapabilities: apiruntime.McpCapabilities{
				Http: resp.AgentCapabilities.McpCapabilities.Http,
				Sse:  resp.AgentCapabilities.McpCapabilities.Sse,
			},
			PromptCapabilities: apiruntime.PromptCapabilities{
				Audio:           resp.AgentCapabilities.PromptCapabilities.Audio,
				EmbeddedContext: resp.AgentCapabilities.PromptCapabilities.EmbeddedContext,
				Image:           resp.AgentCapabilities.PromptCapabilities.Image,
			},
		},
	}

	if resp.AgentInfo != nil {
		session.AgentInfo = &apiruntime.AgentInfo{
			Name:    resp.AgentInfo.Name,
			Version: resp.AgentInfo.Version,
			Title:   resp.AgentInfo.Title,
		}
	}

	if resp.AgentCapabilities.SessionCapabilities.Fork != nil {
		session.Capabilities.SessionCapabilities.Fork = &apiruntime.SessionForkCapabilities{}
	}

	return session
}

// convertModels maps acp.SessionModelState to the runtime-spec mirror type.
func convertModels(m *acp.SessionModelState) *apiruntime.SessionModelState {
	if m == nil {
		return nil
	}
	models := make([]apiruntime.ModelInfo, len(m.AvailableModels))
	for i, mi := range m.AvailableModels {
		models[i] = apiruntime.ModelInfo{
			ModelId:     string(mi.ModelId),
			Name:        mi.Name,
			Description: mi.Description,
		}
	}
	return &apiruntime.SessionModelState{
		AvailableModels: models,
		CurrentModelId:  string(m.CurrentModelId),
	}
}

// convertMcpServers maps apiruntime.McpServer slice to acp.McpServer slice.
// apiruntime.McpServer.Type is "http" or "sse"; both map to the acp union variants.
func convertMcpServers(servers []apiruntime.McpServer) []acp.McpServer {
	result := make([]acp.McpServer, 0, len(servers))
	for _, s := range servers {
		switch s.Type {
		case "stdio":
			env := make([]acp.EnvVariable, len(s.Env))
			for i, e := range s.Env {
				env[i] = acp.EnvVariable{Name: e.Name, Value: e.Value}
			}
			// Ensure non-nil slices — ACP agents reject null; they need [].
			args := s.Args
			if args == nil {
				args = []string{}
			}
			result = append(result, acp.McpServer{Stdio: &acp.McpServerStdio{
				Name:    s.Name,
				Command: s.Command,
				Args:    args,
				Env:     env,
			}})
		case "sse":
			result = append(result, acp.McpServer{Sse: &acp.McpServerSseInline{
				Name:    s.Name,
				Type:    s.Type,
				Url:     s.URL,
				Headers: []acp.HttpHeader{},
			}})
		default:
			result = append(result, acp.McpServer{Http: &acp.McpServerHttpInline{
				Name:    s.Name,
				Type:    s.Type,
				Url:     s.URL,
				Headers: []acp.HttpHeader{},
			}})
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
