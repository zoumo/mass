package triggers

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestDWSDatabaseIdentityAndPermissions(t *testing.T) {
	c := Config{Source: "dws", AccountID: "account", InstanceID: "fixture-instance", DatabasePath: filepath.Join(t.TempDir(), "private", "dws?#.sqlite")}
	queue, closeQueue, err := openDWSQueue(c)
	if err != nil {
		t.Fatal(err)
	}
	var journal string
	var syncMode int
	if err := queue.db.QueryRowContext(t.Context(), `PRAGMA journal_mode`).Scan(&journal); err != nil || journal != "wal" {
		t.Fatalf("journal %q, %v", journal, err)
	}
	if err := queue.db.QueryRowContext(t.Context(), `PRAGMA synchronous`).Scan(&syncMode); err != nil || syncMode != 2 {
		t.Fatalf("synchronous %d, %v", syncMode, err)
	}
	for _, suffix := range []string{"", "-wal", "-shm", ".lock"} {
		file, err := os.Stat(c.DatabasePath + suffix)
		if err != nil || file.Mode().Perm() != 0o600 {
			t.Fatalf("database file %q is not private: %v, %v", suffix, file, err)
		}
	}
	if _, _, err := openDWSQueue(c); err == nil || !strings.Contains(err.Error(), "acquire") {
		t.Fatalf("concurrent database access: %v", err)
	}
	closeQueue()
	other := c
	other.InstanceID = "other"
	if _, _, err := openDWSQueue(other); err == nil || !strings.Contains(err.Error(), "different source, account or instance") {
		t.Fatalf("cross-instance database access: %v", err)
	}
	if _, _, err := openTriggerDatabase(c.DatabasePath, triggerDatabaseIdentity{
		ApplicationID: 0x47485452, Version: 1, Source: "github", AccountID: c.AccountID, InstanceID: c.InstanceID,
	}, ""); err == nil || !strings.Contains(err.Error(), "type") {
		t.Fatalf("cross-trigger database access: %v", err)
	}
	queue, closeQueue, err = openDWSQueue(c)
	if err != nil {
		t.Fatal(err)
	}
	defer closeQueue()
	if count, failures, err := queue.counts(); err != nil || count != 0 || failures != 0 {
		t.Fatalf("empty database counts %d, %d, %v", count, failures, err)
	}
}

func TestDWSQueueRestartsAfterOutageInOrder(t *testing.T) {
	corePath(t)
	addr := freeAddress(t)
	coreDir := t.TempDir()
	database := filepath.Join(t.TempDir(), "dws.sqlite")
	configure := func(c *Config) { c.DatabasePath = database }
	first := event("one", "first")
	first["event_id"] = "original-envelope"
	duplicate := event("one", "first")
	duplicate["event_id"] = "new-envelope"
	trigger, status := triggerStart(t, "http://"+addr, events(t, first, duplicate, event("two", "second")), "", 100, configure)
	wait(t, "durable offline queue", func() bool {
		s := snapshot(t, status)
		return s.Backlog == 2 && s.Duplicates == 1 && s.LastError == "core_unreachable_or_outcome_unknown"
	})
	killAdapter(t, trigger)
	db, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	var originalPayload []byte
	err = db.QueryRowContext(t.Context(), `SELECT payload FROM dws_submissions ORDER BY sequence LIMIT 1`).Scan(&originalPayload)
	_ = db.Close()
	if err != nil {
		t.Fatal(err)
	}
	core := coreStart(t, coreDir, addr)
	var mu sync.Mutex
	var submissions []Submission
	var bodies [][]byte
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/channel/messages" {
			data, err := io.ReadAll(r.Body)
			var item Submission
			if err != nil || json.Unmarshal(data, &item) != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			mu.Lock()
			submissions = append(submissions, item)
			bodies = append(bodies, data)
			mu.Unlock()
			r.Body = io.NopCloser(bytes.NewReader(data))
		}
		forwardDWSRequest(t, w, r, "http://"+addr, false)
	}))
	defer proxy.Close()
	restarted, recovered := triggerStart(t, proxy.URL, events(t, event("three", "third")), "", 100, configure)
	wait(t, "recovered queue then new input", func() bool {
		s := snapshot(t, recovered)
		return s.ConsumerReady && s.Accepted == 3 && s.Backlog == 0
	})
	terminate(t, restarted)
	mu.Lock()
	got := submissions
	gotBodies := bodies
	mu.Unlock()
	if want := []string{"one", "two", "three"}; len(got) != len(want) {
		t.Fatalf("unexpected submissions %v", got)
	} else {
		for i, nativeID := range want {
			if got[i].OperationID != "dws-message:"+nativeID || got[i].Message.SourceMessageID != nativeID ||
				got[i].Message.Text != []string{"first", "second", "third"}[i] {
				t.Fatalf("submission %d changed: %+v", i, got[i])
			}
		}
	}
	if got[0].Message.SourceEventID != "original-envelope" || !bytes.Equal(gotBodies[0], originalPayload) {
		t.Fatalf("redelivery changed the stored submission: %+v", got[0].Message)
	}
	if rows := readMessages(t, "http://"+addr); len(rows) != 3 {
		t.Fatalf("Core admissions: %v", rows)
	}
	terminate(t, core)
}

