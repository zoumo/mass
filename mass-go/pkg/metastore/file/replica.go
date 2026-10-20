package file

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"

	"github.com/zoumo/mass/mass-go/pkg/internal/fileio"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

// A dedicated connection serves a disposable metadata-only query projection.
// File resources remain authoritative; this connection never attaches event SQL.
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
	for _, name := range []string{"metadata.sqlite"} {
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
	for _, name := range []string{"metadata.sqlite"} {
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if err != nil {
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	var err error
	dsn := (&url.URL{Scheme: "file", Path: filepath.Join(dir, "metadata.sqlite")}).String()
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
 CREATE TABLE runtime (name TEXT PRIMARY KEY, type TEXT NOT NULL, state TEXT NOT NULL, payload BLOB NOT NULL);
 CREATE TABLE session (key TEXT PRIMARY KEY, session_id TEXT UNIQUE NOT NULL, namespace TEXT NOT NULL, name TEXT NOT NULL, runtime_ref TEXT NOT NULL, state TEXT NOT NULL, sort_key TEXT NOT NULL, payload BLOB NOT NULL);
 CREATE INDEX session_order ON session(sort_key);
 `)
	return err
}

func (s *Store) syncMetadata(next, previous snapshot) error {
	tx, err := s.sql.conn.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
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
	return tx.Commit()
}

func (s *Store) loadMetadata() error {
	if err := s.openReplicas(); err != nil {
		return err
	}
	return s.syncMetadata(s.data, snapshot{})
}

func (s *Store) sqlRuntime(name string) (resource.Runtime, error) {
	var r resource.Runtime
	var b []byte
	err := s.sql.conn.QueryRowContext(context.Background(), "SELECT payload FROM runtime WHERE name=?", name).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return r, store.ErrNotFound
	}
	if err != nil {
		return r, err
	}
	err = fileio.Decode(b, &r)
	return r, err
}

func (s *Store) sqlSession(ref resource.SessionRef) (resource.Session, error) {
	var r resource.Session
	query := "SELECT payload FROM session WHERE key=?"
	value := key(ref.Namespace, ref.Name)
	if ref.SessionID != "" {
		if ref.Namespace != "" || ref.Name != "" {
			return r, runtime.InvalidArgument("session reference forms are mutually exclusive")
		}
		query = "SELECT payload FROM session WHERE session_id=?"
		value = ref.SessionID
	} else if ref.Namespace == "" || ref.Name == "" {
		return r, runtime.InvalidArgument("invalid session reference")
	}
	var b []byte
	err := s.sql.conn.QueryRowContext(context.Background(), query, value).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return r, store.ErrNotFound
	}
	if err != nil {
		return r, err
	}
	err = fileio.Decode(b, &r)
	return r, err
}
