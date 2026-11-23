package dws

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Send evidence belongs to the exclusive queue owner. Each attempt has its own
// file so a retry keeps the identities and diagnostics of earlier attempts.
type sendEvidence struct {
	OutboxID        string         `json:"outboxId"`
	RetryAt         string         `json:"retryAt,omitempty"`
	ConversationID  string         `json:"conversationId"`
	StartedAt       time.Time      `json:"startedAt"`
	Result          channelOutcome `json:"result"`
	Settled         bool           `json:"settled,omitempty"`
	DismissedReason string         `json:"dismissedReason,omitempty"`
}

func (a sendEvidence) sendKey() string {
	digest := sha256.Sum256([]byte(a.OutboxID + "\x00" + a.RetryAt))
	return fmt.Sprintf("send_%x", digest[:16])
}

func (a sendEvidence) ref() nativeMessageRef {
	if a.Result.Outcome != "sent" || a.Result.ConversationID != a.ConversationID || !validNativeID(a.Result.MessageID) {
		return nativeMessageRef{}
	}
	return nativeMessageRef{ConversationID: a.ConversationID, MessageID: a.Result.MessageID}
}

func (a sendEvidence) equivocal() bool {
	if a.DismissedReason != "" {
		return false
	}
	switch a.Result.Outcome {
	case "", "pending", "unknown":
		return true
	case "sent":
		return a.ref() == (nativeMessageRef{})
	}
	return false
}

type sendEvidenceStore struct {
	mu       sync.Mutex
	path     string
	attempts map[string]sendEvidence
	exact    map[nativeMessageRef]sendEvidence
	open     map[string]map[string]sendEvidence
	byOutbox map[string]map[string]sendEvidence
	recovery map[string]sendEvidence
}

func openSendEvidence(databasePath string) (*sendEvidenceStore, error) {
	path := filepath.Clean(databasePath) + ".dws-sends"
	if err := os.Mkdir(path, 0o700); err == nil {
		parent, err := os.Open(filepath.Dir(path))
		if err != nil {
			return nil, err
		}
		defer parent.Close()
		if err := parent.Sync(); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("DWS send evidence requires a private directory")
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	s := &sendEvidenceStore{
		path: path, attempts: make(map[string]sendEvidence), exact: make(map[nativeMessageRef]sendEvidence),
		open: make(map[string]map[string]sendEvidence), byOutbox: make(map[string]map[string]sendEvidence),
		recovery: make(map[string]sendEvidence),
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue // Incomplete atomic writes are ignored.
		}
		if !entry.Type().IsRegular() {
			return nil, errors.New("invalid DWS send evidence file")
		}
		raw, err := os.ReadFile(filepath.Join(path, entry.Name()))
		var attempt sendEvidence
		if err != nil || json.Unmarshal(raw, &attempt) != nil || attempt.OutboxID == "" ||
			!validNativeID(attempt.ConversationID) || attempt.StartedAt.IsZero() || entry.Name() != attempt.sendKey()+".json" {
			return nil, errors.New("invalid DWS send evidence record")
		}
		if attempt.RetryAt != "" {
			if _, err := time.Parse(time.RFC3339Nano, attempt.RetryAt); err != nil {
				return nil, errors.New("invalid DWS send evidence retry deadline")
			}
		}
		if err := s.checkRef(attempt); err != nil {
			return nil, err
		}
		s.index(attempt)
	}
	return s, nil
}

func (s *sendEvidenceStore) checkRef(a sendEvidence) error {
	if previous, ok := s.exact[a.ref()]; ok && previous.OutboxID != a.OutboxID {
		return errors.New("DWS message identity belongs to different Outbox items")
	}
	return nil
}

func (s *sendEvidenceStore) index(a sendEvidence) {
	key := a.sendKey()
	s.attempts[key] = a
	if s.byOutbox[a.OutboxID] == nil {
		s.byOutbox[a.OutboxID] = make(map[string]sendEvidence)
	}
	s.byOutbox[a.OutboxID][key] = a
	if (!a.Settled && a.Result.Outcome == "sent") ||
		(a.Result.TaskID != "" && a.equivocal() && (a.Result.Outcome == "unknown" || a.Result.Outcome == "sent")) {
		s.recovery[key] = a
	} else {
		delete(s.recovery, key)
	}
	if ref := a.ref(); ref != (nativeMessageRef{}) {
		if previous, ok := s.exact[ref]; !ok || !previous.Settled || a.Settled {
			s.exact[ref] = a
		}
	}
	if a.equivocal() {
		if s.open[a.ConversationID] == nil {
			s.open[a.ConversationID] = make(map[string]sendEvidence)
		}
		s.open[a.ConversationID][key] = a
	} else {
		delete(s.open[a.ConversationID], key)
	}
}

func (s *sendEvidenceStore) put(a sendEvidence) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkRef(a); err != nil {
		return err
	}
	if err := writeEvidence(s.path, a.sendKey()+".json", a); err != nil {
		return err
	}
	s.index(a)
	return nil
}

func writeEvidence(path, name string, value any) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	file, err := os.CreateTemp(path, ".pending-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := json.NewEncoder(file).Encode(value); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), filepath.Join(path, name)); err != nil {
		return err
	}
	return dir.Sync()
}

func (s *sendEvidenceStore) get(a sendEvidence) (sendEvidence, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	attempt, ok := s.attempts[a.sendKey()]
	return attempt, ok
}

func (s *sendEvidenceStore) byTask(id, taskID string) (sendEvidence, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.byOutbox[id] {
		if a.Result.TaskID == taskID {
			return a, true
		}
	}
	return sendEvidence{}, false
}

func (s *sendEvidenceStore) successfulAttempt(id string) (sendEvidence, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.byOutbox[id] {
		if a.Result.Outcome == "sent" {
			return a, true
		}
	}
	return sendEvidence{}, false
}

func (s *sendEvidenceStore) match(ref nativeMessageRef, observedAt time.Time) (exact sendEvidence, unresolved bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if attempt, ok := s.exact[ref]; ok {
		return attempt, false
	}
	for _, a := range s.open[ref.ConversationID] {
		if !observedAt.Before(a.StartedAt) {
			return sendEvidence{}, true
		}
	}
	return sendEvidence{}, false
}

func (s *sendEvidenceStore) hasSent(id, conversationID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.byOutbox[id] {
		if a.ConversationID == conversationID && a.Settled && a.ref() != (nativeMessageRef{}) {
			return true
		}
	}
	return false
}

func (s *sendEvidenceStore) recoverable() []sendEvidence {
	s.mu.Lock()
	defer s.mu.Unlock()
	attempts := make([]sendEvidence, 0, len(s.recovery))
	for _, a := range s.recovery {
		attempts = append(attempts, a)
	}
	return attempts
}
