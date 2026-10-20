package file

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
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

func (s *Store) historyPath(r resource.Session) string {
	return filepath.Join(s.dir, "sessions", r.Metadata.Namespace, r.Metadata.Name, "events.jsonl")
}

func openHistory(path string, flags int) (*os.File, error) {
	if err := checkPath(path, false); err != nil {
		return nil, err
	}
	return os.OpenFile(path, flags, 0o600)
}

// ReadBytes permits large records while retaining at most one event body.
func eachEvent(reader io.Reader, r resource.Session, visit func(events.SessionEvent, []byte) error) error {
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
		if err := decode(line, &e); err != nil {
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

func scanHistory(reader io.Reader, r resource.Session) (*historyIndex, error) {
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
	for _, name := range []string{".pending.json", ".append.pending.json"} {
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
	r, err := resolve(s.data, resource.SessionRef{SessionID: id})
	if err != nil {
		return nil, err
	}
	lock, err := s.layoutLock(true)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if err := s.noPending(); err != nil {
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
			if !equal(stored, e) {
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
	intent := appendIntent{Format: 1, Namespace: r.Metadata.Namespace, Name: r.Metadata.Name, SessionID: id, UID: r.Metadata.UID, Offset: index.End, PrefixHash: hex.EncodeToString(index.Hash[:]), Data: data.Bytes()}
	s.uncertain = true
	if err := atomicFile(filepath.Join(s.dir, ".append.pending.json"), intent); err != nil {
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
func (s *Store) applyAppend(r resource.Session, intent appendIntent, known *historyIndex) (_ *historyIndex, result error) {
	if intent.Format != 1 || intent.Namespace != r.Metadata.Namespace || intent.Name != r.Metadata.Name || intent.SessionID != r.Status.SessionID || intent.UID != r.Metadata.UID || intent.Offset < 0 || len(intent.Data) == 0 {
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
	if known == nil {
		// Recovery validates prefix identities on disk before extending the file.
		if err := s.openReplicas(); err != nil {
			return nil, err
		}
		if err := s.syncMetadata(s.data); err != nil {
			return nil, err
		}
		known = emptyHistory()
		if err := s.restoreHistory(io.NewSectionReader(f, 0, intent.Offset), r, known); err != nil {
			return nil, err
		}
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
		if _, exists, err := s.storedEvent(r.Status.SessionID, e.EventID); err != nil {
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
	if err := os.Remove(filepath.Join(s.dir, ".append.pending.json")); err != nil {
		return err
	}
	return syncDirectory(s.dir)
}

func (s *Store) recoverAppend(write bool) error {
	path := filepath.Join(s.dir, ".append.pending.json")
	if err := checkPath(path, false); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !write {
		return errors.New("incomplete file commit: writable reopen required")
	}
	var intent appendIntent
	if err := readJSON(path, &intent); err != nil {
		return err
	}
	r, err := resolve(s.data, resource.SessionRef{Namespace: intent.Namespace, Name: intent.Name})
	if err != nil {
		return err
	}
	if _, err := s.applyAppend(r, intent, nil); err != nil {
		return err
	}
	return s.finishAppend()
}
