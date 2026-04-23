package agentd

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/pkg/agentd/store"
	runapi "github.com/zoumo/mass/pkg/agentrun/api"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	spec "github.com/zoumo/mass/pkg/runtime-spec"
	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
)

// setupRecoveryTest creates a ProcessManager backed by a real meta.Store with
// no running processes. Returns the manager and store.
func setupRecoveryTest(t *testing.T) (*ProcessManager, *store.Store) {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "meta.db")

	metaStore, err := store.NewStore(dbPath, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = metaStore.Close() })

	agents := NewAgentRunManager(metaStore, slog.Default())

	pm := NewProcessManager(agents, metaStore, filepath.Join(tmpDir, "mass.sock"), filepath.Join(tmpDir, "bundles"), slog.Default(), "info", "pretty")
	return pm, metaStore
}

// createRecoveryTestAgent creates an agent record in the given state with the given socket path.
// Returns the (workspace, name) pair.
func createRecoveryTestAgent(t *testing.T, ctx context.Context, metaStore *store.Store, workspace, name string, state apiruntime.Status, socketPath string) (string, string) {
	t.Helper()
	agent := &pkgariapi.AgentRun{
		Metadata: pkgariapi.ObjectMeta{
			Workspace: workspace,
			Name:      name,
		},
		Spec: pkgariapi.AgentRunSpec{
			Agent: "default",
		},
		Status: pkgariapi.AgentRunStatus{
			Status:     state,
			SocketPath: socketPath,
			StateDir:   "/tmp/run-state-" + name,
			PID:        99999,
		},
	}
	require.NoError(t, metaStore.CreateAgentRun(ctx, agent))
	return workspace, name
}

// TestRecoverSessions_LiveRun verifies that an agent with a live agent-run
// is recovered: the agent-run client is connected, status/subscribe are called,
// and the agent is registered in the processes map.
func TestRecoverSessions_LiveRun(t *testing.T) {
	pm, metaStore := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start a mock agent-run server.
	srv, socketPath := newMockRunServer(t)
	srv.mu.Lock()
	srv.statusResult = runapi.RuntimeStatusResult{
		State: apiruntime.State{
			MassVersion: "0.1.0",
			ID:          "recovered-agent",
			Status:      apiruntime.StatusRunning,
			Bundle:      "/tmp/test-bundle",
		},
		Recovery: runapi.RuntimeStatusRecovery{LastSeq: 5},
	}
	srv.mu.Unlock()

	// Create an agent in "running" state pointing at the mock socket.
	ws, name := createRecoveryTestAgent(t, ctx, metaStore, "default", "alpha", apiruntime.StatusRunning, socketPath)
	key := agentKey(ws, name)

	// Run recovery.
	err := pm.RecoverSessions(ctx)
	require.NoError(t, err)

	// Verify the agent is registered in the processes map.
	runProc := pm.GetProcess(key)
	require.NotNil(t, runProc, "recovered agent should be in processes map")
	assert.Equal(t, key, runProc.AgentKey)
	assert.NotNil(t, runProc.Client, "client should be connected")
	assert.Equal(t, socketPath, runProc.SocketPath)

	// Verify the mock agent-run received a subscribe call.
	srv.mu.Lock()
	subscribed := srv.subscribed
	srv.mu.Unlock()
	assert.True(t, subscribed, "agent-run should have been subscribed")

	// Cleanup: close the mock server and wait for the watcher to clean up.
	srv.close()
	select {
	case <-runProc.Done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for recovered process cleanup")
	}
}

// TestRecoverSessions_DeadRun verifies that when the agent-run socket is
// unreachable, the agent is marked stopped (fail-closed).
func TestRecoverSessions_DeadRun(t *testing.T) {
	pm, metaStore := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Create a "running" agent pointing at a nonexistent socket.
	deadSocket := "/tmp/nonexistent-run-" + "dead1" + ".sock"
	ws, name := createRecoveryTestAgent(t, ctx, metaStore, "default", "dead-agent", apiruntime.StatusRunning, deadSocket)

	// Run recovery.
	err := pm.RecoverSessions(ctx)
	require.NoError(t, err, "RecoverSessions should not return error for individual failures")

	// Verify agent was marked stopped.
	agent, err := metaStore.GetAgentRun(ctx, ws, name)
	require.NoError(t, err)
	assert.Equal(t, apiruntime.StatusStopped, agent.Status.Status,
		"dead agent-run agent should be marked stopped")

	// Verify the agent is NOT in the processes map.
	assert.Nil(t, pm.GetProcess(agentKey(ws, name)),
		"dead agent-run agent should not be in processes map")
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
	pm, metaStore := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create a stopped agent.
	createRecoveryTestAgent(t, ctx, metaStore, "default", "already-stopped", apiruntime.StatusStopped, "/tmp/whatever.sock")

	err := pm.RecoverSessions(ctx)
	require.NoError(t, err)

	// No agents should be recovered.
	assert.Empty(t, pm.ListProcesses())
}

