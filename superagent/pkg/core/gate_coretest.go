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

// This fixture plays a Core delivery producer using the production
// transactional queue API. It is never compiled into the released binary.
func (s *Server) testDispatch(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost || r.URL.Path != "/_test/internal-deliveries" {
		return false
	}
	token := os.Getenv("SUPERAGENT_TEST_EVENTS")
	if token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
		return false
	}
	var deliveries []internalDelivery
	data, err := io.ReadAll(r.Body)
	if err != nil || decodeStrict(data, &deliveries) != nil || len(deliveries) == 0 {
		s.fail(w, problem("invalid_argument", "expected test deliveries"))
		return true
	}
	if err := s.enqueueTestDeliveries(r, deliveries); err != nil {
		s.fail(w, err)
	} else {
		s.write(w, http.StatusOK, deliveries)
	}
	return true
}

func (s *Server) enqueueTestDeliveries(r *http.Request, items []internalDelivery) error {
	ctx := r.Context()
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, d := range items {
		if err := enqueueInternalDelivery(ctx, tx, d); err != nil {
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

func outboxNow() time.Time { return readTestClock("SUPERAGENT_TEST_OUTBOX_CLOCK") }

func timerNow() time.Time { return readTestClock("SUPERAGENT_TEST_TIMER_CLOCK") }

// The file holds a fixed absolute timestamp that tests can advance across Core restarts.
func readTestClock(env string) time.Time {
	path := os.Getenv(env)
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
