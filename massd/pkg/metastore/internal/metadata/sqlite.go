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

	_ "embed"
	_ "modernc.org/sqlite"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"

	"github.com/zoumo/mass/massd/pkg/internal/fileio"
	"github.com/zoumo/mass/massd/pkg/metastore/internal/metadata/ent"
	"github.com/zoumo/mass/massd/pkg/metastore/internal/metadata/ent/runtimerecord"
	entsession "github.com/zoumo/mass/massd/pkg/metastore/internal/metadata/ent/session"
	"github.com/zoumo/mass/massd/pkg/resource"
)

//go:embed migrations/001_authority.sql
var authorityV1DDL string

// This database is the resource authority, never a disposable projection.
type database struct {
	db   *sql.DB
	conn *sql.Conn
	tx   *sql.Tx
}

// The Store owns commit, rollback and connection lifetime, including when Ent
// requests a transaction for an entity write.
type transactionDriver struct {
	entsql.Conn
}

func (d transactionDriver) Tx(context.Context) (dialect.Tx, error) {
	return dialect.NopTx(d), nil
}

func (transactionDriver) Dialect() string { return dialect.SQLite }
func (transactionDriver) Close() error    { return nil }

func metadataClient(tx *sql.Tx) *ent.Client {
	return ent.NewClient(ent.Driver(transactionDriver{Conn: entsql.Conn{ExecQuerier: tx}}))
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
		if _, err := os.Lstat(filepath.Join(s.dir, "metadata.sqlite")); !os.IsNotExist(err) {
			return errors.New("legacy metadata.sqlite requires explicit migration to SQLite authority")
		}
		// Refuse a directory owned by the file MetaStore, even when its
		// identity marker is missing or a commit is still pending.
		for _, name := range []string{".store.json", ".pending.json", "runtime.jsonl", "session_index.jsonl"} {
			if _, err := os.Lstat(filepath.Join(s.dir, name)); !os.IsNotExist(err) {
				return errors.New("existing file metadata requires explicit migration to SQLite authority")
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
		if _, err = tx.ExecContext(context.Background(), authorityV1DDL); err != nil {
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
	ctx := context.Background()
	var format int
	s.data = snapshot{Format: 2, Runtimes: map[string]resource.Runtime{}, Sessions: map[string]resource.Session{}}
	if err := tx.QueryRowContext(ctx, "SELECT format,cursor_key FROM authority").Scan(&format, &s.data.CursorKey); err != nil {
		return fmt.Errorf("invalid metadata authority (not a rebuildable cache): %w", err)
	}
	if format != 1 {
		return errors.New("unsupported metadata authority format")
	}
	// The full resource payload is the source for snapshot validation.
	client := metadataClient(tx)
	runtimes, err := client.RuntimeRecord.Query().Select(runtimerecord.FieldPayload).All(ctx)
	if err != nil {
		return err
	}
	for _, row := range runtimes {
		var r resource.Runtime
		if err := fileio.Decode(row.Payload, &r); err != nil {
			return err
		}
		s.data.Runtimes[r.Metadata.Name] = r
	}
	sessions, err := client.Session.Query().Select(entsession.FieldPayload).All(ctx)
	if err != nil {
		return err
	}
	for _, row := range sessions {
		var r resource.Session
		if err := fileio.Decode(row.Payload, &r); err != nil {
			return err
		}
		s.data.Sessions[key(r.Metadata.Namespace, r.Metadata.Name)] = r
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
	ctx := context.Background()
	client := metadataClient(tx)
	// Replace only changed resource rows.
	for name := range previous.Runtimes {
		if _, exists := next.Runtimes[name]; !exists {
			if _, err := client.RuntimeRecord.Delete().Where(runtimerecord.IDEQ(name)).Exec(ctx); err != nil {
				return err
			}
		}
	}
	for k, old := range previous.Sessions {
		current, exists := next.Sessions[k]
		if exists && current.Status.SessionID == old.Status.SessionID {
			continue
		}
		if _, err := client.Session.Delete().Where(entsession.IDEQ(k)).Exec(ctx); err != nil {
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
		err = client.RuntimeRecord.Create().
			SetID(r.Metadata.Name).
			SetType(r.Spec.Type).
			SetState(r.Status.State).
			SetPayload(b).
			OnConflictColumns(runtimerecord.FieldID).
			UpdateNewValues().
			Exec(ctx)
		if err != nil {
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
		err = client.Session.Create().
			SetID(k).
			SetSessionID(r.Status.SessionID).
			SetNamespace(r.Metadata.Namespace).
			SetName(r.Metadata.Name).
			SetRuntimeRef(r.Spec.RuntimeRef).
			SetState(string(r.Status.State)).
			SetSortKey(sessionOrder(r)).
			SetPayload(b).
			OnConflictColumns(entsession.FieldID).
			UpdateNewValues().
			Exec(ctx)
		if err != nil {
			return err
		}
	}
	return nil
}
