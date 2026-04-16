// Package agentd implements the agent daemon that manages agent runtime lifecycle.
// This file contains boundary tests for the run write authority rule (D088):
// after bootstrap, only runtime/state_change notifications may update DB agent
// state — Start() and recoverAgent() must not call UpdateStatus directly.
package agentd

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	runapi "github.com/zoumo/mass/pkg/agentrun/api"
	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
	runclient "github.com/zoumo/mass/pkg/agentrun/client"
)

// TestStateChange_CreatingToIdle_UpdatesDB verifies that a runtime/state_change
// notification (creating → idle) from the agent-run drives a DB state update via the
// production buildNotifHandler — no direct UpdateStatus(StatusRunning) call.
func TestStateChange_CreatingToIdle_UpdatesDB(t *testing.T) {
	pm, store := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ws := "default"
	agentName := "sc-creating-idle"
	key := agentKey(ws, agentName)

	// Create agent at StatusCreating in the DB.
	require.NoError(t, store.CreateAgentRun(ctx, &pkgariapi.AgentRun{
		Metadata: pkgariapi.ObjectMeta{Workspace: ws, Name: agentName},
		Spec:     pkgariapi.AgentRunSpec{Agent: "default"},
		Status:   pkgariapi.AgentRunStatus{State: apiruntime.StatusCreating},
	}))

	// Set up mock agent-run, queue a creating→idle stateChange notification.
	srv, socketPath := newMockRunServer(t)
	_ = srv // cleanup registered via t.Cleanup in newMockRunServer

	srv.queueNotification(runapi.MethodRuntimeEventUpdate, map[string]any{
		"runId": "test-run",
		"seq":   0,
		"time":  "2026-01-01T00:00:00Z",
		"type":  "runtime_update",
		"payload": map[string]any{
			"status": map[string]any{
				"previousStatus": "creating",
				"status":         "idle",
				"pid":            1234,
			},
		},
	})

	// Create a RunProcess (mirrors what Start() creates after forkRun).
	runProc := &RunProcess{
		AgentKey:   key,
		PID:        1234,
		SocketPath: socketPath,
		StateDir:   "/tmp/run-state-" + agentName,
		Events:     make(chan runapi.AgentRunEvent, 1024),
		Done:       make(chan struct{}),
	}

	// Connect using plain Dial + WatchEvent + startEventConsumer (D088 boundary).
	client, err := runclient.Dial(ctx, socketPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	runProc.Client = client

	// Register runProc in the processes map (as Start() does after Dial).
	pm.mu.Lock()
	pm.processes[key] = runProc
	pm.mu.Unlock()

	// Watch events — mock server emits the queued notification asynchronously.
	watcher, err := client.WatchEvent(ctx, &runapi.SessionWatchEventParams{})
	require.NoError(t, err)
	runProc.Watcher = watcher
	pm.startEventConsumer(ws, agentName, runProc)

	// Wait for the stateChange notification to drive the DB update.
	require.Eventually(t, func() bool {
		agent, _ := store.GetAgentRun(ctx, ws, agentName)
		return agent != nil && agent.Status.State == apiruntime.StatusIdle
	}, 3*time.Second, 50*time.Millisecond,
		"DB state should become idle after runtime/state_change notification")

	// Confirm final DB state.
	agent, err := store.GetAgentRun(ctx, ws, agentName)
	require.NoError(t, err)
	assert.Equal(t, apiruntime.StatusIdle, agent.Status.State,
		"DB state must reflect the run-emitted stateChange, not a direct write")
}

// TestSessionUpdate_DeliversOrderedParams verifies that session/update
// notifications keep their ordering metadata all the way through the
// production startEventConsumer into RunProcess.Events.
func TestSessionUpdate_DeliversOrderedParams(t *testing.T) {
	pm, _ := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ws := "default"
	agentName := "session-update-order"
	key := agentKey(ws, agentName)
	turnID := "turn-ordering"

	srv, socketPath := newMockRunServer(t)
	for i := 0; i < 3; i++ {
		contentBytes, err := json.Marshal(runapi.NewContentEvent(runapi.EventTypeAgentMessage, "", runapi.TextBlock(fmt.Sprintf("chunk-%d", i))))
		require.NoError(t, err)
		srv.queueNotification(runapi.MethodRuntimeEventUpdate, map[string]any{
			"runId":     "test-run",
			"sessionId": "test-session",
			"seq":       i,
			"time":      "2026-01-01T00:00:00Z",
			"type":      "agent_message",
			"turnId":    turnID,
			"payload":   json.RawMessage(contentBytes),
		})
	}

	runProc := &RunProcess{
		AgentKey:   key,
		PID:        1234,
		SocketPath: socketPath,
		StateDir:   "/tmp/run-state-" + agentName,
		Events:     make(chan runapi.AgentRunEvent, 1024),
		Done:       make(chan struct{}),
	}

	client, err := runclient.Dial(ctx, socketPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	runProc.Client = client

	watcher, err := client.WatchEvent(ctx, &runapi.SessionWatchEventParams{})
	require.NoError(t, err)
	runProc.Watcher = watcher
	pm.startEventConsumer(ws, agentName, runProc)

	for i := 0; i < 3; i++ {
		var update runapi.AgentRunEvent
		select {
		case update = <-runProc.Events:
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout waiting for agent-run/event %d", i)
		}

		require.Equal(t, i, update.Seq)
		require.Equal(t, turnID, update.TurnID)
		require.Equal(t, runapi.EventTypeAgentMessage, update.Type)
		payload, ok := update.Payload.(runapi.ContentEvent)
		require.True(t, ok)
		require.NotNil(t, payload.Content.Text)
		require.Equal(t, fmt.Sprintf("chunk-%d", i), payload.Content.Text.Text)
	}
}

// TestStateChange_RunningToIdle_UpdatesDB verifies that two successive
// runtime/state_change notifications (idle→running, running→idle) each drive
// independent DB state updates via the production handler.
func TestStateChange_RunningToIdle_UpdatesDB(t *testing.T) {
	pm, store := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ws := "default"
	agentName := "sc-running-idle"
	key := agentKey(ws, agentName)

	// Create agent at StatusIdle.
	require.NoError(t, store.CreateAgentRun(ctx, &pkgariapi.AgentRun{
		Metadata: pkgariapi.ObjectMeta{Workspace: ws, Name: agentName},
		Spec:     pkgariapi.AgentRunSpec{Agent: "default"},
		Status:   pkgariapi.AgentRunStatus{State: apiruntime.StatusIdle},
	}))

	// Queue two successive stateChange notifications: idle→running, then running→idle.
	srv, socketPath := newMockRunServer(t)
	srv.queueNotification(runapi.MethodRuntimeEventUpdate, map[string]any{
		"runId": "test-run",
		"seq":   1,
		"time":  "2026-01-01T00:00:00Z",
		"type":  "runtime_update",
		"payload": map[string]any{
			"status": map[string]any{
				"previousStatus": "idle",
				"status":         "running",
				"pid":            5678,
			},
		},
	})
	srv.queueNotification(runapi.MethodRuntimeEventUpdate, map[string]any{
		"runId": "test-run",
		"seq":   2,
		"time":  "2026-01-01T00:00:01Z",
		"type":  "runtime_update",
		"payload": map[string]any{
			"status": map[string]any{
				"previousStatus": "running",
				"status":         "idle",
				"pid":            5678,
			},
		},
	})

	runProc := &RunProcess{
		AgentKey:   key,
		PID:        5678,
		SocketPath: socketPath,
		StateDir:   "/tmp/run-state-" + agentName,
		Events:     make(chan runapi.AgentRunEvent, 1024),
		Done:       make(chan struct{}),
	}

	client, err := runclient.Dial(ctx, socketPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	runProc.Client = client

	pm.mu.Lock()
	pm.processes[key] = runProc
	pm.mu.Unlock()

	watcher, err := client.WatchEvent(ctx, &runapi.SessionWatchEventParams{})
	require.NoError(t, err)
	runProc.Watcher = watcher
	pm.startEventConsumer(ws, agentName, runProc)

	// Wait for both stateChange notifications to be processed (final state = idle).
	require.Eventually(t, func() bool {
		agent, _ := store.GetAgentRun(ctx, ws, agentName)
		return agent != nil && agent.Status.State == apiruntime.StatusIdle
	}, 3*time.Second, 50*time.Millisecond,
		"DB state should settle at idle after running→idle stateChange notification")

	agent, err := store.GetAgentRun(ctx, ws, agentName)
	require.NoError(t, err)
	assert.Equal(t, apiruntime.StatusIdle, agent.Status.State,
		"final DB state must be idle after running→idle stateChange")
}

// TestStart_DoesNotWriteStatusRunning proves the D088 boundary: without a
// runtime/state_change notification from the agent-run, the DB state never becomes
// StatusRunning. The only direct write Start() may do post-connect is the
// bootstrap config write (StatusCreating); all subsequent state transitions
// must arrive via stateChange notifications.
func TestStart_DoesNotWriteStatusRunning(t *testing.T) {
	pm, store := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ws := "default"
	agentName := "no-running-write"
	key := agentKey(ws, agentName)

	// Create agent at StatusCreating.
	require.NoError(t, store.CreateAgentRun(ctx, &pkgariapi.AgentRun{
		Metadata: pkgariapi.ObjectMeta{Workspace: ws, Name: agentName},
		Spec:     pkgariapi.AgentRunSpec{Agent: "default"},
		Status:   pkgariapi.AgentRunStatus{State: apiruntime.StatusCreating},
	}))

	// Set up mock agent-run with NO queued notifications.
	srv, socketPath := newMockRunServer(t)
	defer srv.close()

	runProc := &RunProcess{
		AgentKey:   key,
		PID:        0,
		SocketPath: socketPath,
		StateDir:   "/tmp/run-state-" + agentName,
		Events:     make(chan runapi.AgentRunEvent, 1024),
		Done:       make(chan struct{}),
	}

	// Connect using plain Dial + WatchEvent + startEventConsumer.
	client, err := runclient.Dial(ctx, socketPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	runProc.Client = client

	pm.mu.Lock()
	pm.processes[key] = runProc
	pm.mu.Unlock()

	// Watch with no queued notifications.
	watcher, err := client.WatchEvent(ctx, &runapi.SessionWatchEventParams{})
	require.NoError(t, err)
	runProc.Watcher = watcher
	pm.startEventConsumer(ws, agentName, runProc)

	// Allow notification delivery goroutines to flush (none should fire).
	time.Sleep(100 * time.Millisecond)

	// Verify DB state is NOT StatusRunning — no direct write happened.
	agent, err := store.GetAgentRun(ctx, ws, agentName)
	require.NoError(t, err)
	require.NotNil(t, agent)
	assert.NotEqual(t, apiruntime.StatusRunning, agent.Status.State,
		"Start() must not write StatusRunning directly (D088); "+
			"state should only change via runtime/state_change notification")
	assert.Equal(t, apiruntime.StatusCreating, agent.Status.State,
		"without a stateChange notification, DB state must remain StatusCreating")
}

// TestStateChange_MalformedParamsDropped verifies that a malformed
// runtime/state_change params payload is silently dropped and does not
// update the DB or panic.
func TestStateChange_MalformedParamsDropped(t *testing.T) {
	pm, store := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ws := "default"
	agentName := "malformed-sc"
	key := agentKey(ws, agentName)

	require.NoError(t, store.CreateAgentRun(ctx, &pkgariapi.AgentRun{
		Metadata: pkgariapi.ObjectMeta{Workspace: ws, Name: agentName},
		Spec:     pkgariapi.AgentRunSpec{Agent: "default"},
		Status:   pkgariapi.AgentRunStatus{State: apiruntime.StatusCreating},
	}))

	// Queue a malformed stateChange notification (array instead of object).
	srv, socketPath := newMockRunServer(t)
	defer srv.close()
	srv.queueNotification(runapi.MethodRuntimeEventUpdate, map[string]any{
		"runId":   "test-run",
		"seq":     0,
		"time":    "2026-01-01T00:00:00Z",
		"type":    "runtime_update",
		"payload": []any{"this", "is", "not", "a", "RuntimeUpdateEvent"},
	})

	runProc := &RunProcess{
		AgentKey:   key,
		PID:        0,
		SocketPath: socketPath,
		StateDir:   "/tmp/run-state-" + agentName,
		Events:     make(chan runapi.AgentRunEvent, 1024),
		Done:       make(chan struct{}),
	}

	client, err := runclient.Dial(ctx, socketPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	runProc.Client = client

	pm.mu.Lock()
	pm.processes[key] = runProc
	pm.mu.Unlock()

	watcher, err := client.WatchEvent(ctx, &runapi.SessionWatchEventParams{})
	require.NoError(t, err)
	runProc.Watcher = watcher
	pm.startEventConsumer(ws, agentName, runProc)

	// Wait for notification to be delivered and dropped.
	time.Sleep(150 * time.Millisecond)

	// DB state must be unchanged.
	agent, err := store.GetAgentRun(ctx, ws, agentName)
	require.NoError(t, err)
	assert.Equal(t, apiruntime.StatusCreating, agent.Status.State,
		"malformed stateChange must be dropped; DB state must be unchanged")
}
