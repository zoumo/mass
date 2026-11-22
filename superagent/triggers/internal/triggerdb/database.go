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

// Lock the configured destination before opening it or installing a migration.
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
	if err := initializeTriggerDatabase(db, identity, schema); err != nil {
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

func initializeTriggerDatabase(db *sql.DB, identity Identity, schema string) error {
	var applicationID, version int
	if db.QueryRowContext(context.Background(), "PRAGMA application_id").Scan(&applicationID) != nil ||
		db.QueryRowContext(context.Background(), "PRAGMA user_version").Scan(&version) != nil {
		return errors.New("invalid Trigger database")
	}
	fresh := applicationID == 0 && version == 0
	if fresh {
		var objects int
		if db.QueryRowContext(context.Background(), "SELECT count(*) FROM sqlite_schema").Scan(&objects) != nil || objects != 0 {
			return errors.New("invalid Trigger database type")
		}
	} else {
		if applicationID != identity.ApplicationID || version != identity.Version {
			return errors.New("invalid Trigger database type or schema version")
		}
		var source, accountID, instanceID string
		if err := db.QueryRowContext(context.Background(), "SELECT source, account_id, instance_id FROM trigger_identity WHERE id = 1").
			Scan(&source, &accountID, &instanceID); err != nil {
			return errors.New("invalid Trigger database identity")
		}
		if source != identity.Source || accountID != identity.AccountID || instanceID != identity.InstanceID {
			return errors.New("the Trigger database belongs to a different source, account or instance")
		}
	}
	var journal string
	if err := db.QueryRowContext(context.Background(), "PRAGMA journal_mode = WAL").Scan(&journal); err != nil || journal != "wal" {
		return errors.New("cannot enable WAL for Trigger database")
	}
	if !fresh {
		return nil
	}
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

func CheckDestination(path string) error {
	if err := CheckParent(path); err != nil {
		return fmt.Errorf("databasePath: %w", err)
	}
	return checkTriggerDatabaseFile(path)
}
