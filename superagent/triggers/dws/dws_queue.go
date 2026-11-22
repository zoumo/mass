package dws

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/zoumo/mass/superagent/triggers/internal/coreclient"
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
	payload    []byte
	message    coreclient.Message
}

type dwsQueue struct{ db *sql.DB }

func openDWSQueue(c dwsConfig) (dwsQueue, func(), error) {
	db, closeDB, err := triggerdb.Open(c.DatabasePath, triggerdb.Identity{
		ApplicationID: dwsDatabaseApplicationID, Version: 1,
		Source: c.Source, AccountID: c.AccountID, InstanceID: c.InstanceID,
	}, dwsQueueSchema)
	return dwsQueue{db: db}, closeDB, err
}

func (q dwsQueue) counts() (pending, failed int, err error) {
	err = q.db.QueryRowContext(context.Background(), `SELECT
		(SELECT count(*) FROM dws_submissions WHERE failure IS NULL),
		(SELECT count(*) FROM dws_submissions WHERE failure IS NOT NULL)`).
		Scan(&pending, &failed)
	return pending, failed, err
}

func (q dwsQueue) enqueue(s coreclient.Submission, observedAt time.Time) (inserted bool, err error) {
	payload, err := json.Marshal(s)
	if err != nil {
		return false, err
	}
	err = triggerdb.Transaction(q.db, func(tx *sql.Tx) error {
		var stored []byte
		err := tx.QueryRowContext(context.Background(), `SELECT payload FROM dws_submissions WHERE account_id = ? AND native_message_id = ?`,
			s.Message.AccountID, s.Message.SourceMessageID).Scan(&stored)
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
			VALUES (?, ?, ?, ?)`, s.Message.AccountID, s.Message.SourceMessageID, observedAt.UTC().Format(time.RFC3339Nano), payload)
		inserted = err == nil
		return err
	})
	return inserted, err
}

func dwsBusinessPayload(raw []byte) ([]byte, error) {
	// DWS may redeliver an identical native Message in another subscription envelope.
	var payload struct {
		OperationID string         `json:"operationId"`
		Message     map[string]any `json:"message"`
		SelfSent    bool           `json:"selfSent,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	delete(payload.Message, "sourceEventId")
	return json.Marshal(payload)
}

func (q dwsQueue) head() (dwsQueueItem, error) {
	var item dwsQueueItem
	var observedAt string
	err := q.db.QueryRowContext(context.Background(), `SELECT sequence, observed_at, payload FROM dws_submissions
		WHERE failure IS NULL ORDER BY sequence LIMIT 1`).Scan(&item.sequence, &observedAt, &item.payload)
	if err != nil {
		return item, err
	}
	item.observedAt, err = time.Parse(time.RFC3339Nano, observedAt)
	if err != nil {
		return item, err
	}
	var stored struct {
		Message coreclient.Message `json:"message"`
	}
	err = json.Unmarshal(item.payload, &stored)
	item.message = stored.Message
	return item, err
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
