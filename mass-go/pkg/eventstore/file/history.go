package file

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/internal/fileio"
)

// A checkpoint is constant size per Session; event identities and bodies live in SQL.
type historyIndex struct {
	Count int
	Last  string
	End   int64
	Hash  [sha256.Size]byte
}

func emptyHistory() *historyIndex { return &historyIndex{} }
func (h *historyIndex) progress() *string {
	if h.Count == 0 {
		return nil
	}
	id := h.Last
	return &id
}

func (h *historyIndex) add(e events.SessionEvent, line []byte) error {
	h.Count++
	h.Last = e.EventID
	h.End += int64(len(line))
	digest := sha256.New()
	digest.Write(h.Hash[:])
	digest.Write(line)
	copy(h.Hash[:], digest.Sum(nil))
	return nil
}

func (s *Store) historyPath(r identity) string {
	return filepath.Join(s.dir, "sessions", r.Namespace, r.Name, "eventlogs", base64.RawURLEncoding.EncodeToString([]byte(r.SessionID)), "events.jsonl")
}

func openHistory(path string, flags int) (*os.File, error) {
	if err := fileio.CheckPath(path, false); err != nil {
		return nil, err
	}
	return os.OpenFile(path, flags, 0o600)
}

// ReadBytes permits large records while retaining at most one event body.
func eachEvent(reader io.Reader, r identity, visit func(events.SessionEvent, []byte) error) error {
	buffer := bufio.NewReader(reader)
	for {
		line, err := buffer.ReadBytes('\n')
		if errors.Is(err, io.EOF) && len(line) == 0 {
			return nil
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return errors.New("incomplete JSONL record")
			}
			return err
		}
		var e events.SessionEvent
		if err := fileio.Decode(line, &e); err != nil {
			return fmt.Errorf("invalid history record: %w", err)
		}
		if err := validateEvent(r, e); err != nil {
			return err
		}
		if err := visit(e, line); err != nil {
			return err
		}
	}
}

func scanHistory(reader io.Reader, r identity) (*historyIndex, error) {
	index := emptyHistory()
	if err := eachEvent(reader, r, index.add); err != nil {
		return nil, err
	}
	return index, nil
}

