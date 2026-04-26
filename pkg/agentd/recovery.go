// Package agentd implements the agent daemon that manages agent runtime lifecycle.
// This file implements the RecoverAgents startup pass that reconnects to
// live agent-runs after a daemon restart.
package agentd

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	runapi "github.com/zoumo/mass/pkg/agentrun/api"
	runclient "github.com/zoumo/mass/pkg/agentrun/client"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	spec "github.com/zoumo/mass/pkg/runtime-spec"
	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
	"github.com/zoumo/mass/pkg/watch"
)

// RecoverSessions runs at daemon startup and attempts to reconnect to agent-run
// processes that survived a daemon restart. For each non-terminal agent in
// the meta store it:
//
//  1. Reads the persisted run_socket_path from the DB.
//  2. Tries Dial to connect to the agent-run socket.
//  3. On connect failure → marks the agent stopped (fail-closed per D012/D029).
//  4. Calls runtime/status to get state + recovery.lastSeq.
//  5. Calls runtime/watch_event(fromSeq=0) — atomic backfill + live subscription.
//  6. Builds a RunProcess struct and registers it in the processes map.
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

	var candidates []*pkgariapi.AgentRun
	for _, a := range allAgents {
		// Skip terminal states: stopped agents have no agent-run to recover,
		// and error agents require explicit restart per the agent lifecycle model.
		if a.Status.Phase != apiruntime.PhaseStopped && a.Status.Phase != apiruntime.PhaseError {
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

		runStatus, err := m.recoverAgent(ctx, agent)
		if err != nil {
			failed++
			// Agents stuck in "creating" when the daemon restarted will never
			// complete bootstrap — mark them error with a meaningful message
			// rather than stopped. Non-creating agents are marked stopped
			// (fail-closed posture per D012/D029).
			if agent.Status.Phase == apiruntime.PhaseCreating {
				m.logger.Warn("recovery: creating agent has no live agent-run, marking error",
					"agent_key", key, "error", err)
				// Don't mark stopped here — the creating-cleanup pass handles this below.
				// Skip the stopped transition so the creating-cleanup pass can apply
				// the correct "daemon restarted during creating phase" error message.
			} else {
				m.logger.Warn("recovery: agent failed, marking stopped",
					"agent_key", key,
					"socket_path", agent.Status.SocketPath,
					"error", err)
				// Fail-closed: mark agent as stopped (D012/D029).
				if tErr := m.agents.UpdateStatus(ctx, ws, name, pkgariapi.AgentRunStatus{
					Phase:        apiruntime.PhaseStopped,
					ErrorMessage: fmt.Sprintf("agent-run not recovered after daemon restart: %v", err),
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
				"socket_path", agent.Status.SocketPath)
			recoveredAgentIDs[key] = true

			// recoverAgent already reconciles state internally (idle→running etc.).
			// Only apply the outer reconciliation for states where recoverAgent
			// confirmed a clear running/idle status that differs from the DB state.
			// For mismatch cases (e.g., DB=creating, agent-run=running), recoverAgent
			// logs a warning and returns the agent-run status without changing the DB —
			// we preserve the DB state here to match that contract.
			currentAgent, _ := m.store.GetAgentRun(ctx, ws, name)
			currentState := agent.Status.Phase // initial state before recoverAgent
			if currentAgent != nil {
				currentState = currentAgent.Status.Phase
			}
			if currentState == apiruntime.PhaseIdle && runStatus == apiruntime.PhaseRunning {
				// Already reconciled by recoverAgent; no additional update needed.
			} else if currentState == apiruntime.PhaseRunning && runStatus == apiruntime.PhaseIdle {
				// Agent-run became idle during recovery — update to idle.
				if aErr := m.agents.UpdateState(ctx, ws, name, apiruntime.PhaseIdle, ""); aErr != nil {
					m.logger.Warn("recovery: failed to reconcile running→idle",
						"agent_key", key, "error", aErr)
				}
			}
			// For all other state combinations, recoverAgent already applied any
			// needed reconciliation or deliberately left the DB state unchanged.
		}
	}

	// Pending/Creating-cleanup pass: agents stuck in transient states
	// when the daemon crashed will never complete — mark them as error.
	for _, queryState := range []apiruntime.Phase{apiruntime.PhaseCreating} {
		stuckAgents, err := m.store.ListAgentRuns(ctx, &pkgariapi.AgentRunFilter{Phase: queryState})
		if err != nil {
			m.logger.Warn("recovery: failed to list agents for cleanup", "phase", queryState, "error", err)
			continue
		}
		for _, agent := range stuckAgents {
			key := agentKey(agent.Metadata.Workspace, agent.Metadata.Name)
			if recoveredAgentIDs[key] {
				continue
			}
			errMsg := fmt.Sprintf("agent bootstrap lost: daemon restarted during %s phase", queryState)
			m.logger.Warn("recovery: agent stuck in transient state, marking error", "agent_key", key, "phase", queryState)
			if aErr := m.agents.UpdateStatus(ctx, agent.Metadata.Workspace, agent.Metadata.Name,
				pkgariapi.AgentRunStatus{
					Phase:        apiruntime.PhaseError,
					ErrorMessage: errMsg,
				}); aErr != nil {
				m.logger.Warn("recovery: failed to mark agent as error",
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

// recoverAgent attempts to reconnect to a single agent-run process.
// Returns the agent-run's reported apiruntime.Status on success (apiruntime.PhaseStopped on failure).
func (m *ProcessManager) recoverAgent(ctx context.Context, agent *pkgariapi.AgentRun) (apiruntime.Phase, error) {
	if agent.Status.SocketPath == "" {
		return apiruntime.PhaseStopped, fmt.Errorf("no socket path persisted for agent %s/%s",
			agent.Metadata.Workspace, agent.Metadata.Name)
	}

	ws := agent.Metadata.Workspace
	name := agent.Metadata.Name
	key := agentKey(ws, name)
	logger := m.logger.With("agent_key", key)

	// Create the RunProcess struct up-front so the notification handler can
	// route events into its Events channel.
	runProc := &RunProcess{
		AgentKey:   key,
		PID:        agent.Status.PID,
		BundlePath: "", // not needed for recovered agents
		StateDir:   agent.Status.StateDir,
		SocketPath: agent.Status.SocketPath,
		Events:     make(chan runapi.AgentRunEvent, 100),
		Done:       make(chan struct{}),
		stopDrain:  make(chan struct{}),
		// Cmd is nil for recovered agents — we didn't fork the process.
	}
	go runProc.drainEvents()

	// Connect to the agent-run socket (plain Dial, event routing via Watcher).
	client, err := runclient.Dial(ctx, agent.Status.SocketPath)
	if err != nil {
		return apiruntime.PhaseStopped, fmt.Errorf("connect to agent-run socket %s: %w", agent.Status.SocketPath, err)
	}
	runProc.Client = client

	// Call runtime/status to get state + recovery.lastSeq.
	statusResult, err := client.Status(ctx)
	if err != nil {
		_ = client.Close()
		return apiruntime.PhaseStopped, fmt.Errorf("runtime/status: %w", err)
	}
	status := *statusResult
	logger.Info("recovery: agent-run status",
		"status", status.State.Phase,
		"lastSeq", status.Recovery.LastSeq,
		slog.Group("state",
			"id", status.State.ID,
			"pid", status.State.PID,
		))

	// Reconcile agent-run-reported status against DB agent state.
	switch {
	case status.State.Phase == apiruntime.PhaseStopped:
		// Agent-run reports stopped — fail-closed.
		_ = client.Close()
		return apiruntime.PhaseStopped, fmt.Errorf("agent-run reports stopped for agent %s", key)

	case status.State.Phase == apiruntime.PhaseRunning && agent.Status.Phase == apiruntime.PhaseIdle:
		// Agent-run is running but DB still says idle — update DB to match agent-run truth.
		if err := m.agents.UpdateState(ctx, ws, name, apiruntime.PhaseRunning, ""); err != nil {
			logger.Warn("recovery: failed to reconcile agent state idle→running (proceeding)",
				"error", err)
		} else {
			logger.Info("recovery: reconciled agent state idle→running to match agent-run")
		}

	default:
		// For any other mismatch, log and proceed — the agent-run is alive.
		runStatus := string(status.State.Phase)
		dbState := string(agent.Status.Phase)
		if runStatus != dbState {
			logger.Warn("recovery: agent-run status differs from DB state (proceeding)",
				"run_status", runStatus,
				"db_state", dbState)
		}
	}

	// Create RetryWatcher for live-only event stream (recovery: no replay needed,
	// state already reconciled from runtime/status above).
	watcher := watch.NewRetryWatcher(
		ctx,
		runclient.NewWatchFunc(agent.Status.SocketPath),
		status.Recovery.LastSeq,
		func(ev runapi.AgentRunEvent) int { return ev.Seq },
		64,
	)
	runProc.Watcher = watcher
	logger.Info("recovery: RetryWatcher started",
		"initial_cursor", status.Recovery.LastSeq,
		"from_seq", status.Recovery.LastSeq+1)

	// Start the event consumer goroutine (routes runtime_update/Status → DB, others → Events).
	m.startEventConsumer(ws, name, runProc)

	// Best-effort session recovery: always attempt session/load.
	// Agent-run checks ACP loadSession capability internally and auto-fallbacks.
	sessionID, readErr := m.readStateSessionID(agent.Status.StateDir)
	if readErr != nil {
		logger.Info("session/load: could not read sessionId from state file, skipping",
			"error", readErr)
	} else if sessionID != "" {
		if loadErr := client.Load(ctx, &runapi.SessionLoadParams{SessionID: sessionID}); loadErr != nil {
			logger.Info("session/load: failed, continuing",
				"session_id", sessionID, "error", loadErr)
		} else {
			logger.Info("session/load: succeeded", "session_id", sessionID)
		}
		// Persist sessionID and eventPath now that we have them.
		m.syncSessionInfo(ctx, ws, name, agent.Status.StateDir, logger)
	} else {
		logger.Info("session/load: no sessionId in state file, skipping")
	}

	// Register in processes map.
	m.mu.Lock()
	m.processes[key] = runProc
	m.mu.Unlock()

	// Start watchRecoveredProcess goroutine.
	go m.watchRecoveredProcess(ws, name, runProc)

	return status.State.Phase, nil
}

// readStateSessionID reads the protocol session ID from state.json in stateDir.
// Returns ("", error) if the file is missing or unreadable, ("", nil) if the
// file exists but SessionID is empty (e.g. agent-run wrote state before protocol handshake).
func (m *ProcessManager) readStateSessionID(stateDir string) (string, error) {
	if stateDir == "" {
		return "", fmt.Errorf("no state dir")
	}
	state, err := spec.ReadState(stateDir)
	if err != nil {
		return "", err
	}
	return state.SessionID, nil
}

// watchRecoveredProcess watches a recovered agent-run that we didn't fork.
// Since we don't have a Cmd to Wait() on, we use the client's DisconnectNotify
// channel to detect when the agent-run connection is lost.
func (m *ProcessManager) watchRecoveredProcess(workspace, name string, runProc *RunProcess) {
	// Wait for connection loss.
	<-runProc.Client.DisconnectNotify()

	// Stop the watch stream so its goroutine exits cleanly.
	if runProc.Watcher != nil {
		runProc.Watcher.Stop()
	}

	key := runProc.AgentKey
	m.logger.Info("recovered agent-run disconnected", "agent_key", key)

	// Close the Events channel.
	close(runProc.Events)

	// Remove from processes map.
	m.mu.Lock()
	delete(m.processes, key)
	m.mu.Unlock()

	// Transition agent to "stopped" (best effort).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = m.agents.UpdateStatus(ctx, workspace, name, pkgariapi.AgentRunStatus{Phase: apiruntime.PhaseStopped})

	// Close the Done channel LAST to signal all cleanup is complete.
	close(runProc.Done)
}
