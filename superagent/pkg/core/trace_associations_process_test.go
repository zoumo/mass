package core_test

import (
	"bytes"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	traceexport "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/zoumo/mass/superagent/pkg/core"
)

type observerTrace struct {
	TraceID string `json:"traceId"`
	Phase   string `json:"phase"`
}

func traceTestEnv(endpoint string) []string {
	return []string{"OTEL_TRACES_EXPORTER=otlp", "OTEL_EXPORTER_OTLP_ENDPOINT=" + endpoint, "OTEL_BSP_SCHEDULE_DELAY=50"}
}

func spanLinksTrace(span *tracepb.Span, traceID string) bool {
	for _, link := range span.GetLinks() {
		if hex.EncodeToString(link.GetTraceId()) == traceID {
			return true
		}
	}
	return false
}

func observedTraces(t *testing.T, p *process, collection, id string) []observerTrace {
	t.Helper()
	status, body := request(t, http.MethodGet, p.endpoint, "/v1/observer/"+collection+"/"+id+"?trace=true", "", nil)
	require.Equal(t, http.StatusOK, status, string(body))
	for _, key := range []string{`"traceparent"`, `"tracestate"`, `"spanId"`, `"baggage"`} {
		require.NotContains(t, string(body), key)
	}
	var detail struct {
		Associations []observerTrace `json:"traceAssociations"`
	}
	require.NoError(t, json.Unmarshal(body, &detail))
	return detail.Associations
}

func TestTraceAssociationsOnCurrentSchema(t *testing.T) {
	f := fixtureAt(t)
	p := f.start()
	historical := submit(t, p, f, input("before-tracing"))
	p.stop(t, false)

	db, err := sql.Open("sqlite", "file:"+filepath.Join(f.data, "core.sqlite")+"?mode=ro&_pragma=busy_timeout(1000)")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	var tables, schemaVersion int
	checkSchema := func(expectedTables int) {
		t.Helper()
		require.NoError(t, db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&schemaVersion))
		require.NoError(t, db.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&tables))
		require.Equal(t, 25, schemaVersion)
		require.Equal(t, expectedTables, tables)
	}
	checkSchema(8)

	// A configuration refusal after DDL should roll the new table back.
	f.configure(0, "different-account")
	cmd := exec.CommandContext(t.Context(), f.coreExecutable(), "serve", "--config", f.config)
	cmd.Env = []string{"HOME=" + f.dir, "PATH=" + f.dir, "OTEL_TRACES_EXPORTER=otlp", "OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:0"}
	raw, err := cmd.CombinedOutput()
	require.Error(t, err)
	require.Contains(t, string(raw), "configured source/account differs")
	checkSchema(8)

	f.configure(0, "account-1")
	p = f.start("OTEL_TRACES_EXPORTER=otlp", "OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:0")
	require.Empty(t, observedTraces(t, p, "messages", historical.MessageID))
	saved := submit(t, p, f, input("after-tracing"))
	original := observedTraces(t, p, "messages", saved.MessageID)
	require.Len(t, original, 1)
	require.Equal(t, "admission", original[0].Phase)
	checkSchema(9)
	var persisted string
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT traceparent FROM trace_associations WHERE collection='messages' AND object_id=?", saved.MessageID).Scan(&persisted))
	require.Contains(t, persisted, original[0].TraceID)
	p.stop(t, false)

	p = f.start()
	replay := submit(t, p, f, input("after-tracing"))
	require.True(t, replay.Duplicate)
	require.Equal(t, saved.MessageID, replay.MessageID)
	require.Equal(t, original, observedTraces(t, p, "messages", saved.MessageID))
	untraced := submit(t, p, f, input("after-disabling"))
	require.Empty(t, observedTraces(t, p, "messages", untraced.MessageID))
}

