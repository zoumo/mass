package metadata

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"

	"github.com/google/uuid"

	"github.com/zoumo/mass/mass-go/pkg/internal/fileio"
	"github.com/zoumo/mass/mass-go/pkg/resource"
)

// This database is the resource authority, never a disposable projection.
type database struct {
	db   *sql.DB
	conn *sql.Conn
	tx   *sql.Tx
}

func (d *database) close() error {
	if d == nil {
		return nil
	}
	var err error
	if d.conn != nil {
		err = d.conn.Close()
	}
	if d.db != nil {
		err = errors.Join(err, d.db.Close())
	}
	return err
}

func OpenSQLite(dir string, readOnly bool) (*Store, error) {
	if !filepath.IsAbs(dir) {
		return nil, errors.New("absolute data directory required")
	}
	if !readOnly {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	if err := fileio.CheckPath(dir, true); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, readOnly: readOnly, sql: &database{}}
	if !readOnly {
		var err error
		s.lock, err = acquireWriter(dir)
		if err != nil {
			return nil, err
		}
	}
	if err := s.openSQL(); err != nil {
		return nil, errors.Join(err, s.Close())
	}
	return s, nil
}

func (s *Store) openSQL() error {
	path := filepath.Join(s.dir, "metadata-authority.sqlite")
	err := fileio.CheckPath(path, false)
	fresh := os.IsNotExist(err)
	if err != nil && !fresh {
		return err
	}
	marker := filepath.Join(s.dir, ".metadata-sqlite.json")
	if !fresh {
		var identity struct {
			Format int `json:"format"`
		}
		if err := fileio.ReadJSON(marker, &identity); err != nil || identity.Format != 1 {
			return errors.New("missing or invalid SQLite authority identity; explicit verification required")
		}
	}
	if fresh {
		if _, err := os.Lstat(marker); !os.IsNotExist(err) {
			return errors.New("SQLite authority is missing; restore authoritative backup, do not rebuild")
		}
		if s.readOnly {
			return os.ErrNotExist
		}
		// Legacy projections and resource files require an explicit migration. Never
		// reclassify them, and never start an empty authority beside existing data.
		for _, name := range []string{"metadata.sqlite", ".store.json", ".pending.json", "state.json", "runtime.json", "runtime.jsonl", "session_index.jsonl", filepath.Join("data", "state.json")} {
			if _, err := os.Lstat(filepath.Join(s.dir, name)); !os.IsNotExist(err) {
				return errors.New("existing metadata requires explicit migration to SQLite authority")
			}
		}
		if err := s.walkSessions(func(path, _, _ string) error {
			if _, err := os.Lstat(filepath.Join(path, "state.json")); !os.IsNotExist(err) {
				return errors.New("existing Session metadata requires explicit migration")
			}
			return nil
		}); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := fileio.AtomicFile(marker, struct {
			Format int `json:"format"`
		}{1}); err != nil {
			return err
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if err != nil {
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	if s.readOnly {
		q.Set("mode", "ro")
	} else {
		q.Set("mode", "rw")
	}
	u.RawQuery = q.Encode()
	s.sql.db, err = sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	s.sql.db.SetMaxOpenConns(1)
	s.sql.conn, err = s.sql.db.Conn(context.Background())
	if err != nil {
		return err
	}
	if _, err = s.sql.conn.ExecContext(context.Background(), `PRAGMA busy_timeout=5000; PRAGMA synchronous=FULL; PRAGMA cache_size=-2048; PRAGMA temp_store=FILE;`); err != nil {
		return err
	}
	if fresh {
		tx, err := s.sql.conn.BeginTx(context.Background(), nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if _, err = tx.ExecContext(context.Background(), `
 CREATE TABLE authority (format INTEGER NOT NULL, cursor_key TEXT NOT NULL);
 CREATE TABLE runtime (name TEXT PRIMARY KEY, type TEXT NOT NULL, state TEXT NOT NULL, payload BLOB NOT NULL);
 CREATE TABLE session (key TEXT PRIMARY KEY, session_id TEXT UNIQUE NOT NULL, namespace TEXT NOT NULL, name TEXT NOT NULL, runtime_ref TEXT NOT NULL, state TEXT NOT NULL, sort_key TEXT NOT NULL, payload BLOB NOT NULL);
 CREATE UNIQUE INDEX session_name ON session(namespace,name);
 `); err != nil {
			return err
		}
		if _, err = tx.ExecContext(context.Background(), "INSERT INTO authority VALUES(1,?)", uuid.NewString()); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		if err := fileio.SyncDirectory(s.dir); err != nil {
			return err
		}
	}
	return s.refreshSQL()
}

func (s *Store) readSQL(tx *sql.Tx) error {
	var err error
	var format int
	s.data = snapshot{Format: 2, Runtimes: map[string]resource.Runtime{}, Sessions: map[string]resource.Session{}}
	if err = tx.QueryRowContext(context.Background(), "SELECT format,cursor_key FROM authority").Scan(&format, &s.data.CursorKey); err != nil {
		return fmt.Errorf("invalid metadata authority (not a rebuildable cache): %w", err)
	}
	if format != 1 {
		return errors.New("unsupported metadata authority format")
	}
	for _, table := range []string{"runtime", "session"} {
		rows, err := tx.QueryContext(context.Background(), "SELECT payload FROM "+table)
		if err != nil {
			return err
		}
		for rows.Next() {
			var b []byte
			if err = rows.Scan(&b); err != nil {
				break
			}
			if table == "runtime" {
				var r resource.Runtime
				err = fileio.Decode(b, &r)
				s.data.Runtimes[r.Metadata.Name] = r
			} else {
				var r resource.Session
				err = fileio.Decode(b, &r)
				s.data.Sessions[key(r.Metadata.Namespace, r.Metadata.Name)] = r
			}
			if err != nil {
				break
			}
		}
		err = errors.Join(err, rows.Err(), rows.Close())
		if err != nil {
			return err
		}
	}
	return s.validate()
}

func (s *Store) refreshSQL() error {
	tx, err := s.sql.conn.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.readSQL(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) beginSQL() error {
	tx, err := s.sql.conn.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	s.sql.tx = tx
	return s.readSQL(tx)
}

func (s *Store) rollbackSQL() {
	if s.sql != nil && s.sql.tx != nil {
		_ = s.sql.tx.Rollback()
		s.sql.tx = nil
	}
}

func (s *Store) commitSQL(next snapshot) error {
	tx := s.sql.tx
	if tx == nil {
		return errors.New("metadata transaction required")
	}
	if err := s.syncMetadata(tx, next, s.data); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		s.uncertain = true
		return fmt.Errorf("outcome_unknown: metadata authority commit: %w", err)
	}
	s.data = next
	return nil
}

func (s *Store) syncMetadata(tx *sql.Tx, next, previous snapshot) error {
	var err error
	// Replace only changed resource rows.
	for name := range previous.Runtimes {
		if _, exists := next.Runtimes[name]; !exists {
			if _, err = tx.ExecContext(context.Background(), "DELETE FROM runtime WHERE name=?", name); err != nil {
				return err
			}
		}
	}
	for k, old := range previous.Sessions {
		current, exists := next.Sessions[k]
		if exists && current.Status.SessionID == old.Status.SessionID {
			continue
		}
		if _, err = tx.ExecContext(context.Background(), "DELETE FROM session WHERE key=?", k); err != nil {
			return err
		}
	}
	for name, r := range next.Runtimes {
		if old, exists := previous.Runtimes[name]; exists && fileio.Equal(old, r) {
			continue
		}
		b, err := json.Marshal(r)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(context.Background(), "INSERT INTO runtime VALUES(?,?,?,?) ON CONFLICT(name) DO UPDATE SET type=excluded.type,state=excluded.state,payload=excluded.payload", r.Metadata.Name, r.Spec.Type, r.Status.State, b); err != nil {
			return err
		}
	}
	for k, r := range next.Sessions {
		if old, exists := previous.Sessions[k]; exists && fileio.Equal(old, r) {
			continue
		}
		b, err := json.Marshal(r)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(context.Background(), "INSERT INTO session VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(key) DO UPDATE SET session_id=excluded.session_id,namespace=excluded.namespace,name=excluded.name,runtime_ref=excluded.runtime_ref,state=excluded.state,sort_key=excluded.sort_key,payload=excluded.payload", key(r.Metadata.Namespace, r.Metadata.Name), r.Status.SessionID, r.Metadata.Namespace, r.Metadata.Name, r.Spec.RuntimeRef, r.Status.State, sessionOrder(r), b); err != nil {
			return err
		}
	}
	return nil
}
