// Package agentd implements the agent daemon that manages agent runtime sessions.
// This file tests the SessionManager with state machine validation.
package agentd

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/open-agent-d/open-agent-d/pkg/meta"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestMetaStore creates an in-memory SQLite store for testing.
func newTestMetaStore(t *testing.T) *meta.Store {
	t.Helper()

	store, err := meta.NewStore(":memory:")
	require.NoError(t, err, "NewStore with :memory: should succeed")
	require.NotNil(t, store, "Store should not be nil")

	t.Cleanup(func() {
		store.Close()
	})

	return store
}

// newTestSessionManager creates a SessionManager with an in-memory store.
func newTestSessionManager(t *testing.T) *SessionManager {
	t.Helper()

	store := newTestMetaStore(t)
	return NewSessionManager(store)
}

// createTestWorkspace creates a test workspace for session tests.
func createTestWorkspace(t *testing.T, ctx context.Context, store *meta.Store) *meta.Workspace {
	t.Helper()

	workspace := &meta.Workspace{
		ID:     uuid.New().String(),
		Name:   "test-workspace",
		Path:   "/tmp/test-workspace",
		Status: meta.WorkspaceStatusActive,
	}
	require.NoError(t, store.CreateWorkspace(ctx, workspace), "CreateWorkspace should succeed")
	return workspace
}

// TestSessionManagerCRUDRoundTrip tests Create, Get, Update, Delete round-trip.
func TestSessionManagerCRUDRoundTrip(t *testing.T) {
	t.Parallel()

	sm := newTestSessionManager(t)
	ctx := context.Background()

	workspace := createTestWorkspace(t, ctx, sm.store)

	// Test Create - session starts in "creating" state by default.
	session := &meta.Session{
		ID:           uuid.New().String(),
		RuntimeClass: "default",
		WorkspaceID:  workspace.ID,
		Labels:       map[string]string{"env": "test"},
	}

	err := sm.Create(ctx, session)
	require.NoError(t, err, "Create should succeed")
	assert.Equal(t, meta.SessionStateCreating, session.State, "Session should start in 'creating' state")

	// Test Get.
	retrieved, err := sm.Get(ctx, session.ID)
	require.NoError(t, err, "Get should succeed")
	require.NotNil(t, retrieved, "Get should return session")
	assert.Equal(t, session.ID, retrieved.ID, "ID should match")
	assert.Equal(t, meta.SessionStateCreating, retrieved.State, "State should be 'creating'")
	assert.Equal(t, "test", retrieved.Labels["env"], "Labels should match")

	// Test Update - valid transition: creating -> created.
	err = sm.Update(ctx, session.ID, meta.SessionStateCreated, nil)
	require.NoError(t, err, "Update (creating -> created) should succeed")

	retrieved, err = sm.Get(ctx, session.ID)
	require.NoError(t, err, "Get after update should succeed")
	assert.Equal(t, meta.SessionStateCreated, retrieved.State, "State should be 'created'")

	// Test Update: created -> running.
	err = sm.Update(ctx, session.ID, meta.SessionStateRunning, map[string]string{"env": "prod"})
	require.NoError(t, err, "Update with labels should succeed")

	retrieved, err = sm.Get(ctx, session.ID)
	require.NoError(t, err, "Get after label update should succeed")
	assert.Equal(t, meta.SessionStateRunning, retrieved.State, "State should be 'running'")
	assert.Equal(t, "prod", retrieved.Labels["env"], "Labels should be updated")

	// Transition to stopped so we can delete.
	err = sm.Update(ctx, session.ID, meta.SessionStateStopped, nil)
	require.NoError(t, err, "Update to stopped should succeed")

	// Test Delete.
	err = sm.Delete(ctx, session.ID)
	require.NoError(t, err, "Delete should succeed for stopped session")

	// Verify deleted.
	retrieved, err = sm.Get(ctx, session.ID)
	require.NoError(t, err, "Get after delete should succeed")
	assert.Nil(t, retrieved, "Session should be deleted")
}