func TestTraceAssociationsRestartEvent(t *testing.T) {
	boundary := newRouteBoundary(t)
	var drop atomic.Bool
	drop.Store(true)
	retryBoundary := &routeBoundary{url: dropRPCResponse(t, boundary.url, func(method string) bool {
		return method == "session.prompt" && drop.Swap(false)
	}, func() {})}

	receiver, spans := coreOTLPReceiver(t, os.Getenv("SUPERAGENT_ACCEPTANCE_OTLP_ENDPOINT"))
	env := append(traceTestEnv(receiver.URL), "OTEL_METRICS_EXPORTER=otlp", "OTEL_METRIC_EXPORT_INTERVAL=150")

	f := fixtureAt(t)
	p := f.start(env...)
	inputs := []core.SubmitRequest{input("durable-first"), input("durable-second")}
	var receipts []core.Receipt
	for _, record := range inputs {
		receipts = append(receipts, submit(t, p, f, record))
	}
	first, second := receipts[0], receipts[1]
	waitInternalEvent(t, f, first.EventID, "pending")
	waitInternalEvent(t, f, second.EventID, "pending")

	links := make(map[string]string)
	for _, receipt := range receipts {
		admission := observedTraces(t, p, "messages", receipt.MessageID)
		require.Len(t, admission, 1)
		require.Equal(t, "admission", admission[0].Phase)
		require.Equal(t, []observerTrace{{TraceID: admission[0].TraceID, Phase: "enqueue"}},
			observedTraces(t, p, "internal-events", receipt.EventID))
		links[admission[0].TraceID] = receipt.MessageID
	}
	require.Len(t, links, 2)
	require.True(t, submit(t, p, f, inputs[0]).Duplicate)
	current := observedTraces(t, p, "messages", first.MessageID)
	require.Len(t, current, 1)
	require.Equal(t, first.MessageID, links[current[0].TraceID])
	p.stop(t, false)

	f.execution(retryBoundary)
	p = f.start(env...)
	for _, receipt := range receipts {
		waitInternalEvent(t, f, receipt.EventID, "sent")
	}
	p.stop(t, false)
	admissions := make(map[string]string)
	for _, span := range spans() {
		traceID, spanID := hex.EncodeToString(span.GetTraceId()), hex.EncodeToString(span.GetSpanId())
		if span.GetName() == "superagent.message.admit" && links[traceID] != "" {
			admissions[traceID] = spanID
			t.Logf("message=%s admission_trace_id=%s admission_span_id=%s", links[traceID], traceID, spanID)
		}
	}
	require.Len(t, admissions, 2)
	var attempts int
	var largestBatch int64
	var lostReceipt bool
	delivered := make(map[string]bool)
	for _, span := range spans() {
		if span.GetName() != "superagent.internal_event.dispatch" {
			continue
		}
		attempts++
		require.Empty(t, span.GetParentSpanId(), "queued work starts its own trace")
		var outcome string
		var batchSize int64
		for _, attribute := range span.GetAttributes() {
			switch attribute.GetKey() {
			case "superagent.outcome":
				outcome = attribute.GetValue().GetStringValue()
			case "superagent.event.batch.size":
				batchSize = attribute.GetValue().GetIntValue()
			}
		}
		require.NotEmpty(t, span.GetLinks())
		require.Len(t, span.GetLinks(), int(batchSize))
		largestBatch = max(largestBatch, batchSize)
		if outcome == "unknown" {
			lostReceipt = true
		}
		for _, link := range span.GetLinks() {
			traceID := hex.EncodeToString(link.GetTraceId())
			require.NotEmpty(t, admissions[traceID])
			require.Equal(t, admissions[traceID], hex.EncodeToString(link.GetSpanId()))
			if outcome == "delivered" {
				delivered[traceID] = true
			}
		}
		t.Logf("dispatch_trace_id=%x dispatch_span_id=%x outcome=%s batch_size=%d", span.GetTraceId(), span.GetSpanId(), outcome, batchSize)
	}
	require.GreaterOrEqual(t, attempts, 2, "one simulated lost receipt must retry")
	require.True(t, lostReceipt, "first dispatch receipt was lost")
	require.EqualValues(t, 2, largestBatch, "the two admissions must be linked in one batch")
	require.Len(t, delivered, 2)
}

