package file

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/zoumo/mass/mass-go/pkg/resource"
)

// Store metadata is separate from the resource JSONL files.
type storeFile struct {
	Format    int    `json:"format"`
	CursorKey string `json:"cursorKey"`
}

type metadataIntent struct {
	Format  int             `json:"format"`
	Next    snapshot        `json:"next"`
	Created map[string]bool `json:"created"`
}

type sessionIndexEntry struct {
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	SessionID  string `json:"sessionId"`
	RuntimeRef string `json:"runtimeRef"`
}

func writeJSONLines[T any](path string, records []T) error {
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			return err
		}
	}
	return atomicBytes(path, b.Bytes())
}

func readJSONLines[T any](path string) ([]T, error) {
	if err := checkPath(path, false); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	records := []T{}
	if len(b) == 0 {
		return records, nil
	}
	if b[len(b)-1] != '\n' {
		return nil, errors.New("incomplete JSONL record")
	}
	for i, line := range bytes.Split(b[:len(b)-1], []byte{'\n'}) {
		var record T
		if len(bytes.TrimSpace(line)) == 0 {
			return nil, fmt.Errorf("empty JSONL record at line %d", i+1)
		}
		if err := decode(line, &record); err != nil {
			return nil, fmt.Errorf("JSONL line %d: %w", i+1, err)
		}
		records = append(records, record)
	}
	return records, nil
}

func (s *Store) writeIndex(next snapshot) error {
	entries := make([]sessionIndexEntry, 0, len(next.Sessions))
	for _, r := range next.Sessions {
		entries = append(entries, sessionIndexEntry{Namespace: r.Metadata.Namespace, Name: r.Metadata.Name, SessionID: r.Status.SessionID, RuntimeRef: r.Spec.RuntimeRef})
	}
	sort.Slice(entries, func(i, j int) bool {
		return key(entries[i].Namespace, entries[i].Name) < key(entries[j].Namespace, entries[j].Name)
	})
	return writeJSONLines(filepath.Join(s.dir, "session_index.jsonl"), entries)
}

func safeComponent(value string) bool {
	return value != "" && value != "." && value != ".." && utf8.ValidString(value) &&
		!strings.ContainsAny(value, "/\\\x00")
}

func checkPath(path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || info.IsDir() != directory || (!directory && !info.Mode().IsRegular()) {
		return fmt.Errorf("unexpected storage entry: %s", path)
	}
	return nil
}

func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

func ensureDirectory(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil {
		if !os.IsExist(err) {
			return err
		}
		return checkPath(path, true)
	}
	return syncDirectory(filepath.Dir(path))
}

// atomicFile skips unchanged files so a Session update does not rewrite another
// Session's history or Runtime metadata.
func atomicFile(path string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return atomicBytes(path, b)
}

