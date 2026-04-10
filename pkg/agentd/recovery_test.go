package agentd

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/open-agent-d/open-agent-d/pkg/meta"
	"github.com/open-agent-d/open-agent-d/pkg/spec"
)

// setupRecoveryTest creates a ProcessManager backed by a real meta.Store with
// no running processes. Returns the manager and store.
func setupRecoveryTest(t *testing.T) (*ProcessManager, *meta.Store) {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "meta.db")

	store, err := meta.NewStore(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	agents := NewAgentManager(store)

	pm := NewProcessManager(agents, store, filepath.Join(tmpDir, "agentd.sock"), filepath.Join(tmpDir, "bundles"))
	return pm, store
}

// createRecoveryTestAgent creates an agent record in the given state with the given socket path.
// Returns the (workspace, name) pair.
func createRecoveryTestAgent(t *testing.T, ctx context.Context, store *meta.Store, workspace, name string, state spec.Status, socketPath string) (string, string) {
	t.Helper()
	agent := &meta.Agent{
		Metadata: meta.ObjectMeta{
			Workspace: workspace,
			Name:      name,
		},
		Spec: meta.AgentSpec{
			RuntimeClass: "default",
		},
		Status: meta.AgentStatus{
			State:          state,
			ShimSocketPath: socketPath,
			ShimStateDir:   "/tmp/shim-state-" + name,
			ShimPID:        99999,
		},
	}
	require.NoError(t, store.CreateAgent(ctx, agent))
	return workspace, name
}

// TestRecoverSessions_LiveShim verifies that an agent with a live shim
// is recovered: the shim client is connected, status/subscribe are called,
// and the agent is registered in the processes map.
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
			ID:         "recovered-agent",
			Status:     spec.StatusRunning,
			Bundle:     "/tmp/test-bundle",
		},
		Recovery: RuntimeStatusRecovery{LastSeq: 5},
	}
	srv.mu.Unlock()

	// Create an agent in "running" state pointing at the mock socket.
	ws, name := createRecoveryTestAgent(t, ctx, store, "default", "alpha", spec.StatusRunning, socketPath)
	key := agentKey(ws, name)

	// Run recovery.
	err := pm.RecoverSessions(ctx)
	require.NoError(t, err)

	// Verify the agent is registered in the processes map.
	shimProc := pm.GetProcess(key)
	require.NotNil(t, shimProc, "recovered agent should be in processes map")
	assert.Equal(t, key, shimProc.AgentKey)
	assert.NotNil(t, shimProc.Client, "client should be connected")
	assert.Equal(t, socketPath, shimProc.SocketPath)

	// Verify the mock shim received a subscribe call.
	srv.mu.Lock()
	subscribed := srv.subscribed
	srv.mu.Unlock()
	assert.True(t, subscribed, "shim should have been subscribed")

	// Cleanup: close the mock server and wait for the watcher to clean up.
	srv.close()
	select {
	case <-shimProc.Done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for recovered process cleanup")
	}
}

// TestRecoverSessions_DeadShim verifies that when the shim socket is
// unreachable, the agent is marked stopped (fail-closed).
func TestRecoverSessions_DeadShim(t *testing.T) {
	pm, store := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Create a "running" agent pointing at a nonexistent socket.
	deadSocket := "/tmp/nonexistent-shim-" + "dead1" + ".sock"
	ws, name := createRecoveryTestAgent(t, ctx, store, "default", "dead-agent", spec.StatusRunning, deadSocket)

	// Run recovery.
	err := pm.RecoverSessions(ctx)
	require.NoError(t, err, "RecoverSessions should not return error for individual failures")

	// Verify agent was marked stopped.
	agent, err := store.GetAgent(ctx, ws, name)
	require.NoError(t, err)
	assert.Equal(t, spec.StatusStopped, agent.Status.State,
		"dead shim agent should be marked stopped")

	// Verify the agent is NOT in the processes map.
	assert.Nil(t, pm.GetProcess(agentKey(ws, name)),
		"dead shim agent should not be in processes map")
}

// TestRecoverSessions_NoAgents verifies that RecoverSessions is a no-op
// when there are no agents in the database.
func TestRecoverSessions_NoAgents(t *testing.T) {
	pm, _ := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := pm.RecoverSessions(ctx)
	require.NoError(t, err)

	// No agents should be registered.
	assert.Empty(t, pm.ListProcesses())
}