func forwardDWSRequest(t *testing.T, w http.ResponseWriter, r *http.Request, target string, dropReceipt bool) {
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target+r.URL.RequestURI(), r.Body)
	if err != nil {
		t.Error(err)
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	req.Header = r.Header.Clone()
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	defer res.Body.Close()
	if dropReceipt {
		_, _ = io.Copy(io.Discard, res.Body)
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	maps.Copy(w.Header(), res.Header)
	w.WriteHeader(res.StatusCode)
	_, _ = io.Copy(w, res.Body)
}

func TestDWSQueueReplaysLostReceiptWithFeedback(t *testing.T) {
	f := newEgressFixture(t, false)
	database := filepath.Join(f.dir, "dws.sqlite")
	configure := func(c *Config) { c.DatabasePath = database; c.RetryMillis = 1000 }
	offline := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer offline.Close()
	first := f.trigger(offline.URL, f.feedbackConfig, configure)
	firstStatus := first.endpoint(t, "statusEndpoint")
	f.feed(1, "long-wait", "peer", "first queued")
	f.feed(2, "awaiting", "peer", "second queued")
	wait(t, "both messages stored while head is retrying", func() bool {
		s := snapshot(t, firstStatus)
		return s.Backlog == 2 && s.Accepted == 0
	})
	f.feedbackPhase(egressConversation, "long-wait", feedbackAwaiting)
	f.feedbackPhase(egressConversation, "awaiting", feedbackAwaiting)
	killAdapter(t, first)

	// Simulate a process stop between committing SQLite and saving feedback.
	feedbackPath := filepath.Join(f.dir, "feedback.json")
	raw, err := os.ReadFile(feedbackPath)
	if err != nil {
		t.Fatal(err)
	}
	var feedback feedbackState
	if err := json.Unmarshal(raw, &feedback); err != nil {
		t.Fatal(err)
	}
	key := feedbackKey(Channel{ConversationID: egressConversation, MessageID: "long-wait"})
	observed := feedback.Records[key].ObservedAt
	delete(feedback.Records, key)
	raw, err = json.Marshal(feedback)
	if err != nil {
		t.Fatal(err)
	}
	testWrite(t, feedbackPath, raw)
	lost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwardDWSRequest(t, w, r, f.endpoint, r.Method == http.MethodPost && r.URL.Path == "/v1/channel/messages")
	}))
	defer lost.Close()
	emptyFeed := t.TempDir()
	interrupted := f.triggerFeed(lost.URL, emptyFeed, f.feedbackConfig, configure)
	interruptedStatus := interrupted.endpoint(t, "statusEndpoint")
	original := f.message("long-wait")
	f.feedbackPhase(egressConversation, "long-wait", feedbackThinking)
	if local := snapshot(t, interruptedStatus); local.Backlog != 2 || local.ConsumerReady {
		t.Fatalf("admission advanced despite lost receipt: %+v", local)
	}
	killAdapter(t, interrupted)
	raw, err = os.ReadFile(feedbackPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &feedback); err != nil || !feedback.Records[key].ObservedAt.Equal(observed) {
		t.Fatalf("recovered feedback observation time changed: %v", err)
	}
	restarted := f.triggerFeed(f.endpoint, emptyFeed, f.feedbackConfig, configure)
	recoveredStatus := restarted.endpoint(t, "statusEndpoint")
	wait(t, "lost receipt replayed once with original IDs", func() bool {
		s := snapshot(t, recoveredStatus)
		return s.Backlog == 0 && s.Accepted == 2 && s.Duplicates == 1 && s.ConsumerReady
	})
	if got := f.message("long-wait"); !reflect.DeepEqual(got, original) {
		t.Fatalf("replay changed original Core message: %+v", got)
	}
	f.assertRouted(original, 1)
	f.feedbackPhase(egressConversation, "awaiting", feedbackThinking)
	f.requireEmotion("long-wait", "thinking-id")
	f.requireEmotion("awaiting", "thinking-id")
}

