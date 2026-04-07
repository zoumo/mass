// Package agentd implements the agent daemon that manages agent runtime sessions.
// This file implements the RecoverSessions startup pass that reconnects to
// live shims after a daemon restart.
package agentd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/open-agent-d/open-agent-d/pkg/events"
	"github.com/open-agent-d/open-agent-d/pkg/meta"
)

// RecoverSessions runs at daemon startup and attempts to reconnect to shim
// processes that survived a daemon restart. For each non-terminal session in
// the meta store it:
//
//  1. Reads the persisted shim_socket_path from the DB.
//  2. Tries DialWithHandler to connect to the shim socket.
//  3. On connect failure → marks the session stopped (fail-closed per D012/D029).
//  4. Calls runtime/status to get state + recovery.lastSeq.
//  5. Calls runtime/history(fromSeq=0) to replay missed events (logged only).
//  6. Calls session/subscribe(afterSeq=lastSeq) to resume live notifications.
//  7. Builds a ShimProcess struct and registers it in the processes map.
//  8. Starts a watchProcess goroutine for the recovered session.
//
// Returns nil on success (even if individual sessions fail to recover).
// Returns error only for systemic failures (e.g. cannot query the DB).
func (m *ProcessManager) RecoverSessions(ctx context.Context) error {
	m.logger.Info("starting session recovery pass")

	// List all non-terminal sessions. Terminal state is "stopped".
	// We retrieve all sessions and filter out stopped ones since the store's
	// SessionFilter only supports filtering TO a single state, not excluding one.
	allSessions, err := m.store.ListSessions(ctx, nil)
	if err != nil {
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
			m.logger.Info("recovery: session recovered",
				"session_id", session.ID,
				"socket_path", session.ShimSocketPath)
		}
	}

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

	// Step 6: Call runtime/history(fromSeq=0) to replay any missed events.
	fromSeq := 0
	history, err := client.History(ctx, &fromSeq)
	if err != nil {
		_ = client.Close()
		return fmt.Errorf("runtime/history: %w", err)
	}
	logger.Info("recovery: replayed history",
		"entries", len(history.Entries))

	// Step 7: Call session/subscribe(afterSeq=lastSeq) to resume live notifications.
	lastSeq := status.Recovery.LastSeq
	_, err = client.Subscribe(ctx, &lastSeq)
	if err != nil {
		_ = client.Close()
		return fmt.Errorf("session/subscribe afterSeq=%d: %w", lastSeq, err)
	}

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