func TestTraceAssociationsSingleEventParent(t *testing.T) {
	receiver, spans := coreOTLPReceiver(t, "")
	f := fixtureAt(t)
	f.execution(newRouteBoundary(t))
	p := f.start(traceTestEnv(receiver.URL)...)
	routerBinding := waitBinding(t, p, "router", "", "ready")
	router := startRoleMCP(t, f, routerBinding)
	source := submit(t, p, f, input("single-trace"))
	waitInternalEvent(t, f, source.EventID, "sent")
	route := struct {
		core.RouteRequest
		TriggerEventID string `json:"triggerEventId"`
	}{newRoute("single-trace", []string{source.MessageID}, f.dir), source.EventID}
	decision := mcpWork[core.RouteDecision](t, router, "route_messages", route)
	target := decision.Targets[0]
	internalEvent := target.EventIDs[source.MessageID]
	waitInternalEvent(t, f, internalEvent, "sent")

	topic := startRoleMCP(t, f, waitBinding(t, p, "topic", target.TopicID, "ready"))
	create := struct {
		core.CreateIssueRequest
		TriggerEventID string `json:"triggerEventId,omitempty"`
	}{issueInput(f.dir, "issue-trace"), source.EventID}
	mcpWorkError(t, topic, "create_issue", "not_found", create)
	create.TriggerEventID = internalEvent
	created := mcpWork[core.WorkReceipt](t, topic, "create_issue", create)
	replay := mcpWork[core.WorkReceipt](t, topic, "create_issue", create.CreateIssueRequest)
	require.Equal(t, created, replay, "trace metadata does not change the operation's business digest")
	p.stop(t, false)

	byID := make(map[string]*tracepb.Span)
	for _, span := range spans() {
		byID[hex.EncodeToString(span.GetSpanId())] = span
	}
	// Export order can differ from parent order and a replay emits a separate span.
	var issue *tracepb.Span
	for _, span := range spans() {
		httpSpan := byID[hex.EncodeToString(span.GetParentSpanId())]
		if span.GetName() == "superagent.issue.create" && httpSpan != nil && len(httpSpan.GetParentSpanId()) > 0 {
			issue = span
		}
	}
	require.NotNil(t, issue)
	for _, name := range []string{
		"POST /v1/mcp", "superagent.internal_event.dispatch", "superagent.routing.decide",
		"POST /v1/mcp", "superagent.internal_event.dispatch", "superagent.message.admit",
		"POST /v1/channel/messages",
	} {
		parent := byID[hex.EncodeToString(issue.GetParentSpanId())]
		require.NotNil(t, parent, "missing parent %s of %s", name, issue.GetName())
		require.Equal(t, name, parent.GetName())
		require.Equal(t, issue.GetTraceId(), parent.GetTraceId())
		issue = parent
	}
	require.Empty(t, issue.GetParentSpanId())
}

func coreOTLPReceiver(t *testing.T, endpoint string) (*httptest.Server, func() []*tracepb.Span) {
	t.Helper()
	mirror := strings.TrimRight(endpoint, "/")
	client := &http.Client{Timeout: 3 * time.Second}
	var mu sync.Mutex
	var seen []*tracepb.Span
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
		if err != nil {
			t.Errorf("read local OTLP payload: %v", err)
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
		if r.URL.Path == "/v1/traces" {
			var data traceexport.ExportTraceServiceRequest
			if err := proto.Unmarshal(payload, &data); err != nil {
				t.Errorf("decode local span payload: %v", err)
				http.Error(w, "invalid spans", http.StatusBadRequest)
				return
			}
			mu.Lock()
			for _, resource := range data.GetResourceSpans() {
				for _, instrumentation := range resource.GetScopeSpans() {
					seen = append(seen, instrumentation.GetSpans()...)
				}
			}
			mu.Unlock()
		}
		if mirror != "" {
			req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, mirror+r.URL.Path, bytes.NewReader(payload))
			if err != nil {
				t.Errorf("build mirrored OTLP request: %v", err)
				http.Error(w, "mirror unavailable", http.StatusBadGateway)
				return
			}
			req.Header.Set("Content-Type", r.Header.Get("Content-Type"))
			resp, err := client.Do(req)
			if err != nil {
				t.Errorf("export mirrored OTLP payload: %v", err)
				http.Error(w, "mirror unavailable", http.StatusBadGateway)
				return
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode/100 != 2 {
				t.Errorf("mirrored OTLP %s: %d", r.URL.Path, resp.StatusCode)
			}
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(receiver.Close)
	return receiver, func() []*tracepb.Span {
		mu.Lock()
		defer mu.Unlock()
		return append([]*tracepb.Span(nil), seen...)
	}
}
