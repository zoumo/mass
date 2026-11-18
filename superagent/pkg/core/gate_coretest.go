//go:build coretest

package core

import (
	"crypto/subtle"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// This fixture plays a future Core event producer using the production
// transactional queue API. It is never compiled into the released binary.
func (s *Server) testDispatch(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != "/_test/events" || r.Method != http.MethodPost {
		return false
	}
	token := os.Getenv("SUPERAGENT_TEST_EVENTS")
	if token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
		return false
	}
	var items []Event
	data, err := io.ReadAll(r.Body)
	if err != nil || decodeStrict(data, &items) != nil || len(items) == 0 {
		s.fail(w, problem("invalid_argument", "expected test events"))
		return true
	}
	if err := s.enqueueTestEvents(r, items); err != nil {
		s.fail(w, err)
		return true
	}
	s.write(w, http.StatusOK, items)
	return true
}

func (s *Server) enqueueTestEvents(r *http.Request, items []Event) error {
	ctx := r.Context()
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, e := range items {
		e.At = ""
		if err := EnqueueEvent(ctx, tx, e); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Test-build-only process crash gate. Normal binaries never read these variables.
func commitGate(stage string) {
	if os.Getenv("SUPERAGENT_TEST_GATE_STAGE") != stage {
		return
	}
	dir := os.Getenv("SUPERAGENT_TEST_GATE_DIR")
	if dir == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, stage), []byte("reached"), 0o600); err != nil {
		panic(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "release")); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The file holds a fixed absolute timestamp so the test can advance the same
// clock across isolated Core restarts without waiting for production leases.
func outboxNow() time.Time {
	path := os.Getenv("SUPERAGENT_TEST_OUTBOX_CLOCK")
	if path == "" {
		return time.Now().UTC()
	}
	b, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	at, err := time.Parse(time.RFC3339Nano, string(b))
	if err != nil {
		panic(err)
	}
	return at.UTC()
}
