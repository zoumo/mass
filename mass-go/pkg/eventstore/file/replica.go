package file

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/internal/fileio"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

// One connection owns the event-only disk projection. No metadata database is
// attached; a transaction commits event rows and progress together.
type replicas struct {
	db          *sql.DB
	conn        *sql.Conn
	temporary   string
	version     int64
	hasTriggers bool
}

func (r *replicas) close() error {
	if r == nil {
		return nil
	}
	var err error
	if r.conn != nil {
		err = r.conn.Close()
	}
	if r.db != nil {
		err = errors.Join(err, r.db.Close())
	}
	if r.temporary != "" {
		err = errors.Join(err, os.RemoveAll(r.temporary))
	}
	return err
}

func (s *Store) openReplicas() error {
	if err := s.sql.close(); err != nil {
		return err
	}
	s.sql = nil
	dir := s.dir
	r := &replicas{}
	if s.readOnly {
		var err error
		dir, err = os.MkdirTemp("", "mass-replicas-*")
		if err != nil {
			return err
		}
		r.temporary = dir
	}
	s.sql = r // Open failure is cleaned up by Store.Close.
	// These fixed names contain only disposable derived data. Reject symlinks and
	// nonregular entries rather than following or recursively removing user paths.
	for _, name := range []string{"history.sqlite"} {
		for _, suffix := range []string{"", "-journal", "-wal", "-shm"} {
			path := filepath.Join(dir, name+suffix)
			if err := fileio.CheckPath(path, false); os.IsNotExist(err) {
				continue
			} else if err != nil {
				return err
			}
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}
	// Precreate with private permissions, independent of the driver's default mode.
	for _, name := range []string{"history.sqlite"} {
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if err != nil {
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	var err error
	dsn := (&url.URL{Scheme: "file", Path: filepath.Join(dir, "history.sqlite")}).String()
	r.db, err = sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	r.db.SetMaxOpenConns(1)
	r.conn, err = r.db.Conn(context.Background())
	if err != nil {
		return err
	}
	_, err = r.conn.ExecContext(context.Background(), `
 PRAGMA main.journal_mode=DELETE;
 PRAGMA main.synchronous=FULL;
 PRAGMA main.cache_size=-2048;
 PRAGMA temp_store=FILE;
 CREATE TABLE event (session_id TEXT NOT NULL, position INTEGER NOT NULL, event_id TEXT NOT NULL, type TEXT NOT NULL, payload BLOB NOT NULL, PRIMARY KEY(session_id,position), UNIQUE(session_id,event_id));
 CREATE INDEX event_type ON event(session_id,type,position);
 CREATE TABLE sync_progress (session_id TEXT PRIMARY KEY, event_id TEXT, file_count INTEGER NOT NULL, file_end INTEGER NOT NULL, file_hash TEXT NOT NULL);
 `)
	if err != nil {
		return err
	}
	return r.conn.QueryRowContext(context.Background(), "PRAGMA data_version").Scan(&r.version)
}

func insertEvent(tx *sql.Tx, id string, position int, e events.SessionEvent, line []byte) error {
	_, err := tx.ExecContext(context.Background(), "INSERT INTO event VALUES(?,?,?,?,?)", id, position, e.EventID, e.Type(), line)
	return err
}

func (s *Store) loadHistories() error {
	if err := s.openReplicas(); err != nil {
		return err
	}
	if err := s.seedProgress(); err != nil {
		return err
	}
	indexes := make(map[string]*historyIndex, len(s.identities))
	for _, r := range s.identities {
		index := emptyHistory()
		f, err := openHistory(s.historyPath(r), os.O_RDONLY)
		if err != nil {
			return err
		}
		err = s.restoreHistory(f, r, index)
		if err == nil {
			err = s.checkCommit(r, index)
		}
		if err := errors.Join(err, f.Close()); err != nil {
			return err
		}
		indexes[r.SessionID] = index
	}
	s.histories = indexes
	return nil
}

// Validate every committed public history before extending any recovery tail.
// For Reconcile, also compare the original process checkpoints before mutation.
// SQL supplies bounded identity validation; only one checkpoint per Session is
// retained. Driver-private recovery validation remains outside Store.
func (s *Store) restoreBeforeAppend(intent appendIntent) (*historyIndex, error) {
	if intent.Offset < 0 {
		return nil, errors.New("invalid append offset")
	}
	if err := s.openReplicas(); err != nil {
		return nil, err
	}
	if err := s.seedProgress(); err != nil {
		return nil, err
	}
	var target *historyIndex
	for _, r := range s.identities {
		f, err := openHistory(s.historyPath(r), os.O_RDONLY)
		if err != nil {
			return nil, err
		}
		err = func() error {
			var reader io.Reader = f
			if r.SessionID == intent.SessionID {
				reader = io.NewSectionReader(f, 0, intent.Offset)
			}
			if old := s.histories[r.SessionID]; old != nil {
				prefix, e := scanHistory(io.NewSectionReader(f, 0, old.End), r)
				if e != nil || prefix.End != old.End || prefix.Hash != old.Hash {
					return errors.New("committed history prefix changed or missing")
				}
			}
			index := emptyHistory()
			if e := s.restoreHistory(reader, r, index); e != nil {
				return e
			}
			if r.SessionID == intent.SessionID {
				if err := s.checkPendingCommit(f, r, intent, index); err != nil {
					return err
				}
				target = index
			} else if err := s.checkCommit(r, index); err != nil {
				return err
			}
			return nil
		}()
		if err := errors.Join(err, f.Close()); err != nil {
			return nil, err
		}
	}
	if target == nil {
		return nil, errors.New("append Session missing")
	}
	return target, nil
}

// Each transaction contains at most 128 streamed records; no all-history Go index.
func (s *Store) restoreHistory(f io.Reader, r identity, index *historyIndex) error {
	var tx *sql.Tx
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()
	// Every restored batch advances public progress in the same transaction.
	flush := func() error {
		if err := checkpoint(tx, r.SessionID, index); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		tx = nil
		return nil
	}
	err := eachEvent(f, r, func(e events.SessionEvent, line []byte) error {
		if tx == nil {
			var err error
			tx, err = s.sql.conn.BeginTx(context.Background(), nil)
			if err != nil {
				return err
			}
		}
		if err := index.add(e, line); err != nil {
			return err
		}
		if err := insertEvent(tx, r.SessionID, index.Count, e, line); err != nil {
			return fmt.Errorf("invalid history identity: %w", err)
		}
		if index.Count%128 == 0 {
			return flush()
		}
		return nil
	})
	if err != nil {
		return err
	}
	if tx != nil {
		return flush()
	}
	return nil
}

func (s *Store) syncAppend(tx *sql.Tx, r identity, intent appendIntent, old *historyIndex) error {
	updated := *old
	if err := eachEvent(bytes.NewReader(intent.Data), r, func(e events.SessionEvent, line []byte) error {
		if err := updated.add(e, line); err != nil {
			return err
		}
		return insertEvent(tx, r.SessionID, updated.Count, e, line)
	}); err != nil {
		return err
	}
	if err := checkpoint(tx, r.SessionID, &updated); err != nil {
		return err
	}
	if s.sql.hasTriggers {
		if err := s.verifyAppend(tx, r.SessionID, &updated); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) storedEvent(q sqlQueryer, id, eventID string) (events.SessionEvent, bool, error) {
	var b []byte
	err := q.QueryRowContext(context.Background(), "SELECT payload FROM event WHERE session_id=? AND event_id=?", id, eventID).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return events.SessionEvent{}, false, nil
	}
	if err != nil {
		return events.SessionEvent{}, false, err
	}
	var e events.SessionEvent
	err = events.DecodeSessionEvent(b, &e)
	return e, true, err
}

func (s *Store) sqlProgress(q sqlQueryer, id string) (*string, error) {
	var idValue *string
	err := q.QueryRowContext(context.Background(), "SELECT event_id FROM sync_progress WHERE session_id=?", id).Scan(&idValue)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return idValue, err
}

func (s *Store) sqlHistory(q sqlQueryer, id string, types []string, start, limit int) (store.HistoryPage, error) {
	progress, err := s.sqlProgress(q, id)
	if err != nil {
		return store.HistoryPage{}, err
	}
	page := store.HistoryPage{Events: []events.SessionEvent{}, SyncedEventID: progress}
	query := "SELECT position,payload FROM event WHERE session_id=? AND position>?"
	args := []any{id, start}
	if len(types) > 0 {
		query += " AND type IN (" + strings.TrimSuffix(strings.Repeat("?,", len(types)), ",") + ")"
		for _, t := range types {
			args = append(args, t)
		}
	}
	query += " ORDER BY position LIMIT ?"
	args = append(args, int64(limit)+1)
	rows, err := q.QueryContext(context.Background(), query, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	last := start
	for rows.Next() {
		var pos int
		var b []byte
		if err := rows.Scan(&pos, &b); err != nil {
			return page, err
		}
		if len(page.Events) == limit {
			page.Next, err = s.encodeCursor(cursor{id, types, last})
			if err != nil {
				return page, err
			}
			break
		}
		var e events.SessionEvent
		if err := events.DecodeSessionEvent(b, &e); err != nil {
			return page, err
		}
		page.Events = append(page.Events, e)
		last = pos
	}
	return page, rows.Err()
}

func (s *Store) seedProgress() error {
	tx, err := s.sql.conn.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for id := range s.identities {
		if _, err := tx.ExecContext(context.Background(), "INSERT INTO sync_progress VALUES(?,NULL,0,0,?)", id, hex.EncodeToString(make([]byte, 32))); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Event rows and their consumed file boundary advance in one SQL transaction.
// These values are disposable checks, never authority for skipping file validation.
func checkpoint(tx *sql.Tx, id string, index *historyIndex) error {
	result, err := tx.ExecContext(context.Background(), "UPDATE sync_progress SET event_id=?,file_count=?,file_end=?,file_hash=? WHERE session_id=?", index.progress(), index.Count, index.End, hex.EncodeToString(index.Hash[:]), id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("event SQL checkpoint update did not affect one Session")
	}
	return nil
}

// Every public operation uses one SQLite snapshot. An external connection may
// create harmless triggers or alter derived data: data_version makes the common
// path constant-cost, while changes require a bounded verification of all rows.
type sqlQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (s *Store) projectionTx() (*sql.Tx, error) {
	tx, err := s.sql.conn.BeginTx(context.Background(), nil)
	if err == nil {
		err = s.verifyProjection(tx)
	}
	if err != nil {
		if tx != nil {
			_ = tx.Rollback()
		}
		s.uncertain = true
		return nil, fmt.Errorf("outcome_unknown: event SQL projection unavailable: %w", err)
	}
	return tx, nil
}

func (s *Store) verifyProjection(tx *sql.Tx) error {
	var version int64
	if err := tx.QueryRowContext(context.Background(), "PRAGMA data_version").Scan(&version); err != nil {
		return err
	}
	if version == s.sql.version {
		return nil
	}
	if err := s.verifyRows(tx); err != nil {
		return err
	}
	if err := s.verifyProgress(tx); err != nil {
		return err
	}
	var triggers int
	if err := tx.QueryRowContext(context.Background(), "SELECT count(*) FROM sqlite_schema WHERE type='trigger'").Scan(&triggers); err != nil {
		return err
	}
	s.sql.hasTriggers = triggers != 0
	s.sql.version = version
	return nil
}

func (s *Store) verifyRows(tx *sql.Tx) error {
	rows, err := tx.QueryContext(context.Background(), "SELECT session_id,position,event_id,type,payload FROM event ORDER BY session_id,position")
	if err != nil {
		return err
	}
	defer rows.Close()
	current := ""
	index := emptyHistory()
	finish := func() error {
		if current != "" && (s.histories[current] == nil || *s.histories[current] != *index) {
			return errors.New("event SQL rows differ from file checkpoint")
		}
		return nil
	}
	for rows.Next() {
		var id, eventID, kind string
		var position int
		var line []byte
		if err := rows.Scan(&id, &position, &eventID, &kind, &line); err != nil {
			return err
		}
		if id != current {
			if err := finish(); err != nil {
				return err
			}
			current, index = id, emptyHistory()
		}
		r, ok := s.identities[id]
		if !ok {
			return errors.New("SQL-only Session")
		}
		var e events.SessionEvent
		if err := events.DecodeSessionEvent(line, &e); err != nil {
			return err
		}
		if err := validateEvent(r, e); err != nil {
			return err
		}
		if position != index.Count+1 || eventID != e.EventID || kind != e.Type() {
			return errors.New("event SQL identity or order differs from file")
		}
		if err := index.add(e, line); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := finish(); err != nil {
		return err
	}
	// Missing entire Sessions must also fail; an indexed count catches absent rows
	// without retaining any event identity or payload in application memory.
	for id, expected := range s.histories {
		var count int
		if err := tx.QueryRowContext(context.Background(), "SELECT count(*) FROM event WHERE session_id=?", id).Scan(&count); err != nil {
			return err
		}
		if count != expected.Count {
			return errors.New("event SQL count differs from file")
		}
	}
	return nil
}

func (s *Store) verifyProgress(tx *sql.Tx) error {
	rows, err := tx.QueryContext(context.Background(), "SELECT session_id,event_id,file_count,file_end,file_hash FROM sync_progress")
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id, hash string
		var last *string
		var n int
		var end int64
		if err := rows.Scan(&id, &last, &n, &end, &hash); err != nil {
			return err
		}
		expected := s.histories[id]
		if expected == nil || n != expected.Count || end != expected.End || hash != hex.EncodeToString(expected.Hash[:]) || (last == nil) != (expected.Count == 0) || last != nil && *last != expected.Last {
			return errors.New("event SQL progress differs from file checkpoint")
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count != len(s.histories) {
		return errors.New("missing event SQL progress")
	}
	return nil
}

// External triggers can silently change rows in this connection, which does not
// change data_version. For that exceptional schema, verify the transaction's
// actual result against the new file boundary before acknowledging its commit.
func (s *Store) verifyAppend(tx *sql.Tx, id string, updated *historyIndex) error {
	old := s.histories[id]
	s.histories[id] = updated
	defer func() { s.histories[id] = old }()
	if err := s.verifyRows(tx); err != nil {
		return err
	}
	return s.verifyProgress(tx)
}
