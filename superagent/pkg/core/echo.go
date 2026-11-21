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

const echoWait = 60 * time.Second

type EchoReference struct {
	OutboxID string `json:"outboxId"`
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
	at := outboxNow()
	if ref != nil {
		return &MessageEcho{Status: "outbound_echo", Match: ref, ResolvedAt: at.Format(utcTimestamp)}, nil
	}
	if !selfSent {
		return nil, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT o.id FROM outbox o
 JOIN trigger_messages origin ON origin.id=o.message_id
 WHERE origin.thread_key=? AND origin.source=? AND json_extract(origin.body,'$.accountId')=?
 AND json_extract(origin.body,'$.channel.type')='dws' AND json_extract(origin.body,'$.channel.conversationId')=?
 AND (o.status='pending' OR (o.status='failed' AND json_extract(o.receipt,'$.uncertain')
  AND json_extract(o.receipt,'$.retryAt')>?))
 ORDER BY o.rowid`, m.ThreadKey, m.Source, m.AccountID, m.Channel.ConversationID, at.Format(utcTimestamp))
	if err != nil {
		return nil, storageError()
	}
	defer func() { _ = rows.Close() }()
	echo := &MessageEcho{Status: "held", Deadline: at.Add(echoWait).Format(utcTimestamp)}
	for rows.Next() {
		var candidate EchoReference
		if err = rows.Scan(&candidate.OutboxID); err != nil {
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
	err := tx.QueryRowContext(ctx, `SELECT o.id FROM outbox o
 JOIN trigger_messages origin ON origin.id=o.message_id
 WHERE o.status='sent' AND origin.source=? AND json_extract(origin.body,'$.accountId')=?
 AND json_extract(origin.body,'$.channel.type')='dws'
 AND json_extract(o.receipt,'$.conversationId')=? AND json_extract(o.receipt,'$.messageId')=?
 ORDER BY o.rowid LIMIT 1`, m.Source, m.AccountID, m.Channel.ConversationID, m.SourceMessageID).Scan(&ref.OutboxID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, storageError()
	default:
		return &ref, nil
	}
}

func saveEcho(ctx context.Context, tx *sql.Tx, m Message) error {
	return putJSON(ctx, tx, "UPDATE trigger_messages SET body=?2 WHERE id=?1", m, m.ID)
}

func routeEchoInput(ctx context.Context, tx *sql.Tx, m Message, at string) error {
	m.Echo.RoutingEventID = newID("evt")
	if err := acceptRoute(ctx, tx, m, m.Echo.RoutingEventID, at); err != nil {
		return err
	}
	return saveEcho(ctx, tx, m)
}

func reconcileEchoResult(ctx context.Context, tx *sql.Tx, item OutboxItem, source Source, at time.Time) error {
	receipt := receiptValue(item.OutboxState)
	stamp := at.Format(utcTimestamp)
	exact := item.Status == "sent" && receipt.ConversationID != "" && receipt.MessageID != ""
	if exact {
		var m Message
		err := readJSON(ctx, tx, `SELECT body FROM trigger_messages
 WHERE source=? AND native_id=? AND json_extract(body,'$.accountId')=?
 AND json_extract(body,'$.channel.type')='dws' AND json_extract(body,'$.channel.conversationId')=?
 AND json_extract(body,'$.echo.status') IN ('held','echo_unknown','released','cleared')`,
			&m, source.Source, receipt.MessageID, source.AccountID, receipt.ConversationID)
		var p *Error
		if err == nil && m.Echo.Match == nil {
			m.Echo.Match = &EchoReference{OutboxID: item.ID}
			if m.Echo.RoutingEventID == "" {
				m.Echo.Status, m.Echo.ResolvedAt = "outbound_echo", stamp
			}
			if err := saveEcho(ctx, tx, m); err != nil {
				return err
			}
		} else if err != nil && (!errors.As(err, &p) || p.Code != "not_found") {
			return err
		}
	}
	if !exact && (item.Status != "failed" || receipt.Uncertain) {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT m.body FROM trigger_messages m
 WHERE json_extract(m.body,'$.echo.status')='held' AND json_extract(m.body,'$.echo.deadline')>?
 AND EXISTS(SELECT 1 FROM json_each(m.body,'$.echo.candidates') c WHERE json_extract(c.value,'$.outboxId')=?)
 AND NOT EXISTS(
  SELECT 1 FROM json_each(m.body,'$.echo.candidates') c JOIN outbox o ON o.id=json_extract(c.value,'$.outboxId')
  WHERE o.status='pending'
   OR json_extract(o.receipt,'$.uncertain')
   OR (o.status='sent' AND COALESCE(json_extract(o.receipt,'$.messageId'),'')='')
 ) ORDER BY m.seq`, stamp, item.ID)
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
		m.Echo.Status, m.Echo.ResolvedAt = "cleared", stamp
		if err := routeEchoInput(ctx, tx, m, stamp); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) expireEchoHolds(ctx context.Context) error {
	at := outboxNow().Format(utcTimestamp)
	_, err := s.db.ExecContext(ctx, `UPDATE trigger_messages
 SET body=json_set(body,'$.echo.status','echo_unknown','$.echo.resolvedAt',?)
 WHERE json_extract(body,'$.echo.status')='held' AND json_extract(body,'$.echo.deadline')<=?`, at, at)
	if err != nil {
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
	if err := readJSON(ctx, tx, "SELECT body FROM trigger_messages WHERE id=?", &m, req.MessageID); err != nil {
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
