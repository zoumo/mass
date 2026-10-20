// Package file owns event logs and their disposable SQLite query projection.
package file

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"slices"
	"sort"
	"sync"
	"syscall"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/internal/fileio"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

type identity struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	SessionID string `json:"sessionId"`
}
type Store struct {
	mu         sync.Mutex
	dir        string
	lock       *os.File
	readOnly   bool
	closed     bool
	uncertain  bool
	cursorKey  string
	identities map[string]identity
	sql        *replicas
	histories  map[string]*historyIndex
	appendOpen func(string) (appendFile, error)
}

func (s *Store) usable(write bool) error {
	if s.closed {
		return errors.New("event store closed")
	}
	if write && s.readOnly {
		return store.ErrReadOnly
	}
	if s.uncertain {
		return errors.New("outcome_unknown: reopen EventStore to reconcile file commit")
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
	err := s.sql.close()
	if s.lock != nil {
		err = errors.Join(err, syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN), s.lock.Close())
	}
	return err
}

func validateEvent(r identity, e events.SessionEvent) error {
	if err := e.Validate(); err != nil {
		return err
	}
	if !e.Persistent() {
		return errors.New("event is live-only")
	}
	if e.SessionID != r.SessionID || e.Namespace != r.Namespace || e.SessionName != r.Name {
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
	mac := hmac.New(sha256.New, []byte(s.cursorKey))
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
	if err := resource.ValidateSessionID(id); err != nil {
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
	if index == nil {
		index = emptyHistory()
	}
	start := 0
	if q.After != "" {
		b, err := base64.RawURLEncoding.DecodeString(q.After)
		if err != nil || len(b) <= 32 {
			return empty, runtime.InvalidArgument("invalid history cursor")
		}
		mac := hmac.New(sha256.New, []byte(s.cursorKey))
		mac.Write(b[32:])
		if !hmac.Equal(b[:32], mac.Sum(nil)) {
			return empty, runtime.InvalidArgument("invalid history cursor")
		}
		var c cursor
		if fileio.Decode(b[32:], &c) != nil || c.Session != id || !reflect.DeepEqual(c.Types, types) || c.Position < 1 || c.Position > index.Count {
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
	tx, err := s.projectionTx()
	if err != nil {
		return empty, err
	}
	defer func() { _ = tx.Rollback() }()
	return s.sqlHistory(tx, id, types, start, limit)
}

// GetSyncProgress reads the same committed view as history queries.
func (s *Store) GetSyncProgress(id string) (*string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usable(false); err != nil {
		return nil, err
	}
	if err := resource.ValidateSessionID(id); err != nil {
		return nil, err
	}
	lock, err := s.layoutLock(false)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if err := s.noPending(); err != nil {
		return nil, err
	}
	tx, err := s.projectionTx()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	return s.sqlProgress(tx, id)
}

// Reconcile validates original committed checkpoints before completing an append.
func (s *Store) Reconcile() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.readOnly {
		return errors.New("active writer required")
	}
	s.uncertain = true
	lock, err := s.layoutLock(true)
	if err != nil {
		return err
	}
	defer lock.Close()
	oldIdentities := s.identities
	s.identities = map[string]identity{}
	if err := s.loadIdentities(); err != nil {
		s.identities = oldIdentities
		return err
	}
	for id, old := range s.histories {
		r, ok := s.identities[id]
		if !ok {
			return errors.New("committed history identity missing")
		}
		f, err := openHistory(s.historyPath(r), os.O_RDONLY)
		if err != nil {
			return err
		}
		prefix, e := scanHistory(io.NewSectionReader(f, 0, old.End), r)
		e = errors.Join(e, f.Close())
		if e != nil || prefix.Hash != old.Hash {
			return errors.New("committed history prefix changed or missing")
		}
	}
	if err := s.recoverAppend(true); err != nil {
		return err
	}
	if err := s.loadHistories(); err != nil {
		return err
	}
	if err := s.finishAppend(); err != nil {
		return err
	}
	s.uncertain = false
	return nil
}
