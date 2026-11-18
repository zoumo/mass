package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

const (
	schemaVersion = 12
	applicationID = 1398096210
)

type Store struct {
	config Config
	db     *sql.DB
	lock   *os.File
	mu     sync.Mutex
}

func Open(c Config) (_ *Store, retErr error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Refuse foreign/old data before creating even a lock file in that directory.
	if err := os.MkdirAll(c.DataDir, 0o700); err != nil {
		return nil, problem("unavailable", "cannot create data directory")
	}
	entries, err := os.ReadDir(c.DataDir)
	if err != nil {
		return nil, problem("unavailable", "cannot inspect data directory")
	}
	allowed := map[string]bool{".core.lock": true, "core.sqlite": true, "core.sqlite-wal": true, "core.sqlite-shm": true}
	for _, e := range entries {
		if e.Name() == "capabilities" && e.IsDir() {
			info, statErr := e.Info()
			if statErr != nil || info.Mode().Perm()&0o077 != 0 {
				return nil, problem("failed_precondition", "capabilities directory must be private")
			}
			continue
		}
		if !allowed[e.Name()] {
			return nil, problem("failed_precondition", "data directory is not a superagent directory; use a new empty directory; old mass-sa JSON data is never imported or removed")
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil, problem("failed_precondition", "data directory contains a non-regular Core file")
		}
	}
	f, err := os.OpenFile(filepath.Join(c.DataDir, ".core.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, problem("unavailable", "cannot open Core ownership lock")
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, problem("conflict", "data directory is already owned by another Core")
	}
	s := &Store{lock: f, config: c}
	defer func() {
		if retErr != nil {
			_ = s.Close()
		}
	}()
	dbPath := filepath.Join(c.DataDir, "core.sqlite")
	df, err := os.OpenFile(dbPath, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, storageError()
	}
	_ = df.Close()
	s.db, err = sql.Open("sqlite", (&url.URL{Scheme: "file", Path: dbPath}).String())
	if err != nil {
		return nil, storageError()
	}
	s.db.SetMaxOpenConns(1)
	var version, app int
	if err = s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return nil, problem("failed_precondition", "invalid SQLite database")
	}
	if err = s.db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&app); err != nil {
		return nil, storageError()
	}
	if version != 0 && (version < 2 || version > schemaVersion) {
		return nil, problem("unsupported", fmt.Sprintf("unsupported schema version %d; supported version is %d; data was not migrated", version, schemaVersion))
	}
	if (version != 0 && app != applicationID) || (version == 0 && app != 0 && app != applicationID) {
		return nil, problem("failed_precondition", "database belongs to another application")
	}
	if version == 0 {
		var n int
		if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'").Scan(&n); err != nil {
			return nil, storageError()
		}
		if n != 0 {
			return nil, problem("failed_precondition", "unversioned nonempty database; use a new directory")
		}
	}
	rebuildThreads := version > 0 && version < 10
	for _, q := range []string{"PRAGMA busy_timeout=5000", "PRAGMA journal_mode=WAL", "PRAGMA synchronous=FULL"} {
		if _, err = s.db.ExecContext(ctx, q); err != nil {
			return nil, storageError()
		}
	}
	// SQLite's table rebuild preserves the names in dependent foreign keys.
	// Foreign-key enforcement must be off before beginning that transaction.
	foreignKeys := "PRAGMA foreign_keys=ON"
	if rebuildThreads {
		foreignKeys = "PRAGMA foreign_keys=OFF"
	}
	if _, err = s.db.ExecContext(ctx, foreignKeys); err != nil {
		return nil, storageError()
	}
	if c.StorageMaxPages > 0 {
		if _, err = s.db.ExecContext(ctx, fmt.Sprintf("PRAGMA max_page_count=%d", c.StorageMaxPages)); err != nil {
			return nil, storageError()
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, storageError()
	}
	defer func() { _ = tx.Rollback() }()
	if version == 0 {
		_, err = tx.ExecContext(ctx, `
CREATE TABLE source_bindings (source TEXT PRIMARY KEY, account_id TEXT NOT NULL UNIQUE);
CREATE TABLE threads (seq INTEGER PRIMARY KEY AUTOINCREMENT, key TEXT NOT NULL UNIQUE, source TEXT NOT NULL, native_id TEXT NOT NULL, created_at TEXT NOT NULL, last_message_at TEXT NOT NULL, UNIQUE(source,native_id));
CREATE TABLE messages (seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, source TEXT NOT NULL REFERENCES source_bindings(source), native_id TEXT NOT NULL, thread_key TEXT NOT NULL REFERENCES threads(key), accepted_at TEXT NOT NULL, canonical BLOB NOT NULL, body BLOB NOT NULL, receipt BLOB NOT NULL, UNIQUE(source,native_id));
CREATE INDEX messages_thread ON messages(thread_key,accepted_at,id);
CREATE TABLE events (seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, message_id TEXT NOT NULL REFERENCES messages(id), at TEXT NOT NULL, status TEXT NOT NULL, binding_id TEXT NOT NULL DEFAULT '', next_attempt_at TEXT NOT NULL DEFAULT '', body BLOB NOT NULL);
CREATE TABLE operations (source TEXT NOT NULL REFERENCES source_bindings(source), kind TEXT NOT NULL, operation_id TEXT NOT NULL, fingerprint BLOB NOT NULL, receipt BLOB NOT NULL, PRIMARY KEY(source,kind,operation_id));
PRAGMA application_id=1398096210;`)
		if err != nil {
			return nil, storageError()
		}
		if _, err = tx.ExecContext(ctx, routingSchema); err != nil {
			return nil, storageError()
		}
	}
	if version < 3 {
		if _, err = tx.ExecContext(ctx, heartbeatSchema); err != nil {
			return nil, storageError()
		}
	}
	if version < 4 {
		if _, err = tx.ExecContext(ctx, deliverySchema); err != nil {
			return nil, storageError()
		}
	}
	if version < 5 {
		if _, err = tx.ExecContext(ctx, workSchema); err != nil {
			return nil, storageError()
		}
	}
	if version < 6 {
		if _, err = tx.ExecContext(ctx, `UPDATE issues SET body=json_set(body,'$.requirementsRevision',json_extract(body,'$.revision'))`); err != nil {
			return nil, storageError()
		}
	}
	if version < 7 {
		if _, err = tx.ExecContext(ctx, lifecycleSchema); err != nil {
			return nil, storageError()
		}
	}
	if version < 8 {
		if _, err = tx.ExecContext(ctx, "CREATE INDEX reports_execution ON reports(execution_id)"); err != nil {
			return nil, storageError()
		}
	}
	if version < 9 {
		if _, err = tx.ExecContext(ctx, timerSchema); err != nil {
			return nil, storageError()
		}
	}
	if rebuildThreads {
		if _, err = tx.ExecContext(ctx, `
CREATE TABLE new_threads (seq INTEGER PRIMARY KEY AUTOINCREMENT, key TEXT NOT NULL UNIQUE, source TEXT NOT NULL, native_id TEXT NOT NULL, created_at TEXT NOT NULL, last_message_at TEXT NOT NULL, UNIQUE(source,native_id));
INSERT INTO new_threads(seq,key,source,native_id,created_at,last_message_at)
 SELECT seq,key,source,native_id,created_at,last_message_at FROM threads;
DROP TABLE threads;
ALTER TABLE new_threads RENAME TO threads;`); err != nil {
			return nil, storageError()
		}
	}
	if version < 10 {
		if _, err = tx.ExecContext(ctx, pullRequestSchema); err != nil {
			return nil, storageError()
		}
	}
	if version < 11 {
		if _, err = tx.ExecContext(ctx, outboxSchema); err != nil {
			return nil, storageError()
		}
	}
	if version < 12 {
		if _, err = tx.ExecContext(ctx, echoSchema); err != nil {
			return nil, storageError()
		}
	}
	if version < schemaVersion {
		if _, err = tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d", schemaVersion)); err != nil {
			return nil, storageError()
		}
	}
	if err := initializeRouter(ctx, tx, c); err != nil {
		return nil, err
	}
	if err := recoverDispatches(ctx, tx); err != nil {
		return nil, err
	}
	if err := recoverStops(ctx, tx); err != nil {
		return nil, err
	}
	if err := recoverWorkerObservations(ctx, tx); err != nil {
		return nil, err
	}
	// A source can never silently switch account on restart or take another source's account.
	for _, source := range c.Sources {
		var account string
		err = tx.QueryRowContext(ctx, "SELECT account_id FROM source_bindings WHERE source=?", source.Source).Scan(&account)
		if err == nil && account != source.AccountID {
			return nil, problem("failed_precondition", "configured source account differs from persisted binding")
		}
		if errors.Is(err, sql.ErrNoRows) {
			_, err = tx.ExecContext(ctx, "INSERT INTO source_bindings(source,account_id) VALUES(?,?)", source.Source, source.AccountID)
		}
		if err != nil {
			return nil, problem("failed_precondition", "source/account binding cannot be established")
		}
	}
	if rebuildThreads {
		var violations int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM pragma_foreign_key_check").Scan(&violations); err != nil || violations != 0 {
			return nil, storageError()
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, storageError()
	}
	if rebuildThreads {
		if _, err = s.db.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
			return nil, storageError()
		}
	}
	return s, nil
}

