// Package agentd implements the agent daemon that manages agent runtime lifecycle.
// This file implements the RecoverAgents startup pass that reconnects to
// live shims after a daemon restart.
package agentd

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/open-agent-d/open-agent-d/pkg/events"
	"github.com/open-agent-d/open-agent-d/pkg/meta"
	"github.com/open-agent-d/open-agent-d/pkg/spec"
)

// RecoverSessions runs at daemon startup and attempts to reconnect to shim
// processes that survived a daemon restart. For each non-terminal agent in
// the meta store it:
//
//  1. Reads the persisted shim_socket_path from the DB.
//  2. Tries DialWithHandler to connect to the shim socket.
//  3. On connect failure → marks the agent stopped (fail-closed per D012/D029).
//  4. Calls runtime/status to get state + recovery.lastSeq.
//  5. Calls session/subscribe(fromSeq=0) — atomic backfill + live subscription.
//  6. Builds a ShimProcess struct and registers it in the processes map.
//  7. Starts a watchRecoveredProcess goroutine for the recovered agent.
//
// Returns nil on success (even if individual agents fail to recover).
// Returns error only for systemic failures (e.g. cannot query the DB).
func (m *ProcessManager) RecoverSessions(ctx context.Context) error {
	m.logger.Info("starting agent recovery pass")

	// Signal that recovery is in progress — ARI guards block operational
	// actions (prompt, cancel) while this phase is active.
	m.SetRecoveryPhase(RecoveryPhaseRecovering)

	// List all agents. We filter out terminal ones (stopped, error) manually
	// because the store's AgentFilter only supports filtering TO a single state.
	allAgents, err := m.store.ListAgentRuns(ctx, nil)
	if err != nil {
		m.SetRecoveryPhase(RecoveryPhaseComplete)
		return fmt.Errorf("recovery: list agents: %w", err)
	}

	var candidates []*meta.AgentRun
	for _, a := range allAgents {
		// Skip terminal states: stopped agents have no shim to recover,
		// and error agents require explicit restart per the agent lifecycle model.
		if a.Status.State != spec.StatusStopped && a.Status.State != spec.StatusError {
			candidates = append(candidates, a)
		}
	}

	m.logger.Info("recovery: found candidate agents", "count", len(candidates))

	recovered := 0
	failed := 0

	// recoveredAgentIDs tracks agents (workspace+"/"+name) successfully recovered.
	// Used by the creating-cleanup pass below.
	recoveredAgentIDs := make(map[string]bool)

	for _, agent := range candidates {
		ws := agent.Metadata.Workspace
		name := agent.Metadata.Name
		key := agentKey(ws, name)

		shimStatus, err := m.recoverAgent(ctx, agent)
		if err != nil {
			failed++
			// Agents stuck in "creating" when the daemon restarted will never
			// complete bootstrap — mark them error with a meaningful message
			// rather than stopped. Non-creating agents are marked stopped
			// (fail-closed posture per D012/D029).
			if agent.Status.State == spec.StatusCreating {
				m.logger.Warn("recovery: creating agent has no live shim, marking error",
					"agent_key", key, "error", err)
				// Don't mark stopped here — the creating-cleanup pass handles this below.
				// Skip the stopped transition so the creating-cleanup pass can apply
				// the correct "daemon restarted during creating phase" error message.
			} else {
				m.logger.Warn("recovery: agent failed, marking stopped",
					"agent_key", key,
					"socket_path", agent.Status.ShimSocketPath,
					"error", err)
				// Fail-closed: mark agent as stopped (D012/D029).
				if tErr := m.agents.UpdateStatus(ctx, ws, name, meta.AgentRunStatus{
					State:        spec.StatusStopped,
					ErrorMessage: fmt.Sprintf("shim not recovered after daemon restart: %v", err),
				}); tErr != nil {
					m.logger.Error("recovery: failed to mark agent stopped",
						"agent_key", key,
						"error", tErr)
				}
			}
		} else {
			recovered++
			now := time.Now()
			m.SetAgentRecoveryInfo(key, &RecoveryInfo{
				Recovered:   true,
				RecoveredAt: &now,
				Outcome:     RecoveryOutcomeRecovered,
			})
			m.logger.Info("recovery: agent recovered",
				"agent_key", key,
				"socket_path", agent.Status.ShimSocketPath)
			recoveredAgentIDs[key] = true

			// recoverAgent already reconciles state internally (idle→running etc.).
			// Only apply the outer reconciliation for states where recoverAgent
			// confirmed a clear running/idle status that differs from the DB state.
			// For mismatch cases (e.g., DB=creating, shim=running), recoverAgent
			// logs a warning and returns the shim status without changing the DB —
			// we preserve the DB state here to match that contract.
			currentAgent, _ := m.store.GetAgentRun(ctx, ws, name)
			currentState := agent.Status.State // initial state before recoverAgent
			if currentAgent != nil {
				currentState = currentAgent.Status.State
			}
			if currentState == spec.StatusIdle && shimStatus == spec.StatusRunning {
				// Already reconciled by recoverAgent; no additional update needed.
			} else if currentState == spec.StatusRunning && shimStatus == spec.StatusIdle {
				// Shim became idle during recovery — update to idle.
				if aErr := m.agents.UpdateStatus(ctx, ws, name, meta.AgentRunStatus{
					State:          spec.StatusIdle,
					ShimSocketPath: agent.Status.ShimSocketPath,
					ShimStateDir:   agent.Status.ShimStateDir,
					ShimPID:        agent.Status.ShimPID,
				}); aErr != nil {
					m.logger.Warn("recovery: failed to reconcile running→idle",
						"agent_key", key, "error", aErr)
				}
			}
			// For all other state combinations, recoverAgent already applied any
			// needed reconciliation or deliberately left the DB state unchanged.
		}
	}

	// Creating-cleanup pass: agents that were still bootstrapping when the
	// daemon restarted will never complete — mark them as error.
	if creatingAgents, err := m.store.ListAgentRuns(ctx, &meta.AgentRunFilter{State: spec.StatusCreating}); err != nil {
		m.logger.Warn("recovery: failed to list creating agents for cleanup", "error", err)
	} else {
		for _, agent := range creatingAgents {
			key := agentKey(agent.Metadata.Workspace, agent.Metadata.Name)
			if recoveredAgentIDs[key] {
				continue // bootstrap completed — agent was recovered
			}
			m.logger.Warn("recovery: agent stuck in creating, marking error", "agent_key", key)
			if aErr := m.agents.UpdateStatus(ctx, agent.Metadata.Workspace, agent.Metadata.Name,
				meta.AgentRunStatus{
					State:        spec.StatusError,
					ErrorMessage: "agent bootstrap lost: daemon restarted during creating phase",
				}); aErr != nil {
				m.logger.Warn("recovery: failed to mark creating agent as error",
					"agent_key", key,
					"error", aErr)
			}
		}
	}

	// Recovery pass finished — unblock operational actions.
	m.SetRecoveryPhase(RecoveryPhaseComplete)

	m.logger.Info("recovery pass complete",
		"recovered", recovered,
		"failed", failed,
		"total", len(candidates))

	return nil
}