// TestRecoverSessions_SkipsStoppedAgents verifies that already-stopped
// agents are not included in the recovery pass.
func TestRecoverSessions_SkipsStoppedAgents(t *testing.T) {
	pm, store := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create a stopped agent.
	createRecoveryTestAgent(t, ctx, store, "default", "already-stopped", spec.StatusStopped, "/tmp/whatever.sock")

	err := pm.RecoverSessions(ctx)
	require.NoError(t, err)

	// No agents should be recovered.
	assert.Empty(t, pm.ListProcesses())
}

// TestRecoverSessions_MixedLiveAndDead verifies correct handling when some
// agents have live shims and others have dead ones.
func TestRecoverSessions_MixedLiveAndDead(t *testing.T) {
	pm, store := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start a mock shim for the live agent.
	srv, liveSocketPath := newMockShimServer(t)
	srv.mu.Lock()
	srv.statusResult = RuntimeStatusResult{
		State:    spec.State{Status: spec.StatusRunning, ID: "live"},
		Recovery: RuntimeStatusRecovery{LastSeq: 2},
	}
	srv.mu.Unlock()

	// Create a live agent.
	liveWS, liveName := createRecoveryTestAgent(t, ctx, store, "default", "live-agent", spec.StatusRunning, liveSocketPath)
	liveKey := agentKey(liveWS, liveName)

	// Create a dead agent.
	deadWS, deadName := createRecoveryTestAgent(t, ctx, store, "default", "dead-agent2", spec.StatusRunning,
		"/tmp/dead-shim-dead2.sock")
	deadKey := agentKey(deadWS, deadName)

	// Run recovery.
	err := pm.RecoverSessions(ctx)
	require.NoError(t, err)

	// Live agent should be recovered.
	assert.NotNil(t, pm.GetProcess(liveKey), "live agent should be recovered")

	// Dead agent should be marked stopped and not in processes map.
	assert.Nil(t, pm.GetProcess(deadKey), "dead agent should not be in processes map")
	deadAgent, err := store.GetAgent(ctx, deadWS, deadName)
	require.NoError(t, err)
	assert.Equal(t, spec.StatusStopped, deadAgent.Status.State)

	// Clean up the live mock.
	srv.close()
	shimProc := pm.GetProcess(liveKey)
	if shimProc != nil {
		select {
		case <-shimProc.Done:
		case <-time.After(3 * time.Second):
		}
	}
}

// TestRecoverSessions_NoSocketPath verifies that an agent with an empty
// socket path is marked stopped (it cannot be recovered).
func TestRecoverSessions_NoSocketPath(t *testing.T) {
	pm, store := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create a running agent with no socket path.
	ws, name := createRecoveryTestAgent(t, ctx, store, "default", "no-socket", spec.StatusRunning, "")

	err := pm.RecoverSessions(ctx)
	require.NoError(t, err)

	// Agent should be marked stopped.
	agent, err := store.GetAgent(ctx, ws, name)
	require.NoError(t, err)
	assert.Equal(t, spec.StatusStopped, agent.Status.State)
}

// TestRecoverSessions_ShimReportsStopped verifies that when a shim reports
// stopped (but DB still says running), the agent is fail-closed: marked
// stopped in DB, not placed in the processes map.
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
			ID:         "stopped-agent",
			Status:     spec.StatusStopped,
		},
		Recovery: RuntimeStatusRecovery{LastSeq: 0},
	}
	srv.mu.Unlock()

	ws, name := createRecoveryTestAgent(t, ctx, store, "default", "was-running", spec.StatusRunning, socketPath)

	// Run recovery.
	err := pm.RecoverSessions(ctx)
	require.NoError(t, err)

	// Agent should be marked stopped (fail-closed).
	agent, err := store.GetAgent(ctx, ws, name)
	require.NoError(t, err)
	assert.Equal(t, spec.StatusStopped, agent.Status.State,
		"shim-reports-stopped agent should be marked stopped in DB")

	// Agent should NOT be in the processes map.
	assert.Nil(t, pm.GetProcess(agentKey(ws, name)))

	// Mock shim should NOT have been subscribed.
	srv.mu.Lock()
	subscribed := srv.subscribed
	srv.mu.Unlock()
	assert.False(t, subscribed,
		"shim should not have been subscribed when it reports stopped")
}

