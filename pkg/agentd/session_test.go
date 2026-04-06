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

	// Test Create - session starts in "created" state.
	session := &meta.Session{
		ID:           uuid.New().String(),
		RuntimeClass: "default",
		WorkspaceID:  workspace.ID,
		Labels:       map[string]string{"env": "test"},
	}

	err := sm.Create(ctx, session)
	require.NoError(t, err, "Create should succeed")
	assert.Equal(t, meta.SessionStateCreated, session.State, "Session should start in 'created' state")

	// Test Get.
	retrieved, err := sm.Get(ctx, session.ID)
	require.NoError(t, err, "Get should succeed")
	require.NotNil(t, retrieved, "Get should return session")
	assert.Equal(t, session.ID, retrieved.ID, "ID should match")
	assert.Equal(t, meta.SessionStateCreated, retrieved.State, "State should be 'created'")
	assert.Equal(t, "test", retrieved.Labels["env"], "Labels should match")

	// Test Update - valid transition: created -> running.
	err = sm.Update(ctx, session.ID, meta.SessionStateRunning, nil)
	require.NoError(t, err, "Update (created -> running) should succeed")

	retrieved, err = sm.Get(ctx, session.ID)
	require.NoError(t, err, "Get after update should succeed")
	assert.Equal(t, meta.SessionStateRunning, retrieved.State, "State should be 'running'")

	// Test Update with labels.
	err = sm.Update(ctx, session.ID, meta.SessionStatePausedWarm, map[string]string{"env": "prod"})
	require.NoError(t, err, "Update with labels should succeed")

	retrieved, err = sm.Get(ctx, session.ID)
	require.NoError(t, err, "Get after label update should succeed")
	assert.Equal(t, meta.SessionStatePausedWarm, retrieved.State, "State should be 'paused:warm'")
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

	// Create multiple sessions in different states.
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

	// Transition one to running.
	require.NoError(t, sm.Update(ctx, sessionIDs[0], meta.SessionStateRunning, nil), "Transition to running should succeed")

	// Transition another to stopped.
	require.NoError(t, sm.Update(ctx, sessionIDs[1], meta.SessionStateStopped, nil), "Transition to stopped should succeed")

	// Test List all.
	all, err := sm.List(ctx, nil)
	require.NoError(t, err, "List should succeed")
	assert.Len(t, all, 3, "Should have 3 sessions")

	// Test List by state.
	createdSessions, err := sm.List(ctx, &meta.SessionFilter{State: meta.SessionStateCreated})
	require.NoError(t, err, "List by state should succeed")
	assert.Len(t, createdSessions, 1, "Should have 1 created session")

	runningSessions, err := sm.List(ctx, &meta.SessionFilter{State: meta.SessionStateRunning})
	require.NoError(t, err, "List by running state should succeed")
	assert.Len(t, runningSessions, 1, "Should have 1 running session")

	stoppedSessions, err := sm.List(ctx, &meta.SessionFilter{State: meta.SessionStateStopped})
	require.NoError(t, err, "List by stopped state should succeed")
	assert.Len(t, stoppedSessions, 1, "Should have 1 stopped session")
}