// recoverAgent attempts to reconnect to a single shim process.
// Returns the shim's reported spec.Status on success (spec.StatusStopped on failure).
func (m *ProcessManager) recoverAgent(ctx context.Context, agent *meta.AgentRun) (spec.Status, error) {
	if agent.Status.ShimSocketPath == "" {
		return spec.StatusStopped, fmt.Errorf("no socket path persisted for agent %s/%s",
			agent.Metadata.Workspace, agent.Metadata.Name)
	}

	ws := agent.Metadata.Workspace
	name := agent.Metadata.Name
	key := agentKey(ws, name)
	logger := m.logger.With("agent_key", key)

	// Create the ShimProcess struct up-front so the notification handler can
	// route events into its Events channel.
	shimProc := &ShimProcess{
		AgentKey:   key,
		PID:        agent.Status.ShimPID,
		BundlePath: "", // not needed for recovered agents
		StateDir:   agent.Status.ShimStateDir,
		SocketPath: agent.Status.ShimSocketPath,
		Events:     make(chan events.SessionUpdateParams, 100),
		Done:       make(chan struct{}),
		// Cmd is nil for recovered agents — we didn't fork the process.
	}

	// Connect to the shim socket with the unified notification handler.
	// Routes session/update → shimProc.Events and runtime/stateChange → DB (D088).
	client, err := DialWithHandler(ctx, agent.Status.ShimSocketPath, m.buildNotifHandler(ws, name, shimProc))
	if err != nil {
		return spec.StatusStopped, fmt.Errorf("connect to shim socket %s: %w", agent.Status.ShimSocketPath, err)
	}
	shimProc.Client = client

	// Call runtime/status to get state + recovery.lastSeq.
	status, err := client.Status(ctx)
	if err != nil {
		_ = client.Close()
		return spec.StatusStopped, fmt.Errorf("runtime/status: %w", err)
	}
	logger.Info("recovery: shim status",
		"status", status.State.Status,
		"lastSeq", status.Recovery.LastSeq,
		slog.Group("state",
			"id", status.State.ID,
			"pid", status.State.PID,
		))

	// Reconcile shim-reported status against DB agent state.
	switch {
	case status.State.Status == spec.StatusStopped:
		// Shim reports stopped — fail-closed.
		_ = client.Close()
		return spec.StatusStopped, fmt.Errorf("shim reports stopped for agent %s", key)

	case status.State.Status == spec.StatusRunning && agent.Status.State == spec.StatusIdle:
		// Shim is running but DB still says idle — update DB to match shim truth.
		if err := m.agents.UpdateStatus(ctx, ws, name, meta.AgentRunStatus{
			State:          spec.StatusRunning,
			ShimSocketPath: agent.Status.ShimSocketPath,
			ShimStateDir:   agent.Status.ShimStateDir,
			ShimPID:        agent.Status.ShimPID,
		}); err != nil {
			logger.Warn("recovery: failed to reconcile agent state idle→running (proceeding)",
				"error", err)
		} else {
			logger.Info("recovery: reconciled agent state idle→running to match shim")
		}

	default:
		// For any other mismatch, log and proceed — the shim is alive.
		shimStatus := string(status.State.Status)
		dbState := string(agent.Status.State)
		if shimStatus != dbState {
			logger.Warn("recovery: shim status differs from DB state (proceeding)",
				"shim_status", shimStatus,
				"db_state", dbState)
		}
	}

	// Atomic subscribe with backfill.
	fromSeq := 0
	subResult, err := client.Subscribe(ctx, nil, &fromSeq)
	if err != nil {
		_ = client.Close()
		return spec.StatusStopped, fmt.Errorf("session/subscribe fromSeq=%d: %w", fromSeq, err)
	}
	logger.Info("recovery: atomic subscribe with backfill",
		"backfill_entries", len(subResult.Entries),
		"next_seq", subResult.NextSeq)

	// Apply RestartPolicy: tryReload attempts ACP session/load to restore
	// conversation history. alwaysNew (default) starts fresh.
	if agent.Spec.RestartPolicy == meta.RestartPolicyTryReload {
		sessionID, readErr := m.readStateSessionID(agent.Status.ShimStateDir)
		if readErr != nil {
			logger.Info("tryReload: could not read sessionId from state file, skipping",
				"error", readErr)
		} else if sessionID != "" {
			if loadErr := client.Load(ctx, sessionID); loadErr != nil {
				logger.Info("tryReload: session/load failed, continuing",
					"session_id", sessionID, "error", loadErr)
			} else {
				logger.Info("tryReload: session/load succeeded", "session_id", sessionID)
			}
		} else {
			logger.Info("tryReload: no sessionId in state file, skipping")
		}
	}

	// Register in processes map.
	m.mu.Lock()
	m.processes[key] = shimProc
	m.mu.Unlock()

	// Start watchRecoveredProcess goroutine.
	go m.watchRecoveredProcess(ws, name, shimProc)

	return status.State.Status, nil
}

