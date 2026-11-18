package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

const echoSchema = `
CREATE TABLE outbound_message_ids (
 source TEXT NOT NULL, conversation_id TEXT NOT NULL, native_id TEXT NOT NULL,
 outbox_id TEXT NOT NULL REFERENCES outbox(id), attempt_id TEXT NOT NULL REFERENCES outbox_attempts(id),
 PRIMARY KEY(source,conversation_id,native_id)
);
INSERT OR IGNORE INTO outbound_message_ids(source,conversation_id,native_id,outbox_id,attempt_id)
 SELECT o.source,json_extract(r.value,'$.conversationId'),json_extract(r.value,'$.messageId'),o.id,a.id
 FROM outbox o JOIN outbox_attempts a ON a.outbox_id=o.id, json_each(a.body,'$.results') r
 WHERE json_extract(o.body,'$.channel.type')='dws'
 AND json_extract(r.value,'$.outcome')='sent' AND json_extract(r.value,'$.conversationId')<>''
 AND json_extract(r.value,'$.messageId')<>'' ORDER BY a.seq,r.key;
CREATE TABLE message_echo (
 message_id TEXT PRIMARY KEY REFERENCES messages(id), state TEXT NOT NULL, deadline TEXT NOT NULL
);
CREATE INDEX message_echo_deadline ON message_echo(deadline,message_id) WHERE state='held';
CREATE TABLE message_echo_candidates (
 message_id TEXT NOT NULL REFERENCES message_echo(message_id), attempt_id TEXT NOT NULL REFERENCES outbox_attempts(id),
 PRIMARY KEY(message_id,attempt_id)
);
CREATE INDEX echo_by_attempt ON message_echo_candidates(attempt_id,message_id);
`

const echoWait = 60 * time.Second

type EchoReference struct {
	OutboxID  string `json:"outboxId"`
	AttemptID string `json:"attemptId"`
}

type MessageEcho struct {
	Status         string          `json:"status"`
	Deadline       string          `json:"deadline,omitempty"`
	ResolvedAt     string          `json:"resolvedAt,omitempty"`
	Match          *EchoReference  `json:"match,omitempty"`
	Candidates     []EchoReference `json:"candidates,omitempty"`
	RoutingEventID string          `json:"routingEventId,omitempty"`
	ReleaseID      string          `json:"releaseOperationId,omitempty"`
	ReleaseReason  string          `json:"releaseReason,omitempty"`
}

func classifyEcho(ctx context.Context, tx *sql.Tx, m Message, selfSent bool) (*MessageEcho, error) {
	if m.Channel == nil || m.Channel.Type != "dws" {
		return nil, nil
	}
	ref, err := findEchoIdentity(ctx, tx, m)
	if err != nil {
		return nil, err
	}
	if ref != nil {
		return &MessageEcho{Status: "outbound_echo", Match: ref, ResolvedAt: outboxNow().Format(utcTimestamp)}, nil
	}
	if !selfSent {
		return nil, nil
	}
	at := outboxNow()
	rows, err := tx.QueryContext(ctx, `SELECT o.id,a.id FROM messages target
 JOIN outbox o ON o.message_id=target.id JOIN outbox_attempts a ON a.outbox_id=o.id
 WHERE target.thread_key=? AND o.source=?
 AND json_extract(o.body,'$.channel.type')='dws'
 AND json_extract(o.body,'$.channel.conversationId')=?
 AND (a.state IN ('started','task') OR (a.state='unknown' AND
   (json_extract(a.body,'$.taskId') IS NOT NULL OR json_extract(a.body,'$.retryAt')>?)))
 ORDER BY a.seq`, m.ThreadKey, m.Source, m.Channel.ConversationID, at.Format(utcTimestamp))
	if err != nil {
		return nil, storageError()
	}
	defer func() { _ = rows.Close() }()
	echo := &MessageEcho{Status: "held", Deadline: at.Add(echoWait).Format(utcTimestamp)}
	for rows.Next() {
		var candidate EchoReference
		if err = rows.Scan(&candidate.OutboxID, &candidate.AttemptID); err != nil {
			return nil, storageError()
		}
		echo.Candidates = append(echo.Candidates, candidate)
	}
	if rows.Err() != nil {
		return nil, storageError()
	}
	if len(echo.Candidates) == 0 {
		return nil, nil
	}
	return echo, nil
}

