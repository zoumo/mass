// Package file stores Runtime metadata and namespace/name Session directories.
// One writer owns the root; a durable redo record coordinates multi-file changes.
package file

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/zoumo/mass/mass-go/pkg/events"
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
	mu         sync.Mutex
	dir        string
	lock       *os.File
	readOnly   bool
	closed     bool
	uncertain  bool
	data       snapshot
	sql        *replicas
	histories  map[string]*historyIndex
	appendOpen func(string) (appendFile, error)
}

func decode(b []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("trailing snapshot data")
	}
	return nil
}

func copyValue[T any](v T) (T, error) {
	var out T
	b, err := json.Marshal(v)
	if err == nil {
		err = decode(b, &out)
	}
	return out, err
}
func key(ns, name string) string { return ns + "/" + name }
func Open(dir string, readOnly bool) (*Store, error) {
	if !filepath.IsAbs(dir) {
		return nil, errors.New("absolute data directory required")
	}
	s := &Store{dir: dir, readOnly: readOnly, appendOpen: func(path string) (appendFile, error) {
		return openHistory(path, os.O_RDWR|os.O_APPEND)
	}}
	if !readOnly {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		if err := checkPath(dir, true); err != nil {
			return nil, err
		}
		if err := checkPath(filepath.Join(dir, "writer.lock"), false); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		f, err := os.OpenFile(filepath.Join(dir, "writer.lock"), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return nil, err
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			f.Close()
			return nil, fmt.Errorf("writer already owns directory: %w", err)
		}
		s.lock = f
	}
	err := s.load(!readOnly)
	if err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
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
		if k != key(r.Metadata.Namespace, r.Metadata.Name) || !safeComponent(r.Metadata.Namespace) || !safeComponent(r.Metadata.Name) || r.Metadata.UID == "" || r.Metadata.ResourceVersion == "" || r.Metadata.Generation == 0 || r.Metadata.CreatedAt.IsZero() || resource.ValidateSessionID(r.Status.SessionID) != nil {
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
	return nil
}

func (s *Store) next() (snapshot, error) {
	if err := s.usable(true); err != nil {
		return snapshot{}, err
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

func equal(a, b any) bool {
	x, e := json.Marshal(a)
	y, f := json.Marshal(b)
	return e == nil && f == nil && bytes.Equal(x, y)
}

func (s *Store) CreateRuntime(r resource.Runtime) (resource.Runtime, error) {
	for _, value := range r.Spec.Config {
		if err := events.CheckDynamicJSON(value); err != nil {
			return resource.Runtime{}, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
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
	return copyValue(r)
}

func (s *Store) GetRuntime(name string) (resource.Runtime, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usable(false); err != nil {
		return resource.Runtime{}, err
	}
	return s.sqlRuntime(name)
}

func (s *Store) UpdateRuntime(r resource.Runtime) (resource.Runtime, error) {
	for _, value := range r.Spec.Config {
		if err := events.CheckDynamicJSON(value); err != nil {
			return resource.Runtime{}, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := s.next()
	if err != nil {
		return r, err
	}
	old, ok := n.Runtimes[r.Metadata.Name]
	if !ok {
		return r, store.ErrNotFound
	}
	r.Metadata, err = updated(old.Metadata, r.Metadata, !equal(old.Spec, r.Spec), !equal(old, r))
	if err != nil {
		return r, err
	}
	n.Runtimes[r.Metadata.Name] = r
	if err := s.commit(n); err != nil {
		return resource.Runtime{}, err
	}
	return copyValue(r)
}

func (s *Store) CreateSession(r resource.Session) (resource.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	if !safeComponent(r.Metadata.Namespace) || !safeComponent(r.Metadata.Name) || resource.ValidateSessionID(r.Status.SessionID) != nil {
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
	return copyValue(r)
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
	return s.sqlSession(ref)
}

func (s *Store) UpdateSession(r resource.Session) (resource.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	r.Metadata, err = updated(old.Metadata, r.Metadata, !equal(old.Spec, r.Spec), !equal(old, r))
	if err != nil {
		return r, err
	}
	n.Sessions[k] = r
	if err := s.commit(n); err != nil {
		return resource.Session{}, err
	}
	return copyValue(r)
}

func validateEvent(r resource.Session, e events.SessionEvent) error {
	if err := e.Validate(); err != nil {
		return err
	}
	if !e.Persistent() {
		return errors.New("event is live-only")
	}
	if e.SessionID != r.Status.SessionID || e.Namespace != r.Metadata.Namespace || e.SessionName != r.Metadata.Name {
		return errors.New("event identity mismatch")
	}
	return nil
}

type cursor struct {
	Session  string   `json:"session"`
	Types    []string `json:"types"`
	Position int      `json:"position"`
}

func (s *Store) encodeCursor(c cursor) (string, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, []byte(s.data.CursorKey))
	mac.Write(b)
	return base64.RawURLEncoding.EncodeToString(append(mac.Sum(nil), b...)), nil
}

func normalized(types []string) ([]string, error) {
	if types != nil && len(types) == 0 {
		return nil, runtime.InvalidArgument("empty types filter")
	}
	for _, kind := range types {
		if !events.ValidType(kind) {
			return nil, runtime.InvalidArgument("unknown event type")
		}
	}
	out := append([]string(nil), types...)
	sort.Strings(out)
	out = slices.Compact(out)
	return out, nil
}

func (s *Store) ListHistory(id string, q store.HistoryQuery) (store.HistoryPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	empty := store.HistoryPage{}
	if err := s.usable(false); err != nil {
		return empty, err
	}
	if _, err := s.sqlSession(resource.SessionRef{SessionID: id}); err != nil {
		return empty, err
	}
	types, err := normalized(q.Types)
	if err != nil {
		return empty, err
	}
	limit := q.Limit
	if limit == 0 {
		limit = 100
	}
	if limit < 1 {
		return empty, runtime.InvalidArgument("limit must be positive")
	}
	index := s.histories[id]
	start := 0
	if q.After != "" {
		b, err := base64.RawURLEncoding.DecodeString(q.After)
		if err != nil || len(b) <= 32 {
			return empty, runtime.InvalidArgument("invalid history cursor")
		}
		mac := hmac.New(sha256.New, []byte(s.data.CursorKey))
		mac.Write(b[32:])
		if !hmac.Equal(b[:32], mac.Sum(nil)) {
			return empty, runtime.InvalidArgument("invalid history cursor")
		}
		var c cursor
		if decode(b[32:], &c) != nil || c.Session != id || !reflect.DeepEqual(c.Types, types) || c.Position < 1 || c.Position > index.Count {
			return empty, runtime.InvalidArgument("cursor does not match query")
		}
		start = c.Position
	}
	lock, err := s.layoutLock(false)
	if err != nil {
		return empty, err
	}
	defer lock.Close()
	if err := s.noPending(); err != nil {
		return empty, err
	}
	return s.sqlHistory(id, types, start, limit)
}

// GetSyncProgress reads the same committed view as history queries.
func (s *Store) GetSyncProgress(id string) (*string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usable(false); err != nil {
		return nil, err
	}
	if _, err := s.sqlSession(resource.SessionRef{SessionID: id}); err != nil {
		return nil, err
	}
	return s.sqlProgress(id)
}

// Reconcile explicitly reloads and validates the authoritative snapshot under the
// original writer lock. It never creates a second writer or edits corrupt data.
// Service admission remains sealed until its independent responsibility settles.
func (s *Store) Reconcile() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.readOnly {
		return errors.New("active writer required")
	}
	s.uncertain = true
	old, oldHistories := s.data, s.histories
	if err := s.load(true); err != nil {
		s.data, s.histories = old, oldHistories
		return err
	}
	for id, committed := range oldHistories {
		candidate, exists := s.histories[id]
		if !exists {
			continue
		} // An explicitly committed Session deletion removes its log.

		r, err := resolve(s.data, resource.SessionRef{SessionID: id})
		if err != nil {
			return err
		}
		f, err := openHistory(s.historyPath(r), os.O_RDONLY)
		if err != nil {
			return err
		}
		prefix, scanErr := scanHistory(io.NewSectionReader(f, 0, committed.End), r)
		scanErr = errors.Join(scanErr, f.Close())
		if scanErr != nil || candidate.End < committed.End || prefix == nil || prefix.Hash != committed.Hash {
			s.data, s.histories = old, oldHistories
			return errors.New("committed history prefix changed or missing")
		}
	}
	if err := syncDirectory(s.dir); err != nil {
		s.data, s.histories = old, oldHistories
		return err
	}
	s.uncertain = false
	return nil
}
