package core

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "modernc.org/sqlite"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

const (
	schemaVersion       = 26
	applicationID       = 1398096210
	storeStartupTimeout = 10 * time.Second
	queryIndexTimeout   = 2 * time.Minute
)

type Store struct {
	config               Config
	db                   *sql.DB
	lock                 *os.File
	mu                   sync.Mutex
	telemetry            *coreTelemetry
	queueRegistration    metric.Registration
	hasTraceAssociations bool
}

func Open(c Config) (*Store, error) {
	return openStore(c, TelemetryOptions{})
}

func openStore(c Config, options TelemetryOptions) (_ *Store, retErr error) {
	ctx, cancel := context.WithTimeout(context.Background(), storeStartupTimeout)
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
		if c.Sessions != nil && e.Name() == mcpCredentialsDir {
			info, err := e.Info()
			if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
				return nil, problem("failed_precondition", "Core subdirectory "+e.Name()+" must be private")
			}
			continue
		}
		if !allowed[e.Name()] {
			return nil, problem("failed_precondition", "use a new empty Superagent data directory. Existing files are preserved")
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil, problem("failed_precondition", "data directory contains a non-regular Core file")
		}
	}
	dbPath := filepath.Join(c.DataDir, "core.sqlite")
	if _, err := checkDatabaseHeader(dbPath); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(c.DataDir, ".core.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, problem("unavailable", "cannot open Core ownership lock")
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, problem("conflict", "data directory is already owned by another Core")
	}
	s := &Store{lock: f, config: c, telemetry: newCoreTelemetry(options)}
	defer func() {
		if retErr != nil {
			_ = s.Close()
		}
	}()
	hasHeader, err := checkDatabaseHeader(dbPath)
	if err != nil {
		return nil, err
	}
	df, err := os.OpenFile(dbPath, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, storageError()
	}
	if err = df.Close(); err != nil {
		return nil, storageError()
	}
	dbURL := url.URL{Scheme: "file", Path: dbPath}
	pragmas := url.Values{"_pragma": {"busy_timeout(5000)", "foreign_keys(ON)", "synchronous(FULL)"}}
	if c.StorageMaxPages > 0 {
		pragmas.Add("_pragma", fmt.Sprintf("max_page_count(%d)", c.StorageMaxPages))
	}
	dbURL.RawQuery = pragmas.Encode()
	s.db, err = sql.Open("sqlite", dbURL.String())
	if err != nil {
		return nil, storageError()
	}
	s.db.SetMaxOpenConns(1)

	txCtx := ctx
	buildExistingIndexes := false
	if hasHeader {
		buildExistingIndexes, err = missingQueryIndexes(ctx, s.db)
		if err != nil {
			return nil, storageError()
		}
		if buildExistingIndexes {
			// database/sql rolls back when the BeginTx context expires. Keep
			// the transaction alive for the optional index build while the
			// normal checks and recovery below still use the startup deadline.
			var cancelTx context.CancelFunc
			txCtx, cancelTx = context.WithTimeout(context.Background(), storeStartupTimeout+queryIndexTimeout)
			defer cancelTx()
		}
	}
	tx, err := s.db.BeginTx(txCtx, nil)
	if err != nil {
		return nil, storageError()
	}
	defer func() { _ = tx.Rollback() }()
	// The WAL can contain the current database identity before checkpointing.
	var version, app uint32
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return nil, storageError()
	}
	if err = tx.QueryRowContext(ctx, "PRAGMA application_id").Scan(&app); err != nil {
		return nil, storageError()
	}
	switch {
	case !hasHeader && version == 0 && app == 0:
		// Put the schema identity in the main file before enabling WAL. Startup
		// can then reject earlier databases without touching their sidecars.
		if _, err = tx.ExecContext(ctx, schema+fmt.Sprintf("PRAGMA application_id=%d; PRAGMA user_version=%d;", applicationID, schemaVersion)); err != nil {
			return nil, storageError()
		}
	default:
		if err := checkDatabaseIdentity(version, app); err != nil {
			return nil, err
		}
	}
	if s.telemetry.tracesEnabled {
		if _, err = tx.ExecContext(ctx, traceAssociationSchema); err != nil {
			return nil, storageError()
		}
	}
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type='table' AND name='trace_associations')`).Scan(&s.hasTraceAssociations); err != nil {
		return nil, storageError()
	}
	if err := initializeRouter(ctx, tx, c); err != nil {
		return nil, err
	}
	if err := recoverInternalEvents(ctx, tx); err != nil {
		return nil, err
	}
	if err := recoverStops(ctx, tx); err != nil {
		return nil, err
	}
	if err := recoverIssueObservations(ctx, tx); err != nil {
		return nil, err
	}
	for _, source := range c.Sources {
		var mismatch bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM trigger_messages
 WHERE (source=?1 AND account_id<>?2) OR (account_id=?2 AND source<>?1))`, source.Source, source.AccountID).Scan(&mismatch)
		if err != nil {
			return nil, storageError()
		}
		if mismatch {
			return nil, problem("failed_precondition", "configured source/account differs from admitted messages; use a new dataDir")
		}
	}

	if ctx.Err() != nil {
		return nil, storageError()
	}
	if buildExistingIndexes {
		var cancelBuild context.CancelFunc
		ctx, cancelBuild = context.WithTimeout(txCtx, queryIndexTimeout)
		defer cancelBuild()
	}
	if !hasHeader || buildExistingIndexes {
		for _, index := range queryIndexes {
			if _, err := tx.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS "+index.name+" ON "+index.definition); err != nil {
				return nil, storageError()
			}
		}
	}
	if ctx.Err() != nil {
		return nil, storageError()
	}
	if err = tx.Commit(); err != nil {
		return nil, storageError()
	}
	if _, err = s.db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		return nil, storageError()
	}
	s.queueRegistration = s.telemetry.registerQueueDepth(options.Metrics, s)
	return s, nil
}

