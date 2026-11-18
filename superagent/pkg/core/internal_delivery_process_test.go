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
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/superagent/pkg/core"
)

type deliveryParty struct {
	Kind        string `json:"kind"`
	TopicID     string `json:"topic_id,omitempty"`
	ExecutionID string `json:"execution_id,omitempty"`
}

type internalEvent struct {
	ID        string        `json:"delivery_id"`
	Kind      string        `json:"kind"`
	Sender    deliveryParty `json:"sender"`
	Recipient deliveryParty `json:"recipient"`
	Content   struct {
		Body json.RawMessage   `json:"body"`
		Refs []json.RawMessage `json:"refs,omitempty"`
	} `json:"content"`
	Truncated   bool            `json:"truncated,omitempty"`
	BodyPreview string          `json:"body_preview,omitempty"`
	ContentRef  json.RawMessage `json:"content_ref,omitempty"`
}

type internalDeliveryRow struct {
	ID, Kind, Status, SentAt string
	Body                     json.RawMessage
}

func internalRows(t *testing.T, f *fixture, where string, args ...any) []internalDeliveryRow {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(f.data, "core.sqlite")+"?mode=ro&_pragma=busy_timeout(1000)")
	require.NoError(t, err)
	defer db.Close()
	rows, err := db.QueryContext(t.Context(), `SELECT id,kind,status,COALESCE(sent_at,''),json_extract(content,'$.body')
 FROM internal_deliveries WHERE `+where+` ORDER BY rowid`, args...)
	require.NoError(t, err)
	defer rows.Close()
	var result []internalDeliveryRow
	for rows.Next() {
		var d internalDeliveryRow
		var body []byte
		require.NoError(t, rows.Scan(&d.ID, &d.Kind, &d.Status, &d.SentAt, &body))
		d.Body = body
		result = append(result, d)
	}
	require.NoError(t, rows.Err())
	return result
}

func dependencyDeliveries(t *testing.T, f *fixture) []internalDeliveryRow {
	t.Helper()
	return internalRows(t, f, "json_extract(content,'$.body.type')='issue.dependency_changed'")
}

func waitInternalDelivery(t *testing.T, f *fixture, id, status string) internalDeliveryRow {
	t.Helper()
	var row internalDeliveryRow
	require.Eventually(t, func() bool {
		rows := internalRows(t, f, "id=?", id)
		if len(rows) == 1 && rows[0].Status == status {
			row = rows[0]
			return true
		}
		return false
	}, 7*time.Second, 25*time.Millisecond, "%s should be %s", id, status)
	return row
}

func decodeInternalBatch(t *testing.T, call nativeDeliveryCall, ids ...string) []internalEvent {
	t.Helper()
	var batch struct {
		Version int             `json:"schemaVersion"`
		Events  []internalEvent `json:"events"`
	}
	require.LessOrEqual(t, len(call.text), 64*1024)
	require.NoError(t, json.Unmarshal([]byte(call.text), &batch))
	require.Equal(t, 3, batch.Version)
	if ids != nil {
		var actual []string
		for _, event := range batch.Events {
			actual = append(actual, event.ID)
		}
		require.Equal(t, ids, actual)
	}
	return batch.Events
}

func newInternalEvent(id, kind string, recipient deliveryParty) internalEvent {
	e := internalEvent{ID: id, Kind: kind, Sender: deliveryParty{Kind: "core"}, Recipient: recipient}
	e.Content.Body = json.RawMessage(`{"example":"fixture"}`)
	return e
}

func queueInternal(t *testing.T, p *process, items ...internalEvent) {
	t.Helper()
	status, data := request(t, http.MethodPost, p.endpoint, "/_test/internal-deliveries", testProducerToken, items)
	require.Equal(t, http.StatusOK, status, string(data))
}