// TestSessionManagerList tests List with filtering.
func TestSessionManagerList(t *testing.T) {
	t.Parallel()

	sm := newTestSessionManager(t)
	ctx := context.Background()

	workspace := createTestWorkspace(t, ctx, sm.store)

	// Create multiple sessions and advance them to different states.
	sessionIDs := []string{
		uuid.New().String(),
		uuid.New().String(),
		uuid.New().String(),
	}

	for _, id := range sessionIDs {
		session := &meta.Session{
			ID:           id,
			RuntimeClass: "default",
			WorkspaceID:  workspace.ID,
		}
		require.NoError(t, sm.Create(ctx, session), "Create should succeed")
	}

	// All start in creating. Transition first to created->running.
	require.NoError(t, sm.Update(ctx, sessionIDs[0], meta.SessionStateCreated, nil))
	require.NoError(t, sm.Update(ctx, sessionIDs[0], meta.SessionStateRunning, nil))

	// Transition second to created->stopped.
	require.NoError(t, sm.Update(ctx, sessionIDs[1], meta.SessionStateCreated, nil))
	require.NoError(t, sm.Update(ctx, sessionIDs[1], meta.SessionStateStopped, nil))

	// Third stays in creating.

	// Test List all.
	all, err := sm.List(ctx, nil)
	require.NoError(t, err, "List should succeed")
	assert.Len(t, all, 3, "Should have 3 sessions")

	// Test List by state.
	creatingSessions, err := sm.List(ctx, &meta.SessionFilter{State: meta.SessionStateCreating})
	require.NoError(t, err, "List by creating state should succeed")
	assert.Len(t, creatingSessions, 1, "Should have 1 creating session")

	runningSessions, err := sm.List(ctx, &meta.SessionFilter{State: meta.SessionStateRunning})
	require.NoError(t, err, "List by running state should succeed")
	assert.Len(t, runningSessions, 1, "Should have 1 running session")

	stoppedSessions, err := sm.List(ctx, &meta.SessionFilter{State: meta.SessionStateStopped})
	require.NoError(t, err, "List by stopped state should succeed")
	assert.Len(t, stoppedSessions, 1, "Should have 1 stopped session")
}

// TestSessionManagerValidTransitions tests all valid state transitions in the 5-state model.
func TestSessionManagerValidTransitions(t *testing.T) {
	t.Parallel()

	sm := newTestSessionManager(t)
	ctx := context.Background()

	workspace := createTestWorkspace(t, ctx, sm.store)

	// Define all valid transitions to test.
	validTransitions := []struct {
		name string
		from meta.SessionState
		to   meta.SessionState
	}{
		// creating -> created (bootstrap ok)
		{"creating_to_created", meta.SessionStateCreating, meta.SessionStateCreated},
		// creating -> error (bootstrap fail)
		{"creating_to_error", meta.SessionStateCreating, meta.SessionStateError},
		// created -> running (start prompt)
		{"created_to_running", meta.SessionStateCreated, meta.SessionStateRunning},
		// created -> stopped (agent/stop while idle)
		{"created_to_stopped", meta.SessionStateCreated, meta.SessionStateStopped},
		// running -> created (turn done)
		{"running_to_created", meta.SessionStateRunning, meta.SessionStateCreated},
		// running -> stopped (agent/stop)
		{"running_to_stopped", meta.SessionStateRunning, meta.SessionStateStopped},
		// running -> error (runtime failure)
		{"running_to_error", meta.SessionStateRunning, meta.SessionStateError},
		// stopped -> creating (agent/restart)
		{"stopped_to_creating", meta.SessionStateStopped, meta.SessionStateCreating},
	}

	for _, tc := range validTransitions {
		t.Run(tc.name, func(t *testing.T) {
			// Create a session — starts in "creating" state.
			sessionID := uuid.New().String()

			session := &meta.Session{
				ID:           sessionID,
				RuntimeClass: "default",
				WorkspaceID:  workspace.ID,
			}
			require.NoError(t, sm.Create(ctx, session), "Create should succeed")

			// Navigate to the "from" state via valid transitions.
			switch tc.from {
			case meta.SessionStateCreating:
				// Already in creating state.
			case meta.SessionStateCreated:
				require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStateCreated, nil))
			case meta.SessionStateRunning:
				require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStateCreated, nil))
				require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStateRunning, nil))
			case meta.SessionStateStopped:
				require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStateCreated, nil))
				require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStateStopped, nil))
			case meta.SessionStateError:
				require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStateError, nil))
			}

			// Verify current state.
			current, err := sm.Get(ctx, sessionID)
			require.NoError(t, err, "Get should succeed")
			require.Equal(t, tc.from, current.State, "Session should be in 'from' state")

			// Test the valid transition.
			err = sm.Transition(ctx, sessionID, tc.to)
			assert.NoError(t, err, "Valid transition %s -> %s should succeed", tc.from, tc.to)

			// Verify final state.
			final, err := sm.Get(ctx, sessionID)
			require.NoError(t, err, "Get after transition should succeed")
			assert.Equal(t, tc.to, final.State, "Session should be in 'to' state")
		})
	}
}

