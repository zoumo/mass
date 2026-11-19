package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
)

const outboxProjection = `json_patch(json_object('id',outbox.id,'messageId',outbox.message_id,'content',json(outbox.content),'status',outbox.status),
 json_object('receipt',json(outbox.receipt),'blockerRef',json(outbox.blocker_ref)))`

const channelOutboxProjection = `json_patch(` + outboxProjection + `,(SELECT json_object(
 'source',source,'accountId',json_extract(body,'$.accountId'),'channel',json_extract(body,'$.channel'))
 FROM trigger_messages WHERE id=outbox.message_id))`

const channelOutboxScope = ` AND EXISTS(SELECT 1 FROM trigger_messages
 WHERE id=outbox.message_id AND source=? AND json_extract(body,'$.accountId')=?)`

// The immutable reply ID includes its author's binding, so the six-column
// record can retain role ownership without a command or authorship table.
const (
	outboxAuthorID = `substr(outbox.id,8,instr(outbox.id,'.')-8)`
	outboxAuthor   = ` FROM session_bindings b WHERE b.id=` + outboxAuthorID
)

type ReplyRequest struct {
	OperationID string            `json:"operationId"`
	Content     StructuredContent `json:"content"`
	MessageID   string            `json:"messageId,omitempty"`
	MessageIDs  []string          `json:"messageIds,omitempty"`
	BlockerRef  *BlockerRef       `json:"blockerRef,omitempty"`
}

type BlockerRef struct {
	IssueID   string `json:"issueId"`
	BlockerID string `json:"blockerId"`
}

type OutboxItem struct {
	OutboxState
	ID         string            `json:"id"`
	MessageID  string            `json:"messageId"`
	Content    StructuredContent `json:"content"`
	BlockerRef *BlockerRef       `json:"blockerRef,omitempty"`
}

type OutboxState struct {
	Status  string         `json:"status"`
	Receipt *OutboxReceipt `json:"receipt,omitempty"`
}

type OutboxReceipt struct {
	TaskID         string `json:"taskId,omitempty"`
	ConversationID string `json:"conversationId,omitempty"`
	MessageID      string `json:"messageId,omitempty"`
	ErrorCode      string `json:"errorCode,omitempty"`
	RetryAt        string `json:"retryAt,omitempty"`
	Uncertain      bool   `json:"uncertain,omitempty"`
}

func (s *Store) reply(ctx context.Context, b Binding, req ReplyRequest) (json.RawMessage, error) {
	if b.Role != "router" && b.Role != "topic" {
		return nil, problem("forbidden", "only Router and Topic can reply")
	}
	if err := req.Content.normalize(); err != nil {
		return nil, err
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
	if strings.TrimSpace(req.OperationID) == "" {
		return nil, problem("invalid_argument", "operationId is required")
	}
	key := creationKey("outbox_"+b.ID+".reply", b.ID, req.OperationID)
	id := creationID(key, req)
	if existing, err := existingCreation(ctx, tx, "outbox", key, id); err != nil {
		return nil, err
	} else if existing {
		var item OutboxItem
		if err := readJSON(ctx, tx, "SELECT "+outboxProjection+" FROM outbox WHERE id=?", &item, id); err != nil {
			return nil, err
		}
		return json.Marshal(item)
	}
	if req.BlockerRef != nil {
		if err := validateBlockerReply(ctx, tx, b, *req.BlockerRef); err != nil {
			return nil, err
		}
	}
	message, err := replyTarget(ctx, tx, b, req)
	if err != nil {
		return nil, err
	}
	if !replyAddressPresent(message.Channel) {
		return nil, problem("failed_precondition", "selected Message has no reply address")
	}
	if !s.config.hasOutboxConsumer(message.Source, message.AccountID) {
		return nil, problem("failed_precondition", "no Outbox consumer is configured for the Message account")
	}
	item := OutboxItem{
		ID: id, MessageID: message.ID, Content: req.Content, BlockerRef: req.BlockerRef,
		OutboxState: OutboxState{Status: "pending"},
	}
	content, err := json.Marshal(item.Content)
	if err != nil {
		return nil, storageError()
	}
	var blocker any
	if item.BlockerRef != nil {
		raw, err := json.Marshal(item.BlockerRef)
		if err != nil {
			return nil, storageError()
		}
		blocker = string(raw)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO outbox(id,message_id,content,status,blocker_ref) VALUES(?,?,?,?,?)",
		item.ID, item.MessageID, string(content), item.Status, blocker); err != nil {
		return nil, storageError()
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

func replyTarget(ctx context.Context, tx *sql.Tx, b Binding, req ReplyRequest) (Message, error) {
	var message Message
	query := "SELECT body FROM trigger_messages WHERE TRUE"
	var args []any
	if req.MessageID != "" {
		query += " AND id=?"
		args = append(args, req.MessageID)
	}
	if b.Role == "topic" {
		scope, scopeArgs, err := scopeSQL("messages", &b)
		if err != nil {
			return message, err
		}
		query += scope
		args = append(args, scopeArgs...)
	}
	if len(req.MessageIDs) > 0 {
		ids, err := json.Marshal(req.MessageIDs)
		if err != nil {
			return message, storageError()
		}
		var count int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM trigger_messages WHERE id IN (SELECT value FROM json_each(?))", ids).Scan(&count); err != nil {
			return message, storageError()
		}
		if count != len(req.MessageIDs) {
			return message, problem("not_found", "one or more current routing Messages do not exist")
		}
		query += " AND id IN (SELECT value FROM json_each(?))"
		args = append(args, ids)
	}
	var body []byte
	err := tx.QueryRowContext(ctx, query+" ORDER BY accepted_at,seq LIMIT 1", args...).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		if b.Role == "topic" && req.MessageID != "" {
			return message, problem("forbidden", "Message is outside current Topic")
		}
		return message, problem("not_found", "no reply Message in the requested scope")
	}
	if err != nil || json.Unmarshal(body, &message) != nil {
		return message, storageError()
	}
	return message, nil
}

func validateBlockerReply(ctx context.Context, tx *sql.Tx, b Binding, ref BlockerRef) error {
	if b.Role != "topic" {
		return problem("forbidden", "only Topic can attach a blocker to a reply")
	}
	if ref.IssueID == "" || ref.BlockerID == "" {
		return problem("invalid_argument", "blockerRef requires issueId and blockerId")
	}
	issue, err := topicIssue(ctx, tx, b, ref.IssueID)
	if err != nil {
		return err
	}
	for _, blocker := range issue.Blockers {
		if blocker.ID == ref.BlockerID && blocker.Kind == "human" && !terminal(issue.Status) {
			return nil
		}
	}
	return problem("failed_precondition", "blockerRef must name a currently open human blocker")
}
