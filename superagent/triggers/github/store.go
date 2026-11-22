package github

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/zoumo/mass/superagent/triggers/internal/triggerdb"
)

const githubDatabaseApplicationID = 0x47485452 // GHTR

const githubDatabaseSchema = `
CREATE TABLE github_migration (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	source_sha256 TEXT NOT NULL,
	result BLOB NOT NULL
);
CREATE TABLE github_rate_limit (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	pause_until INTEGER NOT NULL
);
INSERT INTO github_rate_limit VALUES (1, 0);
CREATE TABLE github_repositories (
	name TEXT PRIMARY KEY,
	backlog INTEGER NOT NULL DEFAULT 0 CHECK (backlog >= 0),
	accepted INTEGER NOT NULL DEFAULT 0 CHECK (accepted >= 0),
	duplicates INTEGER NOT NULL DEFAULT 0 CHECK (duplicates >= 0),
	last_receipt BLOB NOT NULL DEFAULT '{}',
	last_delivery_error TEXT NOT NULL DEFAULT '',
	next_delivery_at INTEGER NOT NULL DEFAULT 0,
	delivery_failures INTEGER NOT NULL DEFAULT 0 CHECK (delivery_failures >= 0)
);
CREATE TABLE github_streams (
	repository TEXT NOT NULL REFERENCES github_repositories (name),
	scope TEXT NOT NULL,
	kind TEXT NOT NULL CHECK (kind IN ('parent', 'discussion', 'ci')),
	active INTEGER NOT NULL CHECK (active IN (0, 1)),
	discussion_priority INTEGER NOT NULL DEFAULT 0,
	discussion_closed INTEGER NOT NULL DEFAULT 0,
	enabled_at INTEGER NOT NULL,
	last_success_at INTEGER NOT NULL DEFAULT 0,
	last_full_at INTEGER NOT NULL DEFAULT 0,
	next_attempt_at INTEGER NOT NULL DEFAULT 0,
	last_error TEXT NOT NULL DEFAULT '',
	failures INTEGER NOT NULL DEFAULT 0,
	match_key TEXT NOT NULL DEFAULT '',
	silent_baseline INTEGER NOT NULL DEFAULT 0 CHECK (silent_baseline IN (0, 1)),
	item_count INTEGER NOT NULL DEFAULT 0 CHECK (item_count >= 0),
	PRIMARY KEY (repository, scope)
) WITHOUT ROWID;
CREATE INDEX github_due_streams ON github_streams
	(repository, kind, discussion_priority, last_success_at, scope, next_attempt_at) WHERE active = 1;
CREATE TABLE github_snapshots (
	repository TEXT NOT NULL,
	scope TEXT NOT NULL,
	object_key TEXT NOT NULL,
	version INTEGER NOT NULL CHECK (version >= 0),
	updated_at INTEGER NOT NULL,
	data BLOB NOT NULL,
	PRIMARY KEY (repository, scope, object_key),
	FOREIGN KEY (repository, scope) REFERENCES github_streams (repository, scope)
) WITHOUT ROWID;
CREATE TRIGGER github_count_snapshot AFTER INSERT ON github_snapshots BEGIN
	UPDATE github_streams SET item_count = item_count + 1 WHERE repository = NEW.repository AND scope = NEW.scope;
END;
CREATE TRIGGER github_uncount_snapshot AFTER DELETE ON github_snapshots BEGIN
	UPDATE github_streams SET item_count = item_count - 1 WHERE repository = OLD.repository AND scope = OLD.scope;
END;
CREATE TABLE github_pending (
	sequence INTEGER PRIMARY KEY AUTOINCREMENT,
	repository TEXT NOT NULL REFERENCES github_repositories (name),
	message_id TEXT NOT NULL,
	payload BLOB NOT NULL,
	UNIQUE (repository, message_id)
);
CREATE INDEX github_pending_fifo ON github_pending (repository, sequence);
CREATE TRIGGER github_count_pending AFTER INSERT ON github_pending BEGIN
	UPDATE github_repositories SET backlog = backlog + 1 WHERE name = NEW.repository;
END;
CREATE TRIGGER github_uncount_pending AFTER DELETE ON github_pending BEGIN
	UPDATE github_repositories SET backlog = backlog - 1 WHERE name = OLD.repository;
END;
`

