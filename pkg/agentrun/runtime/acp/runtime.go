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

// systemPromptGuard is appended to the user-supplied system prompt before
// sending it to the agent. It prevents the agent from acting on the role
// definition itself — the agent should wait for the next explicit instruction.
const systemPromptGuard = "\n---\nIMPORTANT: The above content is protocol and role setup. " +
	"Do NOT execute any commands or take any action now. Wait for the next message before proceeding."

var disconnectedDone = make(chan struct{})

func buildSeedSystemPrompt(systemPrompt string) string {
	if systemPrompt == "" {
		return ""
	}
	return systemPrompt + systemPromptGuard
}

// BuildSeedSystemPrompt returns the user system prompt plus the runtime guard.
func BuildSeedSystemPrompt(systemPrompt string) string {
	return buildSeedSystemPrompt(systemPrompt)
}

// StateChange describes an externally visible runtime lifecycle transition.
type StateChange struct {
	SessionID      string
	PreviousPhase  apiruntime.Phase
	Phase          apiruntime.Phase
	PID            int
	Reason         string
	SessionChanged []string
}

// StateChangeHook is invoked after a lifecycle transition has been persisted.
type StateChangeHook func(StateChange)

// Manager manages the lifecycle of a single ACP agent process.
// sessionState holds per-session protocol metadata. Multiple sessions may
// co-exist on one agent process — ACP's session/new is multi-session by
// design (each session has its own cwd, sessionId, model state). The
// agent process (e.g. claude-agent-acp) keeps sessions in a map keyed by
// sessionId; this struct mirrors the runtime-side view of each.
//
// Multi-session lets callers keep a long-lived agent process and switch
// session per task, instead of fork+kill an agent process per task.
type sessionState struct {
	id     acp.SessionId
	models *acp.SessionModelState
	cwd    string
	// inflight counts active PromptSession turns. EndSession refuses to
	// release a session with inflight > 0; the prompt's defer decrements it.
	inflight int
}

type Manager struct {
	cfg       apiruntime.Config
	bundleDir string
	stateDir  string
	logger    *slog.Logger

	mu          sync.Mutex
	cmd         *exec.Cmd
	processDone chan struct{}
	conn        *acp.ClientSideConnection
	events      chan acp.SessionNotification

	// sessions holds all active ACP sessions on this agent process, keyed
	// by sessionId. NewSession adds entries; EndSession removes them.
	// Empty until Create()'s initial session/new completes. Per-session
	// inflight prompt count lives on each sessionState — see EndSession.
	sessions map[acp.SessionId]*sessionState

	// activePrompts is the total number of in-flight PromptSession calls
	// across all sessions. state.json's Phase field is process-wide, so
	// it must reflect "any session running" — not "the last prompt that
	// finished". PromptSession bumps this; the deferred decrement +
	// writeState then re-reads it under m.mu to decide Running vs Idle.
	activePrompts int

	// sessionID retains the *first* session created by Create() so legacy
	// callers of Prompt/Cancel/SetModel (which don't pass sessionId) keep
	// working without code changes. New callers should use the explicit
	// sessionId-taking variants (PromptSession etc.). To be removed once
	// all callers migrate.
	sessionID acp.SessionId

	// models tracks the *first* session's models for the same backward-
	// compat reason as sessionID above. Per-session models live in
	// sessions[id].models.
	models *acp.SessionModelState

	stateChangeHook StateChangeHook
	eventCountsFn   func() map[string]int
	usageFn         func() *apiruntime.UsageInfo
}

