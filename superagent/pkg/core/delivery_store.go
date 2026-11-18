package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

const deliverySchema = `
CREATE TABLE event_queue (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE,
 message_id TEXT REFERENCES messages(id), at TEXT NOT NULL, body BLOB NOT NULL,
 invalidated INTEGER NOT NULL DEFAULT 0
);
INSERT INTO event_queue(seq,id,message_id,at,body)
 SELECT seq,id,message_id,at,json_set(body,'$.contentRef',json_object('collection','messages','id',message_id)) FROM events;
CREATE TABLE deliveries (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, created_at TEXT NOT NULL,
 event_id TEXT NOT NULL REFERENCES event_queue(id), binding_id TEXT NOT NULL REFERENCES bindings(id),
 status TEXT NOT NULL, next_attempt_at TEXT NOT NULL DEFAULT '', batch_id TEXT NOT NULL DEFAULT '',
 ack_at TEXT NOT NULL DEFAULT '', body BLOB NOT NULL, UNIQUE(event_id,binding_id)
);
CREATE INDEX deliveries_pending ON deliveries(binding_id,status,event_id);
CREATE INDEX deliveries_event ON deliveries(event_id,seq DESC);
INSERT INTO deliveries(id,created_at,event_id,binding_id,status,body)
 SELECT 'legacy_'||e.id,e.at,e.id,e.binding_id,
        CASE WHEN e.status='dispatching' THEN 'transport_unknown' ELSE e.status END,
        json_object('id','legacy_'||e.id,'createdAt',e.at,'eventId',e.id,'bindingId',e.binding_id,
          'sessionId',json_extract(b.body,'$.sessionId'),'sessionUid',json_extract(b.body,'$.sessionUid'),
          'generation',b.generation,'status',CASE WHEN e.status='dispatching' THEN 'transport_unknown' ELSE e.status END,
          'possibleDuplicate',json(CASE WHEN e.status='dispatching' THEN 'true' ELSE 'false' END),
          'lastError',json_extract(e.body,'$.lastError'),'receipt',json_extract(e.body,'$.receipt'))
 FROM events e JOIN bindings b ON b.id=e.binding_id;
DROP TABLE events;
ALTER TABLE event_queue RENAME TO events;
DROP INDEX sole_router;
CREATE UNIQUE INDEX bindings_current ON bindings(role,owner_id) WHERE state<>'revoked';
CREATE TABLE batches (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, created_at TEXT NOT NULL,
 binding_id TEXT NOT NULL REFERENCES bindings(id), wire BLOB NOT NULL, body BLOB NOT NULL
);
CREATE TABLE attempts (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, created_at TEXT NOT NULL,
 batch_id TEXT NOT NULL REFERENCES batches(id), body BLOB NOT NULL
);
CREATE TABLE recoveries (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, created_at TEXT NOT NULL,
 request BLOB NOT NULL, body BLOB NOT NULL
);
`

type ContentRef struct {
	Collection string `json:"collection"`
	ID         string `json:"id"`
}

type Delivery struct {
	Receipt           json.RawMessage `json:"receipt,omitempty"`
	ID                string          `json:"id"`
	CreatedAt         string          `json:"createdAt"`
	EventID           string          `json:"eventId"`
	BindingID         string          `json:"bindingId"`
	SessionID         string          `json:"sessionId,omitempty"`
	SessionUID        string          `json:"sessionUid,omitempty"`
	BatchID           string          `json:"batchId,omitempty"`
	LastError         string          `json:"lastError,omitempty"`
	NextAttemptAt     string          `json:"nextAttemptAt,omitempty"`
	FirstFailureAt    string          `json:"firstFailureAt,omitempty"`
	ACKAt             string          `json:"ackAt,omitempty"`
	ObservationGap    string          `json:"observationGap,omitempty"`
	Status            string          `json:"status"`
	Generation        int             `json:"generation"`
	Attempts          int             `json:"attempts"`
	Failures          int             `json:"failures"`
	PossibleDuplicate bool            `json:"possibleDuplicate"`
	NeedsAttention    bool            `json:"needsAttention"`
}

type Batch struct {
	Receipt       json.RawMessage `json:"receipt,omitempty"`
	ID            string          `json:"id"`
	CreatedAt     string          `json:"createdAt"`
	BindingID     string          `json:"bindingId"`
	SessionID     string          `json:"sessionId"`
	SessionUID    string          `json:"sessionUid"`
	Method        string          `json:"method"`
	Status        string          `json:"status"`
	LastAttemptID string          `json:"lastAttemptId"`
	EventIDs      []string        `json:"eventIds"`
	Bytes         int             `json:"bytes"`
	Attempts      int             `json:"attempts"`
}

