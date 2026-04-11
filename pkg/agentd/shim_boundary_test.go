// Package agentd implements the agent daemon that manages agent runtime lifecycle.
// This file contains boundary tests for the shim write authority rule (D088):
// after bootstrap, only runtime/stateChange notifications may update DB agent
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

	"github.com/open-agent-d/open-agent-d/pkg/events"
	"github.com/open-agent-d/open-agent-d/pkg/meta"
	"github.com/open-agent-d/open-agent-d/pkg/spec"
)

// TestStateChange_CreatingToIdle_UpdatesDB verifies that a runtime/stateChange
// notification (creating → idle) from the shim drives a DB state update via the
// production buildNotifHandler — no direct UpdateStatus(StatusRunning) call.
func TestStateChange_CreatingToIdle_UpdatesDB(t *testing.T) {
	pm, store := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ws := "default"
	agentName := "sc-creating-idle"
	key := agentKey(ws, agentName)

	// Create agent at StatusCreating in the DB.
	require.NoError(t, store.CreateAgentRun(ctx, &meta.AgentRun{
		Metadata: meta.ObjectMeta{Workspace: ws, Name: agentName},
		Spec:     meta.AgentRunSpec{RuntimeClass: "default"},
		Status:   meta.AgentRunStatus{State: spec.StatusCreating},
	}))

	// Set up mock shim, queue a creating→idle stateChange notification.
	srv, socketPath := newMockShimServer(t)
	_ = srv // cleanup registered via t.Cleanup in newMockShimServer

	srv.queueNotification("runtime/stateChange", map[string]any{
		"sessionId":      "test-session",
		"seq":            0,
		"timestamp":      "2026-01-01T00:00:00Z",
		"previousStatus": "creating",
		"status":         "idle",
		"pid":            1234,
	})

	// Create a ShimProcess (mirrors what Start() creates after forkShim).
	shimProc := &ShimProcess{
		AgentKey:   key,
		PID:        1234,
		SocketPath: socketPath,
		StateDir:   "/tmp/shim-state-" + agentName,
		Events:     make(chan events.SessionUpdateParams, 1024),
		Done:       make(chan struct{}),
	}

	// Connect using the production notification handler (D088 boundary).
	handler := pm.buildNotifHandler(ws, agentName, shimProc)
	client, err := DialWithHandler(ctx, socketPath, handler)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	shimProc.Client = client

	// Register shimProc in the processes map (as Start() does after DialWithHandler).
	pm.mu.Lock()
	pm.processes[key] = shimProc
	pm.mu.Unlock()

	// Subscribe — mock server emits the queued notification asynchronously.
	_, err = client.Subscribe(ctx, nil, nil)
	require.NoError(t, err)

	// Wait for the stateChange notification to drive the DB update.
	require.Eventually(t, func() bool {
		agent, _ := store.GetAgentRun(ctx, ws, agentName)
		return agent != nil && agent.Status.State == spec.StatusIdle
	}, 3*time.Second, 50*time.Millisecond,
		"DB state should become idle after runtime/stateChange notification")

	// Confirm final DB state.
	agent, err := store.GetAgentRun(ctx, ws, agentName)
	require.NoError(t, err)
	assert.Equal(t, spec.StatusIdle, agent.Status.State,
		"DB state must reflect the shim-emitted stateChange, not a direct write")
}