// TestSessionManagerValidTransitions tests all valid state transitions.
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
		// created -> running
		{"created_to_running", meta.SessionStateCreated, meta.SessionStateRunning},
		// created -> stopped
		{"created_to_stopped", meta.SessionStateCreated, meta.SessionStateStopped},
		// running -> paused:warm
		{"running_to_paused_warm", meta.SessionStateRunning, meta.SessionStatePausedWarm},
		// running -> stopped
		{"running_to_stopped", meta.SessionStateRunning, meta.SessionStateStopped},
		// paused:warm -> running
		{"paused_warm_to_running", meta.SessionStatePausedWarm, meta.SessionStateRunning},
		// paused:warm -> paused:cold
		{"paused_warm_to_paused_cold", meta.SessionStatePausedWarm, meta.SessionStatePausedCold},
		// paused:warm -> stopped
		{"paused_warm_to_stopped", meta.SessionStatePausedWarm, meta.SessionStateStopped},
		// paused:cold -> running
		{"paused_cold_to_running", meta.SessionStatePausedCold, meta.SessionStateRunning},
		// paused:cold -> stopped
		{"paused_cold_to_stopped", meta.SessionStatePausedCold, meta.SessionStateStopped},
	}

	for _, tc := range validTransitions {
		t.Run(tc.name, func(t *testing.T) {
			// Create a session in the "from" state.
			sessionID := uuid.New().String()

			session := &meta.Session{
				ID:           sessionID,
				RuntimeClass: "default",
				WorkspaceID:  workspace.ID,
			}
			require.NoError(t, sm.Create(ctx, session), "Create should succeed")

			// Transition to the "from" state if needed.
			if tc.from != meta.SessionStateCreated {
				// We need to navigate to the "from" state via valid transitions.
				// Let's use a helper path based on the from state.
				switch tc.from {
				case meta.SessionStateRunning:
					require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStateRunning, nil), "Transition to running should succeed")
				case meta.SessionStatePausedWarm:
					require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStateRunning, nil), "Transition to running should succeed")
					require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStatePausedWarm, nil), "Transition to paused:warm should succeed")
				case meta.SessionStatePausedCold:
					require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStateRunning, nil), "Transition to running should succeed")
					require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStatePausedWarm, nil), "Transition to paused:warm should succeed")
					require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStatePausedCold, nil), "Transition to paused:cold should succeed")
				case meta.SessionStateStopped:
					require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStateRunning, nil), "Transition to running should succeed")
					require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStateStopped, nil), "Transition to stopped should succeed")
				}
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
		// Cannot transition backwards
		{"running_to_created", meta.SessionStateRunning, meta.SessionStateCreated},
		{"paused_warm_to_created", meta.SessionStatePausedWarm, meta.SessionStateCreated},
		{"stopped_to_created", meta.SessionStateStopped, meta.SessionStateCreated},
		{"stopped_to_running", meta.SessionStateStopped, meta.SessionStateRunning},
		// Cannot skip states
		{"created_to_paused_warm", meta.SessionStateCreated, meta.SessionStatePausedWarm},
		{"created_to_paused_cold", meta.SessionStateCreated, meta.SessionStatePausedCold},
		{"running_to_paused_cold", meta.SessionStateRunning, meta.SessionStatePausedCold},
		// Cannot transition from terminal state
		{"stopped_to_paused_warm", meta.SessionStateStopped, meta.SessionStatePausedWarm},
		{"stopped_to_paused_cold", meta.SessionStateStopped, meta.SessionStatePausedCold},
		// Invalid cross-state jumps
		{"paused_cold_to_paused_warm", meta.SessionStatePausedCold, meta.SessionStatePausedWarm},
		{"paused_cold_to_created", meta.SessionStatePausedCold, meta.SessionStateCreated},
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
			case meta.SessionStateCreated:
				// Already in created state, do nothing.
			case meta.SessionStateRunning:
				require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStateRunning, nil), "Transition to running should succeed")
			case meta.SessionStatePausedWarm:
				require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStateRunning, nil), "Transition to running should succeed")
				require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStatePausedWarm, nil), "Transition to paused:warm should succeed")
			case meta.SessionStatePausedCold:
				require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStateRunning, nil), "Transition to running should succeed")
				require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStatePausedWarm, nil), "Transition to paused:warm should succeed")
				require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStatePausedCold, nil), "Transition to paused:cold should succeed")
			case meta.SessionStateStopped:
				require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStateRunning, nil), "Transition to running should succeed")
				require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStateStopped, nil), "Transition to stopped should succeed")
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
			require.True(t, ok, "Error should be ErrInvalidTransition")
			assert.Equal(t, sessionID, invalidErr.SessionID, "Error should have correct session ID")
			assert.Equal(t, tc.from, invalidErr.FromState, "Error should have correct from state")
			assert.Equal(t, tc.to, invalidErr.ToState, "Error should have correct to state")
			// For terminal states (stopped), ValidTransitions is legitimately empty.
			// For non-terminal states, ValidTransitions should list valid options.
			if tc.from != meta.SessionStateStopped {
				assert.NotEmpty(t, invalidErr.ValidTransitions, "Error should list valid transitions for non-terminal states")
			} else {
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

	// Test delete protection for running session.
	t.Run("running_session", func(t *testing.T) {
		sessionID := uuid.New().String()
		session := &meta.Session{
			ID:           sessionID,
			RuntimeClass: "default",
			WorkspaceID:  workspace.ID,
		}
		require.NoError(t, sm.Create(ctx, session), "Create should succeed")
		require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStateRunning, nil), "Transition to running should succeed")

		err := sm.Delete(ctx, sessionID)
		require.Error(t, err, "Delete should fail for running session")

		deleteErr, ok := err.(*ErrDeleteProtected)
		require.True(t, ok, "Error should be ErrDeleteProtected")
		assert.Equal(t, sessionID, deleteErr.SessionID, "Error should have correct session ID")
		assert.Equal(t, meta.SessionStateRunning, deleteErr.State, "Error should show running state")

		// Verify session still exists.
		stillExists, err := sm.Get(ctx, sessionID)
		require.NoError(t, err, "Get should succeed")
		assert.NotNil(t, stillExists, "Session should still exist")
	})

	// Test delete protection for paused:warm session.
	t.Run("paused_warm_session", func(t *testing.T) {
		sessionID := uuid.New().String()
		session := &meta.Session{
			ID:           sessionID,
			RuntimeClass: "default",
			WorkspaceID:  workspace.ID,
		}
		require.NoError(t, sm.Create(ctx, session), "Create should succeed")
		require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStateRunning, nil), "Transition to running should succeed")
		require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStatePausedWarm, nil), "Transition to paused:warm should succeed")

		err := sm.Delete(ctx, sessionID)
		require.Error(t, err, "Delete should fail for paused:warm session")

		deleteErr, ok := err.(*ErrDeleteProtected)
		require.True(t, ok, "Error should be ErrDeleteProtected")
		assert.Equal(t, meta.SessionStatePausedWarm, deleteErr.State, "Error should show paused:warm state")
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

		err := sm.Delete(ctx, sessionID)
		require.NoError(t, err, "Delete should succeed for created session")

		deleted, err := sm.Get(ctx, sessionID)
		require.NoError(t, err, "Get should succeed")
		assert.Nil(t, deleted, "Session should be deleted")
	})

	// Test delete allowed for paused:cold session.
	t.Run("paused_cold_session", func(t *testing.T) {
		sessionID := uuid.New().String()
		session := &meta.Session{
			ID:           sessionID,
			RuntimeClass: "default",
			WorkspaceID:  workspace.ID,
		}
		require.NoError(t, sm.Create(ctx, session), "Create should succeed")
		require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStateRunning, nil), "Transition to running should succeed")
		require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStatePausedWarm, nil), "Transition to paused:warm should succeed")
		require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStatePausedCold, nil), "Transition to paused:cold should succeed")

		err := sm.Delete(ctx, sessionID)
		require.NoError(t, err, "Delete should succeed for paused:cold session")

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
		require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStateRunning, nil), "Transition to running should succeed")
		require.NoError(t, sm.Update(ctx, sessionID, meta.SessionStateStopped, nil), "Transition to stopped should succeed")

		err := sm.Delete(ctx, sessionID)
		require.NoError(t, err, "Delete should succeed for stopped session")

		deleted, err := sm.Get(ctx, sessionID)
		require.NoError(t, err, "Get should succeed")
		assert.Nil(t, deleted, "Session should be deleted")
	})
}

