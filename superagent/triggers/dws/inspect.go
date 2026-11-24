package dws

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/zoumo/mass/superagent/triggers/dws/internal/queueent"
	"github.com/zoumo/mass/superagent/triggers/dws/internal/queueent/submission"
)

// InspectEvidence reads adapter-owned state while the destination is offline.
func (c Component) InspectEvidence(w io.Writer) error {
	queue, closeQueue, err := openDWSQueue(c.config)
	if err != nil {
		return err
	}
	defer closeQueue()
	store, err := openSendEvidence(c.config.DatabasePath)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	for id, attempt := range store.attempts {
		if err := enc.Encode(struct {
			ID string `json:"attemptId"`
			sendEvidence
		}{id, attempt}); err != nil {
			return err
		}
	}
	return queue.inspect(store, enc)
}

func (q dwsQueue) inspect(store *sendEvidenceStore, enc *json.Encoder) error {
	// Stream the evidence so an accumulated queue history fits in bounded memory.
	rows, err := q.db.QueryContext(context.Background(), `SELECT sequence, observed_at, payload, coalesce(failure,'')
		FROM dws_submissions ORDER BY sequence`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var sequence int64
		var raw []byte
		var at, status string
		if err := rows.Scan(&sequence, &at, &raw, &status); err != nil {
			return err
		}
		item, err := decodeDWSQueueItem(sequence, at, raw)
		if err != nil {
			return err
		}
		decision, outboxID := item.classify(store)
		if status == "" {
			status = decision
		}
		if err := enc.Encode(struct {
			Status          string `json:"status"`
			SourceMessageID string `json:"sourceMessageId"`
			OutboxID        string `json:"outboxId,omitempty"`
			ReleaseReason   string `json:"releaseReason,omitempty"`
		}{status, item.submission.Message.SourceMessageID, outboxID, item.submission.ReleaseReason}); err != nil {
			return err
		}
	}
	return rows.Err()
}

// ReleaseInput allows an operator to clear an unresolved echo or quote after
// inspecting its DWS origin. Exact confirmations always take precedence.
func (c Component) ReleaseInput(nativeID, reason string) error {
	if !validNativeID(nativeID) || strings.TrimSpace(reason) == "" || len(reason) > 1024 {
		return errors.New("a native message ID and a nonempty release reason of up to 1024 bytes are required")
	}
	queue, closeQueue, err := openDWSQueue(c.config)
	if err != nil {
		return err
	}
	defer closeQueue()
	store, err := openSendEvidence(c.config.DatabasePath)
	if err != nil {
		return err
	}
	return queue.releaseInput(store, nativeID, reason)
}

func (q dwsQueue) releaseInput(store *sendEvidenceStore, nativeID, reason string) error {
	ctx := context.Background()
	return q.transaction(ctx, func(client *queueent.SubmissionClient) error {
		row, err := client.Query().
			Where(submission.AccountIDEQ(q.accountID), submission.NativeMessageIDEQ(nativeID), submission.FailureIsNil()).
			Select(submission.FieldID, submission.FieldObservedAt, submission.FieldPayload).
			Only(ctx)
		if queueent.IsNotFound(err) {
			return sql.ErrNoRows
		}
		if err != nil {
			return err
		}
		item, err := decodeDWSQueueItem(row.ID, row.ObservedAt, row.Payload)
		if err != nil {
			return err
		}
		if item.submission.ReleaseReason == reason {
			return nil
		}
		if status, _ := item.classify(store); status != "held" {
			return errors.New("DWS input is not held for clarification")
		}
		item.submission.ReleaseReason = reason
		payload, err := json.Marshal(item.submission)
		if err != nil {
			return err
		}
		return client.Update().Where(submission.IDEQ(item.sequence)).SetPayload(payload).Exec(ctx)
	})
}

// DismissSend clears an unknown attempt after an operator verifies no delivery.
func (c Component) DismissSend(attemptID, reason string) error {
	if strings.TrimSpace(reason) == "" || len(reason) > 1024 {
		return errors.New("a nonempty dismissal reason of up to 1024 bytes is required")
	}
	_, closeQueue, err := openDWSQueue(c.config)
	if err != nil {
		return err
	}
	defer closeQueue()
	store, err := openSendEvidence(c.config.DatabasePath)
	if err != nil {
		return err
	}
	attempt, exists := store.attempts[attemptID]
	if !exists || attempt.Result.Outcome != "unknown" {
		return errors.New("DWS send attempt has no unknown result")
	}
	if attempt.DismissedReason != "" && attempt.DismissedReason != reason {
		return errors.New("DWS send attempt already has a dismissal reason")
	}
	attempt.DismissedReason = reason
	return store.put(attempt)
}