// TestSessionManagerInvalidTransitions tests invalid state transitions.
func TestSessionManagerInvalidTransitions(t *testing.T) {
	t.Parallel()

	sm := newTestSessionManager(t)
	ctx := context.Background()

	workspace := createTestWorkspace(t, ctx, sm.store)

	// Define invalid transitions to test.
	invalidTransitions := []struct {
		name string
		from meta.SessionState
		to   meta.SessionState
	}{
		// creating: cannot go to running, stopped
		{"creating_to_running", meta.SessionStateCreating, meta.SessionStateRunning},
		{"creating_to_stopped", meta.SessionStateCreating, meta.SessionStateStopped},
		// created: cannot go to creating or error
		{"created_to_creating", meta.SessionStateCreated, meta.SessionStateCreating},
		{"created_to_error", meta.SessionStateCreated, meta.SessionStateError},
		// running: cannot go to creating
		{"running_to_creating", meta.SessionStateRunning, meta.SessionStateCreating},
		// stopped: cannot go to created, running, error
		{"stopped_to_created", meta.SessionStateStopped, meta.SessionStateCreated},
		{"stopped_to_running", meta.SessionStateStopped, meta.SessionStateRunning},
		{"stopped_to_error", meta.SessionStateStopped, meta.SessionStateError},
		// error is terminal — no transitions allowed
		{"error_to_creating", meta.SessionStateError, meta.SessionStateCreating},
		{"error_to_created", meta.SessionStateError, meta.SessionStateCreated},
		{"error_to_running", meta.SessionStateError, meta.SessionStateRunning},
		{"error_to_stopped", meta.SessionStateError, meta.SessionStateStopped},
		// Verify paused:warm and paused:cold are rejected as targets
		{"creating_to_paused_warm", meta.SessionStateCreating, "paused:warm"},
		{"creating_to_paused_cold", meta.SessionStateCreating, "paused:cold"},
		{"created_to_paused_warm", meta.SessionStateCreated, "paused:warm"},
		{"running_to_paused_warm", meta.SessionStateRunning, "paused:warm"},
	}

	for _, tc := range invalidTransitions {
		t.Run(tc.name, func(t *testing.T) {
			sessionID := uuid.New().String()

			session := &meta.Session{
				ID:           sessionID,
				RuntimeClass: "default",
				WorkspaceID:  workspace.ID,
			}
			require.NoError(t, sm.Create(ctx, session), "Create should succeed")

			// Navigate to the "from" state via valid transitions.
			switch tc.from {
			case meta.SessionStateCreating:
				// Already in creating state.
			case meta.SessionStateCreated:
				require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStateCreated, nil))
			case meta.SessionStateRunning:
				require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStateCreated, nil))
				require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStateRunning, nil))
			case meta.SessionStateStopped:
				require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStateCreated, nil))
				require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStateStopped, nil))
			case meta.SessionStateError:
				require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStateError, nil))
			}

			// Verify current state.
			current, err := sm.Get(ctx, sessionID)
			require.NoError(t, err, "Get should succeed")
			require.Equal(t, tc.from, current.State, "Session should be in 'from' state")

			// Test the invalid transition.
			err = sm.Transition(ctx, sessionID, tc.to)
			require.Error(t, err, "Invalid transition %s -> %s should fail", tc.from, tc.to)

			// Verify it's ErrInvalidTransition.
			invalidErr, ok := err.(*ErrInvalidTransition)
			require.True(t, ok, "Error should be ErrInvalidTransition, got %T: %v", err, err)
			assert.Equal(t, sessionID, invalidErr.SessionID, "Error should have correct session ID")
			assert.Equal(t, tc.from, invalidErr.FromState, "Error should have correct from state")
			assert.Equal(t, tc.to, invalidErr.ToState, "Error should have correct to state")

			// For terminal states (error), ValidTransitions is empty.
			if tc.from == meta.SessionStateError {
				assert.Empty(t, invalidErr.ValidTransitions, "Terminal state should have no valid transitions")
			}

			// Verify state unchanged.
			unchanged, err := sm.Get(ctx, sessionID)
			require.NoError(t, err, "Get after failed transition should succeed")
			assert.Equal(t, tc.from, unchanged.State, "State should remain unchanged after failed transition")
		})
	}
}