func TestDWSQueueWriteFailurePreservesCommittedRow(t *testing.T) {
	addr := freeAddress(t)
	database := filepath.Join(t.TempDir(), "dws.sqlite")
	delivery := filepath.Join(t.TempDir(), "deliver.ndjson")
	trigger, status := triggerStart(t, "http://"+addr, delivery, "await-file", 100, func(c *Config) { c.DatabasePath = database })
	wait(t, "ready before storage fault", func() bool { return snapshot(t, status).ConsumerReady })
	db, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(t.Context(), `CREATE TRIGGER fail_insert BEFORE INSERT ON dws_submissions
		WHEN NEW.native_message_id = 'write-fault'
		BEGIN SELECT RAISE(ABORT, 'fixture write fault'); END`); err != nil {
		t.Fatal(err)
	}
	source := events(t, event("committed", "persist this"), event("write-fault", "do not log this"))
	if err := os.Rename(source, delivery); err != nil {
		t.Fatal(err)
	}
	expectProcessFailure(t, trigger, "database_write_failed")
	if strings.Contains(trigger.text(), "fixture write fault") || strings.Contains(trigger.text(), "do not log this") {
		t.Fatal("storage failure was not sanitized")
	}
	queue := dwsQueue{db: db}
	head, err := queue.head()
	if err != nil || head.message.SourceMessageID != "committed" {
		t.Fatalf("committed row unavailable: %+v, %v", head, err)
	}
	if count, failed, err := queue.counts(); err != nil || count != 1 || failed != 0 {
		t.Fatalf("atomic insert counts %d, %d, %v", count, failed, err)
	}
}

func TestDWSQueueRetainsPermanentFailure(t *testing.T) {
	rejecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/channel/messages" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":{"code":"forbidden"}}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer rejecting.Close()
	database := filepath.Join(t.TempDir(), "dws.sqlite")
	configure := func(c *Config) { c.DatabasePath = database }
	trigger, _ := triggerStart(t, rejecting.URL, events(t, event("rejected", "retain this")), "", 100, configure)
	expectProcessFailure(t, trigger, "core_rejected_forbidden")
	if !strings.Contains(trigger.text(), `"permanentFailures":1`) {
		t.Fatalf("permanent failures absent from status: %s", trigger.text())
	}
	db, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	queue := dwsQueue{db: db}
	if _, err := queue.head(); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("failed row remains at the pending head: %v", err)
	}
	var failure string
	var payload []byte
	if err := db.QueryRowContext(t.Context(), `SELECT failure, payload FROM dws_submissions`).Scan(&failure, &payload); err != nil ||
		failure != "forbidden" || !bytes.Contains(payload, []byte("retain this")) {
		t.Fatalf("permanent queue record: %q, %s, %v", failure, payload, err)
	}
	restarted, status := triggerStart(t, rejecting.URL, events(t, event("rejected", "retain this")), "", 100, configure)
	wait(t, "redelivered permanently rejected input uses existing row", func() bool {
		s := snapshot(t, status)
		return s.ConsumerReady && s.Backlog == 0 && s.PermanentFailures == 1 && s.Duplicates == 1
	})
	terminate(t, restarted)
}
