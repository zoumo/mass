package core_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"

	"github.com/zoumo/mass/superagent/pkg/core"
)

const testProducerToken = "delivery-fixture-event-producer"

func deliveryFixture(t *testing.T, endpoint string) (*fixture, string) {
	t.Helper()
	f := fixtureAt(t)
	saved, err := os.ReadFile(f.config)
	require.NoError(t, err)
	token := filepath.Join(f.dir, "operator.token")
	write(t, token, strings.Repeat("O", 48))
	cwd, err := filepath.EvalSymlinks(f.dir)
	require.NoError(t, err)
	write(t, f.config, string(saved)+"execution:\n  massdEndpoint: "+endpoint+"\n  runtimeRef: agent\n  routerWorkspace: "+cwd+"\n  operatorTokenFile: "+token+"\n")
	return f, token
}

func deliveryEvent(id, intent string) core.Event {
	return core.Event{ID: id, Type: "fixture.event", TargetRole: "router", Intent: intent, Revision: 1, Payload: json.RawMessage(`{"fixture":true}`)}
}

func queueEvents(t *testing.T, p *process, items ...core.Event) {
	t.Helper()
	status, data := request(t, http.MethodPost, p.endpoint, "/_test/events", testProducerToken, items)
	require.Equal(t, http.StatusOK, status, string(data))
}

func readRecord[T any](t *testing.T, p *process, path string) T {
	t.Helper()
	status, data := request(t, http.MethodGet, p.endpoint, "/v1/observer/"+path, "", nil)
	require.Equal(t, http.StatusOK, status, string(data))
	var out T
	require.NoError(t, json.Unmarshal(data, &out))
	return out
}

func deliveriesFor(t *testing.T, p *process, event string) []core.Delivery {
	t.Helper()
	records := readRecord[struct{ Items []core.Delivery }](t, p, "deliveries?limit=200")
	var out []core.Delivery
	for _, d := range records.Items {
		if d.EventID == event {
			out = append(out, d)
		}
	}
	return out
}

func waitDelivery(t *testing.T, p *process, event, state string) core.Delivery {
	t.Helper()
	var found core.Delivery
	require.Eventually(t, func() bool {
		for _, d := range deliveriesFor(t, p, event) {
			if d.Status == state {
				found = d
				return true
			}
		}
		return false
	}, 7*time.Second, 20*time.Millisecond, "event %s: waiting for %s", event, state)
	return found
}

type nativeBatch struct {
	Events []struct {
		EventID          string          `json:"eventId"`
		Intent           string          `json:"intent"`
		Type             string          `json:"type"`
		Revision         int             `json:"revision"`
		TargetGeneration int             `json:"targetGeneration"`
		Payload          json.RawMessage `json:"payload"`
		Truncated        bool            `json:"truncated"`
		ContentRef       core.ContentRef `json:"contentRef"`
		Message          core.Message    `json:"message"`
	} `json:"events"`
}

func decodeBatch(t *testing.T, call nativeDeliveryCall, expected ...string) nativeBatch {
	t.Helper()
	var batch nativeBatch
	require.LessOrEqual(t, len(call.text), 64*1024)
	require.NoError(t, json.Unmarshal([]byte(call.text), &batch))
	if expected != nil {
		var ids []string
		for _, e := range batch.Events {
			ids = append(ids, e.EventID)
		}
		require.Equal(t, expected, ids)
	}
	return batch
}

