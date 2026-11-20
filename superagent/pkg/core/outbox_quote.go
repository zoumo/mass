package core

import (
	"context"
	"database/sql"
	"encoding/json"
)

// Match the quoted ID to the actual send receipt within the authenticated
// account and inbound conversation. A cross-conversation quote is a reference
// only. A receipt can appear after the Message has already been routed.
const quotedReplyProjection = `(SELECT CASE WHEN count(*)=1 THEN json_object(
 'outboxId',o.id,'blockerRef',json(o.blocker_ref)) END
 FROM outbox o JOIN trigger_messages origin ON origin.id=o.message_id
 WHERE o.status='sent' AND o.blocker_ref IS NOT NULL
 AND origin.source=trigger_messages.source
 AND json_extract(origin.body,'$.accountId')=json_extract(trigger_messages.body,'$.accountId')
 AND json_extract(trigger_messages.body,'$.channel.type')='dws'
 AND json_extract(origin.body,'$.channel.type')='dws'
 AND json_extract(o.receipt,'$.conversationId')=json_extract(trigger_messages.body,'$.channel.conversationId')
 AND COALESCE(NULLIF(json_extract(trigger_messages.body,'$.quotedMessage.conversation_id'),''),
    json_extract(trigger_messages.body,'$.channel.conversationId'))=json_extract(o.receipt,'$.conversationId')
 AND json_extract(o.receipt,'$.messageId')=json_extract(trigger_messages.body,'$.quotedMessage.message_id'))`

const quotedReplyForTopic = `(SELECT candidate FROM (SELECT ` + quotedReplyProjection + ` AS candidate)
 WHERE EXISTS(SELECT 1 FROM issues WHERE issues.id=json_extract(candidate,'$.blockerRef.issueId') AND issues.topic_id=?))`

func checkQuotedBlocker(ctx context.Context, tx *sql.Tx, messageID, issueID, blockerID string) error {
	var raw sql.NullString
	if err := tx.QueryRowContext(ctx, "SELECT "+quotedReplyProjection+" FROM trigger_messages WHERE id=?", messageID).Scan(&raw); err != nil {
		return storageError()
	}
	if !raw.Valid {
		return nil
	}
	var match struct {
		BlockerRef BlockerRef `json:"blockerRef"`
	}
	if json.Unmarshal([]byte(raw.String), &match) != nil {
		return storageError()
	}
	if match.BlockerRef != (BlockerRef{IssueID: issueID, BlockerID: blockerID}) {
		return problem("failed_precondition", "quoted reply addresses a different blocker")
	}
	return nil
}
