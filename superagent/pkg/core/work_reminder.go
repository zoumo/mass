package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"slices"
	"time"

	"github.com/zoumo/agent-sdk-go/agent"
	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"go.opentelemetry.io/otel/trace"
)

// WorkObservation ties the latest observed Issue Session input to a particular
// subscription and attempt. It does not express business or stop status.
type WorkObservation struct {
	SessionBindingID string   `json:"sessionBindingId"`
	Generation       int      `json:"generation"`
	SessionID        string   `json:"sessionId"`
	SessionUID       string   `json:"sessionUid"`
	AttemptID        string   `json:"attemptId"`
	Provider         string   `json:"provider,omitempty"`
	WorkID           string   `json:"workId,omitempty"`
	State            string   `json:"state"`
	ObservedAt       string   `json:"observedAt"`
	Continuous       bool     `json:"continuous"`
	ObservationGap   string   `json:"observationGap,omitempty"`
	End              *WorkEnd `json:"end,omitempty"`
}

type WorkEnd struct {
	EventID string `json:"eventId"`
	At      string `json:"at"`
	Reason  string `json:"reason"`
}

func (o *WorkObservation) inProgress() bool {
	return o.State == "awaiting_receipt" || o.State == "observing"
}

func (o *WorkObservation) loseContinuity(reason string) {
	o.State, o.ObservedAt, o.Continuous, o.ObservationGap = "unknown", now(), false, reason
}

func recoverIssueObservations(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `UPDATE session_bindings SET body=json_set(body,
 '$.workObservation.state','unknown','$.workObservation.observedAt',?,'$.workObservation.continuous',json('false'),
 '$.workObservation.observationGap','core_observation_interrupted')
 WHERE role='issue' AND json_extract(body,'$.workObservation.state') IN ('awaiting_receipt','observing')`, now())
	return err
}

func beginIssueObservation(ctx context.Context, tx *sql.Tx, b SessionBinding, observation WorkObservation) (bool, error) {
	_, x, err := currentBoundIssue(ctx, tx, b)
	if err != nil || x.ReminderEventID != "" {
		return false, err
	}
	x.WorkObservation = &observation
	return true, saveWorkObservation(ctx, tx, x)
}

func settleIssueObservation(ctx context.Context, tx *sql.Tx, observation WorkObservation) error {
	x, err := loadIssueBinding(ctx, tx, observation.SessionBindingID)
	if err != nil {
		return err
	}
	if x.WorkObservation == nil || x.WorkObservation.AttemptID != observation.AttemptID || x.WorkObservation.State != "awaiting_receipt" {
		return nil
	}
	x.WorkObservation = &observation
	return saveWorkObservation(ctx, tx, x)
}

type issueWatch struct {
	evidence     WorkObservation
	sub          *client.Subscription
	dispatchedAt time.Time
	steered      bool
}

func (s *Server) prepareIssueWatch(ctx context.Context, b SessionBinding, a Attempt) *issueWatch {
	w := &issueWatch{evidence: WorkObservation{
		SessionBindingID: b.ID, Generation: b.Generation,
		SessionID: b.SessionID, SessionUID: b.SessionUID, AttemptID: a.ID,
		State: "awaiting_receipt", ObservedAt: now(), Provider: b.RuntimeType,
	}}
	// The Session can become idle before its prompt call returns.
	setup, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	c, err := s.massClient()
	if err == nil {
		w.sub, err = c.Subscribe(setup, resource.SessionRef{SessionID: b.SessionID})
		if err != nil {
			_ = c.Close()
		}
	}
	return w
}

func (w *issueWatch) recordReceipt(a Attempt, result agent.InputResult, submission string) {
	o := &w.evidence
	if a.Status != "delivered" {
		o.loseContinuity("input_work_unconfirmed")
		return
	}
	o.State, o.ObservedAt = a.Status, now()
	// Keep the Codex work reference for diagnostics. Observation uses Session state.
	if o.Provider == "codex" {
		o.WorkID = submission
	}
	if w.sub == nil {
		o.ObservationGap = "live_subscription_unavailable"
	} else {
		w.steered = result == agent.InputAppliedCurrentWork
		o.State, o.Continuous = "observing", true
	}
}

func (w *issueWatch) close() {
	if w != nil && w.sub != nil {
		_ = w.sub.Close()
	}
}

func (s *Server) stopIssueWatch(bindingID string) {
	if value, ok := s.issueWatches.LoadAndDelete(bindingID); ok {
		value.(*issueWatch).close()
	}
}

func (s *Server) startIssueWatch(life context.Context, b SessionBinding, w *issueWatch, settled bool) {
	if !settled || w.evidence.State != "observing" || life.Err() != nil {
		w.close()
		return
	}
	s.issueWatches.Store(b.ID, w)
	s.issueWatchGroup.Go(func() {
		defer s.issueWatches.CompareAndDelete(b.ID, w)
		defer w.close()
		w.consume(life, b)
		if life.Err() != nil {
			return
		}
		for {
			err := s.store.recordIssueObservation(life, b, w.evidence)
			if err == nil || life.Err() != nil {
				return
			}
			slog.Error("Core could not persist Issue Session observation", "sessionBindingId", b.ID, "attemptId", w.evidence.AttemptID)
			select {
			case <-life.Done():
				return
			case <-time.After(time.Second):
			}
		}
	})
}