// Data contains only newly accepted JSONL bytes. Offset and PrefixHash protect
// the already committed prefix; no historical payload is copied into the intent.
type appendIntent struct {
	Format     int    `json:"format"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	SessionID  string `json:"sessionId"`
	UID        string `json:"uid"`
	Offset     int64  `json:"offset"`
	PrefixHash string `json:"prefixHash"`
	Data       []byte `json:"data"`
}

// Keep the I/O boundary narrow so real-file write/sync/close failures can be
// injected without substituting the storage protocol or the filesystem.
type appendFile interface {
	io.ReaderAt
	io.Writer
	Stat() (os.FileInfo, error)
	Sync() error
	Close() error
}

func (s *Store) noPending() error {
	for _, name := range []string{".events.append.pending.json"} {
		if _, err := os.Lstat(filepath.Join(s.dir, name)); err == nil {
			return errors.New("incomplete file commit: writable reopen required")
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (s *Store) Append(id string, records []events.SessionEvent) (*string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usable(true); err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, errors.New("empty history batch")
	}
	lock, err := s.layoutLock(true)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if err := s.noPending(); err != nil {
		return nil, err
	}
	r, err := s.bind(id, records)
	if err != nil {
		return nil, err
	}
	f, err := openHistory(s.historyPath(r), os.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	index := s.histories[id]
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() != index.End {
		return nil, errors.New("history size changed: explicit reconciliation required")
	}
	var data bytes.Buffer
	newIDs := map[string][]byte{}
	for _, e := range records {
		if err := validateEvent(r, e); err != nil {
			return nil, err
		}
		if stored, exists, err := s.storedEvent(id, e.EventID); err != nil {
			return nil, err
		} else if exists {
			if !fileio.Equal(stored, e) {
				return nil, errors.New("eventId content conflict")
			}
			continue
		}
		encoded, err := json.Marshal(e)
		if err != nil {
			return nil, err
		}
		if previous, exists := newIDs[e.EventID]; exists {
			if !bytes.Equal(previous, encoded) {
				return nil, errors.New("eventId content conflict")
			}
			continue
		}
		newIDs[e.EventID] = encoded
		data.Write(encoded)
		data.WriteByte('\n')
	}
	if data.Len() == 0 {
		return s.sqlProgress(id)
	}
	intent := appendIntent{Format: 1, Namespace: r.Namespace, Name: r.Name, SessionID: id, UID: r.SessionID, Offset: index.End, PrefixHash: hex.EncodeToString(index.Hash[:]), Data: data.Bytes()}
	s.uncertain = true
	if err := fileio.AtomicFile(filepath.Join(s.dir, ".events.append.pending.json"), intent); err != nil {
		return nil, fmt.Errorf("outcome_unknown: append intent: %w", err)
	}
	updated, err := s.applyAppend(r, intent, index)
	if err == nil {
		err = s.syncAppend(r, intent, index)
	}
	if err == nil {
		err = s.finishAppend()
	}
	if err != nil {
		return nil, fmt.Errorf("outcome_unknown: history append: %w", err)
	}
	s.histories[id], s.uncertain = updated, false
	return updated.progress(), nil
}

// applyAppend validates the committed prefix and all new records before writing.
// If a previous attempt wrote only part of Data, that exact prefix is retained
// and only the missing suffix is appended. Divergent tails are never truncated.
func (s *Store) applyAppend(r identity, intent appendIntent, known *historyIndex) (_ *historyIndex, result error) {
	if intent.Format != 1 || intent.Namespace != r.Namespace || intent.Name != r.Name || intent.SessionID != r.SessionID || intent.UID != r.SessionID || intent.Offset < 0 || len(intent.Data) == 0 {
		return nil, errors.New("invalid append intent identity")
	}
	f, err := s.appendOpen(s.historyPath(r))
	if err != nil {
		return nil, err
	}
	defer func() { result = errors.Join(result, f.Close()) }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() < intent.Offset || info.Size()-intent.Offset > int64(len(intent.Data)) {
		return nil, errors.New("append file length does not match intent")
	}
	if known.End != intent.Offset || hex.EncodeToString(known.Hash[:]) != intent.PrefixHash {
		return nil, errors.New("committed history prefix does not match intent")
	}
	checkpoint := *known
	updated := &checkpoint
	batchIDs := map[string]bool{}
	if err := eachEvent(bytes.NewReader(intent.Data), r, func(e events.SessionEvent, line []byte) error {
		if batchIDs[e.EventID] {
			return errors.New("duplicate event identity in intent")
		}
		batchIDs[e.EventID] = true
		if _, exists, err := s.storedEvent(r.SessionID, e.EventID); err != nil {
			return err
		} else if exists {
			return errors.New("duplicate event identity in intent")
		}
		return updated.add(e, line)
	}); err != nil {
		return nil, err
	}
	present := info.Size() - intent.Offset
	if err := compareAppendPrefix(f, intent.Offset, intent.Data[:present]); err != nil {
		return nil, err
	}
	if n, err := f.Write(intent.Data[present:]); err != nil {
		return nil, err
	} else if n != len(intent.Data)-int(present) {
		return nil, io.ErrShortWrite
	}
	if err := f.Sync(); err != nil {
		return nil, err
	}
	return updated, nil
}

func compareAppendPrefix(f io.ReaderAt, offset int64, expected []byte) error {
	buffer := make([]byte, 32*1024)
	for len(expected) != 0 {
		n := min(len(expected), len(buffer))
		if _, err := f.ReadAt(buffer[:n], offset); err != nil {
			return err
		}
		if !bytes.Equal(buffer[:n], expected[:n]) {
			return errors.New("append tail differs from intent")
		}
		offset += int64(n)
		expected = expected[n:]
	}
	return nil
}

func (s *Store) finishAppend() error {
	if err := os.Remove(filepath.Join(s.dir, ".events.append.pending.json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	return fileio.SyncDirectory(s.dir)
}

func (s *Store) recoverAppend(write bool) error {
	path := filepath.Join(s.dir, ".events.append.pending.json")
	if err := fileio.CheckPath(path, false); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !write {
		return errors.New("incomplete file commit: writable reopen required")
	}
	var intent appendIntent
	if err := fileio.ReadJSON(path, &intent); err != nil {
		return err
	}
	r, ok := s.identities[intent.SessionID]
	if !ok {
		return errors.New("append identity missing")
	}
	known, err := s.restoreBeforeAppend(intent)
	if err != nil {
		return err
	}
	if _, err := s.applyAppend(r, intent, known); err != nil {
		return err
	}
	// Open/Reconcile clear evidence after all file and replica checks succeed.
	return nil
}
