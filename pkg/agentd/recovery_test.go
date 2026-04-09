package agentd

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/open-agent-d/open-agent-d/pkg/meta"
	"github.com/open-agent-d/open-agent-d/pkg/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupRecoveryTest creates a ProcessManager backed by a real meta.Store with
// no running processes. Returns the manager, store, and a cleanup function.
func setupRecoveryTest(t *testing.T) (*ProcessManager, *meta.Store) {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "meta.db")

	store, err := meta.NewStore(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	sessions := NewSessionManager(store)
	agents := NewAgentManager(store)

	cfg := Config{
		Socket:        filepath.Join(tmpDir, "agentd.sock"),
		WorkspaceRoot: filepath.Join(tmpDir, "workspaces"),
	}

	registry, err := NewRuntimeClassRegistry(nil)
	require.NoError(t, err)

	pm := NewProcessManager(registry, sessions, agents, store, cfg)
	return pm, store
}

// createRecoveryTestSession creates a session in the given state with the given socket path.
func createRecoveryTestSession(t *testing.T, ctx context.Context, store *meta.Store, wsID string, state meta.SessionState, socketPath string) string {
	t.Helper()
	sessionID := uuid.New().String()
	session := &meta.Session{
		ID:             sessionID,
		RuntimeClass:   "default",
		WorkspaceID:    wsID,
		State:          state,
		ShimSocketPath: socketPath,
		ShimStateDir:   "/tmp/shim-state-" + sessionID,
		ShimPID:        99999,
	}
	require.NoError(t, store.CreateSession(ctx, session))
	return sessionID
}

// TestRecoverSessions_LiveShim verifies that a session with a live shim
// is recovered: the shim client is connected, status/history/subscribe are
// called, and the session is registered in the processes map.
func TestRecoverSessions_LiveShim(t *testing.T) {
	pm, store := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start a mock shim server.
	srv, socketPath := newMockShimServer(t)
	srv.mu.Lock()
	srv.statusResult = RuntimeStatusResult{
		State: spec.State{
			OarVersion: "0.1.0",
			ID:         "recovered-session",
			Status:     spec.StatusRunning,
			Bundle:     "/tmp/test-bundle",
		},
		Recovery: RuntimeStatusRecovery{LastSeq: 5},
	}
	srv.mu.Unlock()

	// Create workspace and a "running" session pointing at the mock socket.
	ws := createTestWorkspace(t, ctx, store)
	wsID := ws.ID
	sessionID := createRecoveryTestSession(t, ctx, store, wsID, meta.SessionStateRunning, socketPath)

	// Run recovery.
	err := pm.RecoverSessions(ctx)
	require.NoError(t, err)

	// Verify the session is registered in the processes map.
	shimProc := pm.GetProcess(sessionID)
	require.NotNil(t, shimProc, "recovered session should be in processes map")
	assert.Equal(t, sessionID, shimProc.SessionID)
	assert.NotNil(t, shimProc.Client, "client should be connected")
	assert.Equal(t, socketPath, shimProc.SocketPath)

	// Verify the mock shim received a subscribe call.
	srv.mu.Lock()
	subscribed := srv.subscribed
	srv.mu.Unlock()
	assert.True(t, subscribed, "shim should have been subscribed")

	// Verify session is still in "running" state (not changed).
	session, err := store.GetSession(ctx, sessionID)
	require.NoError(t, err)
	assert.Equal(t, meta.SessionStateRunning, session.State)

	// Cleanup: close the mock server and wait for the watcher to clean up.
	srv.close()
	select {
	case <-shimProc.Done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for recovered process cleanup")
	}
}

// TestRecoverSessions_DeadShim verifies that when the shim socket is
// unreachable, the session is marked as stopped (fail-closed).
func TestRecoverSessions_DeadShim(t *testing.T) {
	pm, store := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ws := createTestWorkspace(t, ctx, store)
	wsID := ws.ID

	// Create a "running" session pointing at a nonexistent socket.
	deadSocketPath := "/tmp/nonexistent-shim-" + uuid.New().String() + ".sock"
	sessionID := createRecoveryTestSession(t, ctx, store, wsID, meta.SessionStateRunning, deadSocketPath)

	// Run recovery.
	err := pm.RecoverSessions(ctx)
	require.NoError(t, err, "RecoverSessions should not return error for individual failures")

	// Verify session was marked stopped.
	session, err := store.GetSession(ctx, sessionID)
	require.NoError(t, err)
	assert.Equal(t, meta.SessionStateStopped, session.State,
		"dead shim session should be marked stopped")

	// Verify the session is NOT in the processes map.
	assert.Nil(t, pm.GetProcess(sessionID),
		"dead shim session should not be in processes map")
}