// New creates a new Manager. It does not start the agent process.
func New(cfg apiruntime.Config, bundleDir, stateDir string, logger *slog.Logger) *Manager {
	return &Manager{
		cfg:       cfg,
		bundleDir: bundleDir,
		stateDir:  stateDir,
		logger:    logger.With("subsystem", "runtime"),
		events:    make(chan acp.SessionNotification, 1024),
		sessions:  make(map[acp.SessionId]*sessionState),
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

// SetUsageFn registers a function that returns the latest context window usage.
// The function is called during every state write to flush usage into state.json.
func (m *Manager) SetUsageFn(fn func() *apiruntime.UsageInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.usageFn = fn
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
		s.Phase = apiruntime.PhaseCreating
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
	processDone := make(chan struct{})
	m.cmd = cmd
	m.processDone = processDone
	m.logger.Info("process started", "pid", cmd.Process.Pid)

	client := &acpClient{mgr: m, logger: m.logger.With("subsystem", "acp")}
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
				s.Phase = apiruntime.PhaseStopped
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
		Meta:       m.cfg.Session.Meta,
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
	// Initial session populates both the legacy single-session fields
	// (sessionID/models) and the multi-session map. Future sessions opened
	// via NewSession only register in the map; sessionID stays pinned to
	// the first one for backward compat with Prompt/Cancel/SetModel.
	m.sessionID = sessionResp.SessionId
	m.models = sessionResp.Models
	m.sessions[sessionResp.SessionId] = &sessionState{
		id:     sessionResp.SessionId,
		models: sessionResp.Models,
		cwd:    workDir,
	}
	m.mu.Unlock()
	m.logger.Info("session created", "sessionID", sessionResp.SessionId)

	m.logger.Info("agent ready", "pid", cmd.Process.Pid, "sessionID", sessionResp.SessionId)
	if err := m.writeState(func(s *apiruntime.State) {
		s.MassVersion = m.cfg.MassVersion
		s.ID = m.cfg.Metadata.Name
		s.SessionID = string(m.sessionID)
		s.Phase = apiruntime.PhaseIdle
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
		defer close(processDone)
		_ = cmd.Wait()
		m.logger.Info("process exited")
		m.clearSessions()
		_ = m.writeState(func(s *apiruntime.State) {
			s.MassVersion = m.cfg.MassVersion
			s.ID = m.cfg.Metadata.Name
			s.Phase = apiruntime.PhaseStopped
			s.Bundle = m.bundleDir
			s.Annotations = m.cfg.Metadata.Annotations
		}, "process-exited")
	}()

	return nil
}

// SeedSystemPrompt sends the configured system prompt, plus the runtime guard,
// as a normal prompt after bootstrap. Call this only after Create() succeeds.
func (m *Manager) SeedSystemPrompt(ctx context.Context) (acp.PromptResponse, error) {
	if m.cfg.Session.SystemPrompt == "" {
		return acp.PromptResponse{}, nil
	}
	return m.Prompt(ctx, []acp.ContentBlock{
		acp.TextBlock(buildSeedSystemPrompt(m.cfg.Session.SystemPrompt)),
	})
}

// Kill sends SIGTERM to the agent process, waits up to 5 seconds for it to
// exit, then sends SIGKILL. It writes stopped state on completion.
func (m *Manager) Kill(ctx context.Context) error {
	m.mu.Lock()
	cmd := m.cmd
	processDone := m.processDone
	m.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return fmt.Errorf("runtime: agent process not started")
	}

	m.logger.Info("killing", "pid", cmd.Process.Pid)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		_ = cmd.Process.Kill()
	}

	done := m.done()
	if processDone != nil {
		done = processDone
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		m.logger.Info("SIGKILL after timeout")
		_ = cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}

	m.clearSessions()
	return m.writeState(func(s *apiruntime.State) {
		s.MassVersion = m.cfg.MassVersion
		s.ID = m.cfg.Metadata.Name
		s.Phase = apiruntime.PhaseStopped
		s.Bundle = m.bundleDir
		s.Annotations = m.cfg.Metadata.Annotations
	}, "runtime-stop")
}

// clearSessions resets the multi-session bookkeeping after the agent
// process has exited (via Kill or natural exit). Without this, Sessions()
// returns stale IDs, EndSession silently succeeds on a dead session, and
// PromptSession passes its conn != nil check before failing on the dead
// pipe — invariants of the session map are broken across the teardown.
//
// In-flight RPCs hold their own captured conn pointer (read under m.mu at
// entry, used without lock); nil-ing m.conn here only guards *future*
// calls. Existing goroutines will fail on the closed pipe, which is fine.
func (m *Manager) clearSessions() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions = map[acp.SessionId]*sessionState{}
	m.conn = nil
	m.sessionID = ""
	m.models = nil
	m.activePrompts = 0
}