func findEchoIdentity(ctx context.Context, tx *sql.Tx, m Message) (*EchoReference, error) {
	var ref EchoReference
	err := tx.QueryRowContext(ctx, `SELECT outbox_id,attempt_id FROM outbound_message_ids
 WHERE source=? AND conversation_id=? AND native_id=?`, m.Source, m.Channel.ConversationID, m.SourceMessageID).Scan(&ref.OutboxID, &ref.AttemptID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, storageError()
	default:
		return &ref, nil
	}
}

func recordEchoAdmission(ctx context.Context, tx *sql.Tx, m Message, e Event) error {
	if _, err := tx.ExecContext(ctx, "INSERT INTO message_echo(message_id,state,deadline) VALUES(?,?,?)", m.ID, m.Echo.Status, m.Echo.Deadline); err != nil {
		return storageError()
	}
	for _, candidate := range m.Echo.Candidates {
		if _, err := tx.ExecContext(ctx, "INSERT INTO message_echo_candidates(message_id,attempt_id) VALUES(?,?)", m.ID, candidate.AttemptID); err != nil {
			return storageError()
		}
	}
	// The receipt points at this audit event. It has no role or delivery.
	e.Type, e.Status = "message."+m.Echo.Status, m.Echo.Status
	e.TargetRole, e.Intent, e.RouteRound = "", "", 0
	return putJSON(ctx, tx, "INSERT INTO events(id,message_id,at,body) VALUES(?,?,?,?)", e, e.ID, e.MessageID, e.At)
}

func saveEcho(ctx context.Context, tx *sql.Tx, m Message) error {
	if _, err := tx.ExecContext(ctx, "UPDATE message_echo SET state=? WHERE message_id=?", m.Echo.Status, m.ID); err != nil {
		return storageError()
	}
	return putJSON(ctx, tx, "UPDATE messages SET body=?2 WHERE id=?1", m, m.ID)
}

func routeEchoInput(ctx context.Context, tx *sql.Tx, m Message, at string) error {
	m.Echo.RoutingEventID = newID("evt")
	e := Event{
		ID: m.Echo.RoutingEventID, Type: "message.received", At: at, MessageID: m.ID,
		ThreadKey: m.ThreadKey, TargetRole: "router", Intent: "act", RouteRound: 1,
	}
	if err := acceptRoute(ctx, tx, &e); err != nil {
		return err
	}
	return saveEcho(ctx, tx, m)
}