// TestRecoverSessions_ReconcileIdleToRunning verifies that when the DB says
// "idle" but the shim reports "running", the reconciliation logic transitions
// the DB to running.
func TestRecoverSessions_ReconcileIdleToRunning(t *testing.T) {
	pm, store := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start a mock shim that reports running.
	srv, socketPath := newMockShimServer(t)
	srv.mu.Lock()
	srv.statusResult = RuntimeStatusResult{
		State: spec.State{
			OarVersion: "0.1.0",
			ID:         "reconciled-agent",
			Status:     spec.StatusRunning,
		},
		Recovery: RuntimeStatusRecovery{LastSeq: 3},
	}
	srv.mu.Unlock()

	// Create an "idle" agent pointing at the mock socket.
	ws, name := createRecoveryTestAgent(t, ctx, store, "default", "was-idle", spec.StatusIdle, socketPath)
	key := agentKey(ws, name)

	// Run recovery.
	err := pm.RecoverSessions(ctx)
	require.NoError(t, err)

	// Agent state in DB should now be "running" (reconciled from idle).
	agent, err := store.GetAgent(ctx, ws, name)
	require.NoError(t, err)
	assert.Equal(t, spec.StatusRunning, agent.Status.State,
		"agent should be transitioned from idle to running")

	// Agent should be in the processes map.
	shimProc := pm.GetProcess(key)
	require.NotNil(t, shimProc, "reconciled agent should be in processes map")
	assert.Equal(t, key, shimProc.AgentKey)
	assert.NotNil(t, shimProc.Client)

	// Mock shim should have been subscribed.
	srv.mu.Lock()
	subscribed := srv.subscribed
	srv.mu.Unlock()
	assert.True(t, subscribed)

	// Cleanup.
	srv.close()
	select {
	case <-shimProc.Done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for recovered process cleanup")
	}
}