func TestDeliveryNativeAggregation(t *testing.T) {
	peer := newNativeDeliveryPeer(t)
	endpoint, _ := startDeliveryMassd(t, peer)
	f, _ := deliveryFixture(t, endpoint)
	p := f.start("SUPERAGENT_TEST_EVENTS=" + testProducerToken)
	router := waitBinding(t, p, "router", "", "ready")
	role := roleToken(t, f, router)

	queueEvents(t, p, deliveryEvent("window-first", "context"))
	start := time.Now()
	time.Sleep(120 * time.Millisecond)
	queueEvents(t, p, deliveryEvent("window-second", "context"))
	time.Sleep(time.Until(start.Add(250 * time.Millisecond)))
	queueEvents(t, p, deliveryEvent("window-third", "context"))
	first := peer.next(t, "thread/inject_items")
	decodeBatch(t, first, "window-first", "window-second")
	d := waitDelivery(t, p, "window-first", "dispatching")
	require.Equal(t, router.SessionUID, d.SessionUID)
	require.Equal(t, router.SessionID, first.session)
	ack := roleCall(t, p, role, "event_ack", map[string]string{"eventId": d.EventID})
	require.JSONEq(t, string(ack), string(roleCall(t, p, role, "event_ack", map[string]string{"eventId": d.EventID})))
	peer.quiet(t, 240*time.Millisecond)
	first.accept()
	d = waitDelivery(t, p, "window-first", "delivered")
	require.NotEmpty(t, d.ACKAt)
	require.JSONEq(t, `{"acceptance":{"sent":true,"nativeAccepted":true}}`, string(d.Receipt))
	third := peer.next(t, "thread/inject_items")
	decodeBatch(t, third, "window-third")
	third.accept()
	require.Empty(t, waitDelivery(t, p, "window-third", "delivered").ACKAt)
	t.Log("first-event window, non-renewal, one in-flight write, Inject evidence and optional in-flight ACK passed")

	var many []core.Event
	var ids []string
	for i := range 21 {
		id := "count-" + strconv.Itoa(i)
		ids = append(ids, id)
		many = append(many, deliveryEvent(id, "context"))
	}
	queueEvents(t, p, many...)
	count := peer.next(t, "thread/inject_items")
	decodeBatch(t, count, ids[:20]...)
	count.accept()
	last := peer.next(t, "thread/inject_items")
	decodeBatch(t, last, ids[20:]...)
	last.accept()
	waitDelivery(t, p, ids[20], "delivered")

	var large []core.Receipt
	for i := range 9 {
		msg := input("bytes-" + strconv.Itoa(i))
		msg.Message.Text = strings.Repeat("x", 8191) + "界 remaining"
		// Dates run backward. Core queue order follows acceptance order.
		msg.Message.At = time.Date(2024, 1, 10-i, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
		large = append(large, submit(t, p, f, msg))
	}
	offset := 0
	for range 2 {
		call := peer.next(t, "turn/start")
		wire := decodeBatch(t, call)
		require.NotEmpty(t, wire.Events)
		for _, e := range wire.Events {
			require.Equal(t, large[offset].EventID, e.EventID)
			require.Equal(t, "act", e.Intent)
			require.True(t, e.Truncated)
			require.True(t, utf8.ValidString(e.Message.Text))
			require.Len(t, e.Message.Text, 8191)
			require.Equal(t, core.ContentRef{Collection: "messages", ID: large[offset].MessageID}, e.ContentRef)
			offset++
		}
		call.accept()
	}
	require.Len(t, large, offset)
	full := roleCall(t, p, role, "source_query", core.QueryRequest{Collection: "messages", ID: large[0].MessageID})
	require.Contains(t, string(full), "界 remaining")
	secondReceipt := waitDelivery(t, p, large[len(large)-1].EventID, "delivered").Receipt
	require.Contains(t, string(secondReceipt), `"result":"applied_current_work"`)
	t.Log("20-event limit, 64 KiB wire limit, UTF-8 preview, scoped original and acceptance ordering passed; steer occurred before any turn completion")

	queueEvents(t, p, deliveryEvent("mix-context", "context"), deliveryEvent("mix-act", "act"))
	mixed := peer.next(t, "turn/start")
	wire := decodeBatch(t, mixed, "mix-context", "mix-act")
	require.Equal(t, "context", wire.Events[0].Intent)
	require.Equal(t, "act", wire.Events[1].Intent)
	mixed.accept()
	waitDelivery(t, p, "mix-act", "delivered")

	r := input("oversize-meta")
	r.Message.Meta = map[string]any{"long": strings.Repeat("m", 70*1024)}
	oversize := submit(t, p, f, r)
	queueEvents(t, p, deliveryEvent("after-oversize", "context"))
	solo := peer.next(t, "turn/start")
	require.True(t, decodeBatch(t, solo, oversize.EventID).Events[0].Truncated)
	solo.accept()
	after := peer.next(t, "thread/inject_items")
	decodeBatch(t, after, "after-oversize")
	after.accept()
	waitDelivery(t, p, "after-oversize", "delivered")
	t.Log("mixed intent used StartOrSteer; oversized metadata sent alone with original retained")

	source := submit(t, p, f, input("topic-parallel"))
	routerSource := peer.next(t, "turn/start")
	decodeBatch(t, routerSource, source.EventID)
	routerSource.accept()
	waitDelivery(t, p, source.EventID, "delivered")
	decision := decide(t, p, role, newRoute("delivery-topic", []string{source.MessageID}, f.dir))
	topic := waitBinding(t, p, "topic", decision.Targets[0].TopicID, "ready")
	initial := peer.next(t, "turn/start")
	require.Equal(t, topic.SessionID, initial.session)
	initial.accept()
	queueEvents(t, p, deliveryEvent("router-in-flight", "context"))
	held := peer.next(t, "thread/inject_items")
	decodeBatch(t, held, "router-in-flight")
	topicEvent := deliveryEvent("independent-topic", "context")
	topicEvent.TargetRole, topicEvent.TargetID = topic.Role, topic.OwnerID
	queueEvents(t, p, topicEvent)
	independent := peer.next(t, "thread/inject_items")
	require.Equal(t, topic.SessionID, independent.session)
	decodeBatch(t, independent, topicEvent.ID)
	independent.accept()
	waitDelivery(t, p, topicEvent.ID, "delivered")
	status, data := request(t, http.MethodPost, p.endpoint, "/v1/role/event_get", roleToken(t, f, topic), map[string]string{"eventId": "router-in-flight"})
	errorCode(t, status, data, 404, "not_found")
	status, data = request(t, http.MethodPost, p.endpoint, "/v1/role/source_query", roleToken(t, f, topic), core.QueryRequest{Collection: "messages", ID: large[0].MessageID})
	errorCode(t, status, data, 404, "not_found")
	held.accept()
	waitDelivery(t, p, "router-in-flight", "delivered")
	t.Log("different Sessions delivered concurrently; a Topic cannot read the Router event or unrelated full text")
}

func TestDeliveryNativeRecoveryAcrossRestart(t *testing.T) {
	peer := newNativeDeliveryPeer(t)
	endpoint, c := startDeliveryMassd(t, peer)
	var drop atomic.Bool
	var dropped atomic.Int32
	proxy := dropRPCResponse(t, endpoint, func(method string) bool {
		return method == "session.inject" && drop.CompareAndSwap(true, false)
	}, func() { dropped.Add(1) })
	f, _ := deliveryFixture(t, proxy)
	env := "SUPERAGENT_TEST_EVENTS=" + testProducerToken
	gate := t.TempDir()
	p := f.start(env, "SUPERAGENT_TEST_GATE_STAGE=delivery_before_settlement", "SUPERAGENT_TEST_GATE_DIR="+gate)
	b := waitBinding(t, p, "router", "", "ready")

	queueEvents(t, p, deliveryEvent("kill-before-save", "context"))
	first := peer.next(t, "thread/inject_items")
	roleCall(t, p, roleToken(t, f, b), "event_ack", map[string]string{"eventId": "kill-before-save"})
	first.accept()
	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(gate, "delivery_before_settlement"))
		return err == nil
	}, 5*time.Second, 10*time.Millisecond)
	dispatched := waitDelivery(t, p, "kill-before-save", "dispatching")
	p.stop(t, true)
	p = f.start(env)
	unknown := waitDelivery(t, p, "kill-before-save", "transport_unknown")
	require.Equal(t, dispatched.BatchID, unknown.BatchID)
	require.True(t, unknown.PossibleDuplicate)
	require.NotEmpty(t, unknown.ACKAt)
	require.Equal(t, "core_stopped_before_settlement", unknown.ObservationGap)
	peer.quiet(t, 400*time.Millisecond)
	retry := peer.next(t, "thread/inject_items")
	require.Equal(t, first.text, retry.text)
	retry.accept()
	known := waitDelivery(t, p, "kill-before-save", "delivered")
	require.Equal(t, 2, known.Attempts)
	batch := readRecord[core.Batch](t, p, "batches/"+known.BatchID)
	require.Equal(t, 2, batch.Attempts)
	require.Equal(t, "delivered", readRecord[core.Attempt](t, p, "attempts/"+batch.LastAttemptID).Status)
	p.stop(t, true)
	p = f.start(env)
	waitBinding(t, p, "router", "", "ready")
	peer.quiet(t, 400*time.Millisecond)
	require.Equal(t, known, waitDelivery(t, p, "kill-before-save", "delivered"))
	t.Log("SIGKILL after actual NativeAccepted and before Core settlement recovered stable batch; persisted success was not resent")

	drop.Store(true)
	queueEvents(t, p, deliveryEvent("lost-rpc-response", "context"))
	lost := peer.next(t, "thread/inject_items")
	lost.accept()
	unknown = waitDelivery(t, p, "lost-rpc-response", "transport_unknown")
	require.EqualValues(t, 1, dropped.Load())
	require.Empty(t, unknown.Receipt)
	require.Equal(t, "massd_receipt_unconfirmed", unknown.ObservationGap)
	same := peer.next(t, "thread/inject_items")
	require.Equal(t, lost.text, same.text)
	same.accept()
	d := waitDelivery(t, p, "lost-rpc-response", "delivered")
	require.True(t, d.PossibleDuplicate)
	require.Equal(t, unknown.BatchID, d.BatchID)

	// Hold a real native Inject via a separate client so Core receives a
	// definite Service admission rejection before its own dispatch.
	held := make(chan error, 1)
	go func() {
		held <- c.Inject(t.Context(), resource.SessionRef{SessionID: b.SessionID}, runtime.Input{Content: []events.ContentBlock{events.Text("outside Core")}}, nil)
	}()
	direct := peer.next(t, "thread/inject_items")
	queueEvents(t, p, deliveryEvent("busy-restart", "context"))
	pending := waitDelivery(t, p, "busy-restart", "pending")
	if pending.Attempts == 0 {
		require.Eventually(t, func() bool {
			pending = deliveriesFor(t, p, "busy-restart")[0]
			return pending.Status == "pending" && pending.NextAttemptAt != ""
		}, 4*time.Second, 10*time.Millisecond)
	}
	require.Equal(t, "conflict", pending.LastError)
	require.False(t, pending.PossibleDuplicate)
	require.JSONEq(t, `{"acceptance":{"sent":false}}`, string(pending.Receipt))
	p.stop(t, true)
	direct.accept()
	require.NoError(t, <-held)
	p = f.start(env)
	deferred := waitDelivery(t, p, "busy-restart", "pending")
	require.Equal(t, pending.NextAttemptAt, deferred.NextAttemptAt)
	nextAt, err := time.Parse(time.RFC3339Nano, pending.NextAttemptAt)
	require.NoError(t, err)
	next := peer.next(t, "thread/inject_items")
	require.False(t, time.Now().Before(nextAt))
	decodeBatch(t, next, "busy-restart")
	next.accept()
	require.Equal(t, 2, waitDelivery(t, p, "busy-restart", "delivered").Attempts)
	t.Log("dropped real RPC result and rejected concurrent input take distinct paths; restart honored the stored deadline")
}