func reconcileEchoResult(ctx context.Context, tx *sql.Tx, item OutboxItem, attemptID string, result OutboxResult) error {
	if item.Channel.Type != "dws" {
		return nil
	}
	exact := result.Outcome == "sent" && result.ConversationID != "" && result.MessageID != ""
	if exact {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO outbound_message_ids(source,conversation_id,native_id,outbox_id,attempt_id)
 VALUES(?,?,?,?,?)`, item.Source, result.ConversationID, result.MessageID, item.ID, attemptID); err != nil {
			return storageError()
		}
		var m Message
		err := readJSON(ctx, tx, `SELECT m.body FROM messages m JOIN message_echo e ON m.id=e.message_id
 WHERE m.source=? AND m.thread_key=? AND m.native_id=? AND e.state IN ('held','echo_unknown','released','cleared')`,
			&m, item.Source, ThreadKey(ThreadRef{Source: item.Source, ID: result.ConversationID}), result.MessageID)
		var p *Error
		if err == nil && m.Echo.Match == nil {
			m.Echo.Match = &EchoReference{OutboxID: item.ID, AttemptID: attemptID}
			if m.Echo.RoutingEventID == "" {
				m.Echo.Status, m.Echo.ResolvedAt = "outbound_echo", result.At
			}
			if err := saveEcho(ctx, tx, m); err != nil {
				return err
			}
		} else if err != nil && (!errors.As(err, &p) || p.Code != "not_found") {
			return err
		}
	}
	if !exact && result.Outcome != "retryable_failure" && result.Outcome != "permanent_failure" {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT m.body FROM message_echo_candidates c
 JOIN message_echo e ON e.message_id=c.message_id JOIN messages m ON m.id=e.message_id
 WHERE c.attempt_id=? AND e.state='held' AND e.deadline>?
 AND NOT EXISTS (
  SELECT 1 FROM message_echo_candidates x JOIN outbox_attempts a ON a.id=x.attempt_id
  WHERE x.message_id=e.message_id AND a.state<>'failed' AND NOT EXISTS (
   SELECT 1 FROM json_each(a.body,'$.results') r WHERE json_extract(r.value,'$.outcome')='sent'
   AND json_extract(r.value,'$.conversationId')<>'' AND json_extract(r.value,'$.messageId')<>''
  )
 ) ORDER BY m.seq`, attemptID, result.At)
	if err != nil {
		return storageError()
	}
	var messages []Message
	for rows.Next() {
		var m Message
		var body []byte
		if err := rows.Scan(&body); err != nil || json.Unmarshal(body, &m) != nil {
			_ = rows.Close()
			return storageError()
		}
		messages = append(messages, m)
	}
	if err = rows.Close(); err != nil || rows.Err() != nil {
		return storageError()
	}
	for _, m := range messages {
		m.Echo.Status, m.Echo.ResolvedAt = "cleared", result.At
		if err := routeEchoInput(ctx, tx, m, result.At); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) expireEchoHolds(ctx context.Context) error {
	at := outboxNow().Format(utcTimestamp)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError()
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `UPDATE messages
 SET body=json_set(body,'$.echo.status','echo_unknown','$.echo.resolvedAt',?)
 WHERE id IN (SELECT message_id FROM message_echo WHERE state='held' AND deadline<=?)`, at, at); err != nil {
		return storageError()
	}
	if _, err = tx.ExecContext(ctx, "UPDATE message_echo SET state='echo_unknown' WHERE state='held' AND deadline<=?", at); err != nil {
		return storageError()
	}
	if err = tx.Commit(); err != nil {
		return storageError()
	}
	return nil
}

type EchoReleaseRequest struct {
	MessageID   string `json:"messageId"`
	OperationID string `json:"operationId"`
	Reason      string `json:"reason"`
}

func (s *Server) releaseEcho(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeOperator(w, r) {
		return
	}
	var req EchoReleaseRequest
	body := http.MaxBytesReader(w, r.Body, 16<<10)
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	var extra json.RawMessage
	if err := dec.Decode(&req); err != nil || dec.Decode(&extra) != io.EOF ||
		!instanceIDFormat.MatchString(req.OperationID) || strings.TrimSpace(req.MessageID) == "" || strings.TrimSpace(req.Reason) == "" {
		s.fail(w, problem("invalid_argument", "messageId, operationId and a release reason are required"))
		return
	}
	m, err := s.store.releaseEcho(r.Context(), req)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.write(w, http.StatusOK, m.Echo)
}

func (s *Store) releaseEcho(ctx context.Context, req EchoReleaseRequest) (Message, error) {
	var m Message
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return m, storageError()
	}
	defer func() { _ = tx.Rollback() }()
	if err := readJSON(ctx, tx, "SELECT body FROM messages WHERE id=?", &m, req.MessageID); err != nil {
		return m, err
	}
	if m.Echo != nil && m.Echo.Status == "released" && m.Echo.ReleaseID == req.OperationID && m.Echo.ReleaseReason == req.Reason {
		return m, nil
	}
	if m.Echo == nil || m.Echo.Status != "echo_unknown" {
		return m, problem("failed_precondition", "only echo_unknown Messages can be released")
	}
	at := outboxNow().Format(utcTimestamp)
	m.Echo.Status, m.Echo.ResolvedAt, m.Echo.ReleaseID, m.Echo.ReleaseReason = "released", at, req.OperationID, req.Reason
	if err := routeEchoInput(ctx, tx, m, at); err != nil {
		return m, err
	}
	if err = tx.Commit(); err != nil {
		return m, storageError()
	}
	return m, nil
}
