package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/events"
	rpcwire "github.com/zoumo/mass/mass-go/pkg/jsonrpc"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

const (
	batchWindow  = 200 * time.Millisecond
	batchLimit   = 20
	batchBytes   = 64 * 1024
	previewBytes = 8 * 1024
)

type queuedDelivery struct {
	event       Event
	delivery    Delivery
	invalidated bool
}

type inputBatch struct {
	BindingID     string            `json:"bindingId"`
	Generation    int               `json:"generation"`
	Events        []json.RawMessage `json:"events"`
	SchemaVersion int               `json:"schemaVersion"`
}

type inputEvent struct {
	ContentRef       *ContentRef     `json:"contentRef"`
	Message          json.RawMessage `json:"message,omitempty"`
	Payload          json.RawMessage `json:"payload,omitempty"`
	PayloadPreview   string          `json:"payloadPreview,omitempty"`
	EventID          string          `json:"eventId"`
	Type             string          `json:"type"`
	At               string          `json:"at"`
	Intent           string          `json:"intent"`
	MessageID        string          `json:"messageId,omitempty"`
	ThreadKey        string          `json:"threadKey,omitempty"`
	TargetRole       string          `json:"targetRole"`
	TargetID         string          `json:"targetId,omitempty"`
	RouteRound       int             `json:"routeRound,omitempty"`
	Revision         int             `json:"revision,omitempty"`
	TargetGeneration int             `json:"targetGeneration,omitempty"`
	TopicGeneration  *int            `json:"topicGeneration,omitempty"`
	Truncated        bool            `json:"truncated,omitempty"`
}

