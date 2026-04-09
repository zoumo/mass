// Package agentd implements the agent daemon that manages agent runtime sessions.
// This file defines the SessionManager for session lifecycle management with state machine validation.
package agentd

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/open-agent-d/open-agent-d/pkg/meta"
)

// ErrInvalidTransition is returned when a session state transition is not allowed.
type ErrInvalidTransition struct {
	SessionID        string
	FromState        meta.SessionState
	ToState          meta.SessionState
	ValidTransitions []meta.SessionState
}

func (e *ErrInvalidTransition) Error() string {
	return fmt.Sprintf("agentd: invalid state transition for session %s: cannot transition from %s to %s (valid transitions: %v)",
		e.SessionID, e.FromState, e.ToState, e.ValidTransitions)
}

// ErrDeleteProtected is returned when attempting to delete an active session.
type ErrDeleteProtected struct {
	SessionID string
	State     meta.SessionState
}

func (e *ErrDeleteProtected) Error() string {
	return fmt.Sprintf("agentd: cannot delete session %s in state %s (session is active)",
		e.SessionID, e.State)
}

// validTransitions defines the allowed state machine transitions.
// Key: current state, Value: set of valid next states.
// Mirrors the 5-state agent model: creating, created, running, stopped, error.
var validTransitions = map[meta.SessionState][]meta.SessionState{
	// creating -> created (bootstrap ok) or error (bootstrap fail)
	meta.SessionStateCreating: {
		meta.SessionStateCreated,
		meta.SessionStateError,
	},
	// created -> running (prompt) or stopped (agent/stop while idle)
	meta.SessionStateCreated: {
		meta.SessionStateRunning,
		meta.SessionStateStopped,
	},
	// running -> created (turn done), stopped (agent/stop), error (runtime failure)
	meta.SessionStateRunning: {
		meta.SessionStateCreated,
		meta.SessionStateStopped,
		meta.SessionStateError,
	},
	// stopped -> creating (agent/restart)
	meta.SessionStateStopped: {
		meta.SessionStateCreating,
	},
	// error is terminal — no valid transitions
	meta.SessionStateError: {},
}

// deleteProtectedStates defines states where deletion is blocked.
// Sessions in these states are considered "active" and cannot be deleted.
var deleteProtectedStates = map[meta.SessionState]bool{
	meta.SessionStateCreating: true,
	meta.SessionStateRunning:  true,
}

// SessionManager manages session lifecycle with state machine validation.
// It wraps meta.Store and adds:
//   - State transition validation (prevents invalid transitions)
//   - Delete protection (blocks deletion of active sessions)
//   - Transition logging for observability
type SessionManager struct {
	store  *meta.Store
	logger *slog.Logger
}

// NewSessionManager creates a new SessionManager wrapping the provided store.
// The logger is configured with component=agentd.session for observability.
func NewSessionManager(store *meta.Store) *SessionManager {
	logger := slog.Default().With("component", "agentd.session")
	return &SessionManager{
		store:  store,
		logger: logger,
	}
}

// Create creates a new session in the "creating" state.
// The session must have a valid workspace_id and runtime_class.
// Returns the created session or an error.
func (m *SessionManager) Create(ctx context.Context, session *meta.Session) error {
	// Ensure initial state is "creating" if not specified.
	if session.State == "" {
		session.State = meta.SessionStateCreating
	}

	// Validate initial state is allowed for new sessions.
	if session.State != meta.SessionStateCreating && session.State != meta.SessionStateCreated {
		return fmt.Errorf("agentd: new session must start in 'creating' or 'created' state, got %s", session.State)
	}

	m.logger.Info("creating session",
		"session_id", session.ID,
		"workspace_id", session.WorkspaceID,
		"runtime_class", session.RuntimeClass,
		"state", session.State)

	if err := m.store.CreateSession(ctx, session); err != nil {
		m.logger.Error("failed to create session",
			"session_id", session.ID,
			"error", err)
		return fmt.Errorf("agentd: failed to create session: %w", err)
	}

	m.logger.Info("session created",
		"session_id", session.ID,
		"state", session.State)

	return nil
}

