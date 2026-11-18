package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
)

const outboxSchema = `
ALTER TABLE messages ADD COLUMN reply_instance TEXT NOT NULL DEFAULT '';
CREATE TABLE outbox (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE,
 message_id TEXT NOT NULL REFERENCES messages(id), source TEXT NOT NULL REFERENCES source_bindings(source),
 instance_id TEXT NOT NULL, created_at TEXT NOT NULL, body BLOB NOT NULL
);
CREATE INDEX outbox_instance ON outbox(source,instance_id,created_at,id);
CREATE INDEX outbox_message ON outbox(message_id);
CREATE TABLE outbox_attempts (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE,
 outbox_id TEXT NOT NULL REFERENCES outbox(id), state TEXT NOT NULL, body BLOB NOT NULL
);
CREATE INDEX outbox_attempt_order ON outbox_attempts(outbox_id,seq);
CREATE INDEX outbox_task_recovery ON outbox_attempts(outbox_id,seq) WHERE state='task';
CREATE TABLE outbox_claims (
 id TEXT PRIMARY KEY, outbox_id TEXT NOT NULL REFERENCES outbox(id),
 attempt_id TEXT NOT NULL REFERENCES outbox_attempts(id), generation INTEGER NOT NULL,
 mode TEXT NOT NULL CHECK(mode IN ('send','recover')), claimed_at TEXT NOT NULL,
 expires_at TEXT NOT NULL, released_at TEXT, UNIQUE(outbox_id,generation)
);
CREATE UNIQUE INDEX outbox_active_claim ON outbox_claims(outbox_id) WHERE released_at IS NULL;
CREATE INDEX outbox_expiring_claim ON outbox_claims(expires_at,outbox_id) WHERE released_at IS NULL;
CREATE INDEX outbox_claim_history ON outbox_claims(attempt_id,generation);
`

// Claim IDs authorize commands and must stay out of read-only projections.
const outboxDetailProjection = `json_set(outbox.body,'$.attempts',json((
 SELECT json_group_array(json(attempt)) FROM (
  SELECT json_set(a.body,'$.claims',json((
   SELECT json_group_array(json(claim)) FROM (
    SELECT json_patch(json_object('generation',generation,'mode',mode,'claimedAt',claimed_at,'leaseExpiresAt',expires_at),
     json_object('releasedAt',released_at)) AS claim
    FROM outbox_claims WHERE attempt_id=a.id ORDER BY generation
   )
  ))) AS attempt
  FROM outbox_attempts a WHERE a.outbox_id=outbox.id ORDER BY a.seq
 )
)))`

const (
	outboxLease     = 60 * time.Second
	outboxUnknown   = 60 * time.Second
	outboxMaxErrors = 10
)

// Earlier schema 11 binaries could mark a recorded task unknown after an
// unsuccessful poll. Its original task remains eligible for recovery.
const outboxRecoverableTask = `(a.state='task' OR
 (a.state='unknown' AND json_extract(a.body,'$.taskId') IS NOT NULL))`

type ReplyRequest struct {
	OperationID string   `json:"operationId"`
	Text        string   `json:"text"`
	MessageID   string   `json:"messageId,omitempty"`
	MessageIDs  []string `json:"messageIds,omitempty"`
}

type OutboxItem struct {
	ID                string  `json:"id"`
	MessageID         string  `json:"messageId"`
	Source            string  `json:"source"`
	AccountID         string  `json:"accountId"`
	InstanceID        string  `json:"instanceId"`
	Channel           Channel `json:"channel"`
	AuthorRole        string  `json:"authorRole"`
	AuthorID          string  `json:"authorId,omitempty"`
	AuthorBindingID   string  `json:"authorBindingId"`
	Text              string  `json:"text"`
	CreatedAt         string  `json:"createdAt"`
	Status            string  `json:"status"`
	CurrentAttempt    string  `json:"currentAttemptId,omitempty"`
	RetryAt           string  `json:"retryAt,omitempty"`
	ErrorCode         string  `json:"errorCode,omitempty"`
	PossibleDuplicate bool    `json:"possibleDuplicate,omitempty"`
	FailureEventID    string  `json:"failureEventId,omitempty"`
}

type OutboxAttempt struct {
	ID        string         `json:"id"`
	OutboxID  string         `json:"outboxId"`
	State     string         `json:"state"`
	CreatedAt string         `json:"createdAt"`
	StartedAt string         `json:"startedAt,omitempty"`
	TaskID    string         `json:"taskId,omitempty"`
	TaskAt    string         `json:"taskAt,omitempty"`
	ErrorCode string         `json:"errorCode,omitempty"`
	RetryAt   string         `json:"retryAt,omitempty"`
	Results   []OutboxResult `json:"results,omitempty"`
}

type OutboxResult struct {
	Outcome        string `json:"outcome"`
	ErrorCode      string `json:"errorCode,omitempty"`
	ConversationID string `json:"conversationId,omitempty"`
	MessageID      string `json:"messageId,omitempty"`
	At             string `json:"at"`
	Generation     int64  `json:"generation"`
}

