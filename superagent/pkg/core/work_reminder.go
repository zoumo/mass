package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"slices"
	"time"

	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
)

// WorkObservation ties the latest observed Worker input to a particular
// subscription and attempt. It does not express business or stop status.
type WorkObservation struct {
	ExecutionID    string   `json:"executionId"`
	BindingID      string   `json:"bindingId"`
	Generation     int      `json:"generation"`
	SessionID      string   `json:"sessionId"`
	SessionUID     string   `json:"sessionUid"`
	AttemptID      string   `json:"attemptId"`
	Provider       string   `json:"provider,omitempty"`
	WorkID         string   `json:"workId,omitempty"`
	State          string   `json:"state"`
	ObservedAt     string   `json:"observedAt"`
	Continuous     bool     `json:"continuous"`
	ObservationGap string   `json:"observationGap,omitempty"`
	End            *WorkEnd `json:"end,omitempty"`
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

func recoverWorkerObservations(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `UPDATE executions SET body=json_set(body,
 '$.workObservation.state','unknown','$.workObservation.observedAt',?,'$.workObservation.continuous',json('false'),
 '$.workObservation.observationGap','core_observation_interrupted')
 WHERE json_extract(body,'$.workObservation.state') IN ('awaiting_receipt','observing')`, now())
	return err
}

func beginWorkerObservation(ctx context.Context, tx *sql.Tx, b Binding, observation WorkObservation) (bool, error) {
	_, x, err := currentWorkerIssue(ctx, tx, b)
	if err != nil || x.ReminderEventID != "" {
		return false, err
	}
	x.WorkObservation = &observation
	return true, saveExecution(ctx, tx, x)
}

func settleWorkerObservation(ctx context.Context, tx *sql.Tx, observation WorkObservation) error {
	var x Execution
	if err := readJSON(ctx, tx, "SELECT body FROM executions WHERE id=?", &x, observation.ExecutionID); err != nil {
		return err
	}
	if x.WorkObservation == nil || x.WorkObservation.AttemptID != observation.AttemptID || x.WorkObservation.State != "awaiting_receipt" {
		return nil
	}
	x.WorkObservation = &observation
	return saveExecution(ctx, tx, x)
}

type workerWatch struct {
	evidence WorkObservation
	sub      *client.Subscription
}