func (s *Store) Close() error {
	var err error
	if s.db != nil {
		err = s.db.Close()
	}
	if s.lock != nil {
		_ = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
		err = errors.Join(err, s.lock.Close())
	}
	return err
}

func (s *Store) Submit(ctx context.Context, source Source, r SubmitRequest) (Receipt, error) {
	var result Receipt
	if strings.TrimSpace(r.OperationID) == "" {
		return result, problem("invalid_argument", "operationId is required")
	}
	if err := r.Message.validate(source); err != nil {
		return result, err
	}
	canonical, err := r.Message.canonical()
	if err != nil {
		return result, problem("invalid_argument", "message cannot be encoded")
	}
	fingerprint := sha256.Sum256(canonical)
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, storageError()
	}
	defer func() { _ = tx.Rollback() }()
	var saved, fp []byte
	err = tx.QueryRowContext(ctx, "SELECT fingerprint,receipt FROM operations WHERE source=? AND kind='submitMessage' AND operation_id=?", source.Source, r.OperationID).Scan(&fp, &saved)
	if err == nil {
		if !bytes.Equal(fp, fingerprint[:]) {
			return result, problem("conflict", "operationId was used with different business parameters")
		}
		if json.Unmarshal(saved, &result) != nil {
			return result, storageError()
		}
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, storageError()
	}
	err = tx.QueryRowContext(ctx, "SELECT canonical,receipt FROM messages WHERE source=? AND native_id=?", source.Source, r.Message.SourceMessageID).Scan(&fp, &saved)
	if err == nil {
		if !bytes.Equal(fp, canonical) {
			return result, problem("conflict", "sourceMessageId already has different business content")
		}
		if json.Unmarshal(saved, &result) != nil {
			return result, storageError()
		}
		result.Duplicate = true
	} else if errors.Is(err, sql.ErrNoRows) {
		at := now()
		key := ThreadKey(r.Message.ThreadRef)
		messageID, eventID := newID("msg"), newID("evt")
		result = Receipt{Status: "accepted", MessageID: messageID, ThreadKey: key, AcceptedAt: at, EventID: eventID}
		m := Message{MessageInput: r.Message, ID: messageID, ThreadKey: key, ReceivedAt: at, Acceptance: "accepted", EventID: eventID}
		e := Event{ID: eventID, Type: "message.received", At: at, MessageID: messageID, ThreadKey: key, TargetRole: "router", Intent: "act", Status: "pending"}
		m.Echo, err = classifyEcho(ctx, tx, m, r.SelfSent && source.InstanceID != "")
		if err != nil {
			return Receipt{}, err
		}
		body, encodeErr := json.Marshal(m)
		if encodeErr != nil {
			return Receipt{}, problem("invalid_argument", "message cannot be encoded")
		}
		e.RouteRound = 1
		receipt, encodeErr := json.Marshal(result)
		if encodeErr != nil {
			return Receipt{}, storageError()
		}
		if err := upsertThread(ctx, tx, key, r.Message.ThreadRef, at, at); err != nil {
			return Receipt{}, err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO messages(id,source,native_id,thread_key,accepted_at,canonical,body,receipt,reply_instance) VALUES(?,?,?,?,?,?,?,?,?)", messageID, source.Source, r.Message.SourceMessageID, key, at, canonical, body, receipt, source.InstanceID); err != nil {
			return Receipt{}, storageError()
		}
		if m.Echo == nil {
			err = acceptRoute(ctx, tx, &e)
		} else {
			err = recordEchoAdmission(ctx, tx, m, e)
		}
		if err != nil {
			return Receipt{}, err
		}
	} else {
		return result, storageError()
	}
	receipt, encodeErr := json.Marshal(result)
	if encodeErr != nil {
		return Receipt{}, storageError()
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO operations(source,kind,operation_id,fingerprint,receipt) VALUES(?,'submitMessage',?,?,?)", source.Source, r.OperationID, fingerprint[:], receipt); err != nil {
		return Receipt{}, storageError()
	}
	commitGate("before_commit")
	if err = tx.Commit(); err != nil {
		return Receipt{}, storageError()
	}
	commitGate("after_commit")
	return result, nil
}