func TestDeliveryNativeUnknownAndReplacement(t *testing.T) {
	peer := newNativeDeliveryPeer(t)
	endpoint, c := startDeliveryMassd(t, peer)
	f, operatorFile := deliveryFixture(t, endpoint)
	env := "SUPERAGENT_TEST_EVENTS=" + testProducerToken
	p := f.start(env)
	r := waitBinding(t, p, "router", "", "ready")
	oldRole := roleToken(t, f, r)

	contextCommand := deliveryEvent("unsupported", "context")
	contextCommand.TargetGeneration = r.Generation
	queueEvents(t, p, contextCommand)
	peer.next(t, "thread/inject_items").answer <- nativeDeliveryAnswer{code: -32601, message: "synthetic method missing"}
	unsupported := waitDelivery(t, p, "unsupported", "blocked")
	require.Equal(t, "unsupported", unsupported.LastError)
	require.JSONEq(t, `{"acceptance":{"sent":true,"nativeAccepted":false}}`, string(unsupported.Receipt))
	peer.quiet(t, 250*time.Millisecond)

	rejectedCommand := deliveryEvent("not-applied", "act")
	rejectedCommand.TargetGeneration = r.Generation
	queueEvents(t, p, rejectedCommand)
	peer.next(t, "turn/start").answer <- nativeDeliveryAnswer{code: -32602, message: "failed to submit turn input: PlanMode"}
	refused := waitDelivery(t, p, "not-applied", "blocked")
	require.Equal(t, "not_applied", refused.LastError)
	require.Contains(t, string(refused.Receipt), `"nativeAccepted":false`)

	queueEvents(t, p, deliveryEvent("sdk-unknown", "act"))
	first := peer.next(t, "turn/start")
	first.answer <- nativeDeliveryAnswer{unknown: true}
	sealed := waitDelivery(t, p, "sdk-unknown", "blocked_unknown")
	require.Equal(t, "outcome_unknown", sealed.LastError)
	require.Empty(t, sealed.NextAttemptAt)
	require.True(t, sealed.NeedsAttention)
	p.stop(t, true)
	p = f.start(env)
	require.Equal(t, sealed, waitDelivery(t, p, "sdk-unknown", "blocked_unknown"))
	peer.quiet(t, 250*time.Millisecond)
	var attempt runtime.InjectReceipt
	err := c.Inject(t.Context(), resource.SessionRef{SessionID: r.SessionID}, runtime.Input{Content: []events.ContentBlock{events.Text("must be denied")}}, &attempt)
	var op *runtime.OperationError
	require.ErrorAs(t, err, &op)
	require.Equal(t, "input_outcome_unknown", op.Reason)

	opToken, err := os.ReadFile(operatorFile)
	require.NoError(t, err)
	recoveryPath := "/v1/operator/input-recovery"
	req := core.RecoveryRequest{OperationID: "retry-sdk", BindingID: r.ID, Mode: "retry", Reconciliation: "Compared native input trace and inspected original Session; prior outcome remains uncertain"}
	status, data := request(t, http.MethodPost, p.endpoint, recoveryPath, string(opToken), req)
	errorCode(t, status, data, 400, "invalid_argument")
	req.AcceptPossibleDuplicate = true
	status, data = request(t, http.MethodPost, p.endpoint, recoveryPath, oldRole, req)
	errorCode(t, status, data, 401, "unauthorized")
	runCLI := exec.CommandContext(t.Context(), build(t), "recover-input", r.ID, "--endpoint", p.endpoint, "--operator-token-file", operatorFile,
		"--mode", req.Mode, "--operation-id", req.OperationID, "--reconciliation", req.Reconciliation, "--accept-possible-duplicate")
	data, err = runCLI.CombinedOutput()
	require.NoError(t, err, "%s", data)
	var recovered core.Recovery
	require.NoError(t, json.Unmarshal(data, &recovered))
	require.Equal(t, "completed", recovered.Status)
	require.NotEmpty(t, recovered.ClosedResource)
	require.NotEmpty(t, recovered.ResumedResource)
	same := peer.next(t, "turn/start")
	require.Equal(t, first.text, same.text)
	same.answer <- nativeDeliveryAnswer{complete: true}
	d := waitDelivery(t, p, "sdk-unknown", "delivered")
	require.True(t, d.PossibleDuplicate)
	require.Equal(t, sealed.BatchID, d.BatchID)
	// Completion replay reads the committed operation without another close.
	status, data = request(t, http.MethodPost, p.endpoint, recoveryPath, string(opToken), req)
	require.Equal(t, http.StatusOK, status, string(data))
	var replay core.Recovery
	require.NoError(t, json.Unmarshal(data, &replay))
	require.Equal(t, recovered, replay)
	t.Log("SDK unknown sealed both Core and massd across restart; local CLI recorded confirmed close, readback and same-identity resume")

	// Seal the old input lane again, then queue one fact and one command.
	seal := deliveryEvent("seal-for-replacement", "act")
	seal.TargetGeneration = r.Generation
	queueEvents(t, p, seal)
	peer.next(t, "turn/start").answer <- nativeDeliveryAnswer{unknown: true}
	waitDelivery(t, p, "seal-for-replacement", "blocked_unknown")
	message := submit(t, p, f, input("retained-after-replace"))
	stale := deliveryEvent("old-binding-command", "act")
	stale.TargetGeneration = r.Generation
	queueEvents(t, p, stale)
	req.OperationID, req.Mode = "replace-sdk", "replace"
	req.StopRuntime = true
	status, data = request(t, http.MethodPost, p.endpoint, recoveryPath, string(opToken), req)
	require.Equal(t, http.StatusOK, status, string(data))
	var replaced core.Recovery
	require.NoError(t, json.Unmarshal(data, &replaced))
	require.NotEmpty(t, replaced.ClosedResource)
	require.Empty(t, replaced.ResumedResource)
	require.NotEmpty(t, replaced.ReplacementBinding)
	old := readRecord[core.Binding](t, p, "bindings/"+r.ID)
	require.Equal(t, "revoked", old.State)
	status, data = request(t, http.MethodPost, p.endpoint, "/v1/role/binding_get", oldRole, map[string]any{})
	errorCode(t, status, data, 409, "stale_binding")
	var sent []string
	var recoveryEvent string
	for range 2 {
		next := peer.next(t, "turn/start")
		require.NotEqual(t, r.SessionID, next.session)
		batch := decodeBatch(t, next)
		for _, e := range batch.Events {
			sent = append(sent, e.EventID)
			if e.Type == "binding.recovered" {
				recoveryEvent = e.EventID
			}
		}
		next.accept()
		if recoveryEvent != "" {
			break
		}
	}
	require.Contains(t, sent, message.EventID)
	require.Len(t, sent, 2)
	require.NotContains(t, sent, "sdk-unknown")
	require.NotContains(t, sent, stale.ID)
	waitDelivery(t, p, stale.ID, "superseded")
	delivered := waitDelivery(t, p, message.EventID, "delivered")
	require.Equal(t, replaced.ReplacementBinding, delivered.BindingID)
	require.Equal(t, r.Generation+1, delivered.Generation)
	require.Len(t, deliveriesFor(t, p, message.EventID), 2)
	require.Len(t, deliveriesFor(t, p, "sdk-unknown"), 1)
	require.NotEmpty(t, recoveryEvent)
	waitDelivery(t, p, recoveryEvent, "delivered")
	p.stop(t, true)
	p = f.start(env)
	waitBinding(t, p, "router", "", "revoked")
	require.Equal(t, replaced.ReplacementBinding, waitBinding(t, p, "router", "", "ready").ID)
	peer.quiet(t, 250*time.Millisecond)
	t.Log("replacement revoked old capability, retained the original pending fact ID, superseded the fenced command and omitted delivered history")
}

func TestDeliveryPublicUnavailabilityEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, code, reason string
		sent               *bool
		err                error
	}{
		{name: "definite_preflight", code: "unavailable", reason: "fixture_unavailable", sent: runtime.Bool(false), err: runtime.Unavailable("fixture_unavailable")},
		{name: "absent_sent", code: "unavailable", reason: "fixture_unavailable", err: runtime.Unavailable("fixture_unavailable")},
		{name: "server_unconfirmed", code: "outcome_unknown", reason: "operation_unconfirmed", err: &runtime.OperationError{Code: "outcome_unknown", Reason: "operation_unconfirmed"}},
		{name: "server_internal_error", code: "massd_rpc_error", reason: "-32603", err: errors.New("synthetic unclassified operation error")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			boundary := newRouteBoundary(t)
			f := fixtureAt(t)
			f.execution(boundary)
			p := f.start("SUPERAGENT_TEST_EVENTS=" + testProducerToken)
			waitBinding(t, p, "router", "", "ready")
			boundary.driver.mu.Lock()
			boundary.driver.injectReceipt = runtime.InjectReceipt{Acceptance: runtime.Acceptance{Sent: tc.sent}}
			boundary.driver.injectErr = tc.err
			boundary.driver.mu.Unlock()
			queueEvents(t, p, deliveryEvent(tc.name, "context"))
			want := "blocked_unknown"
			if tc.sent != nil {
				want = "pending"
			}
			var observed core.Delivery
			require.Eventually(t, func() bool {
				observed = deliveriesFor(t, p, tc.name)[0]
				return observed.Status == want && observed.LastError == tc.code
			}, 5*time.Second, 20*time.Millisecond)
			batch := readRecord[core.Batch](t, p, "batches/"+observed.BatchID)
			attempt := readRecord[core.Attempt](t, p, "attempts/"+batch.LastAttemptID)
			require.Equal(t, tc.reason, attempt.ErrorReason)
			if tc.sent == nil {
				require.Empty(t, observed.Receipt)
				require.Empty(t, observed.NextAttemptAt)
				require.True(t, observed.NeedsAttention)
				return
			}
			require.JSONEq(t, `{"acceptance":{"sent":false}}`, string(observed.Receipt))
			require.NotEmpty(t, observed.NextAttemptAt)
			boundary.driver.mu.Lock()
			boundary.driver.injectReceipt = runtime.InjectReceipt{Acceptance: runtime.Acceptance{Sent: runtime.Yes()}}
			boundary.driver.injectErr = nil
			boundary.driver.mu.Unlock()
			delivered := waitDelivery(t, p, tc.name, "delivered")
			require.Equal(t, 2, delivered.Attempts)
			require.False(t, delivered.PossibleDuplicate)
		})
	}
}
