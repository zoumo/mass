package core

import (
	"context"
	"database/sql"
	"encoding/json"
)

const recordedReplyProjection = `(SELECT json_patch(json_object('outboxId',o.id),
 json_object('blockerRef',json(o.blocker_ref))) FROM outbox o
 WHERE o.id=trigger_messages.body->>'$.inReplyToOutboxId')`

const quotedReplyForTopic = `(SELECT candidate FROM (SELECT ` + recordedReplyProjection + ` AS candidate)
 WHERE json_extract(candidate,'$.blockerRef') IS NULL
 OR EXISTS(SELECT 1 FROM issues WHERE issues.id=json_extract(candidate,'$.blockerRef.issueId') AND issues.topic_id=?))`

type quotedReply struct {
	BlockerRef *BlockerRef `json:"blockerRef"`
}

func validateReplyReference(ctx context.Context, tx *sql.Tx, m Message) error {
	var valid bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM outbox o
 JOIN trigger_messages origin ON origin.id=o.message_id
 WHERE o.id=? AND o.status='sent' AND origin.source=? AND origin.account_id=? AND origin.thread_id=?)`,
		m.InReplyToOutboxID, m.Source, m.AccountID, m.ThreadID).Scan(&valid)
	if err != nil {
		return storageError()
	}
	if !valid {
		return problem("failed_precondition", "inReplyToOutboxId must refer to a sent Outbox item in the Message account and Thread")
	}
	return nil
}

func checkQuotedBlocker(ctx context.Context, tx *sql.Tx, messageID, issueID, blockerID string) error {
	var raw sql.NullString
	if err := tx.QueryRowContext(ctx, "SELECT "+recordedReplyProjection+" FROM trigger_messages WHERE id=?", messageID).Scan(&raw); err != nil {
		return storageError()
	}
	if !raw.Valid {
		return nil
	}
	var match quotedReply
	if json.Unmarshal([]byte(raw.String), &match) != nil {
		return storageError()
	}
	if match.BlockerRef != nil && *match.BlockerRef != (BlockerRef{IssueID: issueID, BlockerID: blockerID}) {
		return problem("failed_precondition", "quoted reply addresses a different blocker")
	}
	return nil
}