type Attempt struct {
	Receipt           json.RawMessage `json:"receipt,omitempty"`
	ID                string          `json:"id"`
	CreatedAt         string          `json:"createdAt"`
	FinishedAt        string          `json:"finishedAt,omitempty"`
	BatchID           string          `json:"batchId"`
	Status            string          `json:"status"`
	ErrorCode         string          `json:"errorCode,omitempty"`
	ErrorReason       string          `json:"errorReason,omitempty"`
	ObservationGap    string          `json:"observationGap,omitempty"`
	PossibleDuplicate bool            `json:"possibleDuplicate"`
}

const deliveryAttention = `status IN ('blocked','blocked_unknown') OR json_extract(body,'$.needsAttention')
 OR (status IN ('pending','transport_unknown') AND julianday(json_extract(body,'$.firstFailureAt'))<=julianday('now','-10 minutes'))`

func eventProjection() string {
	return `json_patch(json_set(events.body,'$.invalidated',json(CASE WHEN events.invalidated THEN 'true' ELSE 'false' END)),COALESCE((SELECT json_object('status',d.status,'bindingId',d.binding_id,
 'deliveryId',d.id,'batchId',NULLIF(d.batch_id,''),'receipt',json_extract(d.body,'$.receipt'),
 'lastError',json_extract(d.body,'$.lastError'),'ackAt',NULLIF(d.ack_at,''),
 'nextAttemptAt',NULLIF(d.next_attempt_at,''),'needsAttention',json(CASE WHEN ` + deliveryAttention + ` THEN 'true' ELSE 'false' END),
 'observationGap',json_extract(d.body,'$.observationGap'))
 FROM deliveries d WHERE d.event_id=events.id ORDER BY d.seq DESC LIMIT 1),'{}'))`
}

// EnqueueEvent appends an event in the caller's Core business transaction.
// ID and At are assigned by Core; callers supply a stable ID for any replayed operation.
// A positive TargetGeneration fences a command; zero allows an undelivered fact to move bindings.
func EnqueueEvent(ctx context.Context, tx *sql.Tx, e Event) error {
	if e.ID == "" || len(e.ID) > 128 || e.Type == "" || len(e.Type) > 128 || len(e.TargetID) > 512 ||
		(e.Intent != "act" && e.Intent != "context") || e.TargetGeneration < 0 || e.Revision < 0 ||
		(e.TopicGeneration != nil && (e.TargetRole != "topic" || *e.TopicGeneration < 0)) ||
		(e.TargetRole != "router" && e.TargetRole != "topic" && e.TargetRole != "worker") || (e.TargetRole == "router") != (e.TargetID == "") {
		return problem("invalid_argument", "invalid event identity, target or intent")
	}
	if e.At == "" {
		e.At = now()
	}
	e.Status, e.Receipt, e.LastError = "pending", nil, ""
	if e.MessageID != "" {
		e.ContentRef = &ContentRef{Collection: "messages", ID: e.MessageID}
	} else {
		e.ContentRef = &ContentRef{Collection: "events", ID: e.ID}
	}
	var binding Binding
	err := readJSON(ctx, tx, "SELECT body FROM bindings WHERE role=? AND owner_id=? AND state<>'revoked'", &binding, e.TargetRole, e.TargetID)
	var p *Error
	if err != nil && (!errors.As(err, &p) || p.Code != "not_found") {
		return err
	}
	if e.BindingID != "" && e.BindingID != binding.ID {
		return problem("stale_binding", "event target binding is no longer current")
	}
	e.BindingID = binding.ID
	if err = putJSON(ctx, tx, "INSERT INTO events(id,message_id,at,body) VALUES(?,NULLIF(?,''),?,?)", e, e.ID, e.MessageID, e.At); err != nil || binding.ID == "" {
		return err
	}
	return insertDelivery(ctx, tx, newDelivery(e, binding, e.At, false))
}

// SupersedeEvent fences future dispatches in the caller's lifecycle transaction.
// A dispatch already claimed may still settle and keeps its real outcome.
func SupersedeEvent(ctx context.Context, tx *sql.Tx, eventID string) error {
	return supersedeEvents(ctx, tx, "SELECT ?", eventID)
}

func supersedeEvents(ctx context.Context, tx *sql.Tx, eventIDs string, args ...any) error {
	if _, err := tx.ExecContext(ctx, "UPDATE events SET invalidated=1 WHERE id IN ("+eventIDs+")", args...); err != nil {
		return storageError()
	}
	_, err := tx.ExecContext(ctx, `UPDATE deliveries SET status='superseded',next_attempt_at='',
 body=json_set(body,'$.status','superseded','$.nextAttemptAt','')
 WHERE event_id IN (`+eventIDs+`) AND status NOT IN ('delivered','dispatching','superseded')`, args...)
	return err
}

