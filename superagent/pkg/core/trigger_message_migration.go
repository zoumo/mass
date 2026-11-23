package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zoumo/mass/superagent/internal/triggercontent"
)

const migrationDirectory = "migration"
const legacyDWSEvidenceFile = "v25-dws-sent-evidence.ndjson"

type legacyDWSSend struct {
	Source         string `json:"source"`
	AccountID      string `json:"accountId"`
	OutboxID       string `json:"outboxId"`
	ConversationID string `json:"conversationId"`
	MessageID      string `json:"messageId"`
	TaskID         string `json:"taskId,omitempty"`
}

func migrateTriggerMessages(ctx context.Context, tx *sql.Tx) ([]legacyDWSSend, error) {
	var heldID string
	err := tx.QueryRowContext(ctx, `SELECT id FROM trigger_messages WHERE body->>'$.echo.status' IN ('held','echo_unknown') ORDER BY seq LIMIT 1`).Scan(&heldID)
	if err == nil {
		return nil, problem("failed_precondition", "reconcile legacy echo for Message "+heldID+" with schema 25 before cutover")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, storageError()
	}
	evidence, err := collectLegacyDWSSends(ctx, tx)
	if err != nil {
		return nil, err
	}

	const batchSize = 128
	var after int64
	for {
		rows, err := tx.QueryContext(ctx, `SELECT seq,body FROM trigger_messages
 WHERE seq>? AND body->'$.content' IS NULL ORDER BY seq LIMIT ?`, after, batchSize)
		if err != nil {
			return nil, storageError()
		}
		var messages []Message
		for rows.Next() {
			var message Message
			var raw []byte
			if err := rows.Scan(&after, &raw); err != nil || json.Unmarshal(raw, &message) != nil {
				_ = rows.Close()
				return nil, storageError()
			}
			messages = append(messages, message)
		}
		if err := rows.Close(); err != nil || rows.Err() != nil {
			return nil, storageError()
		}
		for _, message := range messages {
			if err := migrateTriggerMessage(ctx, tx, message); err != nil {
				return nil, err
			}
		}
		if len(messages) < batchSize {
			break
		}
	}

	if _, err := tx.ExecContext(ctx, `UPDATE outbox SET receipt=NULLIF(json_remove(receipt,'$.conversationId','$.messageId'),'{}')
 WHERE receipt->'$.conversationId' IS NOT NULL OR receipt->'$.messageId' IS NOT NULL;
 DROP INDEX IF EXISTS outbox_native_receipt;
 DROP INDEX IF EXISTS trigger_messages_echo_hold;`); err != nil {
		return nil, storageError()
	}
	return evidence, nil
}

func migrateTriggerMessage(ctx context.Context, tx *sql.Tx, old Message) error {
	content, err := triggercontent.FromLegacyText(old.Text, old.Actor, old.At, old.URL)
	if err != nil {
		return legacyMessageError(old.ID, "invalid source time")
	}
	meta := old.Meta
	if meta == nil {
		meta = make(map[string]any)
	}
	for key, value := range map[string]string{
		"chatType": old.Addressed, "eventId": old.SourceEventID,
		"collectedAt": old.CollectedAt, "collectorInstance": old.CollectorInstance,
	} {
		if value != "" {
			meta[key] = value
		}
	}
	if old.Human != nil {
		meta["human"] = *old.Human
	}
	if len(old.QuotedMessage) > 0 {
		meta["quotedMessage"] = old.QuotedMessage
	}
	var rawMeta json.RawMessage
	if len(meta) != 0 {
		rawMeta, err = json.Marshal(meta)
		if err != nil {
			return legacyMessageError(old.ID, "invalid source metadata")
		}
	}
	req := SubmitTriggerMessageRequest{Message: TriggerMessage{
		SourceMessageID: old.SourceMessageID, ThreadKey: old.ThreadKey, Content: content, Meta: rawMeta,
	}}
	if old.Channel != nil {
		req.ReplyTarget, err = legacyReplyTarget(old.Channel)
		if err != nil {
			return legacyMessageError(old.ID, "invalid reply target")
		}
		req.InReplyToOutboxID, err = legacyReplyReference(ctx, tx, old)
		if err != nil {
			return err
		}
	}
	message, canonical, err := req.prepare(Source{Source: old.Source, AccountID: old.AccountID})
	if err != nil {
		return legacyMessageError(old.ID, err.Error())
	}
	message.ID, message.ReceivedAt, message.Acceptance, message.EventID = old.ID, old.ReceivedAt, old.Acceptance, old.EventID
	message.Echo = old.Echo
	return putJSON(ctx, tx, `UPDATE trigger_messages SET canonical=?2, body=?3 WHERE id=?1`, message, old.ID, canonical)
}

func legacyMessageError(id, explanation string) *Error {
	return problem("failed_precondition", "cannot migrate legacy Message "+id+": "+explanation)
}