func missingQueryIndexes(ctx context.Context, db *sql.DB) (bool, error) {
	names := make([]any, 0, len(queryIndexes))
	for _, index := range queryIndexes {
		names = append(names, index.name)
	}
	var installed int
	err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type='index'
 AND name IN (`+strings.Repeat("?,", len(names)-1)+`?)`, names...).Scan(&installed)
	return installed != len(names), err
}

// Read the main SQLite file directly. Even a read-only SQLite connection can
// create or modify WAL shared-memory files when opening an earlier database.
func checkDatabaseHeader(path string) (bool, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, storageError()
	}
	defer func() { _ = f.Close() }()
	var header [100]byte
	n, err := f.ReadAt(header[:], 0)
	if n == 0 && errors.Is(err, io.EOF) {
		return false, nil
	}
	if err != nil || string(header[:16]) != "SQLite format 3\x00" {
		return false, problem("failed_precondition", "invalid SQLite database; set dataDir to a new empty directory")
	}
	version := binary.BigEndian.Uint32(header[60:64])
	app := binary.BigEndian.Uint32(header[68:72])
	return true, checkDatabaseIdentity(version, app)
}

func checkDatabaseIdentity(version, app uint32) error {
	if app != applicationID {
		return problem("failed_precondition", "database belongs to another format; set dataDir to a new empty directory")
	}
	if version != schemaVersion {
		return problem("unsupported", fmt.Sprintf("schema version %d is unsupported; set dataDir to a new empty directory for schema %d", version, schemaVersion))
	}
	return nil
}

func (s *Store) Close() error {
	var err error
	if s.queueRegistration != nil {
		_ = s.queueRegistration.Unregister()
	}
	if s.db != nil {
		err = s.db.Close()
	}
	if s.lock != nil {
		_ = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
		err = errors.Join(err, s.lock.Close())
	}
	return err
}

func (s *Store) submitMessage(ctx context.Context, source Source, message Message, canonical []byte) (result Receipt, retErr error) {
	ctx, finish := s.telemetry.startOperation(ctx, "message.admit", "")
	defer func() {
		outcome := "accepted"
		if result.Duplicate {
			outcome = "duplicate"
		}
		finish(operationOutcome(retErr, outcome))
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, storageError()
	}
	defer func() { _ = tx.Rollback() }()
	result, err = admitTriggerMessage(ctx, tx, source, message, canonical)
	if err != nil {
		return Receipt{}, err
	}
	commitGate("before_commit")
	if err = tx.Commit(); err != nil {
		return Receipt{}, storageError()
	}
	commitGate("after_commit")
	if s.telemetry.tracesEnabled {
		trace.SpanFromContext(ctx).SetAttributes(messageIDAttribute(result.MessageID))
	}
	return result, nil
}

// admitTriggerMessage takes a validated input and its canonical form. Keeping
// this inside the caller's transaction lets scheduled intake commit its firing
// state with the same Message, Thread and delivery facts as channel intake.
func admitTriggerMessage(ctx context.Context, tx *sql.Tx, source Source, m Message, canonical []byte) (Receipt, error) {
	var result Receipt
	var fingerprint, saved []byte
	var stored Message
	err := tx.QueryRowContext(ctx, "SELECT canonical,body FROM trigger_messages WHERE source=? AND account_id=? AND native_id=?",
		source.Source, source.AccountID, m.SourceMessageID).Scan(&fingerprint, &saved)
	if err == nil {
		if !bytes.Equal(fingerprint, canonical) {
			return result, problem("conflict", "sourceMessageId already has different business content")
		}
		if json.Unmarshal(saved, &stored) != nil {
			return result, storageError()
		}
		return messageReceipt(stored, true), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, storageError()
	}
	if m.InReplyToOutboxID != "" {
		if err := validateReplyReference(ctx, tx, m); err != nil {
			return result, err
		}
	}

	at := now()
	m.ReceivedAt, m.Acceptance = at, "accepted"
	m.EventID = newID("evt")
	if err := upsertThread(ctx, tx, m.ThreadKey, at, at); err != nil {
		return Receipt{}, err
	}
	if err := tx.QueryRowContext(ctx, `INSERT INTO trigger_messages(source,account_id,native_id,thread_key,accepted_at,canonical,body)
 VALUES(?,?,?,?,?,?,'{}') RETURNING id`, source.Source, source.AccountID, m.SourceMessageID, m.ThreadKey, at, canonical).Scan(&m.ID); err != nil {
		return Receipt{}, storageError()
	}
	if err := putJSON(ctx, tx, "UPDATE trigger_messages SET body=?2 WHERE id=?1", m, m.ID); err != nil {
		return Receipt{}, err
	}
	if err := messageAdmission.save(ctx, tx, m.ID, 0); err != nil {
		return Receipt{}, err
	}
	return messageReceipt(m, false), acceptRoute(ctx, tx, m, m.EventID, at)
}

func messageReceipt(m Message, duplicate bool) Receipt {
	return Receipt{
		Status: m.Acceptance, Duplicate: duplicate, MessageID: m.ID,
		ThreadKey: m.ThreadKey, AcceptedAt: m.ReceivedAt, EventID: m.EventID,
	}
}

func (s *Store) channelMessageReceipt(ctx context.Context, source Source, nativeID string) (Receipt, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Receipt{}, storageError()
	}
	defer func() { _ = tx.Rollback() }()
	var m Message
	if err := readJSON(ctx, tx, "SELECT body FROM trigger_messages WHERE source=? AND account_id=? AND native_id=?",
		&m, source.Source, source.AccountID, nativeID); err != nil {
		return Receipt{}, err
	}
	return messageReceipt(m, false), nil
}

func upsertThread(ctx context.Context, tx *sql.Tx, key, createdAt, lastMessageAt string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO threads(key,source,native_id,created_at,last_message_at) VALUES(?,'',?,?,?)
 ON CONFLICT(key) DO UPDATE SET last_message_at=excluded.last_message_at WHERE excluded.last_message_at<>''`,
		key, key, createdAt, lastMessageAt)
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