func (s *Server) prepareWorkerWatch(ctx context.Context, b Binding, a Attempt) *workerWatch {
	w := &workerWatch{evidence: WorkObservation{
		ExecutionID: b.OwnerID, BindingID: b.ID, Generation: b.Generation,
		SessionID: b.SessionID, SessionUID: b.SessionUID, AttemptID: a.ID,
		State: "awaiting_receipt", ObservedAt: now(), Provider: b.RuntimeType,
	}}
	if b.RuntimeType != "codex" {
		return w
	}
	// Watch first. A native turn can complete before its prompt call returns.
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

func (w *workerWatch) recordReceipt(a Attempt, submission string) {
	o := &w.evidence
	if a.Status != "delivered" {
		o.loseContinuity("input_work_unconfirmed")
		return
	}
	o.State, o.ObservedAt = a.Status, now()
	// Only Codex exposes a submission ID that can match a live root turn.
	if o.Provider != "codex" {
		return
	}
	o.WorkID = submission
	switch {
	case submission == "":
		o.ObservationGap = "native_work_identity_unavailable"
	case w.sub == nil:
		o.ObservationGap = "live_subscription_unavailable"
	default:
		o.State, o.Continuous = "observing", true
	}
}

func (w *workerWatch) close() {
	if w != nil && w.sub != nil {
		_ = w.sub.Close()
	}
}

func (s *Server) stopWorkerWatch(bindingID string) {
	if value, ok := s.workerWatches.LoadAndDelete(bindingID); ok {
		value.(*workerWatch).close()
	}
}

func (s *Server) startWorkerWatch(life context.Context, b Binding, w *workerWatch, settled bool) {
	if !settled || w.evidence.State != "observing" || life.Err() != nil {
		w.close()
		return
	}
	s.workerWatches.Store(b.ID, w)
	s.workerWatchGroup.Go(func() {
		defer s.workerWatches.CompareAndDelete(b.ID, w)
		defer w.close()
		w.consume(life, b)
		if life.Err() != nil {
			return
		}
		for {
			err := s.store.recordWorkerObservation(life, b, w.evidence)
			if err == nil || life.Err() != nil {
				return
			}
			slog.Error("Core could not persist Worker observation", "executionId", b.OwnerID, "attemptId", w.evidence.AttemptID)
			select {
			case <-life.Done():
				return
			case <-time.After(time.Second):
			}
		}
	})
}

func (w *workerWatch) consume(ctx context.Context, b Binding) {
	o := &w.evidence
	for {
		event, err := w.sub.Next(ctx)
		switch {
		case err != nil:
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
		}
		if problem, ok := event.Payload().(events.SessionError); ok {
			switch problem.Reason {
			case "unsupported_native_item", "session_delivery_overloaded":
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
		if end := codexWorkEnd(event, state, o.WorkID); end != nil {
			o.State, o.ObservedAt, o.End = "ended", now(), end
			return
		}
	}
}

// Codex's SDK preserves these native fields in the public event metadata.
// A thread/status/changed, interaction update or child turn cannot qualify.
func codexWorkEnd(event events.SessionEvent, state events.StateUpdate, workID string) *WorkEnd {
	meta, _ := event.Meta["codex"].(map[string]any)
	method, _ := meta["method"].(string)
	turnID, _ := meta["turnId"].(string)
	_, child := meta["sourceThreadId"]
	if method != "turn/completed" || child || turnID == "" || turnID != workID {
		return nil
	}
	switch state.StopReason {
	case "completed", "interrupted", "failed":
		return &WorkEnd{EventID: event.EventID, At: event.Time.Format(time.RFC3339Nano), Reason: state.StopReason}
	default:
		return nil
	}
}

func (s *Store) recordWorkerObservation(ctx context.Context, b Binding, evidence WorkObservation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	issue, x, err := currentWorkerIssue(ctx, tx, b)
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
		evidence.loseContinuity("worker_session_changed")
	}
	x.WorkObservation = &evidence
	if evidence.End != nil && x.ReminderEventID == "" {
		if !issueHasExecutionReport(issue, x.ID) {
			if err := enqueueReportReminder(ctx, tx, &x, issue); err != nil {
				return err
			}
		}
	}
	if err := saveExecution(ctx, tx, x); err != nil {
		return err
	}
	return tx.Commit()
}

func enqueueReportReminder(ctx context.Context, tx *sql.Tx, x *Execution, issue Issue) error {
	payload, err := json.Marshal(struct {
		ExecutionID string   `json:"executionId"`
		IssueID     string   `json:"issueId"`
		WorkID      string   `json:"workId"`
		End         *WorkEnd `json:"end"`
		Instruction string   `json:"instruction"`
	}{
		ExecutionID: x.ID, IssueID: x.IssueID, WorkID: x.WorkObservation.WorkID, End: x.WorkObservation.End,
		Instruction: "Use report_submit to summarize only results, progress, or blockers from work already done on this Execution. Use an operationId stable for this delivery_id and the goalRevision you actually used. Do not run the Issue again.",
	})
	if err != nil {
		return err
	}
	x.ReminderEventID = newID("delivery")
	return enqueueInternalDelivery(ctx, tx, internalDelivery{
		ID: x.ReminderEventID, Kind: reportReminderKind,
		Sender: deliveryIdentity{Kind: "core"}, Recipient: deliveryIdentity{Kind: "worker", ExecutionID: x.ID},
		Content: deliveryContent{Body: payload, Refs: []deliveryReference{issueGoalRef(issue)}},
	})
}

func issueHasExecutionReport(issue Issue, executionID string) bool {
	return slices.ContainsFunc(issue.Reports, func(report Report) bool {
		return report.ExecutionID == executionID
	})
}