func (w *issueWatch) consume(ctx context.Context, b SessionBinding) {
	o := &w.evidence
	active := w.steered
	for {
		event, err := w.sub.Next(ctx)
		switch {
		case err != nil || w.sub.Err() != nil:
			o.loseContinuity("live_subscription_interrupted")
			return
		case event.SessionID != o.SessionID || event.Namespace != b.Namespace || event.SessionName != b.Name:
			o.loseContinuity("live_session_identity_mismatch")
			return
		case event.Replay != nil:
			continue
		case event.Type() == "session.history_replay":
			o.loseContinuity("session_history_reload")
			return
		case !inIssueDispatchWindow(event.Time, w.dispatchedAt):
			continue
		}
		if problem, ok := event.Payload().(events.SessionError); ok {
			switch problem.Reason {
			case "unsupported_native_item", "session_delivery_overloaded", "session_state_missing", "session_state_unsupported":
				o.loseContinuity("native_observation_interrupted")
				return
			}
		}
		state, ok := event.Payload().(events.StateUpdate)
		if !ok {
			continue
		}
		if state.State == nil {
			o.loseContinuity("native_session_state_unknown")
			return
		}
		switch *state.State {
		case events.Running, events.RequiresAction:
			active = true
		case events.Idle:
			// For a new or best-effort submission, an idle from the work
			// that preceded it can arrive while the submission is in flight.
			if active && state.PreviousState != nil && *state.PreviousState != events.Idle {
				o.State, o.ObservedAt = "ended", now()
				o.End = &WorkEnd{EventID: event.EventID, At: event.Time.Format(time.RFC3339Nano), Reason: "idle"}
				return
			}
		}
	}
}

func inIssueDispatchWindow(at, dispatchedAt time.Time) bool {
	// Some providers supply whole-second event timestamps. The live, ordered
	// subscription and activity transition still gate those events.
	if at.Nanosecond() == 0 {
		dispatchedAt = dispatchedAt.Truncate(time.Second)
	}
	return !at.Before(dispatchedAt)
}

func (s *Store) recordIssueObservation(ctx context.Context, b SessionBinding, evidence WorkObservation) (retErr error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	issue, x, err := currentBoundIssue(ctx, tx, b)
	switch {
	case errorCode(err) == "not_found" || errorCode(err) == "stale_binding":
		return nil
	case err != nil:
		return err
	case x.WorkObservation == nil || x.WorkObservation.AttemptID != evidence.AttemptID || x.WorkObservation.State != "observing":
		return nil
	}
	var current bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM session_bindings WHERE id=? AND state='ready'
 AND json_extract(body,'$.sessionId')=? AND json_extract(body,'$.sessionUid')=?)`,
		b.ID, evidence.SessionID, evidence.SessionUID).Scan(&current)
	if err != nil {
		return err
	}
	if !current {
		evidence.End = nil
		evidence.loseContinuity("issue_session_changed")
	}
	x.WorkObservation = &evidence
	if evidence.End != nil && x.ReminderEventID == "" && !issueHasCurrentReport(issue) {
		var links []trace.Link
		if s.telemetry.tracesEnabled {
			links = bindingCreation.links(ctx, tx, nil, x.ID, 0)
		}
		enqueueCtx, finish := s.telemetry.startOperation(ctx, "report.reminder", b.Role, trace.WithLinks(links...))
		defer func() { finish(operationOutcome(retErr, "queued")) }()
		if err := enqueueReportReminder(enqueueCtx, tx, &x, issue); err != nil {
			return err
		}
	}
	if err := saveWorkObservation(ctx, tx, x); err != nil {
		return err
	}
	return tx.Commit()
}

func saveWorkObservation(ctx context.Context, tx *sql.Tx, b SessionBinding) error {
	return putJSON(ctx, tx, "UPDATE session_bindings SET body=?2 WHERE id=?1", b, b.ID)
}

func enqueueReportReminder(ctx context.Context, tx *sql.Tx, x *SessionBinding, issue Issue) error {
	payload, err := json.Marshal(struct {
		IssueID     string   `json:"issueId"`
		WorkID      string   `json:"workId,omitempty"`
		End         *WorkEnd `json:"end"`
		Instruction string   `json:"instruction"`
	}{
		IssueID: x.IssueID, WorkID: x.WorkObservation.WorkID, End: x.WorkObservation.End,
		Instruction: "Use submit_report to summarize only results, progress, or blockers from work already done on this Issue. Use an operationId stable for this event_id and the goalRevision you actually used. Do not run the Issue again.",
	})
	if err != nil {
		return err
	}
	x.ReminderEventID = newID("evt")
	return enqueueInternalEvent(ctx, tx, internalEvent{
		ID: x.ReminderEventID, Kind: reportReminderKind,
		Sender: eventIdentity{Kind: "core"}, Recipient: eventIdentity{Kind: "issue", SessionBindingID: x.ID},
		Body: payload, Refs: []eventReference{issueGoalRef(issue)},
	})
}

func issueHasCurrentReport(issue Issue) bool {
	return slices.ContainsFunc(issue.Reports, func(report Report) bool {
		return reportIsCurrent(report, issue)
	})
}