// readStateSessionID reads the ACP session ID from the state.json in stateDir.
// Returns ("", error) if the file is missing or unreadable, ("", nil) if the
// file exists but ID is empty (e.g. shim wrote state before ACP handshake).
func (m *ProcessManager) readStateSessionID(stateDir string) (string, error) {
	if stateDir == "" {
		return "", fmt.Errorf("no state dir")
	}
	state, err := spec.ReadState(stateDir)
	if err != nil {
		return "", err
	}
	return state.ID, nil
}

// watchRecoveredProcess watches a recovered shim that we didn't fork.
// Since we don't have a Cmd to Wait() on, we use the client's DisconnectNotify
// channel to detect when the shim connection is lost.
func (m *ProcessManager) watchRecoveredProcess(workspace, name string, shimProc *ShimProcess) {
	// Wait for connection loss.
	<-shimProc.Client.DisconnectNotify()

	key := shimProc.AgentKey
	m.logger.Info("recovered shim disconnected", "agent_key", key)

	// Close the Events channel.
	close(shimProc.Events)

	// Remove from processes map.
	m.mu.Lock()
	delete(m.processes, key)
	m.mu.Unlock()

	// Transition agent to "stopped" (best effort).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = m.agents.UpdateStatus(ctx, workspace, name, meta.AgentRunStatus{State: spec.StatusStopped})

	// Close the Done channel LAST to signal all cleanup is complete.
	close(shimProc.Done)
}
