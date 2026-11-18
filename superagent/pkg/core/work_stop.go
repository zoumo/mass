package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

type StopRequest struct {
	Status         string                 `json:"status"`
	RequestedAt    string                 `json:"requestedAt"`
	DispatchedAt   string                 `json:"dispatchedAt,omitempty"`
	ObservedAt     string                 `json:"observedAt,omitempty"`
	Receipt        *runtime.CancelReceipt `json:"receipt,omitempty"`
	ErrorCode      string                 `json:"errorCode,omitempty"`
	WorkState      string                 `json:"workState"`
	SessionState   string                 `json:"sessionState,omitempty"`
	Observation    *StopObservation       `json:"observation,omitempty"`
	ObservationGap string                 `json:"observationGap,omitempty"`
}

type StopObservation struct {
	EventID    string `json:"eventId"`
	At         string `json:"at"`
	State      string `json:"state"`
	StopReason string `json:"stopReason,omitempty"`
}

func retireExecution(ctx context.Context, tx *sql.Tx, id, at string, cancel bool) (Execution, error) {
	var x Execution
	if err := readJSON(ctx, tx, "SELECT body FROM executions WHERE id=?", &x, id); err != nil {
		return x, err
	}
	var dispatched bool
	if err := tx.QueryRowContext(ctx, `SELECT json_type(b.body,'$.close') IS NULL AND
 (? OR EXISTS (SELECT 1 FROM internal_deliveries WHERE `+deliveryRecipientKind+`='worker' AND `+deliveryRecipientOwner+`=b.owner_id
 AND status IN ('dispatching','sent'))) FROM session_bindings b WHERE b.id=?`, cancel, x.BindingID).Scan(&dispatched); err != nil {
		return x, storageError()
	}
	if err := supersedeExecutionDeliveries(ctx, tx, x.ID); err != nil {
		return x, err
	}
	if dispatched {
		var err error
		x.Stop, err = requestBindingStop(ctx, tx, x.BindingID, at)
		if err != nil {
			return x, err
		}
	}
	if err := tx.QueryRowContext(ctx, "SELECT state FROM session_bindings WHERE id=?", x.BindingID).Scan(&x.PreparationAtRetirement); err != nil {
		return x, storageError()
	}
	issue, err := issueForExecution(ctx, tx, x)
	if err != nil {
		return x, err
	}
	x.Authority, x.RetiredAt, x.RetiredGoalRevision = "historical", at, issue.RequirementsRevision
	if observation := x.WorkObservation; observation != nil && observation.inProgress() {
		observation.loseContinuity("execution_retired")
	}
	if err := revokeBinding(ctx, tx, x.BindingID); err != nil {
		return x, err
	}
	return x, saveExecution(ctx, tx, x)
}

func saveExecution(ctx context.Context, tx *sql.Tx, x Execution) error {
	return putJSON(ctx, tx, "UPDATE executions SET body=?2 WHERE id=?1", x, x.ID)
}

func recoverStops(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `UPDATE session_bindings SET body=json_set(body,'$.stop.status','unknown',
 '$.stop.errorCode','stop_response_unconfirmed','$.stop.observationGap','core_stopped_before_stop_settlement',
 '$.stop.observedAt',?) WHERE json_extract(body,'$.stop.status')='dispatching'`, now())
	return err
}

func requestBindingStop(ctx context.Context, tx *sql.Tx, bindingID, at string) (*StopRequest, error) {
	stop := &StopRequest{Status: "requested", RequestedAt: at, WorkState: "unknown"}
	err := putJSON(ctx, tx, "UPDATE session_bindings SET body=json_set(body,'$.stop',json(?2)) WHERE id=?1", stop, bindingID)
	return stop, err
}

func (s *Store) claimStop(ctx context.Context, bindingID string) (StopRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var stop StopRequest
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return stop, storageError()
	}
	defer func() { _ = tx.Rollback() }()
	err = readJSON(ctx, tx, "SELECT json_extract(body,'$.stop') FROM session_bindings WHERE id=? AND json_extract(body,'$.stop.status')='requested'", &stop, bindingID)
	if err != nil {
		return stop, err
	}
	stop.Status, stop.DispatchedAt = "dispatching", now()
	if err := putJSON(ctx, tx, "UPDATE session_bindings SET body=json_set(body,'$.stop',json(?2)) WHERE id=?1", stop, bindingID); err != nil {
		return stop, err
	}
	return stop, tx.Commit()
}

