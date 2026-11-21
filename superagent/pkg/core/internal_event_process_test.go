package core_test

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/superagent/pkg/core"
)

type eventParty struct {
	Kind             string `json:"kind"`
	TopicID          string `json:"topic_id,omitempty"`
	SessionBindingID string `json:"session_binding_id,omitempty"`
}

type internalEvent struct {
	ID        string                  `json:"event_id"`
	Kind      string                  `json:"kind"`
	Sender    eventParty              `json:"sender"`
	Recipient eventParty              `json:"recipient"`
	Content   *core.StructuredContent `json:"content,omitempty"`
	Body      json.RawMessage         `json:"body,omitempty"`
	Refs      []json.RawMessage       `json:"refs,omitempty"`
	Truncated bool                    `json:"truncated,omitempty"`
	Issue     json.RawMessage         `json:"issue,omitempty"`
	Report    json.RawMessage         `json:"report,omitempty"`
	Messages  []json.RawMessage       `json:"messages,omitempty"`
}

type internalEventRow struct {
	ID, Kind, Status, SentAt string
	Body, Content            json.RawMessage
}

func internalRows(t *testing.T, f *fixture, where string, args ...any) []internalEventRow {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(f.data, "core.sqlite")+"?mode=ro&_pragma=busy_timeout(1000)")
	require.NoError(t, err)
	defer db.Close()
	rows, err := db.QueryContext(t.Context(), `SELECT event_id,kind,status,COALESCE(sent_at,''),body,content
 FROM internal_events WHERE `+where+` ORDER BY rowid`, args...)
	require.NoError(t, err)
	defer rows.Close()
	var result []internalEventRow
	for rows.Next() {
		var d internalEventRow
		var body, content []byte
		require.NoError(t, rows.Scan(&d.ID, &d.Kind, &d.Status, &d.SentAt, &body, &content))
		d.Body, d.Content = body, content
		result = append(result, d)
	}
	require.NoError(t, rows.Err())
	return result
}

func dependencyEvents(t *testing.T, f *fixture) []internalEventRow {
	t.Helper()
	return internalRows(t, f, "json_extract(body,'$.type')='issue.dependency_changed'")
}

func waitInternalEvent(t *testing.T, f *fixture, id, status string) internalEventRow {
	t.Helper()
	var row internalEventRow
	require.Eventually(t, func() bool {
		rows := internalRows(t, f, "event_id=?", id)
		if len(rows) == 1 && rows[0].Status == status {
			row = rows[0]
			return true
		}
		return false
	}, 7*time.Second, 25*time.Millisecond, "%s should be %s", id, status)
	return row
}

func decodeInternalBatch(t *testing.T, call nativeSubmitCall, ids ...string) []internalEvent {
	t.Helper()
	var batch struct {
		Events []internalEvent `json:"events"`
	}
	require.LessOrEqual(t, len(call.text), 64*1024)
	require.NoError(t, json.Unmarshal([]byte(call.text), &batch))
	var wire map[string][]map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(call.text), &wire))
	require.Len(t, wire, 1)
	for _, event := range wire["events"] {
		require.NotContains(t, event, "sender")
		require.NotContains(t, event, "recipient")
	}
	if ids != nil {
		var actual []string
		for _, event := range batch.Events {
			actual = append(actual, event.ID)
		}
		require.Equal(t, ids, actual)
	}
	return batch.Events
}

func newInternalEvent(id, kind string, recipient eventParty) internalEvent {
	e := internalEvent{ID: id, Kind: kind, Sender: eventParty{Kind: "core"}, Recipient: recipient}
	e.Body = json.RawMessage(`{"example":"fixture"}`)
	return e
}

func queueInternal(t *testing.T, p *process, items ...internalEvent) {
	t.Helper()
	status, data := request(t, http.MethodPost, p.endpoint, "/_test/internal-events", testProducerToken, items)
	require.Equal(t, http.StatusOK, status, string(data))
}