// Delete removes the agent state directory. The agent must be stopped first.
func (m *Manager) Delete() error {
	s, err := spec.ReadState(m.stateDir)
	if err != nil {
		return fmt.Errorf("runtime: read state for delete: %w", err)
	}
	if s.Phase != apiruntime.PhaseStopped {
		return fmt.Errorf("runtime: cannot delete agent in status %q (must be stopped)", s.Phase)
	}
	return spec.DeleteState(m.stateDir)
}

// GetState returns the current persisted state of the agent.
func (m *Manager) GetState() (apiruntime.State, error) {
	return spec.ReadState(m.stateDir)
}

// NewSession opens an additional ACP session on the running agent process.
// The agent must already be started via Create() (which creates the initial
// session). Returns the new session's ID, which callers must pass to
// PromptSession / CancelSession / EndSession to address this session.
//
// cwd is required — each session is scoped to its own working directory.
// All wire-level callers (Service / ARI / CLI) already reject empty cwd;
// the runtime validates here too for in-process callers and so the
// contract is consistent across layers.
//
// mcpServers (optional) are extra MCP servers scoped to this session, layered
// on top of the agent's bundle-level mcpServers.
func (m *Manager) NewSession(ctx context.Context, cwd string, mcpServers []acp.McpServer) (acp.SessionId, error) {
	if cwd == "" {
		return "", fmt.Errorf("runtime: new session: cwd is required")
	}

	m.mu.Lock()
	conn := m.conn
	m.mu.Unlock()

	if conn == nil {
		return "", fmt.Errorf("runtime: agent not started")
	}

	// Layer session-scoped mcpServers on top of bundle-level config. Callers
	// that pass nil/empty get the bundle defaults.
	merged := convertMcpServers(m.cfg.Session.McpServers)
	merged = append(merged, mcpServers...)

	req := acp.NewSessionRequest{
		Meta:       m.cfg.Session.Meta,
		Cwd:        cwd,
		McpServers: merged,
	}
	resp, err := conn.NewSession(ctx, req)
	if err != nil {
		return "", fmt.Errorf("runtime: acp session/new: %w", err)
	}

	m.mu.Lock()
	m.sessions[resp.SessionId] = &sessionState{
		id:     resp.SessionId,
		models: resp.Models,
		cwd:    cwd,
	}
	m.mu.Unlock()
	m.logger.Info("session opened", "sessionID", resp.SessionId, "cwd", cwd)
	return resp.SessionId, nil
}

// EndSession releases runtime tracking of a session id. The ACP protocol
// has no explicit "end session" RPC today, so this method only clears the
// runtime's local map entry — the agent process retains its own session
// state until cancelled or the process exits.
//
// EndSession refuses to release a session that has in-flight Prompt work
// (returns ErrSessionBusy). Callers must CancelSession (or wait for prompt
// completion) first; otherwise the prompt completes against a session no
// longer tracked, and the resulting state.json Phase / log lines diverge
// from the sessions map.
//
// Refuses to end the initial session (the one Create() opened) — callers
// that want to fully tear down the agent should call Kill() instead.
func (m *Manager) EndSession(sessionID acp.SessionId) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sessionID == m.sessionID {
		return fmt.Errorf("runtime: cannot end initial session %q (kill the agent instead)", sessionID)
	}
	sess, ok := m.sessions[sessionID]
	if !ok {
		return fmt.Errorf("runtime: session %q not found", sessionID)
	}
	if sess.inflight > 0 {
		return fmt.Errorf("runtime: session %q busy: %d in-flight prompt(s) (cancel first)", sessionID, sess.inflight)
	}
	delete(m.sessions, sessionID)
	m.logger.Info("session ended", "sessionID", sessionID)
	return nil
}

// resolveSessionLocked maps an empty sessionID to the initial session.
// Caller must hold m.mu.
func (m *Manager) resolveSessionLocked(sessionID acp.SessionId) acp.SessionId {
	if sessionID == "" {
		return m.sessionID
	}
	return sessionID
}