func TestInternalDeliveryBatchProcess(t *testing.T) {
	peer := newNativeDeliveryPeer(t)
	endpoint, _ := startDeliveryMassd(t, peer)
	f, _ := deliveryFixture(t, endpoint)
	p := f.start("SUPERAGENT_TEST_EVENTS=" + testProducerToken)
	router := waitBinding(t, p, "router", "", "ready")
	role := startRoleMCP(t, f, router)
	recipient := deliveryParty{Kind: "router"}
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
	waitInternalDelivery(t, f, ids[0], "dispatching")
	peer.quiet(t, 250*time.Millisecond)
	first.accept()
	second := peer.next(t, "thread/inject_items")
	decodeInternalBatch(t, second, ids[20])
	second.accept()
	for _, id := range ids {
		require.NotEmpty(t, waitInternalDelivery(t, f, id, "sent").SentAt)
	}

	var large []internalEvent
	for i := range 9 {
		e := contextEvent("bytes-" + strconv.Itoa(i))
		e.Content.Body = json.RawMessage(`"` + strings.Repeat("x", 8150) + `"`)
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
	waitInternalDelivery(t, f, large[offset-1].ID, "sent")

	long := contextEvent("long-context")
	long.Content.Body = json.RawMessage(`{"text":"` + strings.Repeat("界", 3000) + `"}`)
	long.Content.Refs = []json.RawMessage{json.RawMessage(`{"kind":"fixture","id":"source-reference"}`)}
	action := newInternalEvent("action", "route_request", recipient)
	queueInternal(t, p, long, action)
	mixed := peer.next(t, "turn/start")
	events := decodeInternalBatch(t, mixed, long.ID, action.ID)
	require.Equal(t, "context_supplement", events[0].Kind)
	require.Equal(t, "route_request", events[1].Kind)
	require.Equal(t, recipient, events[0].Recipient)
	require.True(t, events[0].Truncated)
	require.True(t, utf8.ValidString(events[0].BodyPreview))
	require.Empty(t, events[0].Content.Body)
	require.JSONEq(t, string(long.Content.Refs[0]), string(events[0].Content.Refs[0]))
	require.JSONEq(t, `{"kind":"internal_delivery","id":"long-context"}`, string(events[0].ContentRef))
	full := mcpWork[internalEvent](t, role, "delivery_get", map[string]string{"delivery_id": long.ID})
	require.JSONEq(t, string(long.Content.Body), string(full.Content.Body))
	mixed.accept()
	waitInternalDelivery(t, f, action.ID, "sent")
	peer.quiet(t, 250*time.Millisecond)
	t.Log("Serialized count- and byte-limited batches through Inject and submitted mixed items with StartOrSteer. Full UTF-8 content remained available through MCP.")
}

func TestInternalDeliveryRestartAfterLostReceipt(t *testing.T) {
	peer := newNativeDeliveryPeer(t)
	endpoint, _ := startDeliveryMassd(t, peer)
	var drop atomic.Bool
	drop.Store(true)
	dropped := make(chan struct{}, 1)
	proxy := dropRPCResponse(t, endpoint, func(method string) bool {
		return method == "session.inject" && drop.Swap(false)
	}, func() { dropped <- struct{}{} })
	f, _ := deliveryFixture(t, proxy)
	gate := t.TempDir()
	p := f.start("SUPERAGENT_TEST_EVENTS="+testProducerToken, "SUPERAGENT_TEST_GATE_STAGE=internal_delivery_before_settlement", "SUPERAGENT_TEST_GATE_DIR="+gate)
	router := waitBinding(t, p, "router", "", "ready")
	recipient := deliveryParty{Kind: "router"}
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
	waitWorkGate(t, gate, "internal_delivery_before_settlement")
	for _, id := range []string{first.ID, second.ID} {
		require.Empty(t, waitInternalDelivery(t, f, id, "dispatching").SentAt)
	}
	pending := newInternalEvent("waiting", "context_supplement", recipient)
	queueInternal(t, p, pending)
	waitInternalDelivery(t, f, pending.ID, "pending")
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
		waitInternalDelivery(t, f, id, "sent")
	}
	p.stop(t, false)
	p = f.start()
	waitBinding(t, p, "router", "", "ready")
	peer.quiet(t, 400*time.Millisecond)
	t.Log("Dropped massd acceptance after the native reply, killed Core before settlement, and recovered dispatching and pending IDs. A second restart skipped sent items.")
}

func TestInternalDeliveryRetiredExecution(t *testing.T) {
	peer := newNativeDeliveryPeer(t)
	endpoint, _ := startDeliveryMassd(t, peer)
	f, operatorFile := deliveryFixture(t, endpoint)
	p := f.start("SUPERAGENT_TEST_EVENTS=" + testProducerToken)
	topic, _ := startReminderTopic(t, f, p, peer)
	work, old, start := startReminderWorker(t, f, p, peer, topic)
	start.accept()
	waitWorkDelivery(t, p, work.Execution.StartEventID, "sent")
	recipient := deliveryParty{Kind: "worker", ExecutionID: work.Execution.ID}
	first := newInternalEvent("retire-dispatching", "context_supplement", recipient)
	queueInternal(t, p, first)
	held := peer.next(t, "thread/inject_items")
	decodeInternalBatch(t, held, first.ID)
	waitInternalDelivery(t, f, first.ID, "dispatching")
	pending := newInternalEvent("retire-pending", "context_supplement", recipient)
	queueInternal(t, p, pending)
	waitInternalDelivery(t, f, pending.ID, "pending")
	canceled := mcpWork[core.WorkReceipt](t, topic, "issue_cancel", issueLifecycle("retire", work.Issue))
	held.accept()
	for _, id := range []string{first.ID, pending.ID} {
		require.Empty(t, waitInternalDelivery(t, f, id, "superseded").SentAt)
	}
	_ = waitObservedStop(t, p, "executions", work.Execution.ID)
	release := recoveryFor("release-retired-worker", "replace", old.ID)
	release.StopRuntime = true
	require.Equal(t, "runtime_stopped", recoverRoleBinding(t, p, operatorFile, release).Close.Method)
	p.stop(t, false)
	// Exercise startup's authority check independently of the retirement
	// transaction, which has already superseded both items above.
	db, err := sql.Open("sqlite", filepath.Join(f.data, "core.sqlite"))
	require.NoError(t, err)
	res, err := db.ExecContext(t.Context(), "UPDATE internal_deliveries SET status='dispatching' WHERE id=?", first.ID)
	require.NoError(t, err)
	n, err := res.RowsAffected()
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	require.NoError(t, db.Close())
	p = f.start()
	waitInternalDelivery(t, f, first.ID, "superseded")
	reopened := mcpWork[core.WorkReceipt](t, topic, "issue_reopen", issueLifecycle("reopen", canceled.Issue))
	replacement := mcpWork[core.WorkReceipt](t, topic, "issue_start", startRequest("replace", reopened.Issue))
	require.NotEqual(t, work.Execution.ID, replacement.Execution.ID)
	b := waitBinding(t, p, "worker", replacement.Execution.ID, "ready")
	require.NotEqual(t, old.SessionID, b.SessionID)
	onlyStart := peer.next(t, "turn/start")
	decodeInternalBatch(t, onlyStart, replacement.Execution.StartEventID)
	onlyStart.accept()
	waitWorkDelivery(t, p, replacement.Execution.StartEventID, "sent")
	peer.quiet(t, 400*time.Millisecond)
	for _, id := range []string{first.ID, pending.ID} {
		waitInternalDelivery(t, f, id, "superseded")
	}
	t.Log("A late acceptance kept the old row superseded. Startup independently fenced a stale dispatching fixture. Worker replacement replayed neither old Execution item.")
}
