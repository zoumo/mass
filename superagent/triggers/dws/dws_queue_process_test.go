package dws

import (
	"bytes"
	"database/sql"
	"encoding/json"
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
	"time"

	"github.com/zoumo/mass/superagent/triggers/internal/coreclient"
	"github.com/zoumo/mass/superagent/triggers/internal/triggerdb"
	"github.com/zoumo/mass/superagent/triggers/internal/triggertest"
)

func TestDWSDatabaseIdentityAndPermissions(t *testing.T) {
	c := dwsConfig{Source: "dws", AccountID: "account", InstanceID: "fixture-instance", DatabasePath: filepath.Join(t.TempDir(), "private", "dws?#.sqlite")}
	queue, closeQueue, err := openDWSQueue(c)
	if err != nil {
		t.Fatal(err)
	}
	var journal string
	if err := queue.db.QueryRowContext(t.Context(), `PRAGMA journal_mode`).Scan(&journal); err != nil || journal != "wal" {
		t.Fatalf("journal %q, %v", journal, err)
	}
	for name, want := range map[string]int{
		"application_id": 0x44575354,
		"user_version":   3,
		"synchronous":    2,
		"foreign_keys":   1,
		"busy_timeout":   2000,
	} {
		var got int
		if err := queue.db.QueryRowContext(t.Context(), `PRAGMA `+name).Scan(&got); err != nil || got != want {
			t.Fatalf("%s = %d, want %d: %v", name, got, want, err)
		}
	}
	if got := queue.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("maximum open connections = %d, want 1", got)
	}
	if _, _, err := queue.counts(); err != nil {
		t.Fatal(err)
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
	if _, _, err := triggerdb.Open(c.DatabasePath, triggerdb.Identity{
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
	triggertest.CorePath(t)
	addr := triggertest.FreeAddress(t)
	coreDir := t.TempDir()
	database := filepath.Join(t.TempDir(), "dws.sqlite")
	configure := func(c *dwsConfig) { c.DatabasePath = database }
	first := event("one", "first")
	first["event_id"] = "original-envelope"
	duplicate := event("one", "first")
	duplicate["event_id"] = "new-envelope"
	trigger, status := triggerStart(t, "http://"+addr, events(t, first, duplicate, event("two", "second")), "", 100, configure)
	triggertest.Wait(t, "durable offline queue", func() bool {
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
	var submissions []coreclient.TriggerSubmission
	var bodies [][]byte
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/apis/core/v1/trigger-messages" {
			data, err := io.ReadAll(r.Body)
			var item coreclient.TriggerSubmission
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
	triggertest.Wait(t, "recovered queue then new input", func() bool {
		s := snapshot(t, recovered)
		return s.ConsumerReady && s.Accepted == 3 && s.Backlog == 0
	})
	triggertest.Terminate(t, restarted)
	mu.Lock()
	got := submissions
	gotBodies := bodies
	mu.Unlock()
	want := []string{"one", "two", "three"}
	index := -1
	for i, submission := range got {
		if i > 0 && submission.OperationID == got[i-1].OperationID {
			if !bytes.Equal(gotBodies[i], gotBodies[i-1]) {
				t.Fatalf("retry changed submission %q", submission.OperationID)
			}
			continue
		}
		index++
		if index >= len(want) || submission.OperationID != "dws-message:"+want[index] || submission.Message.SourceMessageID != want[index] ||
			len(submission.Message.Content) != 1 || submission.Message.Content[0].Text == nil ||
			!strings.HasPrefix(submission.Message.Content[0].Text.Text, []string{"first", "second", "third"}[index]+"\n\nAuthor: self") {
			t.Fatalf("submission %d changed or arrived out of order: %+v", index, submission)
		}
	}
	if index != len(want)-1 {
		t.Fatalf("received %d distinct submissions, want %d", index+1, len(want))
	}
	original, err := decodeQueuedSubmission(originalPayload)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got[0].Message.Meta, []byte(`"eventId":"original-envelope"`)) ||
		!bytes.Equal(gotBodies[0], testMarshalMessage(t, original.TriggerSubmission)) {
		t.Fatalf("redelivery changed the stored submission: %+v", got[0].Message)
	}
	if rows := triggertest.ReadMessages(t, "http://"+addr); len(rows) != 3 {
		t.Fatalf("Core admissions: %v", rows)
	}
	triggertest.Terminate(t, core)
}

func TestDWSDrainsExistingWirePayload(t *testing.T) {
	addr := triggertest.FreeAddress(t)
	core := coreStart(t, t.TempDir(), addr)
	c := dwsConfig{
		Source: "dws", AccountID: "account", InstanceID: "fixture-instance",
		DatabasePath: filepath.Join(t.TempDir(), "dws.sqlite"),
	}
	queue, closeQueue, err := openDWSQueue(c)
	if err != nil {
		t.Fatal(err)
	}
	// This is the persisted v3 row shape, including the adapter-only conversationId.
	const message = `{"operationId":"dws-message:pre-upgrade","message":{"sourceMessageId":"pre-upgrade","threadId":"dws:old","content":[{"type":"text","text":"Queued before upgrade"}]}}`
	payload := []byte(strings.TrimSuffix(message, "}") + `,"conversationId":"old"}`)
	_, err = queue.db.ExecContext(t.Context(), `INSERT INTO dws_submissions (account_id,native_message_id,observed_at,payload) VALUES (?,?,?,?)`,
		c.AccountID, "pre-upgrade", time.Now().UTC().Format(time.RFC3339Nano), payload)
	closeQueue()
	if err != nil {
		t.Fatal(err)
	}

	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == coreclient.TriggerMessagesPath {
			body, err := io.ReadAll(r.Body)
			if err != nil || string(body) != message ||
				r.Header.Get("X-Superagent-Source") != c.Source ||
				r.Header.Get("X-Superagent-Account-Id") != c.AccountID {
				t.Error("queued request body or declared Trigger identity changed")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		forwardDWSRequest(t, w, r, "http://"+addr, false)
	}))
	defer proxy.Close()
	adapter, status := triggerStart(t, proxy.URL, events(t), "", 100, func(config *dwsConfig) {
		config.DatabasePath = c.DatabasePath
	})
	triggertest.Wait(t, "existing queue submitted", func() bool {
		s := snapshot(t, status)
		return s.Accepted == 1 && s.Backlog == 0
	})
	rows := triggertest.ReadMessages(t, "http://"+addr)
	if len(rows) != 1 || rows[0]["source"] != c.Source || rows[0]["accountId"] != c.AccountID ||
		rows[0]["sourceMessageId"] != "pre-upgrade" || messageText(rows[0]) != "Queued before upgrade" {
		t.Fatalf("existing queued message was not admitted: %v", rows)
	}
	triggertest.Terminate(t, adapter)
	triggertest.Terminate(t, core)
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
	database := filepath.Join(f.Dir, "dws.sqlite")
	configure := func(c *dwsConfig) { c.DatabasePath = database; c.RetryMillis = 1000 }
	offline := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer offline.Close()
	first := f.trigger(offline.URL, f.feedbackConfig, configure)
	firstStatus := first.Endpoint(t, "statusEndpoint")
	f.feed(1, "long-wait", "peer", "first queued")
	f.feed(2, "awaiting", "peer", "second queued")
	triggertest.Wait(t, "both messages stored while head is retrying", func() bool {
		s := snapshot(t, firstStatus)
		return s.Backlog == 2 && s.Accepted == 0
	})
	f.feedbackPhase(egressConversation, "long-wait", feedbackAwaiting)
	f.feedbackPhase(egressConversation, "awaiting", feedbackAwaiting)
	killAdapter(t, first)

	// Simulate a process stop between committing SQLite and saving feedback.
	feedbackPath := filepath.Join(f.Dir, "feedback.json")
	raw, err := os.ReadFile(feedbackPath)
	if err != nil {
		t.Fatal(err)
	}
	var feedback feedbackState
	if err := json.Unmarshal(raw, &feedback); err != nil {
		t.Fatal(err)
	}
	key := feedbackKey(nativeReplyTarget{ConversationID: egressConversation, MessageID: "long-wait"})
	observed := feedback.Records[key].ObservedAt
	delete(feedback.Records, key)
	raw, err = json.Marshal(feedback)
	if err != nil {
		t.Fatal(err)
	}
	triggertest.Write(t, feedbackPath, raw)
	lost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwardDWSRequest(t, w, r, f.Endpoint, r.Method == http.MethodPost && r.URL.Path == "/apis/core/v1/trigger-messages")
	}))
	defer lost.Close()
	emptyFeed := t.TempDir()
	interrupted := f.triggerFeed(lost.URL, emptyFeed, f.feedbackConfig, configure)
	interruptedStatus := interrupted.Endpoint(t, "statusEndpoint")
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
	restarted := f.triggerFeed(f.Endpoint, emptyFeed, f.feedbackConfig, configure)
	recoveredStatus := restarted.Endpoint(t, "statusEndpoint")
	triggertest.Wait(t, "lost receipt replayed once with original IDs", func() bool {
		s := snapshot(t, recoveredStatus)
		return s.Backlog == 0 && s.Accepted == 2 && s.Duplicates == 1 && s.ConsumerReady
	})
	if got := f.message("long-wait"); !reflect.DeepEqual(got, original) {
		t.Fatalf("replay changed original Core message: %+v", got)
	}
	f.AssertRouted(original, 1)
	f.feedbackPhase(egressConversation, "awaiting", feedbackThinking)
	f.requireEmotion("long-wait", "thinking-id")
	f.requireEmotion("awaiting", "thinking-id")
}

