package github

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	_ "embed"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	githubent "github.com/zoumo/mass/superagent/triggers/github/internal/ent"
	"github.com/zoumo/mass/superagent/triggers/github/internal/ent/pending"
	"github.com/zoumo/mass/superagent/triggers/github/internal/ent/ratelimit"
	entrepository "github.com/zoumo/mass/superagent/triggers/github/internal/ent/repository"
	"github.com/zoumo/mass/superagent/triggers/internal/triggerdb"
)

const (
	githubDatabaseApplicationID = 0x47485452 // GHTR
	githubRateLimitID           = 1
)

//go:embed migrations/0003_bootstrap.sql
var githubDatabaseSchema string

// Streams and snapshots use composite primary keys. Keep their point queries,
// checkpoints, and joins in SQL so SQLite can maintain the counters in the DDL.
const githubStreamColumns = `active, enabled_at, last_success_at, next_attempt_at, last_error, failures, item_count`

const githubUpsertSnapshot = `INSERT INTO github_snapshots (repository, scope, object_key, version, updated_at, data)
	VALUES (?, ?, ?, ?, ?, ?)
	ON CONFLICT (repository, scope, object_key) DO UPDATE SET
		version = excluded.version, updated_at = excluded.updated_at, data = excluded.data`

type githubStore struct {
	db       *sql.DB
	entities *githubent.Client
}

type githubPendingItem struct {
	sequence int64
	payload  []byte
}

func githubDatabaseIdentity(c githubConfig) triggerdb.Identity {
	return triggerdb.Identity{
		ApplicationID: githubDatabaseApplicationID, Version: 3,
		Source: githubSource, AccountID: c.AccountID, InstanceID: c.InstanceID,
	}
}

func openGitHubStore(c githubConfig) (githubStore, func(), error) {
	db, closeDB, err := triggerdb.Open(c.DatabasePath, githubDatabaseIdentity(c), githubDatabaseSchema)
	if err != nil {
		return githubStore{}, nil, err
	}
	store := githubStore{db: db, entities: githubent.NewClient(githubent.Driver(entsql.OpenDB(dialect.SQLite, db)))}
	if err := store.configureRepositories(c.Repositories); err != nil {
		closeDB()
		return githubStore{}, nil, errors.New("cannot configure GitHub database repositories")
	}
	return store, closeDB, nil
}

// Ent statements and composite-key SQL must share the same physical transaction
// on the database's single connection. The caller owns the transaction lifecycle.
func githubTransactionEntities(tx *sql.Tx) *githubent.Client {
	return githubent.NewClient(githubent.Driver(githubEntTxDriver{Conn: entsql.Conn{ExecQuerier: tx}}))
}

type githubEntTxDriver struct{ entsql.Conn }

func (githubEntTxDriver) Dialect() string { return dialect.SQLite }
func (githubEntTxDriver) Close() error    { return nil }

func (d githubEntTxDriver) Tx(context.Context) (dialect.Tx, error) {
	return dialect.NopTx(d), nil
}

func githubRepositoryNames(ctx context.Context, entities *githubent.Client, pendingOnly bool) ([]string, error) {
	query := entities.Repository.Query().Where(entrepository.IDNEQ(githubInboxRepository))
	if pendingOnly {
		query.Where(entrepository.BacklogGT(0))
	}
	return query.Order(entrepository.ByID()).IDs(ctx)
}

func (s githubStore) pendingRepositories() ([]string, error) {
	return githubRepositoryNames(context.Background(), s.entities, true)
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
	// Keep SQLite's literal defaults, including the TEXT value of last_receipt
	// before the first delivery write, and leave existing rows untouched.
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
		return writeGitHubRateLimit(githubTransactionEntities(tx), pause)
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

func (s githubStore) rateLimit() (time.Time, error) {
	return readGitHubRateLimit(context.Background(), s.entities)
}

func readGitHubRateLimit(ctx context.Context, entities *githubent.Client) (time.Time, error) {
	state, err := entities.RateLimit.Get(ctx, githubRateLimitID)
	if githubent.IsNotFound(err) {
		return time.Time{}, sql.ErrNoRows
	}
	if err != nil {
		return time.Time{}, err
	}
	return githubTimeFromValue(state.PauseUntil), nil
}

func writeGitHubRateLimit(entities *githubent.Client, until time.Time) error {
	pause := githubTimeValue(until)
	return entities.RateLimit.Update().
		Where(ratelimit.IDEQ(githubRateLimitID), ratelimit.PauseUntilNEQ(pause)).
		SetPauseUntil(pause).Exec(context.Background())
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
		return writeGitHubRateLimit(githubTransactionEntities(tx), pause)
	})
}

// commitReadback advances the scope and queues its Messages in the same transaction.
func (s githubStore) commitReadback(c githubConfig, repo githubRepository, scope string, stream githubStreamState,
	observations []githubObservation, pause time.Time, pull *githubPullRecord,
) (int, error) {
	queued := 0
	err := triggerdb.Transaction(s.db, func(tx *sql.Tx) error {
		entities := githubTransactionEntities(tx)
		var err error
		queued, err = applyGitHubObservations(tx, entities, c, repo, scope, stream, observations)
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
		return writeGitHubRateLimit(entities, pause)
	})
	return queued, err
}