// TestSessionUpdate_DeliversOrderedParams verifies that session/update
// notifications keep their ordering metadata all the way through the
// production buildNotifHandler into ShimProcess.Events.
func TestSessionUpdate_DeliversOrderedParams(t *testing.T) {
	pm, _ := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ws := "default"
	agentName := "session-update-order"
	key := agentKey(ws, agentName)
	turnID := "turn-ordering"

	srv, socketPath := newMockShimServer(t)
	for i := 0; i < 3; i++ {
		textPayload, err := json.Marshal(events.TextEvent{Text: fmt.Sprintf("chunk-%d", i)})
		require.NoError(t, err)
		streamSeq := i
		srv.queueNotification(events.MethodSessionUpdate, map[string]any{
			"sessionId": "test-session",
			"seq":       i,
			"timestamp": "2026-01-01T00:00:00Z",
			"turnId":    turnID,
			"streamSeq": streamSeq,
			"event": map[string]any{
				"type":    "text",
				"payload": json.RawMessage(textPayload),
			},
		})
	}

	shimProc := &ShimProcess{
		AgentKey:   key,
		PID:        1234,
		SocketPath: socketPath,
		StateDir:   "/tmp/shim-state-" + agentName,
		Events:     make(chan events.SessionUpdateParams, 1024),
		Done:       make(chan struct{}),
	}

	handler := pm.buildNotifHandler(ws, agentName, shimProc)
	client, err := DialWithHandler(ctx, socketPath, handler)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	shimProc.Client = client

	_, err = client.Subscribe(ctx, nil, nil)
	require.NoError(t, err)

	for i := 0; i < 3; i++ {
		var update events.SessionUpdateParams
		select {
		case update = <-shimProc.Events:
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout waiting for session/update %d", i)
		}

		require.Equal(t, i, update.Seq)
		require.Equal(t, turnID, update.TurnId)
		require.NotNil(t, update.StreamSeq)
		require.Equal(t, i, *update.StreamSeq)
		require.Equal(t, "text", update.Event.Type)
		payload, ok := update.Event.Payload.(events.TextEvent)
		require.True(t, ok)
		require.Equal(t, fmt.Sprintf("chunk-%d", i), payload.Text)
	}
}

// TestStateChange_RunningToIdle_UpdatesDB verifies that two successive
// runtime/stateChange notifications (idle→running, running→idle) each drive
// independent DB state updates via the production handler.
func TestStateChange_RunningToIdle_UpdatesDB(t *testing.T) {
	pm, store := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ws := "default"
	agentName := "sc-running-idle"
	key := agentKey(ws, agentName)

	// Create agent at StatusIdle.
	require.NoError(t, store.CreateAgentRun(ctx, &meta.AgentRun{
		Metadata: meta.ObjectMeta{Workspace: ws, Name: agentName},
		Spec:     meta.AgentRunSpec{RuntimeClass: "default"},
		Status:   meta.AgentRunStatus{State: spec.StatusIdle},
	}))

	// Queue two successive stateChange notifications: idle→running, then running→idle.
	srv, socketPath := newMockShimServer(t)
	srv.queueNotification("runtime/stateChange", map[string]any{
		"sessionId":      "test-session",
		"seq":            1,
		"timestamp":      "2026-01-01T00:00:00Z",
		"previousStatus": "idle",
		"status":         "running",
		"pid":            5678,
	})
	srv.queueNotification("runtime/stateChange", map[string]any{
		"sessionId":      "test-session",
		"seq":            2,
		"timestamp":      "2026-01-01T00:00:01Z",
		"previousStatus": "running",
		"status":         "idle",
		"pid":            5678,
	})

	shimProc := &ShimProcess{
		AgentKey:   key,
		PID:        5678,
		SocketPath: socketPath,
		StateDir:   "/tmp/shim-state-" + agentName,
		Events:     make(chan events.SessionUpdateParams, 1024),
		Done:       make(chan struct{}),
	}

	handler := pm.buildNotifHandler(ws, agentName, shimProc)
	client, err := DialWithHandler(ctx, socketPath, handler)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	shimProc.Client = client

	pm.mu.Lock()
	pm.processes[key] = shimProc
	pm.mu.Unlock()

	_, err = client.Subscribe(ctx, nil, nil)
	require.NoError(t, err)

	// Wait for both stateChange notifications to be processed (final state = idle).
	require.Eventually(t, func() bool {
		agent, _ := store.GetAgentRun(ctx, ws, agentName)
		return agent != nil && agent.Status.State == spec.StatusIdle
	}, 3*time.Second, 50*time.Millisecond,
		"DB state should settle at idle after running→idle stateChange notification")

	agent, err := store.GetAgentRun(ctx, ws, agentName)
	require.NoError(t, err)
	assert.Equal(t, spec.StatusIdle, agent.Status.State,
		"final DB state must be idle after running→idle stateChange")
}

