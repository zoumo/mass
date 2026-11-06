// Package metadata implements shared resource rules for the selected authority.
// One writer owns the root; a durable redo record coordinates multi-file changes.
package metadata

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/internal/fileio"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

type snapshot struct {
	Format    int                         `json:"format"`
	CursorKey string                      `json:"cursorKey"`
	Runtimes  map[string]resource.Runtime `json:"runtimes"`
	Sessions  map[string]resource.Session `json:"sessions"`
}
type Store struct {
	mu        sync.Mutex
	dir       string
	lock      *os.File
	readOnly  bool
	closed    bool
	uncertain bool
	data      snapshot
	sql       *database
}

func key(ns, name string) string { return ns + "/" + name }
func OpenFile(dir string, readOnly bool) (*Store, error) {
	if !filepath.IsAbs(dir) {
		return nil, errors.New("absolute data directory required")
	}

	s := &Store{dir: dir, readOnly: readOnly}
	if !readOnly {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		if err := fileio.CheckPath(dir, true); err != nil {
			return nil, err
		}
		var err error
		s.lock, err = acquireWriter(dir)
		if err != nil {
			return nil, err
		}
	}
	// Check authority selection under writer ownership before creating file resources.
	for _, name := range []string{".metadata-sqlite.json", "metadata-authority.sqlite"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			return nil, errors.Join(s.Close(), errors.New("SQLite authority exists: explicit migration is required before selecting file"))
		}
	}
	err := s.load(!readOnly)
	if err == nil && !readOnly {
		err = s.finishMetadata()
	}
	if err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// Both authorities use the same nonblocking ownership boundary.
func acquireWriter(dir string) (*os.File, error) {
	path := filepath.Join(dir, "writer.lock")
	if err := fileio.CheckPath(path, false); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, errors.Join(fmt.Errorf("writer already owns directory: %w", err), f.Close())
	}
	return f, nil
}

func (s *Store) validate() error {
	d := s.data
	if d.Format != 2 || d.CursorKey == "" || d.Runtimes == nil || d.Sessions == nil {
		return errors.New("invalid snapshot format")
	}
	ids := map[string]resource.Session{}
	for k, r := range d.Runtimes {
		if k != r.Metadata.Name || r.Metadata.Namespace != "" || r.Metadata.UID == "" || r.Metadata.ResourceVersion == "" || r.Metadata.Generation == 0 || r.Metadata.CreatedAt.IsZero() {
			return errors.New("invalid runtime record")
		}
	}
	for k, r := range d.Sessions {
		if k != key(r.Metadata.Namespace, r.Metadata.Name) || !fileio.SafeComponent(r.Metadata.Namespace) || !fileio.SafeComponent(r.Metadata.Name) || r.Metadata.UID == "" || r.Metadata.ResourceVersion == "" || r.Metadata.Generation == 0 || r.Metadata.CreatedAt.IsZero() || resource.ValidateSessionID(r.Status.SessionID) != nil {
			return errors.New("invalid session record")
		}
		if _, ok := ids[r.Status.SessionID]; ok {
			return errors.New("duplicate native session identity")
		}
		ids[r.Status.SessionID] = r
	}
	return nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	replicaErr := s.sql.close()
	if s.lock != nil {
		err := syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
		return errors.Join(replicaErr, err, s.lock.Close())
	}
	return replicaErr
}

func (s *Store) usable(write bool) error {
	if s.closed {
		return errors.New("store closed")
	}
	if write && s.readOnly {
		return store.ErrReadOnly
	}
	if s.uncertain {
		return errors.New("outcome_unknown: reopen Store to reconcile file commit")
	}
	if !write && s.sql != nil {
		return s.refreshSQL()
	}
	return nil
}

func (s *Store) next() (snapshot, error) {
	if err := s.usable(true); err != nil {
		return snapshot{}, err
	}
	if s.sql != nil {
		if err := s.beginSQL(); err != nil {
			return snapshot{}, err
		}
	}
	// Mutations replace resources. commit detaches their dynamic values;
	// history bodies are never part of a metadata candidate.
	next := s.data
	next.Runtimes = maps.Clone(s.data.Runtimes)
	next.Sessions = maps.Clone(s.data.Sessions)
	return next, nil
}

func initial(m resource.Metadata) (resource.Metadata, error) {
	if m.UID != "" || !m.CreatedAt.IsZero() || m.Generation != 0 || m.ResourceVersion != "" {
		return m, runtime.InvalidArgument("system metadata is read-only")
	}
	m.UID = uuid.NewString()
	m.CreatedAt = time.Now().UTC()
	m.Generation = 1
	m.ResourceVersion = uuid.NewString()
	return m, nil
}

func updated(old, m resource.Metadata, specChanged, changed bool) (resource.Metadata, error) {
	if old.ResourceVersion != m.ResourceVersion {
		return m, store.ErrConflict
	}
	if old.Namespace != m.Namespace || old.Name != m.Name || old.UID != m.UID || !old.CreatedAt.Equal(m.CreatedAt) || old.Generation != m.Generation {
		return m, errors.New("immutable metadata changed")
	}
	if specChanged {
		m.Generation++
	}
	if changed {
		m.ResourceVersion = uuid.NewString()
	}
	return m, nil
}