func (s *Store) reply(ctx context.Context, b Binding, req ReplyRequest) (json.RawMessage, error) {
	if b.Role != "router" && b.Role != "topic" {
		return nil, problem("forbidden", "only Router and Topic can reply")
	}
	if strings.TrimSpace(req.Text) == "" {
		return nil, problem("invalid_argument", "reply text is required")
	}
	if len(req.MessageIDs) > 0 && (b.Role != "router" || req.MessageID != "") {
		return nil, problem("invalid_argument", "messageIds is only for Router replies without an explicit messageId")
	}
	if b.Role == "router" && req.MessageID == "" && len(req.MessageIDs) == 0 {
		return nil, problem("invalid_argument", "Router must provide messageId or the current routing messageIds")
	}
	if len(req.MessageIDs) > 0 {
		req.MessageIDs = slices.Clone(req.MessageIDs)
		slices.Sort(req.MessageIDs)
		for i, id := range req.MessageIDs {
			if strings.TrimSpace(id) == "" || (i > 0 && id == req.MessageIDs[i-1]) {
				return nil, problem("invalid_argument", "messageIds must contain distinct nonempty IDs")
			}
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, storageError()
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkBinding(ctx, tx, b); err != nil {
		return nil, err
	}
	replay, fp, err := roleReplay(ctx, tx, b.ID, "message_reply", req.OperationID, req)
	if err != nil || replay != nil {
		return replay, err
	}
	message, instance, err := replyTarget(ctx, tx, b, req)
	if err != nil {
		return nil, err
	}
	if !replyAddressPresent(message.Channel) {
		return nil, problem("failed_precondition", "selected Message has no reply address")
	}
	instance, err = s.replyInstance(message, instance)
	if err != nil {
		return nil, err
	}
	item := OutboxItem{
		ID: newID("outbox"), MessageID: message.ID, Channel: *message.Channel,
		Source: message.Source, AccountID: message.AccountID, InstanceID: instance,
		AuthorRole: b.Role, AuthorID: b.OwnerID, AuthorBindingID: b.ID, Text: req.Text,
		CreatedAt: now(), Status: "pending",
	}
	if err := putJSON(ctx, tx, "INSERT INTO outbox(id,message_id,source,instance_id,created_at,body) VALUES(?,?,?,?,?,?)",
		item, item.ID, item.MessageID, item.Source, item.InstanceID, item.CreatedAt); err != nil {
		return nil, err
	}
	if err := roleReceipt(ctx, tx, b.ID, "message_reply", req.OperationID, fp, item); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, storageError()
	}
	return json.Marshal(item)
}

func replyAddressPresent(channel *Channel) bool {
	if channel == nil {
		return false
	}
	if strings.TrimSpace(channel.ConversationID) != "" && strings.TrimSpace(channel.MessageID) != "" {
		return true
	}
	return channel.Type != "dws" && len(channel.Address) > 0
}

func replyTarget(ctx context.Context, tx *sql.Tx, b Binding, req ReplyRequest) (Message, string, error) {
	var message Message
	var instance string
	query := "SELECT body,reply_instance FROM messages WHERE TRUE"
	var args []any
	if req.MessageID != "" {
		query += " AND id=?"
		args = append(args, req.MessageID)
	}
	if b.Role == "topic" {
		scope, scopeArgs, err := scopeSQL("messages", &b)
		if err != nil {
			return message, "", err
		}
		query += scope
		args = append(args, scopeArgs...)
	}
	if len(req.MessageIDs) > 0 {
		ids, err := json.Marshal(req.MessageIDs)
		if err != nil {
			return message, "", storageError()
		}
		var count int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM messages WHERE id IN (SELECT value FROM json_each(?))", ids).Scan(&count); err != nil {
			return message, "", storageError()
		}
		if count != len(req.MessageIDs) {
			return message, "", problem("not_found", "one or more current routing Messages do not exist")
		}
		query += " AND id IN (SELECT value FROM json_each(?))"
		args = append(args, ids)
	}
	var body []byte
	err := tx.QueryRowContext(ctx, query+" ORDER BY accepted_at,seq LIMIT 1", args...).Scan(&body, &instance)
	if errors.Is(err, sql.ErrNoRows) {
		if b.Role == "topic" && req.MessageID != "" {
			return message, "", problem("forbidden", "Message is outside current Topic")
		}
		return message, "", problem("not_found", "no reply Message in the requested scope")
	}
	if err != nil || json.Unmarshal(body, &message) != nil {
		return message, "", storageError()
	}
	return message, instance, nil
}

// Binding comes from authenticated admission. Old instances without provenance
// can resolve only when one configured consumer represents the persisted account.
func (s *Store) replyInstance(message Message, admittedInstance string) (string, error) {
	var instance string
	for _, source := range s.config.Sources {
		if source.Source != message.Source || source.AccountID != message.AccountID || source.InstanceID == "" {
			continue
		}
		if admittedInstance == source.InstanceID {
			return source.InstanceID, nil
		}
		if admittedInstance == "" {
			if instance != "" {
				return "", problem("failed_precondition", "Message has no authenticated consumer instance and multiple instances are configured")
			}
			instance = source.InstanceID
		}
	}
	if instance == "" {
		return "", problem("failed_precondition", "no reply consumer is configured for the Message source instance")
	}
	return instance, nil
}