// TestSessionManagerCreateInvalidInitialState tests that new sessions must start in created state.
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
	assert.Contains(t, err.Error(), "must start in 'created' state", "Error should mention created state requirement")
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

	// Use Transition method (same as Update but only changes state).
	err := sm.Transition(ctx, sessionID, meta.SessionStateRunning)
	require.NoError(t, err, "Transition to running should succeed")

	current, err := sm.Get(ctx, sessionID)
	require.NoError(t, err, "Get should succeed")
	assert.Equal(t, meta.SessionStateRunning, current.State, "State should be running after Transition")

	// Test invalid transition via Transition method.
	err = sm.Transition(ctx, sessionID, meta.SessionStateCreated)
	require.Error(t, err, "Invalid transition via Transition method should fail")
}

// TestIsValidTransition tests the isValidTransition helper function.
func TestIsValidTransition(t *testing.T) {
	t.Parallel()

	// Test all valid transitions.
	validCases := []struct {
		from meta.SessionState
		to   meta.SessionState
	}{
		{meta.SessionStateCreated, meta.SessionStateRunning},
		{meta.SessionStateCreated, meta.SessionStateStopped},
		{meta.SessionStateRunning, meta.SessionStatePausedWarm},
		{meta.SessionStateRunning, meta.SessionStateStopped},
		{meta.SessionStatePausedWarm, meta.SessionStateRunning},
		{meta.SessionStatePausedWarm, meta.SessionStatePausedCold},
		{meta.SessionStatePausedWarm, meta.SessionStateStopped},
		{meta.SessionStatePausedCold, meta.SessionStateRunning},
		{meta.SessionStatePausedCold, meta.SessionStateStopped},
	}

	for _, tc := range validCases {
		t.Run(string(tc.from)+"_to_"+string(tc.to), func(t *testing.T) {
			assert.True(t, isValidTransition(tc.from, tc.to), "Transition should be valid")
		})
	}

	// Test some invalid transitions.
	invalidCases := []struct {
		from meta.SessionState
		to   meta.SessionState
	}{
		{meta.SessionStateRunning, meta.SessionStateCreated},
		{meta.SessionStateStopped, meta.SessionStateRunning},
		{meta.SessionStateStopped, meta.SessionStatePausedWarm},
		{meta.SessionStateCreated, meta.SessionStatePausedWarm},
		{meta.SessionStateRunning, meta.SessionStatePausedCold},
		{meta.SessionStatePausedCold, meta.SessionStatePausedWarm},
	}

	for _, tc := range invalidCases {
		t.Run(string(tc.from)+"_to_"+string(tc.to), func(t *testing.T) {
			assert.False(t, isValidTransition(tc.from, tc.to), "Transition should be invalid")
		})
	}
}