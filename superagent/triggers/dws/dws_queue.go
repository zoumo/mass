package dws

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	_ "embed"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	"github.com/zoumo/mass/superagent/triggers/dws/internal/queueent"
	"github.com/zoumo/mass/superagent/triggers/dws/internal/queueent/submission"
	"github.com/zoumo/mass/superagent/triggers/internal/triggerdb"
)

const dwsDatabaseApplicationID = 0x44575354 // DWST

//go:embed schema/v3.sql
var dwsQueueSchema string

var errDWSQueueConflict = errors.New("pending_message_conflict")

type dwsQueueItem struct {
	sequence   int64
	observedAt time.Time
	submission queuedSubmission
}

type dwsQueue struct {
	db        *sql.DB
	client    *queueent.Client
	accountID string
}

func dwsDatabaseIdentity(c dwsConfig) triggerdb.Identity {
	return triggerdb.Identity{
		ApplicationID: dwsDatabaseApplicationID, Version: 3,
		Source: c.Source, AccountID: c.AccountID, InstanceID: c.InstanceID,
	}
}

func openDWSQueue(c dwsConfig) (dwsQueue, func(), error) {
	db, closeDB, err := triggerdb.Open(c.DatabasePath, dwsDatabaseIdentity(c), dwsQueueSchema)
	if err != nil {
		return dwsQueue{}, nil, err
	}
	return newDWSQueue(db, c.AccountID), closeDB, nil
}

func newDWSQueue(db *sql.DB, accountID string) dwsQueue {
	// triggerdb owns the connection and closes it before releasing the lock.
	driver := entsql.OpenDB(dialect.SQLite, db)
	return dwsQueue{db: db, client: queueent.NewClient(queueent.Driver(driver)), accountID: accountID}
}

// Read both status counts in one SQLite snapshot.
func (q dwsQueue) counts() (pending, failed int, err error) {
	err = q.db.QueryRowContext(context.Background(), `SELECT
		(SELECT count(*) FROM dws_submissions WHERE failure IS NULL),
		(SELECT count(*) FROM dws_submissions WHERE failure IS NOT NULL AND failure NOT IN ('outbound_echo','released'))`).
		Scan(&pending, &failed)
	return pending, failed, err
}

// Queue transactions run to completion during Trigger shutdown.
func (q dwsQueue) transaction(ctx context.Context, apply func(*queueent.SubmissionClient) error) error {
	tx, err := q.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := apply(tx.Submission); err != nil {
		return err
	}
	return tx.Commit()
}

func (q dwsQueue) enqueue(s queuedSubmission, observedAt time.Time) (inserted bool, err error) {
	payload, err := json.Marshal(s)
	if err != nil {
		return false, err
	}
	ctx := context.Background()
	err = q.transaction(ctx, func(client *queueent.SubmissionClient) error {
		stored, err := client.Query().
			Where(submission.AccountIDEQ(q.accountID), submission.NativeMessageIDEQ(s.Message.SourceMessageID)).
			Select(submission.FieldPayload).
			Only(ctx)
		switch {
		case err == nil:
			original, err := dwsBusinessPayload(stored.Payload)
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
		case !queueent.IsNotFound(err):
			return err
		}
		err = client.Create().
			SetAccountID(q.accountID).
			SetNativeMessageID(s.Message.SourceMessageID).
			SetObservedAt(observedAt.UTC().Format(time.RFC3339Nano)).
			SetPayload(payload).
			Exec(ctx)
		inserted = err == nil
		return err
	})
	return inserted, err
}

func dwsBusinessPayload(raw []byte) ([]byte, error) {
	input, err := decodeQueuedSubmission(raw)
	if err != nil {
		return nil, err
	}
	// Delivery diagnostics and operator dispositions do not redefine an input.
	input.Message.Meta = nil
	input.ReleaseReason = ""
	input.Prepared = false
	input.InReplyToOutboxID = ""
	return json.Marshal(input)
}

const dwsQueueReadLimit = 16

func (q dwsQueue) pending(after int64) ([]dwsQueueItem, error) {
	rows, err := q.client.Submission.Query().
		Where(submission.FailureIsNil(), submission.IDGT(after)).
		Order(submission.ByID()).
		Limit(dwsQueueReadLimit).
		Select(submission.FieldID, submission.FieldObservedAt, submission.FieldPayload).
		All(context.Background())
	if err != nil {
		return nil, err
	}
	var items []dwsQueueItem
	for _, row := range rows {
		item, err := decodeDWSQueueItem(row.ID, row.ObservedAt, row.Payload)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func decodeDWSQueueItem(sequence int64, observedAt string, payload []byte) (dwsQueueItem, error) {
	at, err := time.Parse(time.RFC3339Nano, observedAt)
	if err != nil {
		return dwsQueueItem{}, err
	}
	s, err := decodeQueuedSubmission(payload)
	return dwsQueueItem{sequence: sequence, observedAt: at, submission: s}, err
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
	err = q.client.Submission.Update().
		Where(submission.IDEQ(item.sequence), submission.FailureIsNil()).
		SetPayload(payload).
		Exec(context.Background())
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
	ctx := context.Background()
	var rows int
	var err error
	if failure == "" {
		rows, err = q.client.Submission.Delete().
			Where(submission.IDEQ(sequence), submission.FailureIsNil()).
			Exec(ctx)
	} else {
		rows, err = q.client.Submission.Update().
			Where(submission.IDEQ(sequence), submission.FailureIsNil()).
			SetFailure(failure).
			Save(ctx)
	}
	if err != nil {
		return err
	}
	if rows != 1 {
		return errors.New("DWS queue head changed")
	}
	return nil
}
