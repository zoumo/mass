// Package file implements the initial complete-file storage protocol.
// One writer owns a directory lock. Each mutation fsyncs a new full snapshot,
// renames it, and fsyncs the directory. Read-only opens never acquire a Driver.
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
	Format    int                              `json:"format"`
	CursorKey string                           `json:"cursorKey"`
	Runtimes  map[string]resource.Runtime      `json:"runtimes"`
	Sessions  map[string]resource.Session      `json:"sessions"`
	History   map[string][]events.SessionEvent `json:"history"`
}
type Store struct {
	mu        sync.Mutex
	dir       string
	lock      *os.File
	readOnly  bool
	closed    bool
	uncertain bool
	data      snapshot
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
	s := &Store{dir: dir, readOnly: readOnly}
	if !readOnly {
		if err := os.MkdirAll(dir, 0o700); err != nil {
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
	b, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if os.IsNotExist(err) && !readOnly {
		s.data = snapshot{Format: 1, CursorKey: uuid.NewString(), Runtimes: map[string]resource.Runtime{}, Sessions: map[string]resource.Session{}, History: map[string][]events.SessionEvent{}}
		err = s.commit(s.data)
	} else if err == nil {
		err = decode(b, &s.data)
		if err == nil {
			err = s.validate()
		}
	}
	if err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) validate() error {
	d := s.data
	if d.Format != 1 || d.CursorKey == "" || d.Runtimes == nil || d.Sessions == nil || d.History == nil {
		return errors.New("invalid snapshot format")
	}
	ids := map[string]resource.Session{}
	for k, r := range d.Runtimes {
		if k != r.Metadata.Name || r.Metadata.Namespace != "" || r.Metadata.UID == "" || r.Metadata.ResourceVersion == "" || r.Metadata.Generation == 0 || r.Metadata.CreatedAt.IsZero() {
			return errors.New("invalid runtime record")
		}
	}
	for k, r := range d.Sessions {
		if k != key(r.Metadata.Namespace, r.Metadata.Name) || r.Metadata.Namespace == "" || r.Metadata.UID == "" || r.Metadata.ResourceVersion == "" || r.Metadata.Generation == 0 || r.Metadata.CreatedAt.IsZero() || resource.ValidateSessionID(r.Status.SessionID) != nil {
			return errors.New("invalid session record")
		}
		if _, ok := ids[r.Status.SessionID]; ok {
			return errors.New("duplicate native session identity")
		}
		ids[r.Status.SessionID] = r
	}
	for id, records := range d.History {
		r, ok := ids[id]
		if !ok {
			return errors.New("history without identity")
		}
		seen := map[string]bool{}
		for _, e := range records {
			if err := validateEvent(r, e); err != nil {
				return err
			}
			if seen[e.EventID] {
				return errors.New("duplicate event identity in file")
			}
			seen[e.EventID] = true
		}
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
	if s.lock != nil {
		err := syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
		return errors.Join(err, s.lock.Close())
	}
	return nil
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

func (s *Store) commit(next snapshot) error {
	b, err := json.Marshal(next)
	if err != nil {
		return fmt.Errorf("snapshot encoding: %w", err)
	}
	f, err := os.CreateTemp(s.dir, ".state-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(s.dir, "state.json")); err != nil {
		return err
	}
	directory, err := os.Open(s.dir)
	if err == nil {
		err = errors.Join(directory.Sync(), directory.Close())
	}
	if err != nil {
		s.uncertain = true
		return fmt.Errorf("outcome_unknown: directory sync: %w", err)
	}
	var canonical snapshot
	if err := decode(b, &canonical); err != nil {
		s.uncertain = true
		return err
	}
	s.data = canonical
	return nil
}

func (s *Store) next() (snapshot, error) {
	if err := s.usable(true); err != nil {
		return snapshot{}, err
	}
	return copyValue(s.data)
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
	r, ok := s.data.Runtimes[name]
	if !ok {
		return r, store.ErrNotFound
	}
	return copyValue(r)
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
	if r.Metadata.Namespace == "" || r.Metadata.Name == "" || resource.ValidateSessionID(r.Status.SessionID) != nil {
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
	r, err := resolve(s.data, ref)
	if err != nil {
		return r, err
	}
	return copyValue(r)
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

func progress(records []events.SessionEvent) *string {
	if len(records) == 0 {
		return nil
	}
	v := records[len(records)-1].EventID
	return &v
}

func (s *Store) Append(id string, records []events.SessionEvent) (*string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := s.next()
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, errors.New("empty history batch")
	}
	r, err := resolve(n, resource.SessionRef{SessionID: id})
	if err != nil {
		return nil, err
	}
	for _, e := range records {
		if err := validateEvent(r, e); err != nil {
			return nil, err
		}
		duplicate := false
		for _, old := range n.History[id] {
			if old.EventID == e.EventID {
				if !equal(old, e) {
					return nil, errors.New("eventId content conflict")
				}
				duplicate = true
				break
			}
		}
		if !duplicate {
			n.History[id] = append(n.History[id], e)
		}
	}
	if err := s.commit(n); err != nil {
		return nil, err
	}
	return progress(n.History[id]), nil
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
	if _, err := resolve(s.data, resource.SessionRef{SessionID: id}); err != nil {
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
	records := s.data.History[id]
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
		if decode(b[32:], &c) != nil || c.Session != id || !reflect.DeepEqual(c.Types, types) || c.Position < 1 || c.Position > len(records) {
			return empty, runtime.InvalidArgument("cursor does not match query")
		}
		start = c.Position
	}
	page := store.HistoryPage{Events: []events.SessionEvent{}, SyncedEventID: progress(records)}
	last := start
	for i := start; i < len(records); i++ {
		e := records[i]
		matches := len(types) == 0
		for _, t := range types {
			if t == e.Type() {
				matches = true
			}
		}
		if !matches {
			continue
		}
		if len(page.Events) == limit {
			next, err := s.encodeCursor(cursor{id, types, last})
			if err != nil {
				return store.HistoryPage{}, err
			}
			page.Next = next
			break
		}
		page.Events = append(page.Events, e)
		last = i + 1
	}
	return copyValue(page)
}

// GetSyncProgress reads the same committed view as history queries.
func (s *Store) GetSyncProgress(id string) (*string, error) {
	p, err := s.ListHistory(id, store.HistoryQuery{Limit: 1})
	return p.SyncedEventID, err
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
	b, err := os.ReadFile(filepath.Join(s.dir, "state.json"))
	if err != nil {
		return err
	}
	var next snapshot
	if err := decode(b, &next); err != nil {
		return err
	}
	old := s.data
	for id, committed := range old.History {
		candidate := next.History[id]
		if len(candidate) < len(committed) {
			return errors.New("committed history prefix missing")
		}
		for i, e := range committed {
			if !equal(e, candidate[i]) {
				return errors.New("committed history prefix changed")
			}
		}
	}
	s.data = next
	if err := s.validate(); err != nil {
		s.data = old
		return err
	}
	f, err := os.Open(filepath.Join(s.dir, "state.json"))
	if err == nil {
		err = errors.Join(f.Sync(), f.Close())
	}
	if err != nil {
		s.data = old
		return err
	}
	dir, err := os.Open(s.dir)
	if err == nil {
		err = errors.Join(dir.Sync(), dir.Close())
	}
	if err != nil {
		s.data = old
		return err
	}
	s.uncertain = false
	return nil
}