func applyGitHubObservations(tx *sql.Tx, entities *githubent.Client, c githubConfig, repo githubRepository, scope string,
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
			submission, err := item.submission(c, repo, scope, version)
			if err != nil {
				return 0, err
			}
			payload, err := json.Marshal(submission)
			if err != nil {
				return 0, err
			}
			if err := entities.Pending.Create().SetRepository(repo.Repository).
				SetMessageID(submission.Message.SourceMessageID).SetPayload(payload).Exec(context.Background()); err != nil {
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

func (s githubStore) delivery(repository string) (githubDeliveryState, error) {
	return readGitHubDelivery(context.Background(), s.entities, repository)
}

func readGitHubDelivery(ctx context.Context, entities *githubent.Client, name string) (githubDeliveryState, error) {
	row, err := entities.Repository.Get(ctx, name)
	if githubent.IsNotFound(err) {
		return githubDeliveryState{}, sql.ErrNoRows
	}
	if err != nil {
		return githubDeliveryState{}, err
	}
	delivery := githubDeliveryState{
		Backlog: row.Backlog, Accepted: row.Accepted, Duplicates: row.Duplicates,
		LastDeliveryError: row.LastDeliveryError, NextDeliveryAt: githubTimeFromValue(row.NextDeliveryAt),
		DeliveryFailures: row.DeliveryFailures,
	}
	return delivery, json.Unmarshal(row.LastReceipt, &delivery.LastReceipt)
}

func readGitHubRepositoryStatus(ctx context.Context, tx *sql.Tx, entities *githubent.Client, repository string) (githubRepositoryStatus, error) {
	delivery, err := readGitHubDelivery(ctx, entities, repository)
	if err != nil {
		return githubRepositoryStatus{}, err
	}
	status := githubRepositoryStatus{
		Streams: make(map[string]githubStreamStatus), Backlog: delivery.Backlog,
		Accepted: delivery.Accepted, Duplicates: delivery.Duplicates,
		LastReceipt: delivery.LastReceipt, LastDeliveryError: delivery.LastDeliveryError, NextDeliveryAt: delivery.NextDeliveryAt,
	}
	rows, err := tx.QueryContext(ctx, `SELECT s.scope, s.kind, pull.data, ci.data, `+githubStreamColumns+`
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
	row, err := s.entities.Pending.Query().Where(pending.RepositoryEQ(repository)).Order(pending.ByID()).
		Select(pending.FieldID, pending.FieldPayload).First(context.Background())
	if githubent.IsNotFound(err) {
		return githubPendingItem{}, sql.ErrNoRows
	}
	if err != nil {
		return githubPendingItem{}, err
	}
	return githubPendingItem{sequence: row.ID, payload: row.Payload}, nil
}

func writeGitHubDelivery(entities *githubent.Client, name string, delivery githubDeliveryState) error {
	receipt, err := json.Marshal(delivery.LastReceipt)
	if err != nil {
		return err
	}
	return entities.Repository.Update().Where(entrepository.IDEQ(name)).
		SetAccepted(delivery.Accepted).SetDuplicates(delivery.Duplicates).SetLastReceipt(receipt).
		SetLastDeliveryError(delivery.LastDeliveryError).SetNextDeliveryAt(githubTimeValue(delivery.NextDeliveryAt)).
		SetDeliveryFailures(delivery.DeliveryFailures).Exec(context.Background())
}

func (s githubStore) saveDelivery(repository string, delivery githubDeliveryState, acknowledgedSequence int64) error {
	return triggerdb.Transaction(s.db, func(tx *sql.Tx) error {
		entities := githubTransactionEntities(tx)
		if acknowledgedSequence != 0 {
			count, err := entities.Pending.Delete().Where(pending.RepositoryEQ(repository), pending.IDEQ(acknowledgedSequence)).
				Exec(context.Background())
			if err != nil {
				return err
			}
			if count != 1 {
				return errors.New("GitHub pending head changed")
			}
		}
		return writeGitHubDelivery(entities, repository, delivery)
	})
}

func (s githubStore) readStatus(ctx context.Context, c githubConfig) (githubStatus, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return githubStatus{}, err
	}
	defer func() { _ = tx.Rollback() }()
	entities := githubTransactionEntities(tx)
	pause, err := readGitHubRateLimit(ctx, entities)
	if err != nil {
		return githubStatus{}, err
	}
	stream, err := readGitHubStream(tx.QueryRowContext(ctx, `SELECT `+githubStreamColumns+`
		FROM github_streams WHERE repository = ? AND scope = ?`, githubInboxRepository, githubInboxScope))
	if err != nil {
		return githubStatus{}, err
	}
	status := githubStatus{
		Source: githubSource, AccountID: c.AccountID, InstanceID: c.InstanceID, RateLimitedUntil: pause,
		Notifications: githubStreamStatusFor(stream), Repositories: make(map[string]githubRepositoryStatus),
	}
	names, err := githubRepositoryNames(ctx, entities, false)
	if err != nil {
		return githubStatus{}, err
	}
	for _, name := range names {
		out, err := readGitHubRepositoryStatus(ctx, tx, entities, name)
		if err != nil {
			return githubStatus{}, err
		}
		status.Repositories[name] = out
	}
	return status, nil
}