// TestSessionManagerDeleteProtection tests delete protection for active sessions.
func TestSessionManagerDeleteProtection(t *testing.T) {
	t.Parallel()

	sm := newTestSessionManager(t)
	ctx := context.Background()

	workspace := createTestWorkspace(t, ctx, sm.store)

	// Test delete protection for creating session.
	t.Run("creating_session", func(t *testing.T) {
		sessionID := uuid.New().String()
		session := &meta.Session{
			ID:           sessionID,
			RuntimeClass: "default",
			WorkspaceID:  workspace.ID,
		}
		require.NoError(t, sm.Create(ctx, session), "Create should succeed")

		err := sm.Delete(ctx, sessionID)
		require.Error(t, err, "Delete should fail for creating session")

		deleteErr, ok := err.(*ErrDeleteProtected)
		require.True(t, ok, "Error should be ErrDeleteProtected")
		assert.Equal(t, sessionID, deleteErr.SessionID)
		assert.Equal(t, meta.SessionStateCreating, deleteErr.State)
	})

	// Test delete protection for running session.
	t.Run("running_session", func(t *testing.T) {
		sessionID := uuid.New().String()
		session := &meta.Session{
			ID:           sessionID,
			RuntimeClass: "default",
			WorkspaceID:  workspace.ID,
		}
		require.NoError(t, sm.Create(ctx, session), "Create should succeed")
		require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStateCreated, nil))
		require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStateRunning, nil))

		err := sm.Delete(ctx, sessionID)
		require.Error(t, err, "Delete should fail for running session")

		deleteErr, ok := err.(*ErrDeleteProtected)
		require.True(t, ok, "Error should be ErrDeleteProtected")
		assert.Equal(t, sessionID, deleteErr.SessionID)
		assert.Equal(t, meta.SessionStateRunning, deleteErr.State)

		// Verify session still exists.
		stillExists, err := sm.Get(ctx, sessionID)
		require.NoError(t, err, "Get should succeed")
		assert.NotNil(t, stillExists, "Session should still exist")
	})

	// Test delete allowed for created session.
	t.Run("created_session", func(t *testing.T) {
		sessionID := uuid.New().String()
		session := &meta.Session{
			ID:           sessionID,
			RuntimeClass: "default",
			WorkspaceID:  workspace.ID,
		}
		require.NoError(t, sm.Create(ctx, session), "Create should succeed")
		require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStateCreated, nil))

		err := sm.Delete(ctx, sessionID)
		require.NoError(t, err, "Delete should succeed for created session")

		deleted, err := sm.Get(ctx, sessionID)
		require.NoError(t, err, "Get should succeed")
		assert.Nil(t, deleted, "Session should be deleted")
	})

	// Test delete allowed for stopped session.
	t.Run("stopped_session", func(t *testing.T) {
		sessionID := uuid.New().String()
		session := &meta.Session{
			ID:           sessionID,
			RuntimeClass: "default",
			WorkspaceID:  workspace.ID,
		}
		require.NoError(t, sm.Create(ctx, session), "Create should succeed")
		require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStateCreated, nil))
		require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStateStopped, nil))

		err := sm.Delete(ctx, sessionID)
		require.NoError(t, err, "Delete should succeed for stopped session")

		deleted, err := sm.Get(ctx, sessionID)
		require.NoError(t, err, "Get should succeed")
		assert.Nil(t, deleted, "Session should be deleted")
	})

	// Test delete allowed for error session.
	t.Run("error_session", func(t *testing.T) {
		sessionID := uuid.New().String()
		session := &meta.Session{
			ID:           sessionID,
			RuntimeClass: "default",
			WorkspaceID:  workspace.ID,
		}
		require.NoError(t, sm.Create(ctx, session), "Create should succeed")
		require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStateError, nil))

		err := sm.Delete(ctx, sessionID)
		require.NoError(t, err, "Delete should succeed for error session")

		deleted, err := sm.Get(ctx, sessionID)
		require.NoError(t, err, "Get should succeed")
		assert.Nil(t, deleted, "Session should be deleted")
	})
}