// TestRecoverSessions_MixedLiveAndDead verifies correct handling when some
// agents have live agent-runs and others have dead ones.
func TestRecoverSessions_MixedLiveAndDead(t *testing.T) {
	pm, metaStore := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start a mock agent-run for the live agent.
	srv, liveSocketPath := newMockRunServer(t)
	srv.mu.Lock()
	srv.statusResult = runapi.RuntimeStatusResult{
		State:    apiruntime.State{Status: apiruntime.StatusRunning, ID: "live"},
		Recovery: runapi.RuntimeStatusRecovery{LastSeq: 2},
	}
	srv.mu.Unlock()

	// Create a live agent.
	liveWS, liveName := createRecoveryTestAgent(t, ctx, metaStore, "default", "live-agent", apiruntime.StatusRunning, liveSocketPath)
	liveKey := agentKey(liveWS, liveName)

	// Create a dead agent.
	deadWS, deadName := createRecoveryTestAgent(t, ctx, metaStore, "default", "dead-agent2", apiruntime.StatusRunning,
		"/tmp/dead-run-dead2.sock")
	deadKey := agentKey(deadWS, deadName)

	// Run recovery.
	err := pm.RecoverSessions(ctx)
	require.NoError(t, err)

	// Live agent should be recovered.
	assert.NotNil(t, pm.GetProcess(liveKey), "live agent should be recovered")

	// Dead agent should be marked stopped and not in processes map.
	assert.Nil(t, pm.GetProcess(deadKey), "dead agent should not be in processes map")
	deadAgent, err := metaStore.GetAgentRun(ctx, deadWS, deadName)
	require.NoError(t, err)
	assert.Equal(t, apiruntime.StatusStopped, deadAgent.Status.Status)

	// Clean up the live mock.
	srv.close()
	runProc := pm.GetProcess(liveKey)
	if runProc != nil {
		select {
		case <-runProc.Done:
		case <-time.After(3 * time.Second):
		}
	}
}

// TestRecoverSessions_NoSocketPath verifies that an agent with an empty
// socket path is marked stopped (it cannot be recovered).
func TestRecoverSessions_NoSocketPath(t *testing.T) {
	pm, metaStore := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create a running agent with no socket path.
	ws, name := createRecoveryTestAgent(t, ctx, metaStore, "default", "no-socket", apiruntime.StatusRunning, "")

	err := pm.RecoverSessions(ctx)
	require.NoError(t, err)

	// Agent should be marked stopped.
	agent, err := metaStore.GetAgentRun(ctx, ws, name)
	require.NoError(t, err)
	assert.Equal(t, apiruntime.StatusStopped, agent.Status.Status)
}

// TestRecoverSessions_RunReportsStopped verifies that when an agent-run reports
// stopped (but DB still says running), the agent is fail-closed: marked
// stopped in DB, not placed in the processes map.
func TestRecoverSessions_RunReportsStopped(t *testing.T) {
	pm, metaStore := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start a mock agent-run that reports stopped.
	srv, socketPath := newMockRunServer(t)
	srv.mu.Lock()
	srv.statusResult = runapi.RuntimeStatusResult{
		State: apiruntime.State{
			MassVersion: "0.1.0",
			ID:          "stopped-agent",
			Status:      apiruntime.StatusStopped,
		},
		Recovery: runapi.RuntimeStatusRecovery{LastSeq: 0},
	}
	srv.mu.Unlock()

	ws, name := createRecoveryTestAgent(t, ctx, metaStore, "default", "was-running", apiruntime.StatusRunning, socketPath)

	// Run recovery.
	err := pm.RecoverSessions(ctx)
	require.NoError(t, err)

	// Agent should be marked stopped (fail-closed).
	agent, err := metaStore.GetAgentRun(ctx, ws, name)
	require.NoError(t, err)
	assert.Equal(t, apiruntime.StatusStopped, agent.Status.Status,
		"run-reports-stopped agent should be marked stopped in DB")

	// Agent should NOT be in the processes map.
	assert.Nil(t, pm.GetProcess(agentKey(ws, name)))

	// Mock agent-run should NOT have been subscribed.
	srv.mu.Lock()
	subscribed := srv.subscribed
	srv.mu.Unlock()
	assert.False(t, subscribed,
		"agent-run should not have been subscribed when it reports stopped")
}