// TestRecoverSessions_NoSessions verifies that RecoverSessions is a no-op
// when there are no sessions in the database.
func TestRecoverSessions_NoSessions(t *testing.T) {
	pm, _ := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := pm.RecoverSessions(ctx)
	require.NoError(t, err)

	// No sessions should be registered.
	assert.Empty(t, pm.ListProcesses())
}

// TestRecoverSessions_SkipsStoppedSessions verifies that already-stopped
// sessions are not included in the recovery pass.
func TestRecoverSessions_SkipsStoppedSessions(t *testing.T) {
	pm, store := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ws := createTestWorkspace(t, ctx, store)
	wsID := ws.ID

	// Create a stopped session.
	createRecoveryTestSession(t, ctx, store, wsID, meta.SessionStateStopped, "/tmp/whatever.sock")

	err := pm.RecoverSessions(ctx)
	require.NoError(t, err)

	// No sessions should be recovered.
	assert.Empty(t, pm.ListProcesses())
}

// TestRecoverSessions_MixedLiveAndDead verifies correct handling when some
// sessions have live shims and others have dead ones.
func TestRecoverSessions_MixedLiveAndDead(t *testing.T) {
	pm, store := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start a mock shim for the live session.
	srv, liveSocketPath := newMockShimServer(t)
	srv.mu.Lock()
	srv.statusResult = RuntimeStatusResult{
		State:    spec.State{Status: spec.StatusRunning, ID: "live"},
		Recovery: RuntimeStatusRecovery{LastSeq: 2},
	}
	srv.mu.Unlock()

	ws := createTestWorkspace(t, ctx, store)
	wsID := ws.ID

	// Create a live session.
	liveID := createRecoveryTestSession(t, ctx, store, wsID, meta.SessionStateRunning, liveSocketPath)

	// Create a dead session.
	deadID := createRecoveryTestSession(t, ctx, store, wsID, meta.SessionStateRunning,
		"/tmp/dead-shim-"+uuid.New().String()+".sock")

	// Run recovery.
	err := pm.RecoverSessions(ctx)
	require.NoError(t, err)

	// Live session should be recovered.
	assert.NotNil(t, pm.GetProcess(liveID), "live session should be recovered")

	// Dead session should be marked stopped and not in processes map.
	assert.Nil(t, pm.GetProcess(deadID), "dead session should not be in processes map")
	deadSession, err := store.GetSession(ctx, deadID)
	require.NoError(t, err)
	assert.Equal(t, meta.SessionStateStopped, deadSession.State)

	// Clean up the live mock.
	srv.close()
	shimProc := pm.GetProcess(liveID)
	if shimProc != nil {
		select {
		case <-shimProc.Done:
		case <-time.After(3 * time.Second):
		}
	}
}

// TestRecoverSessions_NoSocketPath verifies that a session with an empty
// socket path is marked stopped (it cannot be recovered).
func TestRecoverSessions_NoSocketPath(t *testing.T) {
	pm, store := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ws := createTestWorkspace(t, ctx, store)
	wsID := ws.ID

	// Create a running session with no socket path.
	sessionID := createRecoveryTestSession(t, ctx, store, wsID, meta.SessionStateRunning, "")

	err := pm.RecoverSessions(ctx)
	require.NoError(t, err)

	// Session should be marked stopped.
	session, err := store.GetSession(ctx, sessionID)
	require.NoError(t, err)
	assert.Equal(t, meta.SessionStateStopped, session.State)
}