// TestRecoverSessions_ShimMismatchLogsWarning verifies that when the shim
// reports a different status than the DB (but not the idle→running case),
// recovery proceeds: the agent is placed in the processes map and subscribed,
// but the DB state is NOT changed.
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
			ID:         "mismatched-agent",
			Status:     spec.StatusRunning,
		},
		Recovery: RuntimeStatusRecovery{LastSeq: 1},
	}
	srv.mu.Unlock()

	// DB says "creating" but shim says "running" — mismatch, default branch.
	ws, name := createRecoveryTestAgent(t, ctx, store, "default", "creating-agent", spec.StatusCreating, socketPath)
	key := agentKey(ws, name)

	// Run recovery.
	err := pm.RecoverSessions(ctx)
	require.NoError(t, err)

	// Agent should be in the processes map.
	shimProc := pm.GetProcess(key)
	require.NotNil(t, shimProc, "mismatched agent should still be recovered")

	// DB state should remain "creating" — the default branch logs but
	// does not update the DB state.
	agent, err := store.GetAgent(ctx, ws, name)
	require.NoError(t, err)
	assert.Equal(t, spec.StatusCreating, agent.Status.State,
		"DB state should remain creating (mismatch only logged, not reconciled)")

	// Mock shim should have been subscribed (recovery completed).
	srv.mu.Lock()
	subscribed := srv.subscribed
	srv.mu.Unlock()
	assert.True(t, subscribed)

	// Cleanup.
	srv.close()
	select {
	case <-shimProc.Done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for recovered process cleanup")
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Creating-cleanup pass tests
// ────────────────────────────────────────────────────────────────────────────

// createAgentForRecovery creates an agent directly in the store for recovery tests.
func createAgentForRecovery(t *testing.T, ctx context.Context, store *meta.Store, workspace, name string, state spec.Status) {
	t.Helper()
	agent := &meta.Agent{
		Metadata: meta.ObjectMeta{
			Workspace: workspace,
			Name:      name,
		},
		Spec: meta.AgentSpec{
			RuntimeClass: "default",
		},
		Status: meta.AgentStatus{
			State: state,
		},
	}
	require.NoError(t, store.CreateAgent(ctx, agent))
}

// TestRecoverSessions_CreatingAgentMarkedError verifies that an agent stuck in
// StatusCreating (with no live shim) is transitioned to StatusError
// by the creating-cleanup pass.
func TestRecoverSessions_CreatingAgentMarkedError(t *testing.T) {
	pm, store := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Create an agent in "creating" state with no socket path.
	createAgentForRecovery(t, ctx, store, "default", "stuck-creating", spec.StatusCreating)

	// Run recovery — no running shims, creating-cleanup fires.
	require.NoError(t, pm.RecoverSessions(ctx))

	// Agent should be in error state.
	agent, err := store.GetAgent(ctx, "default", "stuck-creating")
	require.NoError(t, err)
	require.NotNil(t, agent)
	assert.Equal(t, spec.StatusError, agent.Status.State,
		"agent stuck in creating should be marked error after daemon restart")
	assert.Contains(t, agent.Status.ErrorMessage, "daemon restarted during creating phase")
}

// TestRecoverSessions_SkipsErrorAgents verifies that error-state agents are
// not included in the recovery pass.
func TestRecoverSessions_SkipsErrorAgents(t *testing.T) {
	pm, store := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create an error agent.
	createAgentForRecovery(t, ctx, store, "default", "error-agent", spec.StatusError)

	err := pm.RecoverSessions(ctx)
	require.NoError(t, err)

	// No agents should be recovered.
	assert.Empty(t, pm.ListProcesses())

	// The error state should be unchanged.
	agent, err := store.GetAgent(ctx, "default", "error-agent")
	require.NoError(t, err)
	assert.Equal(t, spec.StatusError, agent.Status.State)
}

// ────────────────────────────────────────────────────────────────────────────
// RestartPolicy: tryReload / alwaysNew tests
// ────────────────────────────────────────────────────────────────────────────

// TestRecovery_TryReload_AttemptsSessionLoad verifies that an agent with
// RestartPolicy=tryReload calls session/load on the shim with the sessionId
// read from the persisted state.json.
func TestRecovery_TryReload_AttemptsSessionLoad(t *testing.T) {
	pm, store := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	srv, socketPath := newMockShimServer(t)

	// Write state.json with a known session ID.
	stateDir := t.TempDir()
	const knownSessionID = "reload-session-abc123"
	require.NoError(t, spec.WriteState(stateDir, spec.State{
		OarVersion: "0.1.0",
		ID:         knownSessionID,
		Status:     spec.StatusIdle,
		Bundle:     "/tmp/test-bundle",
	}))

	agent := &meta.Agent{
		Metadata: meta.ObjectMeta{Workspace: "default", Name: "tryreload-agent"},
		Spec: meta.AgentSpec{
			RuntimeClass:  "default",
			RestartPolicy: meta.RestartPolicyTryReload,
		},
		Status: meta.AgentStatus{
			State:          spec.StatusIdle,
			ShimSocketPath: socketPath,
			ShimStateDir:   stateDir,
			ShimPID:        99999,
		},
	}
	require.NoError(t, store.CreateAgent(ctx, agent))
	key := agentKey("default", "tryreload-agent")

	require.NoError(t, pm.RecoverSessions(ctx))

	// Verify session/load was called on the shim with the correct sessionId.
	srv.mu.Lock()
	loadCalled := srv.loadCalled
	loadCalledWith := srv.loadCalledWith
	srv.mu.Unlock()

	assert.True(t, loadCalled, "session/load should have been called for tryReload policy")
	assert.Equal(t, knownSessionID, loadCalledWith, "session/load should carry the persisted sessionId")

	// Agent must be in the processes map.
	assert.NotNil(t, pm.GetProcess(key), "agent should be recovered")

	// Cleanup.
	shimProc := pm.GetProcess(key)
	srv.close()
	if shimProc != nil {
		select {
		case <-shimProc.Done:
		case <-time.After(3 * time.Second):
			t.Fatal("timeout waiting for process cleanup")
		}
	}
}

// TestRecovery_TryReload_FallsBackOnLoadFailure verifies that when the shim
// returns an error for session/load, recoverAgent still succeeds and the agent
// is placed in the processes map (graceful fallback).
func TestRecovery_TryReload_FallsBackOnLoadFailure(t *testing.T) {
	pm, store := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	srv, socketPath := newMockShimServer(t)

	// Inject error for session/load.
	srv.mu.Lock()
	srv.loadSessionErr = fmt.Errorf("runtime does not support session/load")
	srv.mu.Unlock()

	stateDir := t.TempDir()
	require.NoError(t, spec.WriteState(stateDir, spec.State{
		OarVersion: "0.1.0",
		ID:         "some-session",
		Status:     spec.StatusIdle,
		Bundle:     "/tmp/test-bundle",
	}))

	agent := &meta.Agent{
		Metadata: meta.ObjectMeta{Workspace: "default", Name: "tryreload-fallback"},
		Spec: meta.AgentSpec{
			RuntimeClass:  "default",
			RestartPolicy: meta.RestartPolicyTryReload,
		},
		Status: meta.AgentStatus{
			State:          spec.StatusIdle,
			ShimSocketPath: socketPath,
			ShimStateDir:   stateDir,
			ShimPID:        99999,
		},
	}
	require.NoError(t, store.CreateAgent(ctx, agent))
	key := agentKey("default", "tryreload-fallback")

	// RecoverSessions must succeed even though session/load returned an error.
	require.NoError(t, pm.RecoverSessions(ctx))

	// Agent must still be in the processes map.
	shimProc := pm.GetProcess(key)
	assert.NotNil(t, shimProc, "agent should be recovered even if session/load fails")

	// Cleanup.
	srv.close()
	if shimProc != nil {
		select {
		case <-shimProc.Done:
		case <-time.After(3 * time.Second):
			t.Fatal("timeout waiting for process cleanup")
		}
	}
}

// TestRecovery_TryReload_FallsBackOnMissingStateFile verifies that when
// ShimStateDir points to a nonexistent path, recoverAgent proceeds without
// panicking and the agent is placed in the processes map.
func TestRecovery_TryReload_FallsBackOnMissingStateFile(t *testing.T) {
	pm, store := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	srv, socketPath := newMockShimServer(t)

	agent := &meta.Agent{
		Metadata: meta.ObjectMeta{Workspace: "default", Name: "tryreload-nostate"},
		Spec: meta.AgentSpec{
			RuntimeClass:  "default",
			RestartPolicy: meta.RestartPolicyTryReload,
		},
		Status: meta.AgentStatus{
			State:          spec.StatusIdle,
			ShimSocketPath: socketPath,
			ShimStateDir:   "/tmp/nonexistent-state-dir-tryreload-test",
			ShimPID:        99999,
		},
	}
	require.NoError(t, store.CreateAgent(ctx, agent))
	key := agentKey("default", "tryreload-nostate")

	// Must not panic; must succeed.
	require.NoError(t, pm.RecoverSessions(ctx))

	// Agent should be in processes map.
	shimProc := pm.GetProcess(key)
	assert.NotNil(t, shimProc, "agent should be recovered even if state file is missing")

	// Cleanup.
	srv.close()
	if shimProc != nil {
		select {
		case <-shimProc.Done:
		case <-time.After(3 * time.Second):
			t.Fatal("timeout waiting for process cleanup")
		}
	}
}

// TestRecovery_AlwaysNew_SkipsSessionLoad verifies that an agent with
// RestartPolicy="" (default/alwaysNew) does NOT call session/load on the shim.
func TestRecovery_AlwaysNew_SkipsSessionLoad(t *testing.T) {
	pm, store := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	srv, socketPath := newMockShimServer(t)

	// Write a state.json so there's something to load — should be ignored.
	stateDir := t.TempDir()
	require.NoError(t, spec.WriteState(stateDir, spec.State{
		OarVersion: "0.1.0",
		ID:         "existing-session",
		Status:     spec.StatusIdle,
		Bundle:     "/tmp/test-bundle",
	}))

	agent := &meta.Agent{
		Metadata: meta.ObjectMeta{Workspace: "default", Name: "alwaysnew-agent"},
		Spec: meta.AgentSpec{
			RuntimeClass:  "default",
			RestartPolicy: "", // empty = alwaysNew (default)
		},
		Status: meta.AgentStatus{
			State:          spec.StatusIdle,
			ShimSocketPath: socketPath,
			ShimStateDir:   stateDir,
			ShimPID:        99999,
		},
	}
	require.NoError(t, store.CreateAgent(ctx, agent))
	key := agentKey("default", "alwaysnew-agent")

	require.NoError(t, pm.RecoverSessions(ctx))

	// session/load must NOT have been called.
	srv.mu.Lock()
	loadCalled := srv.loadCalled
	srv.mu.Unlock()
	assert.False(t, loadCalled, "session/load should not be called for alwaysNew/empty policy")

	// Agent should still be recovered.
	shimProc := pm.GetProcess(key)
	assert.NotNil(t, shimProc, "agent should be recovered with alwaysNew policy")

	// Cleanup.
	srv.close()
	if shimProc != nil {
		select {
		case <-shimProc.Done:
		case <-time.After(3 * time.Second):
			t.Fatal("timeout waiting for process cleanup")
		}
	}
}
