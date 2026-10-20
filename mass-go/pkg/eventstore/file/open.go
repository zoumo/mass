package file

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/google/uuid"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/internal/fileio"
	"github.com/zoumo/mass/mass-go/pkg/resource"
)

type header struct {
	Format    int    `json:"format"`
	CursorKey string `json:"cursorKey"`
}

// Open never reads resource files or metadata SQL. The root may be shared with
// MetaStore, but locks, identities, recovery intents and SQL are owned separately.
func Open(dir string, readOnly bool) (_ *Store, result error) {
	if !filepath.IsAbs(dir) {
		return nil, errors.New("absolute data directory required")
	}
	s := &Store{dir: dir, readOnly: readOnly, identities: map[string]identity{}, histories: map[string]*historyIndex{}, appendOpen: func(path string) (appendFile, error) { return openHistory(path, os.O_RDWR|os.O_APPEND) }}
	defer func() {
		if result != nil {
			result = errors.Join(result, s.Close())
		}
	}()
	if !readOnly {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		if err := fileio.CheckPath(dir, true); err != nil {
			return nil, err
		}
		path := filepath.Join(dir, "events.writer.lock")
		if err := fileio.CheckPath(path, false); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return nil, err
		}
		s.lock = f
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			return nil, fmt.Errorf("event writer already owns directory: %w", err)
		}
	}
	lock, err := s.layoutLock(!readOnly)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	var h header
	path := filepath.Join(dir, ".events.json")
	err = fileio.ReadJSON(path, &h)
	if os.IsNotExist(err) && !readOnly {
		h = header{Format: 1, CursorKey: uuid.NewString()}
		err = fileio.AtomicFile(path, h)
	}
	if err != nil {
		return nil, err
	}
	if h.Format != 1 || h.CursorKey == "" {
		return nil, errors.New("invalid event store format")
	}
	s.cursorKey = h.CursorKey
	if err := s.loadIdentities(); err != nil {
		return nil, err
	}
	if err := s.recoverAppend(!readOnly); err != nil {
		return nil, err
	}
	if err := s.loadHistories(); err != nil {
		return nil, err
	}
	if !readOnly {
		if err := s.finishAppend(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) loadIdentities() error {
	root := filepath.Join(s.dir, "sessions")
	if err := fileio.CheckPath(root, true); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	namespaces, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, ns := range namespaces {
		if !fileio.SafeComponent(ns.Name()) {
			return errors.New("invalid event namespace")
		}
		nsPath := filepath.Join(root, ns.Name())
		if err := fileio.CheckPath(nsPath, true); err != nil {
			return err
		}
		names, err := os.ReadDir(nsPath)
		if err != nil {
			return err
		}
		for _, name := range names {
			if !fileio.SafeComponent(name.Name()) {
				return errors.New("invalid event session name")
			}
			namePath := filepath.Join(nsPath, name.Name())
			if err := fileio.CheckPath(namePath, true); err != nil {
				return err
			}
			// The old coupled layout needs explicit migration, never an implicit import.
			if _, err := os.Lstat(filepath.Join(namePath, "events.jsonl")); !os.IsNotExist(err) {
				return errors.New("legacy combined event layout requires explicit migration")
			}
			logs := filepath.Join(namePath, "eventlogs")
			if err := fileio.CheckPath(logs, true); os.IsNotExist(err) {
				continue
			} else if err != nil {
				return err
			}
			entries, err := os.ReadDir(logs)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				path := filepath.Join(logs, entry.Name())
				if err := fileio.CheckPath(path, true); err != nil {
					return err
				}
				var r identity
				if err := fileio.ReadJSON(filepath.Join(path, "identity.json"), &r); os.IsNotExist(err) {
					// No identity has been committed yet. Only an empty interrupted
					// creation is safe to leave available for the original append retry.
					children, e := os.ReadDir(path)
					if e != nil {
						return e
					}
					empty := true
					for _, child := range children {
						if child.Name() != "events.jsonl" {
							empty = false
							break
						}
						if e := fileio.CheckPath(filepath.Join(path, child.Name()), false); e != nil {
							return e
						}
						info, e := child.Info()
						if e != nil {
							return e
						}
						if info.Size() != 0 {
							empty = false
							break
						}
					}
					if empty {
						continue
					}
					return errors.New("unbound event directory contains unknown data")
				} else if err != nil {
					return err
				}
				if resource.ValidateSessionID(r.SessionID) != nil || r.Namespace != ns.Name() || r.Name != name.Name() || entry.Name() != base64.RawURLEncoding.EncodeToString([]byte(r.SessionID)) {
					return errors.New("event log identity mismatch")
				}
				if _, ok := s.identities[r.SessionID]; ok {
					return errors.New("duplicate event session identity")
				}
				s.identities[r.SessionID] = r
			}
		}
	}
	return nil
}

func (s *Store) bind(id string, records []events.SessionEvent) (identity, error) {
	if err := resource.ValidateSessionID(id); err != nil {
		return identity{}, err
	}
	first := records[0]
	r := identity{SessionID: id, Namespace: first.Namespace, Name: first.SessionName}
	if !fileio.SafeComponent(r.Namespace) || !fileio.SafeComponent(r.Name) {
		return r, errors.New("invalid event log name")
	}
	if old, ok := s.identities[id]; ok {
		r = old
	}
	for _, e := range records {
		if err := validateEvent(r, e); err != nil {
			return r, err
		}
	}
	if _, ok := s.identities[id]; ok {
		return r, nil
	}
	path := s.dir
	for _, part := range []string{"sessions", r.Namespace, r.Name, "eventlogs", base64.RawURLEncoding.EncodeToString([]byte(id))} {
		path = filepath.Join(path, part)
		if err := fileio.EnsureDirectory(path); err != nil {
			return r, err
		}
	}
	log := s.historyPath(r)
	if err := fileio.CheckPath(log, false); os.IsNotExist(err) {
		f, e := os.OpenFile(log, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if e != nil {
			return r, e
		}
		if e := errors.Join(f.Sync(), f.Close(), fileio.SyncDirectory(path)); e != nil {
			return r, e
		}
	} else if err != nil {
		return r, err
	} else {
		info, err := os.Stat(log)
		if err != nil {
			return r, err
		}
		if info.Size() != 0 {
			return r, errors.New("unbound nonempty event log")
		}
	}
	s.uncertain = true
	if err := fileio.AtomicFile(filepath.Join(path, "identity.json"), r); err != nil {
		return r, err
	}
	if _, err := s.sql.conn.ExecContext(context.Background(), "INSERT INTO sync_progress VALUES(?,NULL)", id); err != nil {
		return r, err
	}
	s.identities[id] = r
	s.histories[id] = emptyHistory()
	s.uncertain = false
	return r, nil
}
