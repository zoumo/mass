package file

import (
	"bytes"
	"context"
	"database/sql"
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
	db        *sql.DB
	conn      *sql.Conn
	temporary string
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
 CREATE TABLE sync_progress (session_id TEXT PRIMARY KEY, event_id TEXT);
 `)
	return err
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
				target = index
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
		if _, err := tx.ExecContext(context.Background(), "UPDATE sync_progress SET event_id=? WHERE session_id=?", index.progress(), r.SessionID); err != nil {
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

func (s *Store) syncAppend(r identity, intent appendIntent, old *historyIndex) error {
	tx, err := s.sql.conn.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	position := old.Count
	var last string
	if err := eachEvent(bytes.NewReader(intent.Data), r, func(e events.SessionEvent, line []byte) error {
		position++
		last = e.EventID
		return insertEvent(tx, r.SessionID, position, e, line)
	}); err != nil {
		return err
	}
	if _, err = tx.ExecContext(context.Background(), "UPDATE sync_progress SET event_id=? WHERE session_id=?", last, r.SessionID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) storedEvent(id, eventID string) (events.SessionEvent, bool, error) {
	var b []byte
	err := s.sql.conn.QueryRowContext(context.Background(), "SELECT payload FROM event WHERE session_id=? AND event_id=?", id, eventID).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return events.SessionEvent{}, false, nil
	}
	if err != nil {
		return events.SessionEvent{}, false, err
	}
	var e events.SessionEvent
	err = fileio.Decode(b, &e)
	return e, true, err
}

func (s *Store) sqlProgress(id string) (*string, error) {
	var idValue *string
	err := s.sql.conn.QueryRowContext(context.Background(), "SELECT event_id FROM sync_progress WHERE session_id=?", id).Scan(&idValue)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return idValue, err
}

func (s *Store) sqlHistory(id string, types []string, start, limit int) (store.HistoryPage, error) {
	progress, err := s.sqlProgress(id)
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
	rows, err := s.sql.conn.QueryContext(context.Background(), query, args...)
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
		if err := fileio.Decode(b, &e); err != nil {
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
		if _, err := tx.ExecContext(context.Background(), "INSERT INTO sync_progress VALUES(?,NULL)", id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