func (s *Store) CreateRuntime(r resource.Runtime) (resource.Runtime, error) {
	for _, value := range r.Spec.Config {
		if err := events.CheckDynamicJSON(value); err != nil {
			return resource.Runtime{}, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.rollbackSQL()
	n, err := s.next()
	if err != nil {
		return r, err
	}
	if _, ok := n.Runtimes[r.Metadata.Name]; ok {
		return r, store.ErrConflict
	}
	if r.Metadata.Namespace != "" || r.Metadata.Name == "" {
		return r, runtime.InvalidArgument("invalid runtime key")
	}
	r.Metadata, err = initial(r.Metadata)
	if err != nil {
		return r, err
	}
	n.Runtimes[r.Metadata.Name] = r
	err = s.commit(n)
	if err != nil {
		return resource.Runtime{}, err
	}
	return fileio.CopyValue(r)
}

func (s *Store) GetRuntime(name string) (resource.Runtime, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usable(false); err != nil {
		return resource.Runtime{}, err
	}
	r, ok := s.data.Runtimes[name]
	if !ok {
		return resource.Runtime{}, store.ErrNotFound
	}
	return fileio.CopyValue(r)
}

func (s *Store) UpdateRuntime(r resource.Runtime) (resource.Runtime, error) {
	for _, value := range r.Spec.Config {
		if err := events.CheckDynamicJSON(value); err != nil {
			return resource.Runtime{}, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.rollbackSQL()
	n, err := s.next()
	if err != nil {
		return r, err
	}
	old, ok := n.Runtimes[r.Metadata.Name]
	if !ok {
		return r, store.ErrNotFound
	}
	r.Metadata, err = updated(old.Metadata, r.Metadata, !fileio.Equal(old.Spec, r.Spec), !fileio.Equal(old, r))
	if err != nil {
		return r, err
	}
	n.Runtimes[r.Metadata.Name] = r
	if err := s.commit(n); err != nil {
		return resource.Runtime{}, err
	}
	return fileio.CopyValue(r)
}

func (s *Store) CreateSession(r resource.Session) (resource.Session, error) {
	if err := resource.ValidateSessionState(r.Status.State); err != nil {
		return r, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.rollbackSQL()
	n, err := s.next()
	if err != nil {
		return r, err
	}
	k := key(r.Metadata.Namespace, r.Metadata.Name)
	if _, ok := n.Sessions[k]; ok {
		return r, store.ErrConflict
	}
	for _, old := range n.Sessions {
		if old.Status.SessionID == r.Status.SessionID {
			return r, store.ErrConflict
		}
	}
	if !fileio.SafeComponent(r.Metadata.Namespace) || !fileio.SafeComponent(r.Metadata.Name) || resource.ValidateSessionID(r.Status.SessionID) != nil {
		return r, runtime.InvalidArgument("invalid session identity")
	}
	r.Metadata, err = initial(r.Metadata)
	if err != nil {
		return r, err
	}
	n.Sessions[k] = r
	if err := s.commit(n); err != nil {
		return resource.Session{}, err
	}
	return fileio.CopyValue(r)
}

func resolve(d snapshot, ref resource.SessionRef) (resource.Session, error) {
	if ref.SessionID != "" {
		if ref.Namespace != "" || ref.Name != "" {
			return resource.Session{}, runtime.InvalidArgument("session reference forms are mutually exclusive")
		}
		for _, r := range d.Sessions {
			if r.Status.SessionID == ref.SessionID {
				return r, nil
			}
		}
	} else if ref.Namespace != "" && ref.Name != "" {
		if r, ok := d.Sessions[key(ref.Namespace, ref.Name)]; ok {
			return r, nil
		}
	} else {
		return resource.Session{}, runtime.InvalidArgument("invalid session reference")
	}
	return resource.Session{}, store.ErrNotFound
}

func (s *Store) GetSession(ref resource.SessionRef) (resource.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usable(false); err != nil {
		return resource.Session{}, err
	}
	r, err := resolve(s.data, ref)
	if err != nil {
		return r, err
	}
	return fileio.CopyValue(r)
}

func (s *Store) UpdateSession(r resource.Session) (resource.Session, error) {
	if err := resource.ValidateSessionState(r.Status.State); err != nil {
		return r, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.rollbackSQL()
	n, err := s.next()
	if err != nil {
		return r, err
	}
	k := key(r.Metadata.Namespace, r.Metadata.Name)
	old, ok := n.Sessions[k]
	if !ok {
		return r, store.ErrNotFound
	}
	if old.Status.SessionID != r.Status.SessionID {
		return r, errors.New("native identity is immutable")
	}
	r.Metadata, err = updated(old.Metadata, r.Metadata, !fileio.Equal(old.Spec, r.Spec), !fileio.Equal(old, r))
	if err != nil {
		return r, err
	}
	n.Sessions[k] = r
	if err := s.commit(n); err != nil {
		return resource.Session{}, err
	}
	return fileio.CopyValue(r)
}

// Reconcile reloads only this module's metadata under its original writer lock.
func (s *Store) Reconcile() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.readOnly {
		return errors.New("active writer required")
	}
	s.uncertain = true
	if s.sql != nil {
		return errors.New("SQLite authority requires reopen after uncertain commit")
	}
	if err := s.load(true); err != nil {
		return err
	}
	if err := s.finishMetadata(); err != nil {
		return err
	}
	s.uncertain = false
	return nil
}
