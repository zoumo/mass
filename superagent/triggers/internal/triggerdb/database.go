package triggerdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"syscall"

	_ "modernc.org/sqlite"
)

type Identity struct {
	ApplicationID int
	Version       int
	Source        string
	AccountID     string
	InstanceID    string
}

// Lock the configured destination before opening it.
func lockTriggerDatabase(path string) (canonical string, unlock func(), err error) {
	if !filepath.IsAbs(path) {
		return "", nil, errors.New("databasePath must be an absolute path")
	}
	path = filepath.Clean(path)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, errors.New("cannot create Trigger database directory")
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return "", nil, errors.New("cannot resolve Trigger database directory")
	}
	path = filepath.Join(dir, filepath.Base(path))
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return "", nil, errors.New("cannot open Trigger database lock")
	}
	unlock = func() { _ = lock.Close() }
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		unlock()
		return "", nil, errors.New("cannot acquire Trigger database lock")
	}
	return path, unlock, nil
}

func Open(path string, identity Identity, schema string) (*sql.DB, func(), error) {
	path, unlock, err := lockTriggerDatabase(path)
	if err != nil {
		return nil, nil, err
	}
	db, err := openLockedTriggerDatabase(path, identity, schema)
	if err != nil {
		unlock()
		return nil, nil, err
	}
	return db, func() {
		_ = db.Close()
		unlock()
	}, nil
}

// The caller owns the destination lock or provides a private temporary path.
func openLockedTriggerDatabase(path string, identity Identity, schema string) (*sql.DB, error) {
	fresh := true
	created, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	switch {
	case err == nil:
		if err := created.Close(); err != nil {
			return nil, errors.New("cannot create Trigger database")
		}
	case !errors.Is(err, os.ErrExist):
		return nil, errors.New("cannot create Trigger database")
	default:
		if err := checkTriggerDatabaseFile(path); err != nil {
			return nil, err
		}
		fresh, err = checkTriggerDatabase(path, identity)
		if err != nil {
			return nil, err
		}
	}
	uri := url.URL{Scheme: "file", Path: path}
	options := uri.Query()
	options.Set("mode", "rw")
	options.Add("_pragma", "busy_timeout(2000)")
	options.Add("_pragma", "synchronous(FULL)")
	options.Add("_pragma", "foreign_keys(1)")
	uri.RawQuery = options.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, errors.New("cannot open Trigger database")
	}
	db.SetMaxOpenConns(1)
	if err := initializeTriggerDatabase(db, identity, schema, fresh); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// A missing file is a valid destination. Existing databases must be private and
// use one path so that the process lock protects every writer.
func checkTriggerDatabaseFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("the Trigger database must be a private regular file")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Nlink != 1 {
		return errors.New("the Trigger database requires a single file path")
	}
	return nil
}

// Probe the existing file without creating or altering SQLite sidecars.
func checkTriggerDatabase(path string, identity Identity) (fresh bool, err error) {
	uri := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&immutable=1"}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return false, errors.New("cannot inspect Trigger database")
	}
	defer db.Close()
	ctx := context.Background()
	var applicationID, version int
	if db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&applicationID) != nil ||
		db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version) != nil {
		return false, errors.New("invalid Trigger database")
	}
	if applicationID == 0 && version == 0 {
		// Older databases could first write their identity to WAL. The
		// immutable probe cannot treat that database as an empty file.
		if info, err := os.Stat(path + "-wal"); err == nil && info.Size() != 0 {
			return false, errors.New("cannot identify Trigger database with an uncheckpointed schema. Configure a new databasePath")
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, errors.New("cannot inspect Trigger database WAL")
		}
		var objects int
		if db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema").Scan(&objects) != nil || objects != 0 {
			return false, errors.New("invalid Trigger database type")
		}
		return true, nil
	}
	if applicationID != identity.ApplicationID {
		return false, errors.New("invalid Trigger database type")
	}
	if version != identity.Version {
		return false, fmt.Errorf("unsupported Trigger database schema version %d (expected %d). Configure a new databasePath", version, identity.Version)
	}
	var source, accountID, instanceID string
	if db.QueryRowContext(ctx, "SELECT source, account_id, instance_id FROM trigger_identity WHERE id = 1").
		Scan(&source, &accountID, &instanceID) != nil {
		return false, errors.New("invalid Trigger database identity")
	}
	if source != identity.Source || accountID != identity.AccountID || instanceID != identity.InstanceID {
		return false, errors.New("the Trigger database belongs to a different source, account or instance")
	}
	return false, nil
}

func initializeTriggerDatabase(db *sql.DB, identity Identity, schema string, fresh bool) error {
	// Store the identity and schema in the main file before enabling WAL so
	// the next startup can verify the version even after an unclean exit.
	if fresh {
		if err := createTriggerSchema(db, identity, schema); err != nil {
			return err
		}
	}
	var journal string
	if err := db.QueryRowContext(context.Background(), "PRAGMA journal_mode = WAL").Scan(&journal); err != nil || journal != "wal" {
		return errors.New("cannot enable WAL for Trigger database")
	}
	return nil
}

func createTriggerSchema(db *sql.DB, identity Identity, schema string) error {
	err := Transaction(db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(context.Background(), `CREATE TABLE trigger_identity (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			source TEXT NOT NULL,
			account_id TEXT NOT NULL,
			instance_id TEXT NOT NULL
		)`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(context.Background(), `INSERT INTO trigger_identity VALUES (1, ?, ?, ?)`,
			identity.Source, identity.AccountID, identity.InstanceID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(context.Background(), schema); err != nil {
			return err
		}
		_, err := tx.ExecContext(context.Background(), fmt.Sprintf("PRAGMA application_id = %d; PRAGMA user_version = %d", identity.ApplicationID, identity.Version))
		return err
	})
	if err != nil {
		return errors.New("cannot initialize Trigger database")
	}
	return nil
}

// Transactions finish durably even when the Trigger is shutting down.
func Transaction(db *sql.DB, apply func(*sql.Tx) error) error {
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := apply(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func CheckParent(path string) error {
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		info, err := os.Stat(dir)
		switch {
		case err == nil && info.IsDir():
			return nil
		case !errors.Is(err, os.ErrNotExist), filepath.Dir(dir) == dir:
			return errors.New("cannot resolve the state directory")
		}
	}
}

func CheckDestination(path string, identity Identity) error {
	if err := CheckParent(path); err != nil {
		return fmt.Errorf("databasePath: %w", err)
	}
	if err := checkTriggerDatabaseFile(path); err != nil {
		return err
	}
	if _, err := os.Lstat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return errors.New("cannot inspect Trigger database")
	}
	_, err := checkTriggerDatabase(path, identity)
	return err
}