func TestDWSQueueWriteFailurePreservesCommittedRow(t *testing.T) {
	addr := triggertest.FreeAddress(t)
	database := filepath.Join(t.TempDir(), "dws.sqlite")
	delivery := filepath.Join(t.TempDir(), "deliver.ndjson")
	trigger, status := triggerStart(t, "http://"+addr, delivery, "await-file", 100, func(c *dwsConfig) { c.DatabasePath = database })
	triggertest.Wait(t, "ready before storage fault", func() bool { return snapshot(t, status).ConsumerReady })
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
	if strings.Contains(trigger.Text(), "fixture write fault") || strings.Contains(trigger.Text(), "do not log this") {
		t.Fatal("storage failure was not sanitized")
	}
	queue := newDWSQueue(db, "")
	pending, err := queue.pending(0)
	if err != nil || len(pending) != 1 || pending[0].submission.Message.SourceMessageID != "committed" {
		t.Fatalf("committed row unavailable: %+v, %v", pending, err)
	}
	if count, failed, err := queue.counts(); err != nil || count != 1 || failed != 0 {
		t.Fatalf("atomic insert counts %d, %d, %v", count, failed, err)
	}
}

func TestDWSQueueRetainsPermanentFailure(t *testing.T) {
	rejecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/apis/core/v1/trigger-messages" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":{"code":"forbidden"}}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer rejecting.Close()
	database := filepath.Join(t.TempDir(), "dws.sqlite")
	configure := func(c *dwsConfig) { c.DatabasePath = database }
	trigger, _ := triggerStart(t, rejecting.URL, events(t, event("rejected", "retain this")), "", 100, configure)
	expectProcessFailure(t, trigger, "core_rejected_forbidden")
	if !strings.Contains(trigger.Text(), `"permanentFailures":1`) {
		t.Fatalf("permanent failures absent from status: %s", trigger.Text())
	}
	db, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	queue := newDWSQueue(db, "")
	if pending, err := queue.pending(0); err != nil || len(pending) != 0 {
		t.Fatalf("failed row remains at the pending head: %v", err)
	}
	var failure string
	var payload []byte
	if err := db.QueryRowContext(t.Context(), `SELECT failure, payload FROM dws_submissions`).Scan(&failure, &payload); err != nil ||
		failure != "forbidden" || !bytes.Contains(payload, []byte("retain this")) {
		t.Fatalf("permanent queue record: %q, %s, %v", failure, payload, err)
	}
	restarted, status := triggerStart(t, rejecting.URL, events(t, event("rejected", "retain this")), "", 100, configure)
	triggertest.Wait(t, "redelivered permanently rejected input uses existing row", func() bool {
		s := snapshot(t, status)
		return s.ConsumerReady && s.Backlog == 0 && s.PermanentFailures == 1 && s.Duplicates == 1
	})
	triggertest.Terminate(t, restarted)
}
