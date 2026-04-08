// Package agentd implements the agent daemon that manages agent runtime sessions.
// This file implements the RecoverSessions startup pass that reconnects to
// live shims after a daemon restart.
package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/open-agent-d/open-agent-d/pkg/events"
	"github.com/open-agent-d/open-agent-d/pkg/meta"
	"github.com/open-agent-d/open-agent-d/pkg/spec"
)

// RecoverSessions runs at daemon startup and attempts to reconnect to shim
// processes that survived a daemon restart. For each non-terminal session in
// the meta store it:
//
//  1. Reads the persisted shim_socket_path from the DB.
//  2. Tries DialWithHandler to connect to the shim socket.
//  3. On connect failure → marks the session stopped (fail-closed per D012/D029).
//  4. Calls runtime/status to get state + recovery.lastSeq.
//  5. Calls session/subscribe(fromSeq=0) — atomic backfill + live subscription
//     under a single lock hold, eliminating the History→Subscribe event gap.
//  6. Builds a ShimProcess struct and registers it in the processes map.
//  7. Starts a watchProcess goroutine for the recovered session.
//
// Returns nil on success (even if individual sessions fail to recover).
// Returns error only for systemic failures (e.g. cannot query the DB).
func (m *ProcessManager) RecoverSessions(ctx context.Context) error {
	m.logger.Info("starting session recovery pass")

	// Signal that recovery is in progress — ARI guards block operational
	// actions (prompt, cancel) while this phase is active.
	m.SetRecoveryPhase(RecoveryPhaseRecovering)

	// List all non-terminal sessions. Terminal state is "stopped".
	// We retrieve all sessions and filter out stopped ones since the store's
	// SessionFilter only supports filtering TO a single state, not excluding one.
	allSessions, err := m.store.ListSessions(ctx, nil)
	if err != nil {
		// Mark recovery complete even on systemic failure so the daemon
		// doesn't stay permanently in recovering phase.
		m.SetRecoveryPhase(RecoveryPhaseComplete)
		return fmt.Errorf("recovery: list sessions: %w", err)
	}

	var candidates []*meta.Session
	for _, s := range allSessions {
		if s.State != meta.SessionStateStopped {
			candidates = append(candidates, s)
		}
	}

	if len(candidates) == 0 {
		m.logger.Info("recovery: no non-terminal sessions to recover")
		m.SetRecoveryPhase(RecoveryPhaseComplete)
		return nil
	}

	m.logger.Info("recovery: found candidate sessions", "count", len(candidates))

	recovered := 0
	failed := 0

	for _, session := range candidates {
		err := m.recoverSession(ctx, session)
		if err != nil {
			failed++
			m.logger.Warn("recovery: session failed, marking stopped",
				"session_id", session.ID,
				"socket_path", session.ShimSocketPath,
				"error", err)
			// Fail-closed: mark session as stopped (D012/D029).
			if tErr := m.sessions.Transition(ctx, session.ID, meta.SessionStateStopped); tErr != nil {
				m.logger.Error("recovery: failed to mark session stopped",
					"session_id", session.ID,
					"error", tErr)
			}
		} else {
			recovered++
			// Store per-session recovery metadata on the registered ShimProcess.
			now := time.Now()
			m.SetSessionRecoveryInfo(session.ID, &RecoveryInfo{
				Recovered:   true,
				RecoveredAt: &now,
				Outcome:     RecoveryOutcomeRecovered,
			})
			m.logger.Info("recovery: session recovered",
				"session_id", session.ID,
				"socket_path", session.ShimSocketPath)
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

// recoverSession attempts to reconnect to a single shim process.
func (m *ProcessManager) recoverSession(ctx context.Context, session *meta.Session) error {
	if session.ShimSocketPath == "" {
		return fmt.Errorf("no socket path persisted for session %s", session.ID)
	}

	sessionID := session.ID
	logger := m.logger.With("session_id", sessionID)

	// Create the ShimProcess struct up-front so the notification handler can
	// route events into its Events channel.
	shimProc := &ShimProcess{
		SessionID:  sessionID,
		PID:        session.ShimPID,
		BundlePath: "", // not needed for recovered sessions (we don't own the bundle)
		StateDir:   session.ShimStateDir,
		SocketPath: session.ShimSocketPath,
		Events:     make(chan events.Event, 100),
		Done:       make(chan struct{}),
		// Cmd is nil for recovered sessions — we didn't fork the process.
	}

	// Step 2-3: Connect to the shim socket.
	client, err := DialWithHandler(ctx, session.ShimSocketPath, func(ctx context.Context, method string, params json.RawMessage) {
		if method != events.MethodSessionUpdate {
			return
		}
		p, err := ParseSessionUpdate(params)
		if err != nil {
			logger.Warn("malformed session/update notification dropped",
				"error", err)
			return
		}
		ev, ok := p.Event.Payload.(events.Event)
		if !ok {
			logger.Warn("session/update payload is not an events.Event — dropped",
				"type", p.Event.Type)
			return
		}
		select {
		case shimProc.Events <- ev:
		default:
			logger.Warn("event channel full, dropping event",
				"seq", p.Seq)
		}
	})
	if err != nil {
		return fmt.Errorf("connect to shim socket %s: %w", session.ShimSocketPath, err)
	}
	shimProc.Client = client

	// Step 4-5: Call runtime/status to get state + recovery.lastSeq.
	status, err := client.Status(ctx)
	if err != nil {
		_ = client.Close()
		return fmt.Errorf("runtime/status: %w", err)
	}
	logger.Info("recovery: shim status",
		"status", status.State.Status,
		"lastSeq", status.Recovery.LastSeq,
		slog.Group("state",
			"id", status.State.ID,
			"pid", status.State.PID,
		))

	// Reconcile shim-reported status against DB session state.
	switch {
	case status.State.Status == spec.StatusStopped:
		// Shim reports stopped — fail-closed: don't proceed with recovery.
		_ = client.Close()
		return fmt.Errorf("shim reports stopped for session %s", sessionID)

	case status.State.Status == spec.StatusRunning && session.State == meta.SessionStateCreated:
		// Shim is running but DB still says created — update DB to match shim truth.
		if err := m.sessions.Transition(ctx, sessionID, meta.SessionStateRunning); err != nil {
			var invalidErr *ErrInvalidTransition
			if errors.As(err, &invalidErr) {
				logger.Warn("recovery: could not transition session to running (invalid transition, proceeding)",
					"session_id", sessionID,
					"db_state", session.State,
					"shim_status", status.State.Status,
					"error", err)
			} else {
				logger.Warn("recovery: failed to transition session to running (proceeding)",
					"session_id", sessionID,
					"error", err)
			}
		} else {
			logger.Info("recovery: reconciled session state created→running to match shim",
				"session_id", sessionID)
		}

	default:
		// For any other mismatch, log and proceed — the shim is alive.
		shimStatus := string(status.State.Status)
		dbState := string(session.State)
		if shimStatus != dbState {
			logger.Warn("recovery: shim status differs from DB state (proceeding)",
				"session_id", sessionID,
				"shim_status", shimStatus,
				"db_state", dbState)
		}
	}

	// Step 6: Atomic subscribe with backfill — replaces the old separate
	// History + Subscribe calls to eliminate the event gap between them.
	fromSeq := 0
	subResult, err := client.Subscribe(ctx, nil, &fromSeq)
	if err != nil {
		_ = client.Close()
		return fmt.Errorf("session/subscribe fromSeq=%d: %w", fromSeq, err)
	}
	logger.Info("recovery: atomic subscribe with backfill",
		"backfill_entries", len(subResult.Entries),
		"next_seq", subResult.NextSeq)

	// Step 8: Register in processes map.
	m.mu.Lock()
	m.processes[sessionID] = shimProc
	m.mu.Unlock()

	// Step 9: Start watchProcess goroutine.
	// For recovered sessions without a Cmd, we watch via DisconnectNotify.
	go m.watchRecoveredProcess(shimProc)

	return nil
}

// watchRecoveredProcess watches a recovered shim that we didn't fork.
// Since we don't have a Cmd to Wait() on, we use the client's DisconnectNotify
// channel to detect when the shim connection is lost.
func (m *ProcessManager) watchRecoveredProcess(shimProc *ShimProcess) {
	// Wait for connection loss.
	<-shimProc.Client.DisconnectNotify()

	m.logger.Info("recovered shim disconnected", "session_id", shimProc.SessionID)

	// Close the Events channel.
	close(shimProc.Events)

	// Remove from processes map.
	m.mu.Lock()
	delete(m.processes, shimProc.SessionID)
	m.mu.Unlock()

	// Transition session to "stopped" (best effort).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = m.sessions.Transition(ctx, shimProc.SessionID, meta.SessionStateStopped)

	// Close the Done channel LAST to signal all cleanup is complete.
	close(shimProc.Done)
}