// decrementPromptInflight is the deferred cleanup for PromptSession. The
// session may have been deleted by Kill/clearSessions in the window between
// PromptSession's entry and conn.Prompt returning; both decrements guard
// for that — sess.inflight only if the session still exists, and
// activePrompts only when positive so a racing clearSessions (which sets
// activePrompts to 0) doesn't drive the counter negative.
func (m *Manager) decrementPromptInflight(sessionID acp.SessionId) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[sessionID]; ok {
		s.inflight--
	}
	if m.activePrompts > 0 {
		m.activePrompts--
	}
}

// SessionIDs returns a snapshot of currently-active session IDs. Order is
// not stable — caller should sort if a deterministic order is needed.
func (m *Manager) SessionIDs() []acp.SessionId {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]acp.SessionId, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	return ids
}

// Prompt is a backward-compat shim — equivalent to PromptSession(ctx, "", prompt),
// which resolves empty SessionId to the initial session.
func (m *Manager) Prompt(ctx context.Context, prompt []acp.ContentBlock) (acp.PromptResponse, error) {
	return m.PromptSession(ctx, "", prompt)
}

// PromptSession sends a user prompt to a specific session and blocks until
// the agent returns a PromptResponse. Empty sessionID is resolved to the
// agent's initial session (the one Create() opened) — single resolution
// point for the empty-string convention.
//
// Session notifications emitted by the agent during the turn are forwarded
// to the Events channel. On completion (success or error), state.json is
// updated.
//
// The state.json Phase field is single-valued today, so the "PhaseRunning"
// stamp applies process-wide — when multiple sessions are in-flight
// concurrently, phase is "running" if any session is. Per-session phase
// tracking is a follow-up state-schema change.
func (m *Manager) PromptSession(ctx context.Context, sessionID acp.SessionId, prompt []acp.ContentBlock) (acp.PromptResponse, error) {
	m.mu.Lock()
	conn := m.conn
	sessionID = m.resolveSessionLocked(sessionID)
	sess, known := m.sessions[sessionID]
	if conn == nil {
		m.mu.Unlock()
		return acp.PromptResponse{}, fmt.Errorf("runtime: agent not started")
	}
	if !known {
		m.mu.Unlock()
		return acp.PromptResponse{}, fmt.Errorf("runtime: prompt: session %q not found", sessionID)
	}
	sess.inflight++
	m.activePrompts++
	m.mu.Unlock()

	defer m.decrementPromptInflight(sessionID)

	m.logger.Debug("prompt started", "sessionID", sessionID, "blocks", len(prompt))

	_ = m.writeState(m.phaseFromActivePromptsLocked, "prompt-started")

	resp, err := conn.Prompt(ctx, acp.PromptRequest{
		SessionId: sessionID,
		Prompt:    prompt,
	})

	{
		reason := "prompt-completed"
		if err != nil {
			reason = "prompt-failed"
		}
		m.logger.Debug("prompt done", "sessionID", sessionID, "reason", reason)
		_ = m.writeState(m.phaseFromActivePromptsLocked, reason)
	}

	if err != nil {
		return acp.PromptResponse{}, fmt.Errorf("runtime: prompt: %w", err)
	}
	return resp, nil
}

// phaseFromActivePromptsLocked sets state.Phase based on the current
// activePrompts counter. Used as the writeState apply callback for prompt
// start/end so Phase reflects "any session running" rather than the last
// caller's local view. Runs under m.mu (writeState locks before calling).
//
// Skips the write when m.conn is nil — Kill / clearSessions has taken
// ownership of lifecycle phase (Stopped), and a late-firing PromptSession
// writeState (from a prompt that was in flight when Kill ran) must not
// clobber that with Idle / Running.
func (m *Manager) phaseFromActivePromptsLocked(s *apiruntime.State) {
	if m.conn == nil {
		return
	}
	if m.activePrompts > 0 {
		s.Phase = apiruntime.PhaseRunning
	} else {
		s.Phase = apiruntime.PhaseIdle
	}
}

// Cancel is a backward-compat shim — equivalent to CancelSession(ctx, "").
func (m *Manager) Cancel(ctx context.Context) error {
	return m.CancelSession(ctx, "")
}