func (s *Server) stopBinding(life, call context.Context, b Binding, spec resource.SessionSpec) {
	stop, err := s.store.claimStop(life, b.ID)
	if err != nil {
		return
	}
	commitGate("stop_before_rpc")
	if b.PreparationStarted {
		s.dispatchStop(call, b, spec, &stop)
	} else {
		stop.Status, stop.WorkState = "observed", "never_dispatched"
	}
	stop.ObservedAt = now()
	for {
		// No retry of the native request. Only persist the already observed result.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(life), time.Second)
		err := s.store.finishStop(ctx, b.ID, stop)
		cancel()
		if err == nil || life.Err() != nil {
			return
		}
		select {
		case <-life.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

func (s *Store) finishStop(ctx context.Context, bindingID string, stop StopRequest) error {
	raw, err := json.Marshal(stop)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.ExecContext(ctx, "UPDATE session_bindings SET body=json_set(body,'$.stop',json(?)) WHERE id=? AND json_extract(body,'$.stop.status')='dispatching'", raw, bindingID)
	return err
}

func (s *Server) dispatchStop(ctx context.Context, b Binding, spec resource.SessionSpec, stop *StopRequest) {
	stop.Status = "blocked"
	c, err := s.massClient()
	if err != nil {
		stop.ErrorCode = "massd_configuration_invalid"
		return
	}
	defer func() { _ = c.Close() }()
	ref := bindingSessionRef(b)
	var session resource.Session
	if err := c.GetSession(ctx, ref, &session); err != nil {
		stop.ErrorCode = errorCode(err)
		return
	}
	if !bindingMatches(b, spec, session) {
		stop.ErrorCode = "session_configuration_conflict"
		return
	}
	ref = resource.SessionRef{SessionID: session.Status.SessionID}
	b.SessionID = session.Status.SessionID
	stop.SessionState = string(session.Status.State)
	// Subscribe before interrupting; a fast live state transition can arrive before the RPC response.
	// The public event envelope omits native work identity, so even observed idle is not correlated termination.
	subCtx, cancelSub := context.WithTimeout(ctx, time.Second)
	sub, subErr := c.Subscribe(subCtx, ref)
	cancelSub()
	if subErr != nil {
		stop.ObservationGap = "stop_subscription_unavailable"
	} else {
		defer func() { _ = sub.Close() }()
	}
	var receipt runtime.CancelReceipt
	err = c.Cancel(ctx, ref, &receipt)
	if err != nil {
		stop.Status, stop.ErrorCode = "unknown", errorCode(err)
		if errors.Is(err, client.ErrWorkResponseUnconfirmed) {
			stop.ErrorCode = "stop_response_unconfirmed"
		}
		return
	}
	stop.Status, stop.Receipt = "observed", &receipt
	if sub != nil {
		recordStopObservation(ctx, sub, b, stop)
	}
}

func recordStopObservation(ctx context.Context, sub *client.Subscription, b Binding, stop *StopRequest) {
	observe, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	for {
		event, err := sub.Next(observe)
		if err != nil {
			stop.ObservationGap = "no_live_stop_fact"
			return
		}
		if event.Replay != nil || event.SessionID != b.SessionID || event.Namespace != b.Namespace || event.SessionName != b.Name || event.Type() != "session.state_update" {
			continue
		}
		var state struct {
			State      string `json:"state"`
			StopReason string `json:"stopReason"`
		}
		raw, err := json.Marshal(event.Payload())
		if err != nil || json.Unmarshal(raw, &state) != nil || state.State != "idle" {
			continue
		}
		stop.Observation = &StopObservation{EventID: event.EventID, At: event.Time.UTC().Format(time.RFC3339Nano), State: state.State, StopReason: state.StopReason}
		stop.ObservationGap = "native_work_identity_not_in_live_event"
		return
	}
}