func legacyReplyTarget(ch *Channel) ([]byte, error) {
	if ch.Type != "dws" {
		return json.Marshal(ch)
	}
	// Legacy DWS queue replay spells every field in this object, including
	// empty actorId and address. Use the same representation for the stored copy.
	return json.Marshal(struct {
		Type           string         `json:"type"`
		ConversationID string         `json:"conversationId"`
		MessageID      string         `json:"messageId"`
		ActorID        string         `json:"actorId"`
		Address        map[string]any `json:"address"`
	}{ch.Type, ch.ConversationID, ch.MessageID, ch.ActorID, ch.Address})
}

func legacyReplyReference(ctx context.Context, tx *sql.Tx, m Message) (string, error) {
	if m.Channel.Type != "dws" || len(m.QuotedMessage) == 0 {
		return "", nil
	}
	var quote struct {
		ConversationID string `json:"conversation_id"`
		MessageID      string `json:"message_id"`
	}
	if json.Unmarshal(m.QuotedMessage, &quote) != nil || !legacyNativeID(quote.MessageID) ||
		(quote.ConversationID != "" && quote.ConversationID != m.Channel.ConversationID) {
		return "", nil
	}
	var count int
	var id string
	err := tx.QueryRowContext(ctx, `SELECT count(*),coalesce(min(o.id),'') FROM outbox o
 JOIN trigger_messages origin ON origin.id=o.message_id
 WHERE o.status='sent' AND origin.source=? AND origin.account_id=? AND origin.thread_key=?
 AND coalesce(origin.body->>'$.channel.type',origin.body->>'$.replyTarget.type')='dws'
 AND o.receipt->>'$.conversationId'=? AND o.receipt->>'$.messageId'=?`,
		m.Source, m.AccountID, m.ThreadKey, m.Channel.ConversationID, quote.MessageID).Scan(&count, &id)
	if err != nil {
		return "", storageError()
	}
	if count > 1 {
		return "", legacyMessageError(m.ID, "quoted message matches multiple sent Outbox items")
	}
	return id, nil
}

func legacyNativeID(value string) bool {
	return value != "" && strings.TrimSpace(value) == value
}

func collectLegacyDWSSends(ctx context.Context, tx *sql.Tx) ([]legacyDWSSend, error) {
	rows, err := tx.QueryContext(ctx, `SELECT m.source,m.account_id,o.id,o.status,o.receipt,m.body->>'$.channel.conversationId' FROM outbox o
 JOIN trigger_messages m ON m.id=o.message_id
 WHERE m.body->>'$.channel.type'='dws' AND (o.status='sent' OR o.receipt->>'$.uncertain'=1)
 ORDER BY o.rowid`)
	if err != nil {
		return nil, storageError()
	}
	defer func() { _ = rows.Close() }()
	var evidence []legacyDWSSend
	matched := make(map[[4]string]string)
	for rows.Next() {
		var row legacyDWSSend
		var status, originConversation string
		var raw []byte
		if err := rows.Scan(&row.Source, &row.AccountID, &row.OutboxID, &status, &raw, &originConversation); err != nil {
			return nil, storageError()
		}
		var receipt OutboxReceipt
		if json.Unmarshal(raw, &receipt) != nil || status != "sent" ||
			!legacyNativeID(receipt.ConversationID) || receipt.ConversationID != originConversation || !legacyNativeID(receipt.MessageID) {
			return nil, problem("failed_precondition", "reconcile legacy DWS send evidence for Outbox "+row.OutboxID+" before cutover")
		}
		row.ConversationID, row.MessageID, row.TaskID = receipt.ConversationID, receipt.MessageID, receipt.TaskID
		key := [4]string{row.Source, row.AccountID, row.ConversationID, row.MessageID}
		if other := matched[key]; other != "" {
			return nil, problem("failed_precondition", fmt.Sprintf("legacy DWS send evidence matches both Outbox %s and %s", other, row.OutboxID))
		}
		matched[key] = row.OutboxID
		evidence = append(evidence, row)
	}
	if err := rows.Err(); err != nil {
		return nil, storageError()
	}
	return evidence, nil
}

// Persist the handoff before retiring native receipts. If a process stops
// between the rename and SQL commit, the old rows remain the source on retry.
func exportLegacyDWSSends(dataDir string, rows []legacyDWSSend) error {
	path := filepath.Join(dataDir, migrationDirectory)
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	file, err := os.CreateTemp(path, ".pending-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	encoder := json.NewEncoder(file)
	for _, row := range rows {
		if err := encoder.Encode(row); err != nil {
			return err
		}
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), filepath.Join(path, legacyDWSEvidenceFile)); err != nil {
		return err
	}
	if err := dir.Sync(); err != nil {
		return err
	}
	parent, err := os.Open(dataDir)
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}