// CancelSession sends a cancel notification for a specific session. Empty
// sessionID resolves to the initial session.
func (m *Manager) CancelSession(ctx context.Context, sessionID acp.SessionId) error {
	m.mu.Lock()
	conn := m.conn
	sessionID = m.resolveSessionLocked(sessionID)
	_, known := m.sessions[sessionID]
	m.mu.Unlock()

	if conn == nil {
		return fmt.Errorf("runtime: agent not started")
	}
	if !known {
		return fmt.Errorf("runtime: cancel: session %q not found", sessionID)
	}
	m.logger.Debug("cancel", "sessionID", sessionID)

	if err := conn.Cancel(ctx, acp.CancelNotification{SessionId: sessionID}); err != nil {
		return fmt.Errorf("runtime: cancel: %w", err)
	}
	return nil
}

// SetModel is a backward-compat shim — equivalent to SetModelSession(ctx, "", modelID).
func (m *Manager) SetModel(ctx context.Context, modelID string) error {
	return m.SetModelSession(ctx, "", modelID)
}

// SetModelSession switches a specific session to a different model via ACP
// session/set_model. Empty sessionID resolves to the initial session.
// Updates in-memory per-session models state; for the initial session
// also mirrors to the legacy state.json Session.Models field. Per-session
// models persistence is a follow-up state-schema change.
func (m *Manager) SetModelSession(ctx context.Context, sessionID acp.SessionId, modelID string) error {
	m.mu.Lock()
	conn := m.conn
	sessionID = m.resolveSessionLocked(sessionID)
	_, known := m.sessions[sessionID]
	initialID := m.sessionID
	m.mu.Unlock()

	if conn == nil {
		return fmt.Errorf("runtime: agent not started")
	}
	if !known {
		return fmt.Errorf("runtime: set_model: session %q not found", sessionID)
	}
	m.logger.Debug("set_model", "sessionID", sessionID, "modelID", modelID)

	_, err := conn.UnstableSetSessionModel(ctx, acp.UnstableSetSessionModelRequest{
		SessionId: sessionID,
		ModelId:   acp.UnstableModelId(modelID),
	})
	if err != nil {
		return fmt.Errorf("runtime: set model: %w", err)
	}

	// Re-lookup under the second lock — the session could have been ended
	// (or the whole map cleared by teardown) between the first unlock and
	// here, in which case the in-memory model mutation should silently no-op
	// rather than write through an orphan struct.
	m.mu.Lock()
	if sess, ok := m.sessions[sessionID]; ok && sess.models != nil {
		sess.models.CurrentModelId = acp.ModelId(modelID) //nolint:gosec // ModelId is string
	}
	// Legacy single-session models mirror for the initial session only.
	if sessionID == initialID && m.models != nil {
		m.models.CurrentModelId = acp.ModelId(modelID) //nolint:gosec // ModelId is string
	}
	m.mu.Unlock()

	// state.json's Session.Models is single-session; only update for the
	// initial session to keep legacy semantics. Multi-session persistence
	// is a follow-up.
	if sessionID == initialID {
		_ = m.writeState(func(s *apiruntime.State) {
			if s.Session != nil && s.Session.Models != nil {
				s.Session.Models.CurrentModelId = modelID
			}
		}, "set-model")
	}

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
// Returns a shared never-closing channel if the connection has not been established.
func (m *Manager) done() <-chan struct{} {
	m.mu.Lock()
	conn := m.conn
	m.mu.Unlock()
	if conn != nil {
		return conn.Done()
	}
	return disconnectedDone
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
	if m.usageFn != nil {
		state.Usage = m.usageFn()
	}

	if err := spec.WriteState(m.stateDir, state); err != nil {
		m.mu.Unlock()
		return err
	}

	statusChanged := prevErr == nil && previous.Phase != state.Phase
	if statusChanged {
		m.logger.Debug("state written", "from", previous.Phase, "to", state.Phase, "reason", reason)
	}
	hook := m.stateChangeHook
	change := StateChange{
		SessionID:     state.ID,
		PreviousPhase: previous.Phase,
		Phase:         state.Phase,
		PID:           state.PID,
		Reason:        reason,
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
	if m.usageFn != nil {
		state.Usage = m.usageFn()
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
		PreviousPhase:  state.Phase,
		Phase:          state.Phase,
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
