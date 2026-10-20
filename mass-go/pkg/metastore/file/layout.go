package file

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/google/uuid"

	"github.com/zoumo/mass/mass-go/pkg/internal/fileio"
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
	return fileio.AtomicBytes(path, b.Bytes())
}

func readJSONLines[T any](path string) ([]T, error) {
	if err := fileio.CheckPath(path, false); err != nil {
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
		if err := fileio.Decode(line, &record); err != nil {
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

// This short-lived lock prevents a read-only Open from seeing a mixed commit.
// writer.lock separately protects the lifetime of the sole writable Store.
func (s *Store) layoutLock(write bool) (*os.File, error) {
	return fileio.Lock(s.dir, "snapshot.lock", write)
}

func (s *Store) load(write bool) error {
	lock, err := s.layoutLock(write)
	if err != nil {
		return err
	}
	defer lock.Close()
	pending := filepath.Join(s.dir, ".pending.json")
	if err := fileio.CheckPath(pending, false); err == nil {
		if !write {
			return errors.New("incomplete file commit: writable reopen required")
		}
		var intent metadataIntent
		if err := fileio.ReadJSON(pending, &intent); err != nil {
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
		if err := s.materialize(intent.Next); err != nil {
			return err
		}
		// Re-sync directories even for unchanged files: a previous rename or
		// deletion may have succeeded while its parent directory sync failed.
		if err := s.syncLayoutDirectories(); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	var header storeFile
	err = fileio.ReadJSON(filepath.Join(s.dir, ".store.json"), &header)
	if os.IsNotExist(err) && write {
		for _, legacy := range []string{"state.json", "runtime.json", "runtime.jsonl", filepath.Join("data", "state.json")} {
			if _, err := os.Lstat(filepath.Join(s.dir, legacy)); !os.IsNotExist(err) {
				return errors.New("legacy state.json layout is unsupported; existing data was not migrated")
			}
		}
		// Shared directories may contain only independently owned event logs.
		err := s.walkSessions(func(path, _, _ string) error {
			if _, err := os.Lstat(filepath.Join(path, "state.json")); !os.IsNotExist(err) {
				if err != nil {
					return err
				}
				return errors.New("session resource without .store.json")
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		next := snapshot{Format: 2, CursorKey: uuid.NewString(), Runtimes: map[string]resource.Runtime{}, Sessions: map[string]resource.Session{}}
		if err := s.install(next); err != nil {
			return err
		}
		s.data = next
		return s.loadMetadata()
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
		if err := fileio.ReadJSON(filepath.Join(path, "state.json"), &r); err != nil {
			if os.IsNotExist(err) {
				return nil
			}
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
	if err := s.loadMetadata(); err != nil {
		return err
	}
	if write {
		return s.writeIndex(next)
	}
	return nil
}

func (s *Store) syncLayoutDirectories() error {
	if err := s.walkSessions(func(path, _, _ string) error { return fileio.SyncDirectory(path) }); err != nil {
		return err
	}
	root := filepath.Join(s.dir, "sessions")
	namespaces, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, ns := range namespaces {
		if err := fileio.SyncDirectory(filepath.Join(root, ns.Name())); err != nil {
			return err
		}
	}
	return errors.Join(fileio.SyncDirectory(root), fileio.SyncDirectory(s.dir))
}

func (s *Store) walkSessions(visit func(path, namespace, name string) error) error {
	root := filepath.Join(s.dir, "sessions")
	if err := fileio.CheckPath(root, true); err != nil {
		return err
	}
	namespaces, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, ns := range namespaces {
		namespace := filepath.Join(root, ns.Name())
		if !fileio.SafeComponent(ns.Name()) {
			return errors.New("invalid namespace directory")
		}
		if err := fileio.CheckPath(namespace, true); err != nil {
			return err
		}
		entries, err := os.ReadDir(namespace)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			path := filepath.Join(namespace, entry.Name())
			if !fileio.SafeComponent(entry.Name()) {
				return errors.New("invalid Session directory")
			}
			if err := fileio.CheckPath(path, true); err != nil {
				return err
			}
			if err := visit(path, ns.Name(), entry.Name()); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) materialize(next snapshot) error {
	root := filepath.Join(s.dir, "sessions")
	if err := fileio.EnsureDirectory(root); err != nil {
		return err
	}
	for _, r := range next.Sessions {
		ns := filepath.Join(root, r.Metadata.Namespace)
		if err := fileio.EnsureDirectory(ns); err != nil {
			return err
		}
		path := filepath.Join(ns, r.Metadata.Name)
		if err := fileio.EnsureDirectory(path); err != nil {
			return err
		}
		if err := fileio.AtomicFile(filepath.Join(path, "state.json"), r); err != nil {
			return err
		}
	}
	if err := s.walkSessions(func(path, namespace, name string) error {
		if _, exists := next.Sessions[key(namespace, name)]; exists {
			return nil
		}
		// Delete only metadata; event retention belongs to a separate module.
		for _, name := range []string{"state.json"} {
			if err := os.Remove(filepath.Join(path, name)); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		return fileio.SyncDirectory(path)
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
	return fileio.AtomicFile(filepath.Join(s.dir, ".store.json"), storeFile{Format: next.Format, CursorKey: next.CursorKey})
}

func (s *Store) commit(next snapshot) error {
	canonical, err := fileio.CopyValue(next)
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
	if err := s.syncMetadata(canonical, s.data); err != nil {
		return fmt.Errorf("outcome_unknown: metadata replica: %w", err)
	}
	if err := s.finishMetadata(); err != nil {
		return fmt.Errorf("outcome_unknown: metadata completion: %w", err)
	}
	s.data, s.uncertain = canonical, false
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
	if err := fileio.AtomicFile(pending, metadataIntent{Format: 1, Next: next, Created: created}); err != nil {
		return err
	}
	if err := s.materialize(next); err != nil {
		return err
	}
	return nil
}

// Retain resource-only redo evidence until the verified SQL view is complete.
func (s *Store) finishMetadata() error {
	if err := os.Remove(filepath.Join(s.dir, ".pending.json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	return fileio.SyncDirectory(s.dir)
}

func (s *Store) noPending() error {
	if _, err := os.Lstat(filepath.Join(s.dir, ".pending.json")); !os.IsNotExist(err) {
		if err != nil {
			return err
		}
		return errors.New("incomplete metadata commit: writable reopen required")
	}
	return nil
}