// TestRecoverSessions_ShimReportsStopped verifies that when a shim reports
// stopped (but DB still says running), the session is fail-closed: marked
// stopped in DB, not placed in the processes map, and NOT subscribed.
func TestRecoverSessions_ShimReportsStopped(t *testing.T) {
	pm, store := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start a mock shim that reports stopped.
	srv, socketPath := newMockShimServer(t)
	srv.mu.Lock()
	srv.statusResult = RuntimeStatusResult{
		State: spec.State{
			OarVersion: "0.1.0",
			ID:         "stopped-session",
			Status:     spec.StatusStopped,
			Bundle:     "/tmp/test-bundle",
		},
		Recovery: RuntimeStatusRecovery{LastSeq: 0},
	}
	srv.mu.Unlock()

	// Create workspace and a "running" session pointing at the mock socket.
	ws := createTestWorkspace(t, ctx, store)
	sessionID := createRecoveryTestSession(t, ctx, store, ws.ID, meta.SessionStateRunning, socketPath)

	// Run recovery.
	err := pm.RecoverSessions(ctx)
	require.NoError(t, err, "RecoverSessions should not return error for individual failures")

	// Session should be marked stopped in DB (fail-closed).
	session, err := store.GetSession(ctx, sessionID)
	require.NoError(t, err)
	assert.Equal(t, meta.SessionStateStopped, session.State,
		"shim-reports-stopped session should be marked stopped in DB")

	// Session should NOT be in the processes map.
	assert.Nil(t, pm.GetProcess(sessionID),
		"shim-reports-stopped session should not be in processes map")

	// Mock shim should NOT have been subscribed.
	srv.mu.Lock()
	subscribed := srv.subscribed
	srv.mu.Unlock()
	assert.False(t, subscribed,
		"shim should not have been subscribed when it reports stopped")
}

// TestRecoverSessions_ReconcileCreatedToRunning verifies that when the DB
// says "created" but the shim reports "running", the reconciliation logic
// transitions the DB to running and the session is fully recovered.
func TestRecoverSessions_ReconcileCreatedToRunning(t *testing.T) {
	pm, store := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start a mock shim that reports running.
	srv, socketPath := newMockShimServer(t)
	srv.mu.Lock()
	srv.statusResult = RuntimeStatusResult{
		State: spec.State{
			OarVersion: "0.1.0",
			ID:         "reconciled-session",
			Status:     spec.StatusRunning,
			Bundle:     "/tmp/test-bundle",
		},
		Recovery: RuntimeStatusRecovery{LastSeq: 3},
	}
	srv.mu.Unlock()

	// Create workspace and a "created" session pointing at the mock socket.
	ws := createTestWorkspace(t, ctx, store)
	sessionID := createRecoveryTestSession(t, ctx, store, ws.ID, meta.SessionStateCreated, socketPath)

	// Run recovery.
	err := pm.RecoverSessions(ctx)
	require.NoError(t, err)

	// Session state in DB should now be "running" (reconciled from created).
	session, err := store.GetSession(ctx, sessionID)
	require.NoError(t, err)
	assert.Equal(t, meta.SessionStateRunning, session.State,
		"session should be transitioned from created to running")

	// Session should be in the processes map (fully recovered).
	shimProc := pm.GetProcess(sessionID)
	require.NotNil(t, shimProc, "reconciled session should be in processes map")
	assert.Equal(t, sessionID, shimProc.SessionID)
	assert.NotNil(t, shimProc.Client, "client should be connected")

	// Mock shim should have been subscribed.
	srv.mu.Lock()
	subscribed := srv.subscribed
	srv.mu.Unlock()
	assert.True(t, subscribed, "shim should have been subscribed after reconciliation")

	// Cleanup.
	srv.close()
	select {
	case <-shimProc.Done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for recovered process cleanup")
	}
}