// TestRecoverSessions_ReconcileIdleToRunning verifies that when the DB says
// "idle" but the agent-run reports "running", the reconciliation logic transitions
// the DB to running.
func TestRecoverSessions_ReconcileIdleToRunning(t *testing.T) {
	pm, metaStore := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start a mock agent-run that reports running.
	srv, socketPath := newMockRunServer(t)
	srv.mu.Lock()
	srv.statusResult = runapi.RuntimeStatusResult{
		State: apiruntime.State{
			MassVersion: "0.1.0",
			ID:          "reconciled-agent",
			Status:      apiruntime.StatusRunning,
		},
		Recovery: runapi.RuntimeStatusRecovery{LastSeq: 3},
	}
	srv.mu.Unlock()

	// Create an "idle" agent pointing at the mock socket.
	ws, name := createRecoveryTestAgent(t, ctx, metaStore, "default", "was-idle", apiruntime.StatusIdle, socketPath)
	key := agentKey(ws, name)

	// Run recovery.
	err := pm.RecoverSessions(ctx)
	require.NoError(t, err)

	// Agent state in DB should now be "running" (reconciled from idle).
	agent, err := metaStore.GetAgentRun(ctx, ws, name)
	require.NoError(t, err)
	assert.Equal(t, apiruntime.StatusRunning, agent.Status.Status,
		"agent should be transitioned from idle to running")

	// Agent should be in the processes map.
	runProc := pm.GetProcess(key)
	require.NotNil(t, runProc, "reconciled agent should be in processes map")
	assert.Equal(t, key, runProc.AgentKey)
	assert.NotNil(t, runProc.Client)

	// Mock agent-run should have been subscribed.
	srv.mu.Lock()
	subscribed := srv.subscribed
	srv.mu.Unlock()
	assert.True(t, subscribed)

	// Cleanup.
	srv.close()
	select {
	case <-runProc.Done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for recovered process cleanup")
	}
}