// A continuation must match the collection, filters, role and ordering of the first page.
func (c *cursor) resume(token string) error {
	var next cursor
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || json.Unmarshal(raw, &next) != nil || next.Upper < 1 || next.At == "" || next.ID == "" {
		return problem("invalid_argument", "invalid or mismatched cursor")
	}
	scope := next
	scope.Upper, scope.At, scope.ID = 0, "", ""
	if scope != *c {
		return problem("invalid_argument", "invalid or mismatched cursor")
	}
	*c = next
	return nil
}

func (s *Store) List(ctx context.Context, kind, thread, token string, limit int) (Page, error) {
	return s.listScoped(ctx, QueryRequest{Collection: kind, ThreadKey: thread, Cursor: token, Limit: limit}, nil, nil)
}

func (s *Store) listScoped(ctx context.Context, q QueryRequest, binding *SessionBinding, source *Source) (Page, error) {
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
		scopeID = strings.ReplaceAll(url.PathEscape(source.Source), ":", "%3A") + ":" + strings.ReplaceAll(url.PathEscape(source.InstanceID), ":", "%3A")
	}
	var parentColumn, parent string
	var relatedFilter string
	switch {
	case q.TopicID != "" && q.IssueID == "" && (kind == "issues" || kind == "timers" || kind == "pull-requests" || kind == "subscriptions" || kind == "relations" || kind == "routing-events"):
		parentColumn, parent = "topic_id", q.TopicID
		if kind == "pull-requests" {
			relatedFilter = "topic_id=? AND issue_id IS NULL"
		}
	case q.TopicID != "" && q.IssueID == "" && binding == nil && kind == "outbox":
		parentColumn, parent = "related_topic", q.TopicID
		relatedFilter = "message_id IN (SELECT message_id FROM topic_message_history WHERE topic_id=?)"
	case q.IssueID != "" && q.TopicID == "" && binding == nil && source == nil && kind == "internal-events":
		parentColumn, parent = "referenced_issue", q.IssueID
		relatedFilter = `EXISTS (SELECT 1 FROM json_each(refs) ref
 WHERE CASE json_extract(ref.value,'$.kind') WHEN 'issue' THEN json_extract(ref.value,'$.id') ELSE json_extract(ref.value,'$.issue_id') END=?)`
	case q.IssueID != "" && q.TopicID == "" && kind == "session-bindings" && binding == nil && source == nil:
		parentColumn, parent = "owner_id", q.IssueID
		relatedFilter = "role='issue' AND owner_id=?"
	case q.IssueID != "" && q.TopicID == "" && (kind == "reports" || kind == "pull-requests"):
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
		table, timeCol, idCol = "trigger_messages", "accepted_at", "id"
	case "threads":
		table, timeCol, idCol = "threads", "created_at", "key"
	case "subscriptions":
		table, timeCol, idCol = "thread_subscriptions", "created_at", "id"
	case "relations":
		table, timeCol, idCol = "routing_relations", "created_at", "id"
	case "internal-events":
		table, timeCol, idCol = "(SELECT rowid AS seq,* FROM internal_events)", "created_at", "event_id"
	case "routing-events":
		table, timeCol, idCol = "routing_events", "created_at", "event_id"
	case "session-bindings":
		table, timeCol, idCol = "session_bindings", "created_at", "id"
	case "outbox":
		table, timeCol, idCol = "(SELECT rowid AS seq,* FROM outbox) AS outbox", "printf('%020d',outbox.seq)", "id"
	case "topics", "issues", "reports":
		table, timeCol, idCol = kind, "created_at", "id"
	case "timers":
		table, timeCol, idCol = "scheduled_tasks", "created_at", "id"
	case "pull-requests":
		table, timeCol, idCol = "work_pull_requests", "created_at", "id"
	default:
		return p, problem("not_found", "unknown collection")
	}
	if thread != "" && kind != "messages" && kind != "subscriptions" && kind != "outbox" {
		return p, problem("invalid_argument", "threadKey filters Messages, subscriptions and Outbox")
	}
	if q.MessageID != "" {
		materialFilter := kind == "issues" && binding == nil && source == nil
		internalEventFilter := kind == "internal-events" && binding == nil && source == nil
		if parent != "" || (!materialFilter && !internalEventFilter && kind != "outbox" && kind != "relations" && kind != "routing-events") {
			return p, problem("invalid_argument", "unsupported messageId filter; use only one parent")
		}
		parentColumn, parent, c.Parent = "message_id", q.MessageID, q.MessageID
		if materialFilter {
			relatedFilter = `EXISTS (SELECT 1 FROM json_each(issues.body,'$.materials') AS m WHERE json_extract(m.value,'$.messageId')=?)`
		}
		if internalEventFilter {
			// The indexed first reference is the Trigger Message for routing events.
			table = "routing_events"
		}
	}
	c.Filter = parentColumn
	if q.search != "" {
		c.Filter += "search:" + q.search
	}
	if q.status != "" {
		c.Filter += "status:" + q.status
	}
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
		if err := c.resume(token); err != nil {
			return p, err
		}
	} else if err = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(seq),0) FROM "+table).Scan(&c.Upper); err != nil {
		return p, storageError()
	}
	projection := projection(kind, binding)
	if kind == "internal-events" && q.MessageID != "" {
		projection = routingEventProjection
	}
	if source != nil {
		projection = channelOutboxProjection
	}
	if q.preview {
		previewKind := kind
		if kind == "internal-events" && table == "routing_events" {
			previewKind = "routing-events"
		}
		projection = observerListProjection(previewKind, projection)
		if q.MessageID != "" && (kind == "issues" || table == "routing_events") {
			projection = "json_set(" + projection + ",'$.topicTitle',(SELECT json_extract(t.body,'$.title') FROM topics t WHERE t.id=" + table + ".topic_id))"
		}
	}
	if q.observerTrace && s.hasTraceAssociations && binding == nil && source == nil {
		projection = observerTraceProjection(kind, idCol, projection)
	}
	query := "SELECT " + projection + "," + timeCol + "," + idCol + " FROM " + table + " WHERE seq<=?"
	args := append(projectionArgs(kind, binding), c.Upper)
	operator, order := ">", " ASC"
	if q.newest {
		operator, order = "<", " DESC"
	}
	if token != "" {
		if kind == "topics" || (kind == "issues" && q.status != "") {
			query += " AND (" + timeCol + "," + idCol + ")" + operator + "(?,?)"
			args = append(args, c.At, c.ID)
		} else {
			query += " AND (" + timeCol + operator + "? OR (" + timeCol + "=? AND " + idCol + operator + "?))"
			args = append(args, c.At, c.At, c.ID)
		}
	}
	if thread != "" {
		if kind == "outbox" {
			query += " AND message_id IN (SELECT id FROM trigger_messages WHERE thread_key=?)"
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
	if q.search != "" {
		query += ` AND (instr(lower(id),lower(?))>0 OR instr(lower(json_extract(body,'$.title')),lower(?))>0)`
		args = append(args, q.search, q.search)
	}
	if q.status != "" {
		query += " AND " + issueStatus + "=?"
		args = append(args, q.status)
	}
	query += scope
	args = append(args, scopeArgs...)
	if source != nil {
		query += channelOutboxScope + ` AND (outbox.status='pending'
 OR (outbox.status='failed' AND json_extract(outbox.receipt,'$.retryAt')<=?))`
		args = append(args, source.Source, source.AccountID, outboxNow().Format(utcTimestamp))
	}
	query += " ORDER BY " + timeCol + order + "," + idCol + order + " LIMIT ?"
	args = append(args, limit+1)
	return readListPage(ctx, tx, query, args, limit, c)
}

func readListPage(ctx context.Context, tx *sql.Tx, query string, args []any, limit int, c cursor) (Page, error) {
	p := Page{Items: []json.RawMessage{}}
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

func (s *Store) getScoped(ctx context.Context, kind, id string, binding *SessionBinding, source *Source) (json.RawMessage, error) {
	return s.getRecord(ctx, kind, id, binding, source, false)
}

func (s *Store) getRecord(ctx context.Context, kind, id string, binding *SessionBinding, source *Source, observerTrace bool) (json.RawMessage, error) {
	scope, args, err := scopeSQL(kind, binding)
	if err != nil {
		return nil, err
	}
	table, idCol := kind, "id"
	switch kind {
	case "messages":
		table = "trigger_messages"
	case "subscriptions":
		table = "thread_subscriptions"
	case "relations":
		table = "routing_relations"
	case "internal-events":
		table, idCol = "internal_events", "event_id"
	case "routing-events":
		table, idCol = "routing_events", "event_id"
	case "session-bindings":
		table = "session_bindings"
	case "topics", "issues", "reports", "outbox":
	case "timers":
		table = "scheduled_tasks"
	case "threads":
		idCol = "key"
	case "pull-requests":
		table = "work_pull_requests"
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
	args = append(append(projectionArgs(kind, binding), id), args...)
	project := projection(kind, binding)
	if kind == "internal-events" {
		project = "COALESCE((SELECT " + routingEventProjection + " FROM routing_events WHERE event_id=internal_events.event_id)," + project + ")"
	}
	if source != nil {
		project = channelOutboxProjection
		scope += channelOutboxScope
		args = append(args, source.Source, source.AccountID)
	}
	if observerTrace && s.hasTraceAssociations && binding == nil && source == nil {
		if kind == "issues" {
			project = observerIssueActivityTraces(observerTraceProjection(kind, "issues.id", project), "issues.id")
		} else {
			project = observerTraceProjection(kind, idCol, project)
		}
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