func atomicBytes(path string, b []byte) error {
	if err := checkPath(path, false); err != nil && !os.IsNotExist(err) {
		return err
	}
	previous, err := os.ReadFile(path)
	if err == nil && bytes.Equal(previous, b) {
		return nil
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".write-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if err := errors.Join(err, f.Close()); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func readJSON(path string, value any) error {
	if err := checkPath(path, false); err != nil {
		return err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return decode(b, value)
}

// This short-lived lock prevents a read-only Open from seeing a mixed commit.
// writer.lock separately protects the lifetime of the sole writable Store.
func (s *Store) layoutLock(write bool) (*os.File, error) {
	flags, operation := os.O_RDONLY, syscall.LOCK_SH
	if write {
		flags, operation = os.O_CREATE|os.O_RDWR, syscall.LOCK_EX
	}
	path := filepath.Join(s.dir, "snapshot.lock")
	if err := checkPath(path, false); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), operation); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func (s *Store) load(write bool) error {
	lock, err := s.layoutLock(write)
	if err != nil {
		return err
	}
	defer lock.Close()
	pending := filepath.Join(s.dir, ".pending.json")
	if err := checkPath(pending, false); err == nil {
		if !write {
			return errors.New("incomplete file commit: writable reopen required")
		}
		if _, err := os.Lstat(filepath.Join(s.dir, ".append.pending.json")); !os.IsNotExist(err) {
			return errors.New("overlapping metadata and append intents")
		}
		var intent metadataIntent
		if err := readJSON(pending, &intent); err != nil {
			return err
		}
		if intent.Format != 1 || intent.Created == nil {
			return errors.New("invalid metadata intent")
		}
		if err := (&Store{data: intent.Next}).validate(); err != nil {
			return err
		}
		for key := range intent.Created {
			if _, exists := intent.Next.Sessions[key]; !exists {
				return errors.New("invalid created Session identity")
			}
		}
		if err := s.materialize(intent.Next, intent.Created); err != nil {
			return err
		}
		// Re-sync directories even for unchanged files: a previous rename or
		// deletion may have succeeded while its parent directory sync failed.
		if err := s.syncLayoutDirectories(); err != nil {
			return err
		}
		if err := os.Remove(pending); err != nil {
			return err
		}
		if err := syncDirectory(s.dir); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	var header storeFile
	err = readJSON(filepath.Join(s.dir, ".store.json"), &header)
	if os.IsNotExist(err) && write {
		if _, err := os.Lstat(filepath.Join(s.dir, ".append.pending.json")); !os.IsNotExist(err) {
			return errors.New("append intent without store metadata")
		}
		for _, legacy := range []string{"state.json", "runtime.json", "runtime.jsonl", filepath.Join("data", "state.json")} {
			if _, err := os.Lstat(filepath.Join(s.dir, legacy)); !os.IsNotExist(err) {
				return errors.New("legacy state.json layout is unsupported; existing data was not migrated")
			}
		}
		if entries, err := os.ReadDir(filepath.Join(s.dir, "sessions")); err == nil && len(entries) != 0 {
			return errors.New("session files without .store.json")
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
		next := snapshot{Format: 2, CursorKey: uuid.NewString(), Runtimes: map[string]resource.Runtime{}, Sessions: map[string]resource.Session{}}
		if err := s.install(next); err != nil {
			return err
		}
		s.data = next
		s.histories = map[string]*historyIndex{}
		return nil
	}
	if err != nil {
		return err
	}
	next := snapshot{Format: header.Format, CursorKey: header.CursorKey, Runtimes: map[string]resource.Runtime{}, Sessions: map[string]resource.Session{}}
	runtimes, err := readJSONLines[resource.Runtime](filepath.Join(s.dir, "runtime.jsonl"))
	if err != nil {
		return err
	}
	for _, r := range runtimes {
		if _, exists := next.Runtimes[r.Metadata.Name]; exists {
			return errors.New("duplicate runtime record")
		}
		next.Runtimes[r.Metadata.Name] = r
	}
	if err := s.walkSessions(func(path, namespace, name string) error {
		var r resource.Session
		if err := readJSON(filepath.Join(path, "state.json"), &r); err != nil {
			return err
		}
		if r.Metadata.Namespace != namespace || r.Metadata.Name != name {
			return errors.New("session directory identity mismatch")
		}
		next.Sessions[key(namespace, name)] = r
		return nil
	}); err != nil {
		return err
	}
	if err := (&Store{data: next}).validate(); err != nil {
		return err
	}
	s.data = next
	if err := s.recoverAppend(write); err != nil {
		return err
	}
	if err := s.loadHistories(); err != nil {
		return err
	}
	if write {
		return s.writeIndex(next)
	}
	return nil
}

func (s *Store) syncLayoutDirectories() error {
	if err := s.walkSessions(func(path, _, _ string) error { return syncDirectory(path) }); err != nil {
		return err
	}
	root := filepath.Join(s.dir, "sessions")
	namespaces, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, ns := range namespaces {
		if err := syncDirectory(filepath.Join(root, ns.Name())); err != nil {
			return err
		}
	}
	return errors.Join(syncDirectory(root), syncDirectory(s.dir))
}

func (s *Store) walkSessions(visit func(path, namespace, name string) error) error {
	root := filepath.Join(s.dir, "sessions")
	if err := checkPath(root, true); err != nil {
		return err
	}
	namespaces, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, ns := range namespaces {
		namespace := filepath.Join(root, ns.Name())
		if !safeComponent(ns.Name()) {
			return errors.New("invalid namespace directory")
		}
		if err := checkPath(namespace, true); err != nil {
			return err
		}
		entries, err := os.ReadDir(namespace)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			path := filepath.Join(namespace, entry.Name())
			if !safeComponent(entry.Name()) {
				return errors.New("invalid Session directory")
			}
			if err := checkPath(path, true); err != nil {
				return err
			}
			if err := visit(path, ns.Name(), entry.Name()); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) materialize(next snapshot, created map[string]bool) error {
	root := filepath.Join(s.dir, "sessions")
	if err := ensureDirectory(root); err != nil {
		return err
	}
	for _, r := range next.Sessions {
		ns := filepath.Join(root, r.Metadata.Namespace)
		if err := ensureDirectory(ns); err != nil {
			return err
		}
		path := filepath.Join(ns, r.Metadata.Name)
		if err := ensureDirectory(path); err != nil {
			return err
		}
		history := filepath.Join(path, "events.jsonl")
		if err := checkPath(history, false); err != nil {
			if !os.IsNotExist(err) || !created[key(r.Metadata.Namespace, r.Metadata.Name)] {
				return err
			}
			f, err := os.OpenFile(history, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			if err := errors.Join(f.Sync(), f.Close(), syncDirectory(path)); err != nil {
				return err
			}
		} else if created[key(r.Metadata.Namespace, r.Metadata.Name)] {
			info, err := os.Stat(history)
			if err != nil {
				return err
			}
			if info.Size() != 0 {
				return errors.New("new Session has an existing nonempty history")
			}
		}
		if err := atomicFile(filepath.Join(path, "state.json"), r); err != nil {
			return err
		}
	}
	if err := s.walkSessions(func(path, namespace, name string) error {
		if _, exists := next.Sessions[key(namespace, name)]; exists {
			return nil
		}
		// Delete only the two Store-owned files, preserving unrelated material.
		for _, name := range []string{"events.jsonl", "state.json"} {
			if err := os.Remove(filepath.Join(path, name)); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		return syncDirectory(filepath.Dir(path))
	}); err != nil {
		return err
	}
	runtimes := make([]resource.Runtime, 0, len(next.Runtimes))
	for _, r := range next.Runtimes {
		runtimes = append(runtimes, r)
	}
	sort.Slice(runtimes, func(i, j int) bool { return runtimes[i].Metadata.Name < runtimes[j].Metadata.Name })
	if err := writeJSONLines(filepath.Join(s.dir, "runtime.jsonl"), runtimes); err != nil {
		return err
	}
	if err := s.writeIndex(next); err != nil {
		return err
	}
	return atomicFile(filepath.Join(s.dir, ".store.json"), storeFile{Format: next.Format, CursorKey: next.CursorKey})
}

func (s *Store) commit(next snapshot) error {
	canonical, err := copyValue(next)
	if err != nil {
		return err
	}
	lock, err := s.layoutLock(true)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := s.noPending(); err != nil {
		return err
	}
	s.uncertain = true
	if err := s.install(canonical); err != nil {
		return fmt.Errorf("outcome_unknown: metadata commit: %w", err)
	}
	indexes := make(map[string]*historyIndex, len(canonical.Sessions))
	for _, r := range canonical.Sessions {
		index := s.histories[r.Status.SessionID]
		if index == nil {
			index = emptyHistory()
		}
		indexes[r.Status.SessionID] = index
	}
	s.data, s.histories, s.uncertain = canonical, indexes, false
	return nil
}

// Metadata recovery contains resources only; event payloads use append intents.
func (s *Store) install(next snapshot) error {
	created := map[string]bool{}
	for key, r := range next.Sessions {
		old, exists := s.data.Sessions[key]
		if !exists || old.Metadata.UID != r.Metadata.UID {
			created[key] = true
		}
	}
	pending := filepath.Join(s.dir, ".pending.json")
	if err := atomicFile(pending, metadataIntent{Format: 1, Next: next, Created: created}); err != nil {
		return err
	}
	if err := s.materialize(next, created); err != nil {
		return err
	}
	if err := os.Remove(pending); err != nil {
		return err
	}
	return syncDirectory(s.dir)
}