func pendingDeliveries(ctx context.Context, tx *sql.Tx, bindingID string, includeBlocked bool) ([]queuedDelivery, error) {
	q := `SELECT e.body,d.body,e.invalidated FROM deliveries d JOIN events e ON e.id=d.event_id
 WHERE d.binding_id=? AND d.status IN ('pending','transport_unknown') ORDER BY e.seq LIMIT 21`
	if includeBlocked {
		q = `SELECT e.body,d.body,e.invalidated FROM deliveries d JOIN events e ON e.id=d.event_id
 WHERE d.binding_id=? AND d.status NOT IN ('delivered','superseded') ORDER BY e.seq`
	}
	rows, err := tx.QueryContext(ctx, q, bindingID)
	if err != nil {
		return nil, storageError()
	}
	defer func() { _ = rows.Close() }()
	var result []queuedDelivery
	for rows.Next() {
		var rawEvent, rawDelivery []byte
		var item queuedDelivery
		if rows.Scan(&rawEvent, &rawDelivery, &item.invalidated) != nil || json.Unmarshal(rawEvent, &item.event) != nil || json.Unmarshal(rawDelivery, &item.delivery) != nil {
			return nil, storageError()
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func eventCurrent(ctx context.Context, tx *sql.Tx, b Binding, item queuedDelivery) (bool, error) {
	e := item.event
	if item.invalidated || e.TargetRole != b.Role || e.TargetID != b.OwnerID ||
		(e.TargetGeneration != 0 && e.TargetGeneration != b.Generation) {
		return false, nil
	}
	if e.Type == reportReminderEvent {
		return reminderCurrent(ctx, tx, b, e)
	}
	if e.TopicGeneration != nil {
		var topic Topic
		if err := readJSON(ctx, tx, "SELECT body FROM topics WHERE id=?", &topic, e.TargetID); err != nil {
			return false, err
		}
		if terminal(topic.Status) || topic.LifecycleGeneration != *e.TopicGeneration {
			return false, nil
		}
	}
	if e.MessageID == "" || e.RouteRound == 0 {
		return true, nil
	}
	var matched bool
	var err error
	if e.TargetRole == "router" {
		err = tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM message_routes WHERE message_id=? AND round=? AND state='pending')", e.MessageID, e.RouteRound).Scan(&matched)
	} else {
		err = tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM message_topics WHERE message_id=? AND topic_id=? AND active=1 AND round=?)", e.MessageID, e.TargetID, e.RouteRound).Scan(&matched)
	}
	return matched, err
}

func encodeInputEvent(ctx context.Context, tx *sql.Tx, e Event) (raw json.RawMessage, solo bool, err error) {
	input := inputEvent{
		EventID: e.ID, Type: e.Type, At: e.At, Intent: e.Intent, ContentRef: e.ContentRef, Payload: e.Payload,
		MessageID: e.MessageID, ThreadKey: e.ThreadKey, TargetRole: e.TargetRole, TargetID: e.TargetID, RouteRound: e.RouteRound,
		Revision: e.Revision, TargetGeneration: e.TargetGeneration, TopicGeneration: e.TopicGeneration,
	}
	var message Message
	if e.MessageID != "" {
		if err := readJSON(ctx, tx, "SELECT body FROM messages WHERE id=?", &message, e.MessageID); err != nil {
			return nil, false, err
		}
		if len(message.Text) > previewBytes {
			message.Text, input.Truncated = truncatePreview(message.Text), true
		}
		input.Message, err = json.Marshal(message)
		if err != nil {
			return nil, false, err
		}
	} else if len(input.Payload) > previewBytes {
		// A string preview may end before a JSON delimiter. The full payload
		// remains valid JSON through event_get.
		input.PayloadPreview = truncatePreview(string(input.Payload))
		input.Payload, input.Truncated = nil, true
	}
	raw, err = json.Marshal(input)
	if err != nil || len(raw) <= batchBytes-512 {
		return raw, false, err
	}
	// Keep Core identities and the bounded preview; native identifiers and
	// optional metadata can be arbitrarily large and have a scoped reference.
	input.ThreadKey, input.Payload, input.Truncated = "", nil, true
	if e.MessageID != "" {
		input.Message, err = json.Marshal(map[string]string{"id": e.MessageID, "text": message.Text})
		if err != nil {
			return nil, false, err
		}
	}
	raw, err = json.Marshal(input)
	return raw, true, err
}

func truncatePreview(text string) string {
	cut := previewBytes
	for !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

func assembleBatch(ctx context.Context, tx *sql.Tx, b Binding, pending []queuedDelivery, at time.Time) (Batch, []byte, []queuedDelivery, error) {
	first, err := time.Parse(time.RFC3339Nano, pending[0].event.At)
	if err != nil {
		return Batch{}, nil, nil, storageError()
	}
	deadline := first.Add(batchWindow)
	due := at.Format("2006-01-02T15:04:05.000000000Z")
	batch := Batch{ID: newID("batch"), CreatedAt: now(), BindingID: b.ID, SessionID: b.SessionID, SessionUID: b.SessionUID, Method: "inject"}
	envelope := inputBatch{BindingID: b.ID, Generation: b.Generation, SchemaVersion: 2}
	var selected []queuedDelivery
	var wire []byte
	full := false
	for _, item := range pending {
		when, parseErr := time.Parse(time.RFC3339Nano, item.event.At)
		if parseErr != nil {
			return Batch{}, nil, nil, storageError()
		}
		if when.After(deadline) || item.delivery.NextAttemptAt > due {
			break
		}
		raw, solo, encodeErr := encodeInputEvent(ctx, tx, item.event)
		if encodeErr != nil {
			return Batch{}, nil, nil, encodeErr
		}
		if solo && len(selected) != 0 {
			full = true
			break
		}
		envelope.Events = append(envelope.Events, raw)
		candidate, marshalErr := json.Marshal(envelope)
		if marshalErr != nil {
			return Batch{}, nil, nil, marshalErr
		}
		if len(candidate) > batchBytes {
			if len(selected) == 0 {
				return Batch{}, nil, nil, problem("invalid_argument", "event identity exceeds batch capacity")
			}
			full = true
			break
		}
		wire = candidate
		batch.EventIDs = append(batch.EventIDs, item.event.ID)
		selected = append(selected, item)
		if item.event.Intent == "act" {
			batch.Method = "start_or_steer"
		}
		if solo || len(selected) == batchLimit {
			full = true
			break
		}
	}
	if len(selected) == 0 || !full && at.Before(deadline) {
		return Batch{}, nil, nil, nil
	}
	batch.Bytes = len(wire)
	return batch, wire, selected, nil
}

func (s *Store) claimBatch(ctx context.Context, b Binding) (Attempt, Batch, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Attempt{}, Batch{}, nil, storageError()
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkBinding(ctx, tx, b); err != nil {
		return Attempt{}, Batch{}, nil, err
	}
	var sealed bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM deliveries WHERE binding_id=? AND status IN ('blocked_unknown','dispatching'))", b.ID).Scan(&sealed); err != nil || sealed {
		return Attempt{}, Batch{}, nil, err
	}
	pending, err := pendingDeliveries(ctx, tx, b.ID, false)
	if err != nil || len(pending) == 0 {
		return Attempt{}, Batch{}, nil, err
	}
	// Validate queue heads and the entire previous batch before ever resending.
	eligible := pending[:0]
	for _, item := range pending {
		current, checkErr := eventCurrent(ctx, tx, b, item)
		if checkErr != nil {
			return Attempt{}, Batch{}, nil, checkErr
		}
		if current {
			eligible = append(eligible, item)
		} else {
			d := item.delivery
			d.Status, d.NextAttemptAt = "superseded", ""
			if err := saveDelivery(ctx, tx, d); err != nil {
				return Attempt{}, Batch{}, nil, err
			}
		}
	}
	if len(eligible) == 0 || eligible[0].delivery.NextAttemptAt > now() {
		return Attempt{}, Batch{}, nil, tx.Commit()
	}
	batch, wire, selected, err := selectBatch(ctx, tx, b, eligible)
	if err != nil {
		return Attempt{}, Batch{}, nil, err
	}
	if len(selected) == 0 {
		return Attempt{}, Batch{}, nil, tx.Commit()
	}
	a := Attempt{ID: newID("attempt"), BatchID: batch.ID, CreatedAt: now(), Status: "dispatching"}
	for _, item := range selected {
		d := item.delivery
		d.SessionID, d.SessionUID = b.SessionID, b.SessionUID
		d.Status, d.BatchID, d.NextAttemptAt = "dispatching", batch.ID, ""
		d.Attempts++
		a.PossibleDuplicate = a.PossibleDuplicate || d.PossibleDuplicate
		if err := saveDelivery(ctx, tx, d); err != nil {
			return Attempt{}, Batch{}, nil, err
		}
	}
	batch.Attempts++
	batch.Status, batch.LastAttemptID = a.Status, a.ID
	if err := saveBatch(ctx, tx, batch); err != nil {
		return Attempt{}, Batch{}, nil, err
	}
	if err := putJSON(ctx, tx, "INSERT INTO attempts(id,created_at,batch_id,body) VALUES(?,?,?,?)", a, a.ID, a.CreatedAt, a.BatchID); err != nil {
		return Attempt{}, Batch{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		return Attempt{}, Batch{}, nil, storageError()
	}
	return a, batch, wire, nil
}

func selectBatch(ctx context.Context, tx *sql.Tx, b Binding, pending []queuedDelivery) (Batch, []byte, []queuedDelivery, error) {
	oldID := pending[0].delivery.BatchID
	if oldID != "" {
		var raw, wire []byte
		var previous Batch
		if tx.QueryRowContext(ctx, "SELECT body,wire FROM batches WHERE id=?", oldID).Scan(&raw, &wire) != nil || json.Unmarshal(raw, &previous) != nil {
			return Batch{}, nil, nil, storageError()
		}
		matches := len(previous.EventIDs) <= len(pending) && previous.SessionID == b.SessionID && previous.SessionUID == b.SessionUID
		if matches {
			for i, id := range previous.EventIDs {
				if pending[i].event.ID != id || pending[i].delivery.BatchID != oldID {
					matches = false
					break
				}
			}
		}
		if matches {
			return previous, wire, pending[:len(previous.EventIDs)], nil
		}
		previous.Status = "superseded"
		if err := saveBatch(ctx, tx, previous); err != nil {
			return Batch{}, nil, nil, err
		}
	}
	batch, wire, selected, err := assembleBatch(ctx, tx, b, pending, time.Now().UTC())
	if err != nil || len(selected) == 0 {
		return batch, wire, selected, err
	}
	err = putJSON(ctx, tx, "INSERT INTO batches(id,created_at,binding_id,wire,body) VALUES(?,?,?,?,?)", batch, batch.ID, batch.CreatedAt, b.ID, wire)
	return batch, wire, selected, err
}

func retryDelay(failures int, attention bool) time.Duration {
	if attention || failures > 6 {
		return time.Duration(54+rand.IntN(7)) * time.Second
	}
	base := time.Second << (failures - 1)
	return base + rand.N(base/5+1)
}

func needsDeliveryAttention(d Delivery, at time.Time) bool {
	firstFailure, _ := time.Parse(time.RFC3339Nano, d.FirstFailureAt)
	return d.Status == "blocked" || d.Status == "blocked_unknown" ||
		(d.Status != "delivered" && (d.Failures >= 10 || !firstFailure.IsZero() && at.Sub(firstFailure) >= 10*time.Minute))
}

func settleBatch(ctx context.Context, tx *sql.Tx, a Attempt) error {
	var batch Batch
	if err := readJSON(ctx, tx, "SELECT body FROM batches WHERE id=?", &batch, a.BatchID); err != nil {
		return err
	}
	var existing Attempt
	if err := readJSON(ctx, tx, "SELECT body FROM attempts WHERE id=?", &existing, a.ID); err != nil {
		return err
	}
	if existing.Status != "dispatching" {
		return nil
	}
	a.FinishedAt = now()
	batch.Status, batch.Receipt = a.Status, a.Receipt
	if err := saveBatch(ctx, tx, batch); err != nil {
		return err
	}
	var deliveries []Delivery
	maxFailures, attention := 0, false
	for _, id := range batch.EventIDs {
		var d Delivery
		if err := readJSON(ctx, tx, "SELECT body FROM deliveries WHERE event_id=? AND binding_id=?", &d, id, batch.BindingID); err != nil {
			return err
		}
		d.Status, d.Receipt, d.LastError = a.Status, a.Receipt, a.ErrorCode
		d.PossibleDuplicate = d.PossibleDuplicate || a.PossibleDuplicate
		if a.ObservationGap != "" {
			d.ObservationGap = a.ObservationGap
		}
		if a.Status != "delivered" {
			d.Failures++
			if d.FirstFailureAt == "" {
				d.FirstFailureAt = a.CreatedAt
			}
		}
		d.NeedsAttention = needsDeliveryAttention(d, time.Now())
		maxFailures = max(maxFailures, d.Failures)
		attention = attention || d.NeedsAttention
		deliveries = append(deliveries, d)
	}
	next := ""
	if a.Status == "pending" || a.Status == "transport_unknown" {
		next = after(retryDelay(maxFailures, attention))
	}
	for _, d := range deliveries {
		var invalidated bool
		if err := tx.QueryRowContext(ctx, "SELECT invalidated FROM events WHERE id=?", d.EventID).Scan(&invalidated); err != nil {
			return storageError()
		}
		if invalidated && d.Status != "delivered" {
			d.Status, d.NextAttemptAt = "superseded", ""
		} else {
			d.NextAttemptAt = next
		}
		if err := saveDelivery(ctx, tx, d); err != nil {
			return err
		}
	}
	return putJSON(ctx, tx, "UPDATE attempts SET body=?2 WHERE id=?1", a, a.ID)
}

func classifyAttempt(a Attempt, acceptance runtime.Acceptance, result string, isPrompt bool, receipt any, err error) Attempt {
	if receipt != nil {
		raw, encodeErr := json.Marshal(receipt)
		if encodeErr != nil {
			a.Status, a.ErrorCode, a.ObservationGap, a.PossibleDuplicate = "blocked_unknown", "receipt_encoding_failed", "receipt_not_retained", true
			return a
		}
		a.Receipt = raw
	}
	a.Status = "blocked"
	var op *runtime.OperationError
	var rpc *rpcwire.RPCError
	switch {
	case errors.Is(err, client.ErrWorkResponseUnconfirmed):
		a.Status, a.ErrorCode, a.ObservationGap, a.PossibleDuplicate = "transport_unknown", "transport_unknown", "massd_receipt_unconfirmed", true
	case errors.As(err, &op):
		a.ErrorCode, a.ErrorReason = op.Code, op.Reason
		switch {
		case op.Code == "outcome_unknown" || op.Reason == "input_outcome_unknown":
			a.Status, a.ObservationGap, a.PossibleDuplicate = "blocked_unknown", "native_input_outcome_unknown", true
		case op.Reason == "input_in_flight" || op.Reason == "session_resume_required" || op.Reason == "session_delivery_overloaded":
			a.Status = "pending"
		case op.Code == "unavailable" || op.Code == "resource_exhausted" || op.Code == "deadline_exceeded":
			if acceptance.Sent != nil && !*acceptance.Sent || acceptance.NativeAccepted != nil && !*acceptance.NativeAccepted {
				a.Status = "pending"
			} else {
				a.Status, a.ObservationGap, a.PossibleDuplicate = "blocked_unknown", "missing_submission_evidence", true
			}
		}
	case errors.As(err, &rpc):
		a.ErrorCode, a.ErrorReason = "massd_rpc_error", strconv.FormatInt(rpc.Code, 10)
		if rpc.Code == -32603 {
			a.Status, a.ObservationGap, a.PossibleDuplicate = "blocked_unknown", "massd_internal_outcome_unknown", true
		}
	case err != nil:
		a.Status, a.ErrorCode, a.ObservationGap, a.PossibleDuplicate = "transport_unknown", "transport_unknown", "massd_response_missing", true
	case result == "not_applied":
		a.ErrorCode = "not_applied"
	case acceptance.NativeAccepted != nil && !*acceptance.NativeAccepted:
		a.ErrorCode = "native_rejected"
	case acceptance.Sent != nil && *acceptance.Sent &&
		(!isPrompt || result == "applied_new_work" || result == "applied_current_work" || result == "applied_best_effort"):
		a.Status = "delivered"
	default:
		a.ErrorCode = "input_not_confirmed"
		a.ObservationGap = "missing_submission_evidence"
	}
	return a
}

func (s *Server) dispatchBatch(ctx context.Context, b Binding, spec resource.SessionSpec, a Attempt, batch Batch, wire []byte) (Attempt, *workerWatch) {
	c, err := s.massClient()
	if err != nil {
		a.Status, a.ErrorCode = "blocked", "massd_configuration_invalid"
		return a, nil
	}
	defer func() { _ = c.Close() }()
	if err = c.Initialize(ctx, nil, nil); err != nil {
		a.Status, a.ErrorCode = "pending", "massd_connect_unavailable"
		var op *runtime.OperationError
		var handshake *client.WebSocketHandshakeError
		if errors.As(err, &handshake) && handshake.StatusCode >= http.StatusBadRequest && handshake.StatusCode < http.StatusInternalServerError && handshake.StatusCode != http.StatusTooManyRequests {
			a.Status, a.ErrorCode = "blocked", "massd_handshake_rejected"
		} else if errors.As(err, &op) && (op.Code == "unauthenticated" || op.Code == "permission_denied" || op.Code == "unsupported" || op.Code == "invalid_argument") {
			a.Status, a.ErrorCode, a.ErrorReason = "blocked", op.Code, op.Reason
		}
		return a, nil
	}
	ref := resource.SessionRef{SessionID: b.SessionID}
	var session resource.Session
	err = c.GetSession(ctx, ref, &session)
	switch {
	case errorCode(err) == "not_found" || err == nil && !bindingMatches(b, spec, session):
		a.Status, a.ErrorCode = "blocked", "session_configuration_conflict"
		return a, nil
	case err != nil:
		a.Status, a.ErrorCode = "pending", "massd_session_read_unavailable"
		return a, nil
	case session.Status.State == "offline" || session.Status.State == "closing":
		a.Status, a.ErrorCode, a.ErrorReason = "pending", "conflict", "session_resume_required"
		return a, nil
	}
	var watch *workerWatch
	switch b.Role {
	case "worker":
		if batch.Method == "start_or_steer" {
			watch = s.prepareWorkerWatch(ctx, b, a)
		}
		commitGate("worker_before_rpc")
	case "topic":
		commitGate("topic_before_rpc")
	}
	armed, err := s.store.admitDispatch(ctx, b, batch, watch)
	if err != nil {
		watch.close()
		a.Status, a.ErrorCode = "pending", "dispatch_no_longer_admitted"
		return a, nil
	}
	if watch != nil {
		if armed {
			s.stopWorkerWatch(b.ID)
		} else {
			watch.close()
			watch = nil
		}
	}
	input := runtime.Input{Content: []events.ContentBlock{events.Text(string(wire))}}
	if batch.Method == "inject" {
		var receipt runtime.InjectReceipt
		var observed any
		err := c.Inject(ctx, ref, input, &receipt)
		if err == nil || receipt.Acceptance != (runtime.Acceptance{}) {
			observed = receipt
		}
		return classifyAttempt(a, receipt.Acceptance, "", false, observed, err), nil
	}
	var receipt runtime.InputReceipt
	err = c.SendPrompt(ctx, ref, input, &receipt)
	var observed any
	if err == nil {
		observed = receipt
	}
	a = classifyAttempt(a, receipt.Acceptance, string(receipt.Result), true, observed, err)
	if watch != nil {
		watch.recordReceipt(a, receipt)
	}
	return a, watch
}

func (s *Store) admitDispatch(ctx context.Context, b Binding, batch Batch, watch *workerWatch) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkBinding(ctx, tx, b); err != nil {
		return false, err
	}
	for _, id := range batch.EventIDs {
		var raw []byte
		var item queuedDelivery
		err := tx.QueryRowContext(ctx, "SELECT body,invalidated FROM events WHERE id=?", id).Scan(&raw, &item.invalidated)
		if err != nil || json.Unmarshal(raw, &item.event) != nil {
			return false, storageError()
		}
		current, err := eventCurrent(ctx, tx, b, item)
		if err != nil {
			return false, err
		}
		if !current {
			return false, problem("stale_binding", "batch contains an invalidated event")
		}
	}
	if watch == nil {
		return false, nil
	}
	armed, err := beginWorkerObservation(ctx, tx, b, watch.evidence)
	if err != nil {
		return false, err
	}
	return armed, tx.Commit()
}
