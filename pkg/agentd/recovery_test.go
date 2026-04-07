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

	cfg := Config{
		Socket:        filepath.Join(tmpDir, "agentd.sock"),
		WorkspaceRoot: filepath.Join(tmpDir, "workspaces"),
	}

	registry, err := NewRuntimeClassRegistry(nil)
	require.NoError(t, err)

	pm := NewProcessManager(registry, sessions, store, cfg)
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
