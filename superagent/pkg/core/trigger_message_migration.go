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

const (
	migrationDirectory    = "migration"
	legacyDWSEvidenceFile = "v25-dws-sent-evidence.ndjson"
)

type legacyMessage struct {
	Message
	ThreadRef struct {
		ID string `json:"id"`
	} `json:"threadRef"`
	Actor             string          `json:"actor"`
	Text              string          `json:"text"`
	At                string          `json:"at"`
	Human             *bool           `json:"human"`
	Addressed         string          `json:"addressed"`
	Channel           *legacyChannel  `json:"channel"`
	URL               string          `json:"url"`
	Meta              map[string]any  `json:"meta"`
	QuotedMessage     json.RawMessage `json:"quotedMessage"`
	SourceEventID     string          `json:"sourceEventId"`
	CollectedAt       string          `json:"collectedAt"`
	CollectorInstance string          `json:"collectorInstance"`
}

type legacyChannel struct {
	Type           string         `json:"type"`
	ConversationID string         `json:"conversationId"`
	MessageID      string         `json:"messageId"`
	ActorID        string         `json:"actorId,omitempty"`
	Address        map[string]any `json:"address,omitempty"`
}

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
		var messages []legacyMessage
		for rows.Next() {
			var message legacyMessage
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

func migrateTriggerMessage(ctx context.Context, tx *sql.Tx, old legacyMessage) error {
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
	}
	req.InReplyToOutboxID, err = legacyReplyReference(ctx, tx, old)
	if err != nil {
		return err
	}
	message, canonical, err := req.prepare(Source{Source: old.Source, AccountID: old.AccountID})
	if err != nil {
		return legacyMessageError(old.ID, err.Error())
	}
	message.ID, message.ReceivedAt, message.Acceptance, message.EventID = old.ID, old.ReceivedAt, old.Acceptance, old.EventID
	message.LegacyEcho = old.LegacyEcho
	message.LegacyEchoStatus = legacyEchoStatus(old.LegacyEcho)
	return putJSON(ctx, tx, `UPDATE trigger_messages SET canonical=?2, body=?3 WHERE id=?1`, message, old.ID, canonical)
}

func legacyMessageError(id, explanation string) *Error {
	return problem("failed_precondition", "cannot migrate legacy Message "+id+": "+explanation)
}

func legacyReplyTarget(ch *legacyChannel) ([]byte, error) {
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

func legacyReplyReference(ctx context.Context, tx *sql.Tx, m legacyMessage) (string, error) {
	if len(m.QuotedMessage) == 0 {
		return "", nil
	}
	var quote struct {
		ConversationID string `json:"conversation_id"`
		MessageID      string `json:"message_id"`
	}
	validQuote := json.Unmarshal(m.QuotedMessage, &quote) == nil && legacyNativeID(quote.MessageID) &&
		(quote.ConversationID == "" || quote.ConversationID == m.ThreadRef.ID)
	if !validQuote {
		return "", nil
	}
	var count int
	var id string
	var pending bool
	err := tx.QueryRowContext(ctx, `SELECT
 count(CASE WHEN o.status='sent' AND o.receipt->>'$.conversationId'=?4 AND o.receipt->>'$.messageId'=?5 THEN 1 END),
 coalesce(min(CASE WHEN o.status='sent' AND o.receipt->>'$.conversationId'=?4 AND o.receipt->>'$.messageId'=?5 THEN o.id END),''),
 coalesce(max(o.status='pending' AND coalesce(o.receipt->>'$.taskId','')<>''),0)
 FROM outbox o JOIN trigger_messages origin ON origin.id=o.message_id
 WHERE origin.source=?1 AND origin.account_id=?2 AND origin.thread_key=?3
 AND coalesce(origin.body->>'$.channel.type',origin.body->>'$.replyTarget.type')='dws'
 AND coalesce(origin.body->>'$.channel.conversationId',origin.body->>'$.replyTarget.conversationId')=?4`,
		m.Source, m.AccountID, m.ThreadKey, m.ThreadRef.ID, quote.MessageID).Scan(&count, &id, &pending)
	if err != nil {
		return "", storageError()
	}
	if count > 1 {
		return "", legacyMessageError(m.ID, "quoted message matches multiple sent Outbox items")
	}
	if count == 0 && pending {
		return "", legacyMessageError(m.ID, "settle the pending DWS Outbox task in the quoted Message's Thread before cutover")
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
		var receipt struct {
			TaskID         string `json:"taskId"`
			ConversationID string `json:"conversationId"`
			MessageID      string `json:"messageId"`
		}
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
	if len(rows) == 0 {
		// Keep a fresh data directory free of an empty handoff. A previous
		// interrupted migration may have left a file to replace.
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
	}
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

// A historical echo kept its original admission receipt even if a later
// resolution created a routing event. Keep that receipt on an exact replay.
func legacyEchoStatus(raw json.RawMessage) string {
	var echo struct {
		Status string          `json:"status"`
		Match  json.RawMessage `json:"match"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &echo) != nil {
		return ""
	}
	if len(echo.Match) != 0 && string(echo.Match) != "null" {
		return "outbound_echo"
	}
	return echo.Status
}