// TestSessionManagerCreateInvalidInitialState tests that new sessions must start in creating or created state.
func TestSessionManagerCreateInvalidInitialState(t *testing.T) {
	t.Parallel()

	sm := newTestSessionManager(t)
	ctx := context.Background()

	workspace := createTestWorkspace(t, ctx, sm.store)

	// Try to create session in running state.
	session := &meta.Session{
		ID:           uuid.New().String(),
		RuntimeClass: "default",
		WorkspaceID:  workspace.ID,
		State:        meta.SessionStateRunning, // Invalid initial state.
	}

	err := sm.Create(ctx, session)
	require.Error(t, err, "Create should fail with invalid initial state")
	assert.Contains(t, err.Error(), "must start in 'creating' or 'created' state")
}

// TestSessionManagerCreateWithCreatedState tests that sessions can also start in "created" state.
func TestSessionManagerCreateWithCreatedState(t *testing.T) {
	t.Parallel()

	sm := newTestSessionManager(t)
	ctx := context.Background()

	workspace := createTestWorkspace(t, ctx, sm.store)

	session := &meta.Session{
		ID:           uuid.New().String(),
		RuntimeClass: "default",
		WorkspaceID:  workspace.ID,
		State:        meta.SessionStateCreated,
	}

	err := sm.Create(ctx, session)
	require.NoError(t, err, "Create with 'created' state should succeed")
	assert.Equal(t, meta.SessionStateCreated, session.State)
}

// TestSessionManagerGetNonExistent tests Get for non-existent session.
func TestSessionManagerGetNonExistent(t *testing.T) {
	t.Parallel()

	sm := newTestSessionManager(t)
	ctx := context.Background()

	retrieved, err := sm.Get(ctx, uuid.New().String())
	require.NoError(t, err, "Get should not error for non-existent session")
	assert.Nil(t, retrieved, "Get should return nil for non-existent session")
}

// TestSessionManagerUpdateNonExistent tests Update for non-existent session.
func TestSessionManagerUpdateNonExistent(t *testing.T) {
	t.Parallel()

	sm := newTestSessionManager(t)
	ctx := context.Background()

	err := sm.Update(ctx, uuid.New().String(), meta.SessionStateRunning, nil)
	require.Error(t, err, "Update should fail for non-existent session")
	assert.Contains(t, err.Error(), "does not exist", "Error should mention session doesn't exist")
}