func upsertThread(ctx context.Context, tx *sql.Tx, key string, ref ThreadRef, createdAt, lastMessageAt string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO threads(key,source,native_id,created_at,last_message_at) VALUES(?,?,?,?,?)
 ON CONFLICT(source,native_id) DO UPDATE SET last_message_at=excluded.last_message_at WHERE excluded.last_message_at<>''`,
		key, ref.Source, ref.ID, createdAt, lastMessageAt)
	if err != nil {
		return storageError()
	}
	return nil
}

type cursor struct {
	Scope  string `json:"s,omitempty"`
	Kind   string `json:"k"`
	Thread string `json:"t,omitempty"`
	Parent string `json:"p,omitempty"`
	Filter string `json:"f,omitempty"`
	Newest bool   `json:"n,omitempty"`
	Upper  int64  `json:"u"`
	At     string `json:"a"`
	ID     string `json:"i"`
}

func (s *Store) List(ctx context.Context, kind, thread, token string, limit int) (Page, error) {
	return s.listScoped(ctx, QueryRequest{Collection: kind, ThreadKey: thread, Cursor: token, Limit: limit}, nil, nil)
}

func (s *Store) listScoped(ctx context.Context, q QueryRequest, binding *Binding, source *Source) (Page, error) {
	p := Page{Items: []json.RawMessage{}}
	kind, thread, token, limit := q.Collection, q.ThreadKey, q.Cursor, q.Limit
	if limit < 1 || limit > 200 {
		return p, problem("invalid_argument", "limit must be between 1 and 200")
	}
	scopeID := ""
	if binding != nil {
		scopeID = binding.ID
	}
	if source != nil {
		scopeID = ThreadKey(ThreadRef{Source: source.Source, ID: source.InstanceID})
	}
	var parentColumn, parent string
	var relatedFilter string
	switch {
	case q.TopicID != "" && q.IssueID == "" && (kind == "issues" || kind == "timers" || kind == "pull-requests" || kind == "subscriptions" || kind == "relations"):
		parentColumn, parent = "topic_id", q.TopicID
	case q.TopicID != "" && q.IssueID == "" && binding == nil && (kind == "decisions" || kind == "outbox"):
		parentColumn, parent = "related_topic", q.TopicID
		if kind == "decisions" {
			relatedFilter = "EXISTS (SELECT 1 FROM json_each(decisions.body,'$.messageIds') WHERE value IN (SELECT message_id FROM relations WHERE topic_id=?))"
		} else {
			relatedFilter = "message_id IN (SELECT message_id FROM relations WHERE topic_id=?)"
		}
	case q.IssueID != "" && q.TopicID == "" && (kind == "reports" || kind == "executions" || kind == "pull-requests"):
		parentColumn, parent = "issue_id", q.IssueID
	case q.TopicID != "" || q.IssueID != "":
		return p, problem("invalid_argument", "unsupported topicId or issueId filter; use only one parent")
	}
	c := cursor{Kind: kind, Thread: thread, Parent: parent, Scope: scopeID, Newest: q.newest}
	scope, scopeArgs, scopeErr := scopeSQL(kind, binding)
	if scopeErr != nil {
		return p, scopeErr
	}
	var table, timeCol, idCol string
	switch kind {
	case "messages":
		table, timeCol, idCol = "messages", "accepted_at", "id"
	case "threads":
		table, timeCol, idCol = "threads", "created_at", "key"
	case "events":
		table, timeCol, idCol = "events", "at", "id"
	case "topics", "decisions", "subscriptions", "relations", "bindings", "deliveries", "batches", "attempts", "recoveries", "issues", "executions", "reports", "timers", "outbox":
		table, timeCol, idCol = kind, "created_at", "id"
	case "channel-health":
		table, timeCol, idCol = "channel_health", "created_at", "id"
	case "pull-requests":
		table, timeCol, idCol = "pull_requests", "created_at", "id"
	default:
		return p, problem("not_found", "unknown collection")
	}
	if thread != "" && kind != "messages" && kind != "subscriptions" && kind != "outbox" {
		return p, problem("invalid_argument", "threadKey filters Messages, subscriptions and Outbox")
	}
	if q.MessageID != "" {
		if parent == "" && kind == "decisions" && binding == nil {
			parentColumn, parent, c.Parent = "decision_message", q.MessageID, q.MessageID
			relatedFilter = "EXISTS (SELECT 1 FROM json_each(decisions.body,'$.messageIds') WHERE value=?)"
		} else if parent != "" || (kind != "outbox" && kind != "relations" && kind != "events") {
			return p, problem("invalid_argument", "messageId filters Outbox, routing relations and Events; use only one parent")
		} else {
			parentColumn, parent, c.Parent = "message_id", q.MessageID, q.MessageID
		}
	}
	c.Filter = parentColumn
	// One read transaction binds the high-water mark and first-page data to one snapshot.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return p, storageError()
	}
	defer func() { _ = tx.Rollback() }()
	if binding != nil {
		if err := checkBinding(ctx, tx, *binding); err != nil {
			return p, err
		}
	}
	if token != "" {
		c = cursor{}
		b, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil || json.Unmarshal(b, &c) != nil || c.Kind != kind || c.Scope != scopeID || c.Thread != thread || c.Parent != parent || c.Newest != q.newest ||
			(c.Filter != parentColumn && (c.Filter != "" || kind == "pull-requests" || kind == "relations")) || c.Upper < 1 || c.At == "" || c.ID == "" {
			return p, problem("invalid_argument", "invalid or mismatched cursor")
		}
	} else if err = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(seq),0) FROM "+table).Scan(&c.Upper); err != nil {
		return p, storageError()
	}
	projection := projection(kind, binding)
	if q.preview {
		projection = observerListProjection(kind, projection)
	}
	query := "SELECT " + projection + "," + timeCol + "," + idCol + " FROM " + table + " WHERE seq<=?"
	args := []any{c.Upper}
	operator, order := ">", " ASC"
	if q.newest {
		operator, order = "<", " DESC"
	}
	if token != "" {
		query += " AND (" + timeCol + operator + "? OR (" + timeCol + "=? AND " + idCol + operator + "?))"
		args = append(args, c.At, c.At, c.ID)
	}
	if thread != "" {
		if kind == "outbox" {
			query += " AND message_id IN (SELECT id FROM messages WHERE thread_key=?)"
		} else {
			query += " AND thread_key=?"
		}
		args = append(args, thread)
	}
	if parentColumn != "" {
		if relatedFilter == "" {
			relatedFilter = parentColumn + "=?"
		}
		query += " AND " + relatedFilter
		args = append(args, parent)
	}
	query += scope
	args = append(args, scopeArgs...)
	if source != nil {
		at := outboxNow().Format(utcTimestamp)
		query += ` AND source=? AND instance_id=?
 AND NOT EXISTS(SELECT 1 FROM outbox_claims c WHERE c.outbox_id=outbox.id AND c.released_at IS NULL AND c.expires_at>?)
 AND (json_extract(body,'$.status')='pending'
   OR (json_extract(body,'$.status')='unknown' AND json_extract(body,'$.retryAt')<=?)
   OR EXISTS(SELECT 1 FROM outbox_attempts a WHERE a.outbox_id=outbox.id AND ` + outboxRecoverableTask + `))`
		args = append(args, source.Source, source.InstanceID, at, at)
	}
	query += " ORDER BY " + timeCol + order + "," + idCol + order + " LIMIT ?"
	args = append(args, limit+1)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return p, storageError()
	}
	defer func() { _ = rows.Close() }()
	var last cursor
	for rows.Next() {
		var b []byte
		var at, id string
		if err = rows.Scan(&b, &at, &id); err != nil {
			return p, storageError()
		}
		if len(p.Items) == limit {
			b, err = json.Marshal(last)
			if err != nil {
				return p, storageError()
			}
			p.NextCursor = base64.RawURLEncoding.EncodeToString(b)
			break
		}
		p.Items = append(p.Items, json.RawMessage(b))
		last = c
		last.At = at
		last.ID = id
	}
	if rows.Err() != nil {
		return p, storageError()
	}
	return p, nil
}

func (s *Store) Get(ctx context.Context, kind, id string) (json.RawMessage, error) {
	return s.getScoped(ctx, kind, id, nil, nil)
}

func (s *Store) getScoped(ctx context.Context, kind, id string, binding *Binding, source *Source) (json.RawMessage, error) {
	scope, args, err := scopeSQL(kind, binding)
	if err != nil {
		return nil, err
	}
	table, idCol := kind, "id"
	switch kind {
	case "messages", "events", "topics", "decisions", "subscriptions", "relations", "bindings", "deliveries", "batches", "attempts", "recoveries", "issues", "executions", "reports", "timers", "outbox":
	case "threads":
		idCol = "key"
	case "channel-health":
		table = "channel_health"
	case "pull-requests":
		table = "pull_requests"
	default:
		return nil, problem("not_found", "unknown collection")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, storageError()
	}
	defer func() { _ = tx.Rollback() }()
	if binding != nil {
		if err := checkBinding(ctx, tx, *binding); err != nil {
			return nil, err
		}
	}
	var body []byte
	args = append([]any{id}, args...)
	project := projection(kind, binding)
	if kind == "outbox" {
		project = outboxDetailProjection
	}
	if source != nil {
		scope += " AND source=? AND instance_id=?"
		args = append(args, source.Source, source.InstanceID)
	}
	err = tx.QueryRowContext(ctx, "SELECT "+project+" FROM "+table+" WHERE "+idCol+"=?"+scope, args...).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, problem("not_found", "resource not found in current scope")
	}
	if err != nil {
		return nil, storageError()
	}
	return body, nil
}