// TestRecoverSessions_ShimMismatchLogsWarning verifies that when the shim
// reports a different status than the DB (but not the stopped or created→running
// cases), recovery proceeds: the session is placed in the processes map and
// subscribed, but the DB state is NOT changed.
func TestRecoverSessions_ShimMismatchLogsWarning(t *testing.T) {
	pm, store := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start a mock shim that reports running.
	srv, socketPath := newMockShimServer(t)
	srv.mu.Lock()
	srv.statusResult = RuntimeStatusResult{
		State: spec.State{
			OarVersion: "0.1.0",
			ID:         "mismatched-session",
			Status:     spec.StatusRunning,
			Bundle:     "/tmp/test-bundle",
		},
		Recovery: RuntimeStatusRecovery{LastSeq: 1},
	}
	srv.mu.Unlock()

	// Create workspace and a "creating" session pointing at the mock socket.
	// The DB says "creating" but the runtime says "running" — this is a state mismatch
	// that falls into the default branch (logged but not reconciled).
	ws := createTestWorkspace(t, ctx, store)
	sessionID := createRecoveryTestSession(t, ctx, store, ws.ID, meta.SessionStateCreating, socketPath)

	// Run recovery.
	err := pm.RecoverSessions(ctx)
	require.NoError(t, err)

	// Session should be in the processes map (recovery proceeded despite mismatch).
	shimProc := pm.GetProcess(sessionID)
	require.NotNil(t, shimProc, "mismatched session should still be recovered")
	assert.Equal(t, sessionID, shimProc.SessionID)

	// DB state should remain "creating" — the default branch logs but
	// does not update the DB state.
	session, err := store.GetSession(ctx, sessionID)
	require.NoError(t, err)
	assert.Equal(t, meta.SessionStateCreating, session.State,
		"DB state should remain creating (mismatch only logged, not reconciled)")

	// Mock shim should have been subscribed (recovery completed).
	srv.mu.Lock()
	subscribed := srv.subscribed
	srv.mu.Unlock()
	assert.True(t, subscribed, "shim should have been subscribed despite mismatch")

	// Cleanup.
	srv.close()
	select {
	case <-shimProc.Done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for recovered process cleanup")
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Agent state reconciliation tests
// ────────────────────────────────────────────────────────────────────────────

// createTestAgentForRecovery creates a room and an agent record for recovery tests.
// Returns the agent ID.
func createTestAgentForRecovery(t *testing.T, ctx context.Context, store *meta.Store, state meta.AgentState) string {
	t.Helper()
	roomName := "test-room-" + uuid.New().String()
	require.NoError(t, store.CreateRoom(ctx, &meta.Room{
		Name:              roomName,
		CommunicationMode: meta.CommunicationModeMesh,
	}))

	ws := createTestWorkspace(t, ctx, store)
	agentID := uuid.New().String()
	require.NoError(t, store.CreateAgent(ctx, &meta.Agent{
		ID:           agentID,
		Room:         roomName,
		Name:         "agent-" + agentID[:8],
		RuntimeClass: "default",
		WorkspaceID:  ws.ID,
		State:        state,
	}))
	return agentID
}

// TestRecoverSessions_AgentStateErrorOnDeadShim verifies that when a session's
// shim is unreachable and the session is stopped, the linked agent is
// transitioned to AgentStateError.
func TestRecoverSessions_AgentStateErrorOnDeadShim(t *testing.T) {
	pm, store := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Create workspace + agent.
	ws := createTestWorkspace(t, ctx, store)
	agentID := createTestAgentForRecovery(t, ctx, store, meta.AgentStateRunning)

	// Create a "running" session linked to the agent, pointing at a dead socket.
	deadSocket := "/tmp/dead-" + uuid.New().String() + ".sock"
	sessionID := uuid.New().String()
	require.NoError(t, store.CreateSession(ctx, &meta.Session{
		ID:             sessionID,
		RuntimeClass:   "default",
		WorkspaceID:    ws.ID,
		State:          meta.SessionStateRunning,
		ShimSocketPath: deadSocket,
		ShimStateDir:   "/tmp/shim-state-" + sessionID,
		ShimPID:        99999,
		AgentID:        agentID,
	}))

	// Run recovery — shim unreachable, session → stopped, agent → error.
	require.NoError(t, pm.RecoverSessions(ctx))

	// Session should be stopped.
	session, err := store.GetSession(ctx, sessionID)
	require.NoError(t, err)
	assert.Equal(t, meta.SessionStateStopped, session.State, "session should be stopped")

	// Agent should be in error state.
	agent, err := store.GetAgent(ctx, agentID)
	require.NoError(t, err)
	require.NotNil(t, agent)
	assert.Equal(t, meta.AgentStateError, agent.State,
		"agent linked to dead-shim session should be in error state")
	assert.Contains(t, agent.ErrorMessage, "session lost")
}

// TestRecoverSessions_AgentStateRunningOnLiveShim verifies that when a session's
// shim is alive and reporting running, the linked agent is reconciled to
// AgentStateRunning.
func TestRecoverSessions_AgentStateRunningOnLiveShim(t *testing.T) {
	pm, store := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start a live mock shim reporting running.
	srv, socketPath := newMockShimServer(t)
	srv.mu.Lock()
	srv.statusResult = RuntimeStatusResult{
		State:    spec.State{Status: spec.StatusRunning, ID: "live-agent-session"},
		Recovery: RuntimeStatusRecovery{LastSeq: 2},
	}
	srv.mu.Unlock()

	// Create workspace + agent (starts in "creating" to test reconciliation).
	ws := createTestWorkspace(t, ctx, store)
	agentID := createTestAgentForRecovery(t, ctx, store, meta.AgentStateCreating)

	// Create a "running" session linked to the agent.
	sessionID := uuid.New().String()
	require.NoError(t, store.CreateSession(ctx, &meta.Session{
		ID:             sessionID,
		RuntimeClass:   "default",
		WorkspaceID:    ws.ID,
		State:          meta.SessionStateRunning,
		ShimSocketPath: socketPath,
		ShimStateDir:   "/tmp/shim-state-" + sessionID,
		ShimPID:        99999,
		AgentID:        agentID,
	}))

	// Run recovery — shim alive, session recovered, agent → running.
	require.NoError(t, pm.RecoverSessions(ctx))

	// Session should be in the processes map.
	shimProc := pm.GetProcess(sessionID)
	require.NotNil(t, shimProc, "live session should be recovered")

	// Agent should be running.
	agent, err := store.GetAgent(ctx, agentID)
	require.NoError(t, err)
	require.NotNil(t, agent)
	assert.Equal(t, meta.AgentStateRunning, agent.State,
		"agent linked to live shim should be in running state")

	// Cleanup.
	srv.close()
	select {
	case <-shimProc.Done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for recovered process cleanup")
	}
}

// TestRecoverSessions_AgentStateCreatedOnIdleShim verifies that when a session's
// shim is alive but idle (created), a linked agent stuck in "creating" is
// reconciled to AgentStateCreated rather than remaining unusable.
func TestRecoverSessions_AgentStateCreatedOnIdleShim(t *testing.T) {
	pm, store := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start a live mock shim reporting created/idle.
	srv, socketPath := newMockShimServer(t)
	srv.mu.Lock()
	srv.statusResult = RuntimeStatusResult{
		State:    spec.State{Status: spec.StatusCreated, ID: "idle-agent-session"},
		Recovery: RuntimeStatusRecovery{LastSeq: 1},
	}
	srv.mu.Unlock()

	// Create workspace + agent (starts in "creating" to test reconciliation).
	ws := createTestWorkspace(t, ctx, store)
	agentID := createTestAgentForRecovery(t, ctx, store, meta.AgentStateCreating)

	// Create a session linked to the agent. The shim is healthy but idle.
	sessionID := uuid.New().String()
	require.NoError(t, store.CreateSession(ctx, &meta.Session{
		ID:             sessionID,
		RuntimeClass:   "default",
		WorkspaceID:    ws.ID,
		State:          meta.SessionStateCreated,
		ShimSocketPath: socketPath,
		ShimStateDir:   "/tmp/shim-state-" + sessionID,
		ShimPID:        99999,
		AgentID:        agentID,
	}))

	// Run recovery — shim alive, session recovered, agent -> created.
	require.NoError(t, pm.RecoverSessions(ctx))

	// Session should be in the processes map.
	shimProc := pm.GetProcess(sessionID)
	require.NotNil(t, shimProc, "idle session should be recovered")

	// Agent should be promoted out of creating into created.
	agent, err := store.GetAgent(ctx, agentID)
	require.NoError(t, err)
	require.NotNil(t, agent)
	assert.Equal(t, meta.AgentStateCreated, agent.State,
		"agent linked to idle shim should be reconciled to created")

	// Cleanup.
	srv.close()
	select {
	case <-shimProc.Done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for recovered process cleanup")
	}
}

// TestRecoverSessions_CreatingAgentMarkedError verifies that an agent stuck in
// AgentStateCreating (with no live session) is transitioned to AgentStateError
// by the creating-cleanup pass.
func TestRecoverSessions_CreatingAgentMarkedError(t *testing.T) {
	pm, store := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Create an agent in "creating" state with no session row at all.
	agentID := createTestAgentForRecovery(t, ctx, store, meta.AgentStateCreating)

	// Run recovery — no sessions, creating-cleanup fires.
	require.NoError(t, pm.RecoverSessions(ctx))

	// Agent should be in error state.
	agent, err := store.GetAgent(ctx, agentID)
	require.NoError(t, err)
	require.NotNil(t, agent)
	assert.Equal(t, meta.AgentStateError, agent.State,
		"agent stuck in creating should be marked error after daemon restart")
	assert.Contains(t, agent.ErrorMessage, "daemon restarted during creating phase")
}