func TestInternalEventBatchProcess(t *testing.T) {
	peer := newNativeSubmitPeer(t)
	endpoint, _ := startEventTestMassd(t, peer)
	f, _ := eventFixture(t, endpoint)
	p := f.start("SUPERAGENT_TEST_EVENTS=" + testProducerToken)
	router := waitBinding(t, p, "router", "", "ready")
	role := startRoleMCP(t, f, router)
	recipient := eventParty{Kind: "router"}
	contextEvent := func(id string) internalEvent {
		return newInternalEvent(id, "context_supplement", recipient)
	}

	var many []internalEvent
	var ids []string
	for i := range 21 {
		id := "context-" + strconv.Itoa(i)
		ids = append(ids, id)
		many = append(many, contextEvent(id))
	}
	queueInternal(t, p, many...)
	first := peer.next(t, "thread/inject_items")
	require.Equal(t, router.SessionID, first.session)
	decodeInternalBatch(t, first, ids[:20]...)
	waitInternalEvent(t, f, ids[0], "dispatching")
	peer.quiet(t, 250*time.Millisecond)
	first.accept()
	second := peer.next(t, "thread/inject_items")
	decodeInternalBatch(t, second, ids[20])
	second.accept()
	for _, id := range ids {
		require.NotEmpty(t, waitInternalEvent(t, f, id, "sent").SentAt)
	}

	var large []internalEvent
	for i := range 9 {
		e := contextEvent("bytes-" + strconv.Itoa(i))
		content := markdownReply(strings.Repeat("x", 7900))
		e.Content = &content
		large = append(large, e)
	}
	queueInternal(t, p, large...)
	offset := 0
	for range 2 {
		call := peer.next(t, "thread/inject_items")
		events := decodeInternalBatch(t, call)
		require.NotEmpty(t, events)
		for _, e := range events {
			require.Equal(t, large[offset].ID, e.ID)
			require.False(t, e.Truncated)
			offset++
		}
		call.accept()
	}
	require.Len(t, large, offset)
	waitInternalEvent(t, f, large[offset-1].ID, "sent")

	long := contextEvent("long-context")
	content := markdownReply(strings.Repeat("界", 3000))
	long.Content = &content
	action := newInternalEvent("action", "route_request", recipient)
	queueInternal(t, p, long, action)
	mixed := peer.next(t, "turn/start")
	events := decodeInternalBatch(t, mixed, long.ID, action.ID)
	require.Equal(t, "context_supplement", events[0].Kind)
	require.Equal(t, "route_request", events[1].Kind)
	require.True(t, events[0].Truncated)
	require.Empty(t, events[0].Body)
	require.Nil(t, events[0].Content)
	full := mcpWork[internalEvent](t, role, "get_internal_event", map[string]string{"event_id": long.ID})
	require.Equal(t, long.Content, full.Content)
	mixed.accept()
	waitInternalEvent(t, f, action.ID, "sent")
	peer.quiet(t, 250*time.Millisecond)
	t.Log("Serialized count- and byte-limited batches through Inject and submitted mixed items with StartOrSteer. Full UTF-8 content remained available through MCP.")
}

func TestInternalEventRestartAfterLostReceipt(t *testing.T) {
	peer := newNativeSubmitPeer(t)
	endpoint, _ := startEventTestMassd(t, peer)
	var drop atomic.Bool
	drop.Store(true)
	dropped := make(chan struct{}, 1)
	proxy := dropRPCResponse(t, endpoint, func(method string) bool {
		return method == "session.inject" && drop.Swap(false)
	}, func() { dropped <- struct{}{} })
	f, _ := eventFixture(t, proxy)
	gate := t.TempDir()
	p := f.start("SUPERAGENT_TEST_EVENTS="+testProducerToken, "SUPERAGENT_TEST_GATE_STAGE=internal_event_before_settlement", "SUPERAGENT_TEST_GATE_DIR="+gate)
	router := waitBinding(t, p, "router", "", "ready")
	recipient := eventParty{Kind: "router"}
	first := newInternalEvent("lost-first", "context_supplement", recipient)
	second := newInternalEvent("lost-second", "context_supplement", recipient)
	queueInternal(t, p, first, second)
	call := peer.next(t, "thread/inject_items")
	decodeInternalBatch(t, call, first.ID, second.ID)
	call.accept()
	select {
	case <-dropped:
	case <-time.After(5 * time.Second):
		t.Fatal("massd response was not dropped")
	}
	waitWorkGate(t, gate, "internal_event_before_settlement")
	for _, id := range []string{first.ID, second.ID} {
		require.Empty(t, waitInternalEvent(t, f, id, "dispatching").SentAt)
	}
	pending := newInternalEvent("waiting", "context_supplement", recipient)
	queueInternal(t, p, pending)
	waitInternalEvent(t, f, pending.ID, "pending")
	p.stop(t, true)

	p = f.start()
	recovered := peer.next(t, "thread/inject_items")
	require.Equal(t, router.SessionID, recovered.session)
	require.Equal(t, call.text, recovered.text)
	recovered.accept()
	next := peer.next(t, "thread/inject_items")
	decodeInternalBatch(t, next, pending.ID)
	next.accept()
	for _, id := range []string{first.ID, second.ID, pending.ID} {
		waitInternalEvent(t, f, id, "sent")
	}
	p.stop(t, false)
	p = f.start()
	waitBinding(t, p, "router", "", "ready")
	peer.quiet(t, 400*time.Millisecond)
	t.Log("Dropped massd acceptance after the native reply, killed Core before settlement, and recovered dispatching and pending IDs. A second restart skipped sent items.")
}