func newDelivery(e Event, b Binding, at string, possibleDuplicate bool) Delivery {
	return Delivery{ID: newID("delivery"), EventID: e.ID, BindingID: b.ID, Generation: b.Generation, CreatedAt: at, Status: "pending", PossibleDuplicate: possibleDuplicate}
}

func insertDelivery(ctx context.Context, tx *sql.Tx, d Delivery) error {
	return putJSON(ctx, tx, "INSERT INTO deliveries(id,created_at,event_id,binding_id,status,body) VALUES(?,?,?,?,?,?)", d, d.ID, d.CreatedAt, d.EventID, d.BindingID, d.Status)
}

func saveDelivery(ctx context.Context, tx *sql.Tx, d Delivery) error {
	// ACK may have arrived while the native call was in flight.
	if err := tx.QueryRowContext(ctx, "SELECT ack_at FROM deliveries WHERE id=?", d.ID).Scan(&d.ACKAt); err != nil {
		return storageError()
	}
	return putJSON(ctx, tx, "UPDATE deliveries SET status=?2,next_attempt_at=?3,batch_id=?4,body=?5 WHERE id=?1", d, d.ID, d.Status, d.NextAttemptAt, d.BatchID)
}

func saveBatch(ctx context.Context, tx *sql.Tx, b Batch) error {
	return putJSON(ctx, tx, "UPDATE batches SET body=?2 WHERE id=?1", b, b.ID)
}

type Acknowledgement struct {
	EventID   string `json:"eventId"`
	BindingID string `json:"bindingId"`
	ACKAt     string `json:"ackAt"`
}

func (s *Store) ackEvent(ctx context.Context, b Binding, eventID string) (Acknowledgement, error) {
	var d Delivery
	var ack Acknowledgement
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ack, storageError()
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkBinding(ctx, tx, b); err != nil {
		return ack, err
	}
	if err := readJSON(ctx, tx, "SELECT body FROM deliveries WHERE event_id=? AND binding_id=?", &d, eventID, b.ID); err != nil {
		return ack, err
	}
	if d.Attempts == 0 && d.Status != "delivered" {
		return ack, problem("failed_precondition", "event has not been dispatched to this binding")
	}
	if d.ACKAt == "" {
		d.ACKAt = now()
		if err := putJSON(ctx, tx, "UPDATE deliveries SET ack_at=?2,body=?3 WHERE id=?1", d, d.ID, d.ACKAt); err != nil {
			return ack, err
		}
	}
	return Acknowledgement{EventID: d.EventID, BindingID: d.BindingID, ACKAt: d.ACKAt}, tx.Commit()
}

func attachUnboundEvents(ctx context.Context, tx *sql.Tx, b Binding) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO deliveries(id,created_at,event_id,binding_id,status,body)
 SELECT 'initial_'||e.id,e.at,e.id,?,'pending',json_object('id','initial_'||e.id,'createdAt',e.at,
 'eventId',e.id,'bindingId',?,'generation',?,'status','pending')
 FROM events e WHERE NOT e.invalidated AND json_extract(e.body,'$.targetRole')=? AND COALESCE(json_extract(e.body,'$.targetId'),'')=?
 AND NOT EXISTS (SELECT 1 FROM deliveries d WHERE d.event_id=e.id)`, b.ID, b.ID, b.Generation, b.Role, b.OwnerID)
	return err
}

func recoverDispatches(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "SELECT body FROM attempts WHERE json_extract(body,'$.status')='dispatching'")
	if err != nil {
		return storageError()
	}
	var list []Attempt
	for rows.Next() {
		var raw []byte
		var a Attempt
		if rows.Scan(&raw) != nil || json.Unmarshal(raw, &a) != nil {
			_ = rows.Close()
			return storageError()
		}
		list = append(list, a)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return storageError()
	}
	for _, a := range list {
		a.Status, a.ObservationGap, a.ErrorCode, a.PossibleDuplicate = "transport_unknown", "core_stopped_before_settlement", "transport_unknown", true
		if err := settleBatch(ctx, tx, a); err != nil {
			return err
		}
	}
	// Older Core versions did not retain the batch that was in flight.
	_, err = tx.ExecContext(ctx, `UPDATE deliveries SET next_attempt_at=?1,
 body=json_set(body,'$.nextAttemptAt',?1,'$.observationGap','legacy_core_stopped_before_settlement')
 WHERE status='transport_unknown' AND next_attempt_at=''`, after(time.Second))
	return err
}
