package dws

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/zoumo/mass/superagent/triggers/internal/triggerdb"
)

const dwsDatabaseApplicationID = 0x44575354 // DWST

const dwsQueueSchema = `
CREATE TABLE dws_submissions (
	sequence INTEGER PRIMARY KEY AUTOINCREMENT,
	account_id TEXT NOT NULL,
	native_message_id TEXT NOT NULL,
	observed_at TEXT NOT NULL,
	payload BLOB NOT NULL,
	failure TEXT,
	UNIQUE (account_id, native_message_id)
);
CREATE INDEX dws_pending_sequence ON dws_submissions(sequence) WHERE failure IS NULL;
`

var errDWSQueueConflict = errors.New("pending_message_conflict")

type dwsQueueItem struct {
	sequence   int64
	observedAt time.Time
	submission queuedSubmission
}

type dwsQueue struct {
	db        *sql.DB
	accountID string
}

func openDWSQueue(c dwsConfig) (dwsQueue, func(), error) {
	db, closeDB, err := triggerdb.Open(c.DatabasePath, triggerdb.Identity{
		ApplicationID: dwsDatabaseApplicationID, Version: 1,
		Source: c.Source, AccountID: c.AccountID, InstanceID: c.InstanceID,
	}, dwsQueueSchema)
	return dwsQueue{db: db, accountID: c.AccountID}, closeDB, err
}

func (q dwsQueue) counts() (pending, failed int, err error) {
	err = q.db.QueryRowContext(context.Background(), `SELECT
		(SELECT count(*) FROM dws_submissions WHERE failure IS NULL),
		(SELECT count(*) FROM dws_submissions WHERE failure IS NOT NULL AND failure NOT IN ('outbound_echo','released'))`).
		Scan(&pending, &failed)
	return pending, failed, err
}

func (q dwsQueue) enqueue(s queuedSubmission, observedAt time.Time) (inserted bool, err error) {
	payload, err := json.Marshal(s)
	if err != nil {
		return false, err
	}
	err = triggerdb.Transaction(q.db, func(tx *sql.Tx) error {
		var stored []byte
		err := tx.QueryRowContext(context.Background(), `SELECT payload FROM dws_submissions WHERE account_id = ? AND native_message_id = ?`,
			q.accountID, s.Message.SourceMessageID).Scan(&stored)
		switch {
		case err == nil:
			original, err := dwsBusinessPayload(stored)
			if err != nil {
				return err
			}
			replay, err := dwsBusinessPayload(payload)
			if err != nil {
				return err
			}
			if !bytes.Equal(original, replay) {
				return errDWSQueueConflict
			}
			return nil
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		_, err = tx.ExecContext(context.Background(), `INSERT INTO dws_submissions (account_id, native_message_id, observed_at, payload)
			VALUES (?, ?, ?, ?)`, q.accountID, s.Message.SourceMessageID, observedAt.UTC().Format(time.RFC3339Nano), payload)
		inserted = err == nil
		return err
	})
	return inserted, err
}

func dwsBusinessPayload(raw []byte) ([]byte, error) {
	submission, err := decodeQueuedSubmission(raw)
	if err != nil {
		return nil, err
	}
	// Delivery diagnostics and operator dispositions do not redefine an input.
	submission.Message.Meta = nil
	submission.ReleaseReason = ""
	submission.Prepared = false
	submission.InReplyToOutboxID = ""
	return json.Marshal(submission)
}

const dwsQueueReadLimit = 16

func (q dwsQueue) pending(after int64) ([]dwsQueueItem, error) {
	rows, err := q.db.QueryContext(context.Background(), `SELECT sequence, observed_at, payload FROM dws_submissions
        WHERE failure IS NULL AND sequence > ? ORDER BY sequence LIMIT ?`, after, dwsQueueReadLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []dwsQueueItem
	for rows.Next() {
		var item dwsQueueItem
		var observedAt string
		var payload []byte
		if err := rows.Scan(&item.sequence, &observedAt, &payload); err != nil {
			return nil, err
		}
		item.observedAt, err = time.Parse(time.RFC3339Nano, observedAt)
		if err != nil {
			return nil, err
		}
		item.submission, err = decodeQueuedSubmission(payload)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (q dwsQueue) prepare(item dwsQueueItem, outboxID string) (queuedSubmission, error) {
	s := item.submission
	if s.Prepared {
		return s, nil
	}
	s.Prepared = true
	s.InReplyToOutboxID = outboxID
	payload, err := json.Marshal(s)
	if err != nil {
		return s, err
	}
	_, err = q.db.ExecContext(context.Background(), `UPDATE dws_submissions SET payload=? WHERE sequence=? AND failure IS NULL`, payload, item.sequence)
	return s, err
}

func (item dwsQueueItem) classify(evidence *sendEvidenceStore) (string, string) {
	s := item.submission
	if s.Prepared {
		return "ready", s.InReplyToOutboxID
	}
	ref := nativeMessageRef{ConversationID: s.ConversationID, MessageID: s.Message.SourceMessageID}
	a, unresolved := evidence.match(ref, item.observedAt)
	if a.OutboxID != "" {
		return "outbound_echo", a.OutboxID
	}
	if s.ReleaseReason != "" {
		return "released", ""
	}
	if s.SelfSent && unresolved {
		return "held", ""
	}
	if s.Quote != nil {
		a, unresolved = evidence.match(*s.Quote, item.observedAt)
		if a.Settled {
			return "quoted_reply", a.OutboxID
		}
		if unresolved || a.OutboxID != "" {
			return "held", a.OutboxID
		}
	}
	return "ready", ""
}

func (q dwsQueue) finish(sequence int64, failure string) error {
	var result sql.Result
	var err error
	if failure == "" {
		result, err = q.db.ExecContext(context.Background(), `DELETE FROM dws_submissions WHERE sequence = ? AND failure IS NULL`, sequence)
	} else {
		result, err = q.db.ExecContext(context.Background(), `UPDATE dws_submissions SET failure = ? WHERE sequence = ? AND failure IS NULL`, failure, sequence)
	}
	if err != nil {
		return err
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		return errors.New("DWS queue head changed")
	}
	return nil
}