const githubStreamColumns = `active, enabled_at, last_success_at, next_attempt_at, last_error, failures, item_count`

const githubUpsertSnapshot = `INSERT INTO github_snapshots (repository, scope, object_key, version, updated_at, data)
	VALUES (?, ?, ?, ?, ?, ?)
	ON CONFLICT (repository, scope, object_key) DO UPDATE SET
		version = excluded.version, updated_at = excluded.updated_at, data = excluded.data`

const githubInsertPending = `INSERT INTO github_pending (repository, message_id, payload) VALUES (?, ?, ?)`

type githubStore struct{ db *sql.DB }

type githubRowReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func githubRepositoryNames(db interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, pendingOnly bool,
) ([]string, error) {
	rows, err := db.QueryContext(context.Background(), `SELECT name FROM github_repositories
		WHERE name <> ? AND (NOT ? OR backlog > 0) ORDER BY name`, githubInboxRepository, pendingOnly)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

type githubPendingItem struct {
	sequence int64
	payload  []byte
}

func githubDatabaseIdentity(c githubConfig) triggerdb.Identity {
	return triggerdb.Identity{
		ApplicationID: githubDatabaseApplicationID, Version: 1,
		Source: githubSource, AccountID: c.AccountID, InstanceID: c.InstanceID,
	}
}

func openGitHubStore(c githubConfig) (githubStore, func(), error) {
	db, closeDB, err := triggerdb.Open(c.DatabasePath, githubDatabaseIdentity(c), githubDatabaseSchema)
	if err != nil {
		return githubStore{}, nil, err
	}
	store := githubStore{db}
	if err := store.configureRepositories(c.Repositories); err != nil {
		closeDB()
		return githubStore{}, nil, errors.New("cannot configure GitHub database repositories")
	}
	return store, closeDB, nil
}

func (s githubStore) configureRepositories(repositories []githubRepository) error {
	// GitHub API timestamps for these observations have second precision.
	enabled := time.Now().UTC().Truncate(time.Second)
	return triggerdb.Transaction(s.db, func(tx *sql.Tx) error {
		if err := ensureGitHubRepository(tx, githubInboxRepository); err != nil {
			return err
		}
		if _, err := tx.ExecContext(context.Background(), `INSERT INTO github_streams (repository, scope, kind, active, enabled_at)
			VALUES (?, ?, 'parent', 1, ?) ON CONFLICT DO NOTHING`, githubInboxRepository, githubInboxScope, githubTimeValue(enabled)); err != nil {
			return err
		}
		for _, repo := range repositories {
			if err := ensureGitHubRepository(tx, repo.Repository); err != nil {
				return err
			}
		}
		return nil
	})
}

func ensureGitHubRepository(tx *sql.Tx, repo string) error {
	_, err := tx.ExecContext(context.Background(), `INSERT INTO github_repositories (name) VALUES (?) ON CONFLICT DO NOTHING`, repo)
	return err
}

func (s githubStore) acceptNotifications(c githubConfig, enabled time.Time, items []githubNotification, pause time.Time) error {
	return triggerdb.Transaction(s.db, func(tx *sql.Tx) error {
		for _, item := range items {
			repo := strings.ToLower(item.Repository.FullName)
			notice, valid := item.notice(repo)
			if _, allowed := c.repository(repo); !allowed || !validGitHubRepository(repo) || !valid || item.UpdatedAt.Before(enabled) {
				continue
			}
			if err := saveGitHubNotice(tx, repo, enabled, notice); err != nil {
				return err
			}
		}
		return writeGitHubRateLimit(tx, pause)
	})
}

func saveGitHubNotice(tx *sql.Tx, repo string, enabled time.Time, notice githubNotice) error {
	if err := ensureGitHubRepository(tx, repo); err != nil {
		return err
	}
	ctx := context.Background()
	scope := githubNoticePrefix + notice.ID
	if _, err := tx.ExecContext(ctx, `INSERT INTO github_streams (repository, scope, kind, active, enabled_at)
		VALUES (?, ?, 'discussion', 1, ?) ON CONFLICT DO NOTHING`, repo, scope, githubTimeValue(enabled)); err != nil {
		return err
	}
	data, err := json.Marshal(notice)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, githubUpsertSnapshot+` WHERE excluded.updated_at > github_snapshots.updated_at`,
		repo, scope, githubNoticeKey, 0, githubTimeValue(notice.UpdatedAt), data)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed == 0 {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE github_streams SET active = 1,
		next_attempt_at = CASE WHEN active = 0 THEN 0 ELSE next_attempt_at END WHERE repository = ? AND scope = ?`, repo, scope)
	return err
}

func githubTimeValue(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

func githubTimeFromValue(value int64) time.Time {
	if value == 0 {
		return time.Time{}
	}
	return time.Unix(0, value).UTC()
}

func readGitHubRateLimit(db githubRowReader) (time.Time, error) {
	var value int64
	err := db.QueryRowContext(context.Background(), `SELECT pause_until FROM github_rate_limit WHERE id = 1`).Scan(&value)
	return githubTimeFromValue(value), err
}

func writeGitHubRateLimit(tx *sql.Tx, until time.Time) error {
	pause := githubTimeValue(until)
	_, err := tx.ExecContext(context.Background(), `UPDATE github_rate_limit SET pause_until = ? WHERE id = 1 AND pause_until <> ?`, pause, pause)
	return err
}

func (s githubStore) stream(repository, scope string) (githubStreamState, error) {
	return readGitHubStream(s.db.QueryRowContext(context.Background(), `SELECT `+githubStreamColumns+`
		FROM github_streams WHERE repository = ? AND scope = ?`, repository, scope))
}

func readGitHubStream(row interface{ Scan(...any) error }, prefix ...any) (githubStreamState, error) {
	var stream githubStreamState
	var enabled, lastSuccess, nextAttempt int64
	err := row.Scan(append(prefix, &stream.Active, &enabled, &lastSuccess, &nextAttempt, &stream.LastError, &stream.Failures, &stream.ItemCount)...)
	stream.EnabledAt = githubTimeFromValue(enabled)
	stream.LastSuccessAt = githubTimeFromValue(lastSuccess)
	stream.NextAttemptAt = githubTimeFromValue(nextAttempt)
	return stream, err
}

func writeGitHubStream(tx *sql.Tx, repository, scope string, stream githubStreamState) error {
	_, err := tx.ExecContext(context.Background(), `UPDATE github_streams SET active = ?, last_success_at = ?,
		next_attempt_at = ?, last_error = ?, failures = ? WHERE repository = ? AND scope = ?`,
		stream.Active, githubTimeValue(stream.LastSuccessAt), githubTimeValue(stream.NextAttemptAt),
		stream.LastError, stream.Failures, repository, scope)
	return err
}

func (s githubStore) scopeItems(repository, scope string) (map[string]githubRecord, error) {
	rows, err := s.db.QueryContext(context.Background(), `SELECT object_key, version, updated_at, data FROM github_snapshots
		WHERE repository = ? AND scope = ?`, repository, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make(map[string]githubRecord)
	for rows.Next() {
		var key string
		var item githubRecord
		var updated int64
		if err := rows.Scan(&key, &item.Version, &updated, &item.Data); err != nil {
			return nil, err
		}
		item.UpdatedAt = githubTimeFromValue(updated)
		items[key] = item
	}
	return items, rows.Err()
}

type githubDueScope struct {
	repository string
	scope      string
}

func (s githubStore) dueScopes(c githubConfig, kind, prefix string, now time.Time) ([]githubDueScope, error) {
	rows, err := s.db.QueryContext(context.Background(), `SELECT repository, scope FROM github_streams
		WHERE kind = ? AND scope GLOB ? AND active = 1 AND next_attempt_at <= ?
		ORDER BY last_success_at, next_attempt_at, repository, scope`, kind, prefix+"*", githubTimeValue(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var due []githubDueScope
	for rows.Next() && len(due) < 16 {
		var next githubDueScope
		if err := rows.Scan(&next.repository, &next.scope); err != nil {
			return nil, err
		}
		if _, allowed := c.repository(next.repository); allowed {
			due = append(due, next)
		}
	}
	return due, rows.Err()
}

func (s githubStore) savePoll(repository, scope string, stream githubStreamState, pause time.Time) error {
	return triggerdb.Transaction(s.db, func(tx *sql.Tx) error {
		if err := writeGitHubStream(tx, repository, scope, stream); err != nil {
			return err
		}
		return writeGitHubRateLimit(tx, pause)
	})
}

// commitReadback advances the scope and queues its Messages in the same transaction.
func (s githubStore) commitReadback(c githubConfig, repo githubRepository, scope string, stream githubStreamState,
	observations []githubObservation, pause time.Time, pull *githubPullRecord,
) (int, error) {
	queued := 0
	err := triggerdb.Transaction(s.db, func(tx *sql.Tx) error {
		var err error
		queued, err = applyGitHubObservations(tx, c, repo, scope, stream, observations)
		if err != nil {
			return err
		}
		if pull != nil {
			if err := trackGitHubPull(tx, repo.Repository, *pull, stream.EnabledAt); err != nil {
				return err
			}
		}
		if err := writeGitHubStream(tx, repo.Repository, scope, stream); err != nil {
			return err
		}
		return writeGitHubRateLimit(tx, pause)
	})
	return queued, err
}

func applyGitHubObservations(tx *sql.Tx, c githubConfig, repo githubRepository, scope string,
	stream githubStreamState, observations []githubObservation,
) (int, error) {
	if len(observations) == 0 {
		return 0, nil
	}
	record, err := tx.PrepareContext(context.Background(), githubUpsertSnapshot)
	if err != nil {
		return 0, err
	}
	defer record.Close()
	queued := 0
	for _, item := range observations {
		version := stream.Items[item.Key].Version
		if item.Kind != "" {
			version++
			submission := item.submission(c, repo, scope, version)
			payload, err := json.Marshal(submission)
			if err != nil {
				return 0, err
			}
			if _, err := tx.ExecContext(context.Background(), githubInsertPending, repo.Repository, submission.Message.SourceMessageID, payload); err != nil {
				return 0, err
			}
			queued++
		}
		if _, err := record.ExecContext(context.Background(), repo.Repository, scope, item.Key, version, githubTimeValue(item.UpdatedAt), item.Data); err != nil {
			return 0, err
		}
	}
	return queued, nil
}

func readGitHubDelivery(db githubRowReader, repository string) (githubDeliveryState, error) {
	var delivery githubDeliveryState
	var receipt []byte
	var next int64
	err := db.QueryRowContext(context.Background(), `SELECT backlog, accepted, duplicates, last_receipt, last_delivery_error,
		next_delivery_at, delivery_failures FROM github_repositories WHERE name = ?`, repository).
		Scan(&delivery.Backlog, &delivery.Accepted, &delivery.Duplicates, &receipt, &delivery.LastDeliveryError,
			&next, &delivery.DeliveryFailures)
	if err != nil {
		return delivery, err
	}
	delivery.NextDeliveryAt = githubTimeFromValue(next)
	return delivery, json.Unmarshal(receipt, &delivery.LastReceipt)
}

func readGitHubRepositoryStatus(tx *sql.Tx, repository string) (githubRepositoryStatus, error) {
	delivery, err := readGitHubDelivery(tx, repository)
	if err != nil {
		return githubRepositoryStatus{}, err
	}
	status := githubRepositoryStatus{
		Streams: make(map[string]githubStreamStatus), Backlog: delivery.Backlog,
		Accepted: delivery.Accepted, Duplicates: delivery.Duplicates,
		LastReceipt: delivery.LastReceipt, LastDeliveryError: delivery.LastDeliveryError, NextDeliveryAt: delivery.NextDeliveryAt,
	}
	rows, err := tx.QueryContext(context.Background(), `SELECT s.scope, s.kind, pull.data, ci.data, `+githubStreamColumns+`
		FROM github_streams AS s
		LEFT JOIN github_snapshots AS pull ON s.kind = 'ci' AND pull.repository = s.repository
			AND pull.scope = s.scope AND pull.object_key = ?
		LEFT JOIN github_snapshots AS ci ON s.kind = 'ci' AND ci.repository = s.repository
			AND ci.scope = s.scope AND ci.object_key = ?
		WHERE s.repository = ? AND (s.scope GLOB ? OR (s.active = 1 AND s.scope GLOB ?))`,
		githubTrackedPull, githubCISnapshotKey, repository, githubTrackedPrefix+"*", githubNoticePrefix+"*")
	if err != nil {
		return githubRepositoryStatus{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var scope, kind string
		var pull, snapshot []byte
		stream, err := readGitHubStream(rows, &scope, &kind, &pull, &snapshot)
		if err != nil {
			return githubRepositoryStatus{}, err
		}
		if kind == "ci" {
			status.Streams[scope] = githubCIStatusFor(stream, pull, snapshot)
		} else {
			status.PendingReadbacks++
			status.Streams[scope] = githubStreamStatusFor(stream)
		}
	}
	return status, rows.Err()
}

func githubCIStatusFor(stream githubStreamState, pullData, snapshotData []byte) githubStreamStatus {
	status := githubStreamStatusFor(stream)
	var pull githubPullRecord
	if json.Unmarshal(pullData, &pull) != nil || pull.ID <= 0 {
		status.State, status.LastError = "stale", "invalid tracked GitHub pull snapshot"
		return status
	}
	status.HeadSHA, status.TestMergeSHA = pull.HeadSHA, pull.MergeSHA
	var snapshot githubCISnapshot
	if len(snapshotData) > 0 && json.Unmarshal(snapshotData, &snapshot) == nil {
		status.CI = &snapshot
		mergeSHA := ""
		if snapshot.Merge != nil {
			mergeSHA = snapshot.Merge.SHA
		}
		if stream.Active && (snapshot.Head.SHA != pull.HeadSHA || mergeSHA != pull.MergeSHA && pull.MergeSHA != pull.HeadSHA) {
			status.State = "stale"
		}
	}
	return status
}

func (s githubStore) pendingHead(repository string) (githubPendingItem, error) {
	var item githubPendingItem
	err := s.db.QueryRowContext(context.Background(), `SELECT sequence, payload FROM github_pending
		WHERE repository = ? ORDER BY sequence LIMIT 1`, repository).Scan(&item.sequence, &item.payload)
	return item, err
}

func writeGitHubDelivery(tx *sql.Tx, repository string, delivery githubDeliveryState) error {
	receipt, err := json.Marshal(delivery.LastReceipt)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(context.Background(), `UPDATE github_repositories SET accepted = ?, duplicates = ?, last_receipt = ?,
		last_delivery_error = ?, next_delivery_at = ?, delivery_failures = ? WHERE name = ?`,
		delivery.Accepted, delivery.Duplicates, receipt, delivery.LastDeliveryError,
		githubTimeValue(delivery.NextDeliveryAt), delivery.DeliveryFailures, repository)
	return err
}

func (s githubStore) saveDelivery(repository string, delivery githubDeliveryState, acknowledgedSequence int64) error {
	return triggerdb.Transaction(s.db, func(tx *sql.Tx) error {
		if acknowledgedSequence != 0 {
			result, err := tx.ExecContext(context.Background(), `DELETE FROM github_pending WHERE repository = ? AND sequence = ?`, repository, acknowledgedSequence)
			if err != nil {
				return err
			}
			if count, err := result.RowsAffected(); err != nil || count != 1 {
				return errors.New("GitHub pending head changed")
			}
		}
		return writeGitHubDelivery(tx, repository, delivery)
	})
}