// Get retrieves a session by ID.
// Returns nil if the session doesn't exist.
func (m *SessionManager) Get(ctx context.Context, id string) (*meta.Session, error) {
	session, err := m.store.GetSession(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("agentd: failed to get session: %w", err)
	}
	return session, nil
}

// List retrieves sessions matching the filter.
// If filter is nil, returns all sessions.
func (m *SessionManager) List(ctx context.Context, filter *meta.SessionFilter) ([]*meta.Session, error) {
	sessions, err := m.store.ListSessions(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("agentd: failed to list sessions: %w", err)
	}
	return sessions, nil
}

// Update updates a session's state and/or labels.
// Validates state transitions before applying changes.
// Returns ErrInvalidTransition if the transition is not allowed.
func (m *SessionManager) Update(ctx context.Context, id string, newState meta.SessionState, labels map[string]string) error {
	// Get current session to validate transition.
	current, err := m.store.GetSession(ctx, id)
	if err != nil {
		return fmt.Errorf("agentd: failed to get session for update: %w", err)
	}
	if current == nil {
		return fmt.Errorf("agentd: session %s does not exist", id)
	}

	// Validate state transition if newState is specified.
	if newState != "" && newState != current.State {
		if !isValidTransition(current.State, newState) {
			validTargets := validTransitions[current.State]
			m.logger.Warn("invalid state transition attempted",
				"session_id", id,
				"from_state", current.State,
				"to_state", newState,
				"valid_transitions", validTargets)
			return &ErrInvalidTransition{
				SessionID:        id,
				FromState:        current.State,
				ToState:          newState,
				ValidTransitions: validTargets,
			}
		}

		// Log the transition.
		m.logger.Info("session state transition",
			"session_id", id,
			"from_state", current.State,
			"to_state", newState)
	}

	// Apply the update.
	if err := m.store.UpdateSession(ctx, id, newState, labels); err != nil {
		m.logger.Error("failed to update session",
			"session_id", id,
			"error", err)
		return fmt.Errorf("agentd: failed to update session: %w", err)
	}

	m.logger.Debug("session updated",
		"session_id", id,
		"state", newState)

	return nil
}

// Delete deletes a session by ID.
// Returns ErrDeleteProtected if the session is in an active state (running or creating).
// Sessions in created, stopped, or error states can be deleted.
func (m *SessionManager) Delete(ctx context.Context, id string) error {
	// Get current session to validate deletion.
	current, err := m.store.GetSession(ctx, id)
	if err != nil {
		return fmt.Errorf("agentd: failed to get session for deletion: %w", err)
	}
	if current == nil {
		return fmt.Errorf("agentd: session %s does not exist", id)
	}

	// Check if session is in a delete-protected state.
	if deleteProtectedStates[current.State] {
		m.logger.Warn("delete blocked: session is active",
			"session_id", id,
			"state", current.State)
		return &ErrDeleteProtected{
			SessionID: id,
			State:     current.State,
		}
	}

	m.logger.Info("deleting session",
		"session_id", id,
		"state", current.State)

	if err := m.store.DeleteSession(ctx, id); err != nil {
		m.logger.Error("failed to delete session",
			"session_id", id,
			"error", err)
		return fmt.Errorf("agentd: failed to delete session: %w", err)
	}

	m.logger.Info("session deleted",
		"session_id", id)

	return nil
}

// Transition performs a state transition on a session.
// This is the primary method for Process Manager integration.
// Validates the transition and returns ErrInvalidTransition if not allowed.
func (m *SessionManager) Transition(ctx context.Context, id string, toState meta.SessionState) error {
	return m.Update(ctx, id, toState, nil)
}

// isValidTransition checks if a transition from one state to another is valid.
func isValidTransition(from, to meta.SessionState) bool {
	validTargets, ok := validTransitions[from]
	if !ok {
		// Unknown source state - no transitions allowed.
		return false
	}

	for _, valid := range validTargets {
		if valid == to {
			return true
		}
	}

	return false
}