// TestSessionManagerDeleteNonExistent tests Delete for non-existent session.
func TestSessionManagerDeleteNonExistent(t *testing.T) {
	t.Parallel()

	sm := newTestSessionManager(t)
	ctx := context.Background()

	err := sm.Delete(ctx, uuid.New().String())
	require.Error(t, err, "Delete should fail for non-existent session")
	assert.Contains(t, err.Error(), "does not exist", "Error should mention session doesn't exist")
}

// TestSessionManagerTransitionMethod tests the Transition method specifically.
func TestSessionManagerTransitionMethod(t *testing.T) {
	t.Parallel()

	sm := newTestSessionManager(t)
	ctx := context.Background()

	workspace := createTestWorkspace(t, ctx, sm.store)

	sessionID := uuid.New().String()
	session := &meta.Session{
		ID:           sessionID,
		RuntimeClass: "default",
		WorkspaceID:  workspace.ID,
	}
	require.NoError(t, sm.Create(ctx, session), "Create should succeed")

	// Use Transition method: creating -> created.
	err := sm.Transition(ctx, sessionID, meta.SessionStateCreated)
	require.NoError(t, err, "Transition to created should succeed")

	// created -> running.
	err = sm.Transition(ctx, sessionID, meta.SessionStateRunning)
	require.NoError(t, err, "Transition to running should succeed")

	current, err := sm.Get(ctx, sessionID)
	require.NoError(t, err, "Get should succeed")
	assert.Equal(t, meta.SessionStateRunning, current.State, "State should be running after Transition")

	// Test invalid transition via Transition method.
	err = sm.Transition(ctx, sessionID, meta.SessionStateCreating)
	require.Error(t, err, "Invalid transition via Transition method should fail")
}

// TestIsValidTransition tests the isValidTransition helper function.
func TestIsValidTransition(t *testing.T) {
	t.Parallel()

	// Test all valid transitions in 5-state model.
	validCases := []struct {
		from meta.SessionState
		to   meta.SessionState
	}{
		{meta.SessionStateCreating, meta.SessionStateCreated},
		{meta.SessionStateCreating, meta.SessionStateError},
		{meta.SessionStateCreated, meta.SessionStateRunning},
		{meta.SessionStateCreated, meta.SessionStateStopped},
		{meta.SessionStateRunning, meta.SessionStateCreated},
		{meta.SessionStateRunning, meta.SessionStateStopped},
		{meta.SessionStateRunning, meta.SessionStateError},
		{meta.SessionStateStopped, meta.SessionStateCreating},
	}

	for _, tc := range validCases {
		t.Run(string(tc.from)+"_to_"+string(tc.to), func(t *testing.T) {
			assert.True(t, isValidTransition(tc.from, tc.to), "Transition should be valid")
		})
	}

	// Test invalid transitions including paused:* rejection.
	invalidCases := []struct {
		from meta.SessionState
		to   meta.SessionState
	}{
		// Standard invalid
		{meta.SessionStateCreating, meta.SessionStateRunning},
		{meta.SessionStateCreating, meta.SessionStateStopped},
		{meta.SessionStateCreated, meta.SessionStateCreating},
		{meta.SessionStateCreated, meta.SessionStateError},
		{meta.SessionStateRunning, meta.SessionStateCreating},
		{meta.SessionStateError, meta.SessionStateCreating},
		{meta.SessionStateError, meta.SessionStateCreated},
		{meta.SessionStateError, meta.SessionStateRunning},
		{meta.SessionStateError, meta.SessionStateStopped},
		// paused:* are no longer valid states — rejected as unknown
		{meta.SessionStateRunning, "paused:warm"},
		{meta.SessionStateRunning, "paused:cold"},
		{meta.SessionStateCreated, "paused:warm"},
		// paused:* as source states — unknown, no transitions defined
		{"paused:warm", meta.SessionStateRunning},
		{"paused:cold", meta.SessionStateRunning},
	}

	for _, tc := range invalidCases {
		t.Run(string(tc.from)+"_to_"+string(tc.to), func(t *testing.T) {
			assert.False(t, isValidTransition(tc.from, tc.to), "Transition should be invalid")
		})
	}
}