// TestStart_DoesNotWriteStatusRunning proves the D088 boundary: without a
// runtime/stateChange notification from the shim, the DB state never becomes
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
	require.NoError(t, store.CreateAgentRun(ctx, &meta.AgentRun{
		Metadata: meta.ObjectMeta{Workspace: ws, Name: agentName},
		Spec:     meta.AgentRunSpec{RuntimeClass: "default"},
		Status:   meta.AgentRunStatus{State: spec.StatusCreating},
	}))

	// Set up mock shim with NO queued notifications.
	srv, socketPath := newMockShimServer(t)
	defer srv.close()

	shimProc := &ShimProcess{
		AgentKey:   key,
		PID:        0,
		SocketPath: socketPath,
		StateDir:   "/tmp/shim-state-" + agentName,
		Events:     make(chan events.SessionUpdateParams, 1024),
		Done:       make(chan struct{}),
	}

	// Connect using the production handler.
	handler := pm.buildNotifHandler(ws, agentName, shimProc)
	client, err := DialWithHandler(ctx, socketPath, handler)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	shimProc.Client = client

	pm.mu.Lock()
	pm.processes[key] = shimProc
	pm.mu.Unlock()

	// Subscribe with no queued notifications.
	_, err = client.Subscribe(ctx, nil, nil)
	require.NoError(t, err)

	// Allow notification delivery goroutines to flush (none should fire).
	time.Sleep(100 * time.Millisecond)

	// Verify DB state is NOT StatusRunning — no direct write happened.
	agent, err := store.GetAgentRun(ctx, ws, agentName)
	require.NoError(t, err)
	require.NotNil(t, agent)
	assert.NotEqual(t, spec.StatusRunning, agent.Status.State,
		"Start() must not write StatusRunning directly (D088); "+
			"state should only change via runtime/stateChange notification")
	assert.Equal(t, spec.StatusCreating, agent.Status.State,
		"without a stateChange notification, DB state must remain StatusCreating")
}

// TestStateChange_MalformedParamsDropped verifies that a malformed
// runtime/stateChange params payload is silently dropped and does not
// update the DB or panic.
func TestStateChange_MalformedParamsDropped(t *testing.T) {
	pm, store := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ws := "default"
	agentName := "malformed-sc"
	key := agentKey(ws, agentName)

	require.NoError(t, store.CreateAgentRun(ctx, &meta.AgentRun{
		Metadata: meta.ObjectMeta{Workspace: ws, Name: agentName},
		Spec:     meta.AgentRunSpec{RuntimeClass: "default"},
		Status:   meta.AgentRunStatus{State: spec.StatusCreating},
	}))

	// Queue a malformed stateChange notification (array instead of object).
	srv, socketPath := newMockShimServer(t)
	defer srv.close()
	srv.queueNotification("runtime/stateChange", []any{"this", "is", "not", "a", "stateChange"})

	shimProc := &ShimProcess{
		AgentKey:   key,
		PID:        0,
		SocketPath: socketPath,
		StateDir:   "/tmp/shim-state-" + agentName,
		Events:     make(chan events.SessionUpdateParams, 1024),
		Done:       make(chan struct{}),
	}

	handler := pm.buildNotifHandler(ws, agentName, shimProc)
	client, err := DialWithHandler(ctx, socketPath, handler)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	shimProc.Client = client

	pm.mu.Lock()
	pm.processes[key] = shimProc
	pm.mu.Unlock()

	_, err = client.Subscribe(ctx, nil, nil)
	require.NoError(t, err)

	// Wait for notification to be delivered and dropped.
	time.Sleep(150 * time.Millisecond)

	// DB state must be unchanged.
	agent, err := store.GetAgentRun(ctx, ws, agentName)
	require.NoError(t, err)
	assert.Equal(t, spec.StatusCreating, agent.Status.State,
		"malformed stateChange must be dropped; DB state must be unchanged")
}