func TestInternalEventRetiredSessionBinding(t *testing.T) {
	peer := newNativeSubmitPeer(t)
	endpoint, _ := startEventTestMassd(t, peer)
	f, operatorFile := eventFixture(t, endpoint)
	p := f.start("SUPERAGENT_TEST_EVENTS=" + testProducerToken)
	topic, _ := startReminderTopic(t, f, p, peer)
	work, old, start := startReminderWorker(t, f, p, peer, topic)
	start.accept()
	waitWorkEvent(t, p, work.SessionBinding.StartEventID, "sent")
	recipient := eventParty{Kind: "issue", SessionBindingID: work.SessionBinding.ID}
	first := newInternalEvent("retire-dispatching", "context_supplement", recipient)
	queueInternal(t, p, first)
	held := peer.next(t, "thread/inject_items")
	decodeInternalBatch(t, held, first.ID)
	waitInternalEvent(t, f, first.ID, "dispatching")
	pending := newInternalEvent("retire-pending", "context_supplement", recipient)
	queueInternal(t, p, pending)
	waitInternalEvent(t, f, pending.ID, "pending")
	canceled := mcpWork[core.WorkReceipt](t, topic, "issue_cancel", issueLifecycle("retire", work.Issue))
	held.accept()
	for _, id := range []string{first.ID, pending.ID} {
		require.Empty(t, waitInternalEvent(t, f, id, "superseded").SentAt)
	}
	_ = waitObservedStop(t, p, "session-bindings", work.SessionBinding.ID)
	release := recoveryFor("release-retired-issue", "replace", old.ID)
	release.StopRuntime = true
	require.Equal(t, "runtime_stopped", recoverRoleBinding(t, p, operatorFile, release).Close.Method)
	p.stop(t, false)
	// Exercise startup's authority check independently of the retirement
	// transaction, which has already superseded both items above.
	db, err := sql.Open("sqlite", filepath.Join(f.data, "core.sqlite"))
	require.NoError(t, err)
	res, err := db.ExecContext(t.Context(), "UPDATE internal_events SET status='dispatching' WHERE event_id=?", first.ID)
	require.NoError(t, err)
	n, err := res.RowsAffected()
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	require.NoError(t, db.Close())
	p = f.start()
	waitInternalEvent(t, f, first.ID, "superseded")
	reopened := mcpWork[core.WorkReceipt](t, topic, "issue_reopen", issueLifecycle("reopen", canceled.Issue))
	replacement := mcpWork[core.WorkReceipt](t, topic, "start_issue", startRequest("replace", reopened.Issue))
	require.NotEqual(t, work.SessionBinding.ID, replacement.SessionBinding.ID)
	b := waitBinding(t, p, "issue", replacement.SessionBinding.ID, "ready")
	require.NotEqual(t, old.SessionID, b.SessionID)
	onlyStart := peer.next(t, "turn/start")
	decodeInternalBatch(t, onlyStart, replacement.SessionBinding.StartEventID)
	onlyStart.accept()
	waitWorkEvent(t, p, replacement.SessionBinding.StartEventID, "sent")
	peer.quiet(t, 400*time.Millisecond)
	for _, id := range []string{first.ID, pending.ID} {
		waitInternalEvent(t, f, id, "superseded")
	}
	t.Log("A late acceptance kept the old row superseded. Startup independently fenced a stale dispatching fixture. Issue Session replacement replayed neither item from the retired SessionBinding.")
}