// TestRecoverSessions_RunMismatchLogsWarning verifies that when the agent-run
// reports a different status than the DB (but not the idle→running case),
// recovery proceeds: the agent is placed in the processes map and subscribed,
// but the DB state is NOT changed.
func TestRecoverSessions_RunMismatchLogsWarning(t *testing.T) {
	pm, metaStore := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start a mock agent-run that reports running.
	srv, socketPath := newMockRunServer(t)
	srv.mu.Lock()
	srv.statusResult = runapi.RuntimeStatusResult{
		State: apiruntime.State{
			MassVersion: "0.1.0",
			ID:          "mismatched-agent",
			Status:      apiruntime.StatusRunning,
		},
		Recovery: runapi.RuntimeStatusRecovery{LastSeq: 1},
	}
	srv.mu.Unlock()

	// DB says "creating" but agent-run says "running" — mismatch, default branch.
	ws, name := createRecoveryTestAgent(t, ctx, metaStore, "default", "creating-agent", apiruntime.StatusCreating, socketPath)
	key := agentKey(ws, name)

	// Run recovery.
	err := pm.RecoverSessions(ctx)
	require.NoError(t, err)

	// Agent should be in the processes map.
	runProc := pm.GetProcess(key)
	require.NotNil(t, runProc, "mismatched agent should still be recovered")

	// DB state should remain "creating" — the default branch logs but
	// does not update the DB state.
	agent, err := metaStore.GetAgentRun(ctx, ws, name)
	require.NoError(t, err)
	assert.Equal(t, apiruntime.StatusCreating, agent.Status.Status,
		"DB state should remain creating (mismatch only logged, not reconciled)")

	// Mock agent-run should have been subscribed (recovery completed).
	srv.mu.Lock()
	subscribed := srv.subscribed
	srv.mu.Unlock()
	assert.True(t, subscribed)

	// Cleanup.
	srv.close()
	select {
	case <-runProc.Done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for recovered process cleanup")
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Creating-cleanup pass tests
// ────────────────────────────────────────────────────────────────────────────

// createAgentForRecovery creates an agent directly in the store for recovery tests.
func createAgentForRecovery(t *testing.T, ctx context.Context, metaStore *store.Store, workspace, name string, state apiruntime.Status) {
	t.Helper()
	agent := &pkgariapi.AgentRun{
		Metadata: pkgariapi.ObjectMeta{
			Workspace: workspace,
			Name:      name,
		},
		Spec: pkgariapi.AgentRunSpec{
			Agent: "default",
		},
		Status: pkgariapi.AgentRunStatus{
			Status: state,
		},
	}
	require.NoError(t, metaStore.CreateAgentRun(ctx, agent))
}

// TestRecoverSessions_CreatingAgentMarkedError verifies that an agent stuck in
// StatusCreating (with no live agent-run) is transitioned to StatusError
// by the creating-cleanup pass.
func TestRecoverSessions_CreatingAgentMarkedError(t *testing.T) {
	pm, metaStore := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Create an agent in "creating" state with no socket path.
	createAgentForRecovery(t, ctx, metaStore, "default", "stuck-creating", apiruntime.StatusCreating)

	// Run recovery — no running agent-runs, creating-cleanup fires.
	require.NoError(t, pm.RecoverSessions(ctx))

	// Agent should be in error state.
	agent, err := metaStore.GetAgentRun(ctx, "default", "stuck-creating")
	require.NoError(t, err)
	require.NotNil(t, agent)
	assert.Equal(t, apiruntime.StatusError, agent.Status.Status,
		"agent stuck in creating should be marked error after daemon restart")
	assert.Contains(t, agent.Status.ErrorMessage, "daemon restarted during creating phase")
}

// TestRecoverSessions_SkipsErrorAgents verifies that error-state agents are
// not included in the recovery pass.
func TestRecoverSessions_SkipsErrorAgents(t *testing.T) {
	pm, metaStore := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create an error agent.
	createAgentForRecovery(t, ctx, metaStore, "default", "error-agent", apiruntime.StatusError)

	err := pm.RecoverSessions(ctx)
	require.NoError(t, err)

	// No agents should be recovered.
	assert.Empty(t, pm.ListProcesses())

	// The error state should be unchanged.
	agent, err := metaStore.GetAgentRun(ctx, "default", "error-agent")
	require.NoError(t, err)
	assert.Equal(t, apiruntime.StatusError, agent.Status.Status)
}

// ────────────────────────────────────────────────────────────────────────────
// Best-effort session recovery tests (unconditional session/load)
// ────────────────────────────────────────────────────────────────────────────

// TestRecovery_AlwaysAttemptsSessionLoad verifies that session/load is called
// unconditionally with the SessionID from persisted state.json.
func TestRecovery_AlwaysAttemptsSessionLoad(t *testing.T) {
	pm, metaStore := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	srv, socketPath := newMockRunServer(t)

	// Write state.json with a known session ID.
	stateDir := t.TempDir()
	const knownSessionID = "session-abc123"
	require.NoError(t, spec.WriteState(stateDir, apiruntime.State{
		MassVersion: "0.1.0",
		ID:          "agent-name",
		SessionID:   knownSessionID,
		Status:      apiruntime.StatusIdle,
		Bundle:      "/tmp/test-bundle",
	}))

	agent := &pkgariapi.AgentRun{
		Metadata: pkgariapi.ObjectMeta{Workspace: "default", Name: "session-load-agent"},
		Spec:     pkgariapi.AgentRunSpec{Agent: "default"},
		Status: pkgariapi.AgentRunStatus{
			Status:     apiruntime.StatusIdle,
			SocketPath: socketPath,
			StateDir:   stateDir,
			PID:        99999,
		},
	}
	require.NoError(t, metaStore.CreateAgentRun(ctx, agent))
	key := agentKey("default", "session-load-agent")

	require.NoError(t, pm.RecoverSessions(ctx))

	// Verify session/load was called with the correct SessionID.
	srv.mu.Lock()
	loadCalled := srv.loadCalled
	loadCalledWith := srv.loadCalledWith
	srv.mu.Unlock()

	assert.True(t, loadCalled, "session/load should be called unconditionally")
	assert.Equal(t, knownSessionID, loadCalledWith, "session/load should carry persisted SessionID")

	// Agent must be in the processes map.
	assert.NotNil(t, pm.GetProcess(key), "agent should be recovered")

	// Cleanup.
	runProc := pm.GetProcess(key)
	srv.close()
	if runProc != nil {
		select {
		case <-runProc.Done:
		case <-time.After(3 * time.Second):
			t.Fatal("timeout waiting for process cleanup")
		}
	}
}

// TestRecovery_SessionLoadFailure_Continues verifies that when the agent-run
// returns an error for session/load, recovery still succeeds (graceful fallback).
func TestRecovery_SessionLoadFailure_Continues(t *testing.T) {
	pm, metaStore := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	srv, socketPath := newMockRunServer(t)

	// Inject error for session/load.
	srv.mu.Lock()
	srv.loadSessionErr = fmt.Errorf("runtime does not support session/load")
	srv.mu.Unlock()

	stateDir := t.TempDir()
	require.NoError(t, spec.WriteState(stateDir, apiruntime.State{
		MassVersion: "0.1.0",
		ID:          "agent-name",
		SessionID:   "some-session",
		Status:      apiruntime.StatusIdle,
		Bundle:      "/tmp/test-bundle",
	}))

	agent := &pkgariapi.AgentRun{
		Metadata: pkgariapi.ObjectMeta{Workspace: "default", Name: "load-fail-agent"},
		Spec:     pkgariapi.AgentRunSpec{Agent: "default"},
		Status: pkgariapi.AgentRunStatus{
			Status:     apiruntime.StatusIdle,
			SocketPath: socketPath,
			StateDir:   stateDir,
			PID:        99999,
		},
	}
	require.NoError(t, metaStore.CreateAgentRun(ctx, agent))
	key := agentKey("default", "load-fail-agent")

	// RecoverSessions must succeed even though session/load returned an error.
	require.NoError(t, pm.RecoverSessions(ctx))

	// Agent must still be in the processes map.
	runProc := pm.GetProcess(key)
	assert.NotNil(t, runProc, "agent should be recovered even if session/load fails")

	// Cleanup.
	srv.close()
	if runProc != nil {
		select {
		case <-runProc.Done:
		case <-time.After(3 * time.Second):
			t.Fatal("timeout waiting for process cleanup")
		}
	}
}

// TestRecovery_NoSessionID_SkipsLoad verifies that when state.json has no
// SessionID, session/load is not called.
func TestRecovery_NoSessionID_SkipsLoad(t *testing.T) {
	pm, metaStore := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	srv, socketPath := newMockRunServer(t)

	// Write state.json without SessionID.
	stateDir := t.TempDir()
	require.NoError(t, spec.WriteState(stateDir, apiruntime.State{
		MassVersion: "0.1.0",
		ID:          "agent-name",
		Status:      apiruntime.StatusIdle,
		Bundle:      "/tmp/test-bundle",
	}))

	agent := &pkgariapi.AgentRun{
		Metadata: pkgariapi.ObjectMeta{Workspace: "default", Name: "no-session-agent"},
		Spec:     pkgariapi.AgentRunSpec{Agent: "default"},
		Status: pkgariapi.AgentRunStatus{
			Status:     apiruntime.StatusIdle,
			SocketPath: socketPath,
			StateDir:   stateDir,
			PID:        99999,
		},
	}
	require.NoError(t, metaStore.CreateAgentRun(ctx, agent))
	key := agentKey("default", "no-session-agent")

	require.NoError(t, pm.RecoverSessions(ctx))

	// session/load must NOT have been called.
	srv.mu.Lock()
	loadCalled := srv.loadCalled
	srv.mu.Unlock()
	assert.False(t, loadCalled, "session/load should not be called when SessionID is empty")

	// Agent should still be recovered.
	runProc := pm.GetProcess(key)
	assert.NotNil(t, runProc, "agent should be recovered even without session ID")

	// Cleanup.
	srv.close()
	if runProc != nil {
		select {
		case <-runProc.Done:
		case <-time.After(3 * time.Second):
			t.Fatal("timeout waiting for process cleanup")
		}
	}
}
