package core_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/zoumo/mass/superagent/pkg/core"
)

func TestCoreTelemetryHTTP(t *testing.T) {
	f := fixtureAt(t)
	recorder := tracetest.NewSpanRecorder()
	traces := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	reader := sdkmetric.NewManualReader()
	metrics := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		require.NoError(t, traces.Shutdown(context.Background()))
		require.NoError(t, metrics.Shutdown(context.Background()))
	})
	cfg, err := core.LoadConfig(f.config)
	require.NoError(t, err)
	server, err := core.StartWithTelemetry(cfg, core.TelemetryOptions{Traces: traces, Metrics: metrics})
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- server.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, server.Shutdown(ctx))
		require.NoError(t, <-done)
	})

	const (
		traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
		parent  = "00f067aa0ba902b7"
		secret  = "private-example"
	)
	client := &http.Client{Timeout: 2 * time.Second}
	request := func(method, path, origin, traceparent string, body []byte) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, server.Endpoint()+path, bytes.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+f.token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		req.Header.Set("Traceparent", traceparent)
		req.Header.Set("Tracestate", "vendor="+secret)
		req.Header.Set("Baggage", "key="+secret)
		response, err := client.Do(req)
		require.NoError(t, err)
		return response
	}
	body, err := json.Marshal(input(secret))
	require.NoError(t, err)
	response := request(http.MethodPost, "/v1/channel/messages", "http://foreign.invalid", "00-"+traceID+"-"+parent+"-01", body)
	require.Equal(t, http.StatusForbidden, response.StatusCode)
	require.NoError(t, response.Body.Close())
	require.Empty(t, recorder.Ended())

	for _, sampled := range []string{"01", "00"} {
		response = request(http.MethodPost, "/v1/channel/messages?ignored="+secret, server.Endpoint(), "00-"+traceID+"-"+parent+"-"+sampled, body)
		require.Equal(t, http.StatusOK, response.StatusCode)
		var receipt core.Receipt
		require.NoError(t, json.NewDecoder(response.Body).Decode(&receipt))
		require.NoError(t, response.Body.Close())
		require.Equal(t, sampled == "00", receipt.Duplicate)
	}
	response = request(http.MethodGet, "/v1/observer/messages/"+secret, "", "invalid", nil)
	require.Equal(t, http.StatusNotFound, response.StatusCode)
	require.NoError(t, response.Body.Close())

	require.Eventually(t, func() bool { return len(recorder.Ended()) == 3 }, time.Second, time.Millisecond)
	spans := make(map[string]sdktrace.ReadOnlySpan)
	for _, span := range recorder.Ended() {
		spans[span.Name()] = span
		require.NotContains(t, span.Name(), secret)
		require.Empty(t, span.SpanContext().TraceState().String())
		for _, attr := range span.Attributes() {
			require.NotContains(t, attr.Value.String(), secret)
			require.NotContains(t, attr.Value.String(), f.token)
		}
	}
	serverSpan := spans["POST /v1/channel/messages"]
	operation := spans["superagent.message.admit"]
	require.NotNil(t, serverSpan)
	require.NotNil(t, operation)
	require.Equal(t, trace.SpanKindServer, serverSpan.SpanKind())
	require.Equal(t, parent, serverSpan.Parent().SpanID().String())
	require.Equal(t, traceID, serverSpan.SpanContext().TraceID().String())
	require.Equal(t, serverSpan.SpanContext(), operation.Parent())
	require.NotNil(t, spans["GET /v1/observer/messages/{id}"])
	require.False(t, spans["GET /v1/observer/messages/{id}"].Parent().IsValid())

	var collected metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(t.Context(), &collected))
	outcomes := map[string]uint64{}
	queueDepths := 0
	var requests uint64
	for _, scope := range collected.ScopeMetrics {
		for _, sample := range scope.Metrics {
			switch sample.Name {
			case "http.server.request.duration":
				for _, point := range sample.Data.(metricdata.Histogram[float64]).DataPoints {
					requests += point.Count
					require.NotContains(t, point.Attributes.Encoded(attribute.DefaultEncoder()), secret)
				}
			case "superagent.core.operation.duration":
				for _, point := range sample.Data.(metricdata.Histogram[float64]).DataPoints {
					outcome, ok := point.Attributes.Value(attribute.Key("superagent.outcome"))
					require.True(t, ok)
					outcomes[outcome.AsString()] += point.Count
					require.NotContains(t, point.Attributes.Encoded(attribute.DefaultEncoder()), "superagent.message.id")
				}
			case "superagent.core.queue.depth":
				queueDepths = len(sample.Data.(metricdata.Gauge[int64]).DataPoints)
			}
		}
	}
	require.Equal(t, uint64(3), requests)
	require.Equal(t, map[string]uint64{"accepted": 1, "duplicate": 1}, outcomes)
	require.Equal(t, 2, queueDepths)
}

func TestCoreTelemetryExporterUnavailable(t *testing.T) {
	f := fixtureAt(t)
	p := f.start("OTEL_TRACES_EXPORTER=otlp", "OTEL_METRICS_EXPORTER=otlp",
		"OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:0", "OTEL_BSP_SCHEDULE_DELAY=1", "OTEL_METRIC_EXPORT_INTERVAL=5")
	receipt := submit(t, p, f, input("collector-unavailable"))
	require.Equal(t, "accepted", receipt.Status)
	started := time.Now()
	p.stop(t, false)
	require.Less(t, time.Since(started), 8*time.Second)
}

func TestCoreMCPEndpointTelemetry(t *testing.T) {
	boundary := newRouteBoundary(t)
	f := fixtureAt(t)
	f.token = `mcp-source-test-secret-"quoted"-\escaped`
	write(t, filepath.Join(f.dir, "source.token"), f.token)
	f.execution(boundary)
	recorder := tracetest.NewSpanRecorder()
	traces := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	reader := sdkmetric.NewManualReader()
	metrics := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		require.NoError(t, traces.Shutdown(context.Background()))
		require.NoError(t, metrics.Shutdown(context.Background()))
	})
	cfg, err := core.LoadConfig(f.config)
	require.NoError(t, err)
	server, err := core.StartWithTelemetry(cfg, core.TelemetryOptions{Traces: traces, Metrics: metrics})
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- server.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, server.Shutdown(ctx))
		require.NoError(t, <-done)
	})
	p := &process{endpoint: server.Endpoint()}
	router := waitBinding(t, p, "router", "", "ready")
	routerToken := roleToken(t, f, router)
	incoming := input("http-mcp")
	incoming.Message.Text = "Original input with protected source text " + f.token
	message := submit(t, p, f, incoming)
	decision := decide(t, p, routerToken, newRoute("http-mcp", []string{message.MessageID}, f.dir))
	topicID := decision.Targets[0].TopicID
	topic := waitBinding(t, p, "topic", topicID, "ready")
	topicToken := roleToken(t, f, topic)
	start := issueInput(f.dir, "http-mcp-issue")
	start.Start = true
	work := roleWork[core.WorkReceipt](t, p, topicToken, "create_issue", start)
	retried := roleWork[core.WorkReceipt](t, p, topicToken, "create_issue", start)
	require.Equal(t, work.Issue.ID, retried.Issue.ID)
	issueBinding := waitBinding(t, p, "issue", work.SessionBinding.ID, "ready")
	issueToken := roleToken(t, f, issueBinding)

	call := func(token, method string, id, params any) (int, []byte) {
		t.Helper()
		req, err := newMCPRequest(t.Context(), p.endpoint, token, method, id, params)
		require.NoError(t, err)
		return sendMCPRequest(t, req)
	}
	for _, check := range []struct {
		token, role, present, absent string
	}{
		{routerToken, "router", "route_messages", "create_issue"},
		{topicToken, "topic", "create_issue", "route_messages"},
		{issueToken, "issue", "submit_report", "create_issue"},
	} {
		identity := initializeMCP(t, p.endpoint, check.token)
		var binding core.SessionBinding
		require.NoError(t, json.Unmarshal(identity, &binding))
		require.Equal(t, check.role, binding.Role)
		status, body := call(check.token, "notifications/initialized", nil, nil)
		require.Equal(t, http.StatusAccepted, status)
		require.Empty(t, body)
		status, body = call(check.token, "tools/list", -9007199254740993, nil)
		require.Equal(t, http.StatusOK, status)
		response := decodeMCPResponse(t, body, "-9007199254740993")
		require.Equal(t, "-9007199254740993", string(response.ID))
		require.Nil(t, response.Error)
		require.Contains(t, string(response.Result), `"name":"`+check.present+`"`)
		require.NotContains(t, string(response.Result), `"name":"`+check.absent+`"`)
	}
	status, body := call(topicToken, "notifications/initialized", json.RawMessage(`null`), nil)
	require.Equal(t, http.StatusBadRequest, status)
	require.Contains(t, string(body), "Invalid Request")
	status, body = call(topicToken, "ping", json.RawMessage(`1.5`), nil)
	require.Equal(t, http.StatusBadRequest, status)
	require.Contains(t, string(body), "Invalid Request")
	status, _ = request(t, http.MethodGet, p.endpoint, "/v1/mcp", topicToken, nil)
	require.Equal(t, http.StatusMethodNotAllowed, status)
	status, _ = request(t, http.MethodPost, p.endpoint, "/v1/role/topic_query", topicToken, struct{}{})
	require.Equal(t, http.StatusNotFound, status)

	args := map[string]any{"name": "get_topic", "arguments": struct{}{}}
	status, _ = call(f.token, "tools/call", 1, args)
	require.Equal(t, http.StatusUnauthorized, status)
	status, body = call(topicToken, "tools/call", 2, args)
	require.Equal(t, http.StatusOK, status)
	response := decodeMCPResponse(t, body, "2")
	require.Nil(t, response.Error)
	var result struct {
		StructuredContent core.Topic `json:"structuredContent"`
		IsError           bool       `json:"isError"`
		Content           []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	require.NoError(t, json.Unmarshal(response.Result, &result))
	require.False(t, result.IsError)
	require.Equal(t, topicID, result.StructuredContent.ID)
	require.Len(t, result.Content, 1)
	require.Equal(t, "text", result.Content[0].Type)
	require.Contains(t, result.Content[0].Text, topicID)

	status, body = call(topicToken, "tools/call", "redaction", map[string]any{
		"name": "get_message", "arguments": map[string]string{"messageId": message.MessageID},
	})
	require.Equal(t, http.StatusOK, status)
	response = decodeMCPResponse(t, body, `"redaction"`)
	require.Nil(t, response.Error)
	var redacted struct {
		StructuredContent json.RawMessage `json:"structuredContent"`
		Content           []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	require.NoError(t, json.Unmarshal(response.Result, &redacted))
	require.Contains(t, string(redacted.StructuredContent), "[redacted]")
	require.Len(t, redacted.Content, 1)
	require.JSONEq(t, string(redacted.StructuredContent), redacted.Content[0].Text)

	status, body = call(issueToken, "tools/call", 3, map[string]string{"name": "get_issue"})
	require.Equal(t, http.StatusOK, status)
	response = decodeMCPResponse(t, body, "3")
	require.Nil(t, response.Error)
	require.Contains(t, string(response.Result), work.Issue.ID)
	status, body = call(topicToken, "tools/call", 4, map[string]string{"name": "route_messages"})
	require.Equal(t, http.StatusOK, status)
	response = decodeMCPResponse(t, body, "4")
	require.NotNil(t, response.Error)
	require.Equal(t, "forbidden", response.Error.Data.Code)
	const unknown = "private-tool-from-untrusted-input"
	status, body = call(topicToken, "tools/call", 5, map[string]string{"name": unknown})
	require.Equal(t, http.StatusOK, status)
	response = decodeMCPResponse(t, body, "5")
	require.NotNil(t, response.Error)
	require.Equal(t, "forbidden", response.Error.Data.Code)
	status, body = call(topicToken, "tools/call", 6, map[string]any{
		"name": "get_topic", "arguments": map[string]string{"role": "forged"},
	})
	require.Equal(t, http.StatusOK, status)
	response = decodeMCPResponse(t, body, "6")
	require.Nil(t, response.Error)
	var failure mcpTestToolResult
	require.NoError(t, json.Unmarshal(response.Result, &failure))
	require.Equal(t, "invalid_argument", decodeMCPToolError(t, failure).Code)

	wanted := map[[3]string]uint64{
		{"router", "route_messages", "ok"}:         1,
		{"topic", "create_issue", "ok"}:            2,
		{"topic", "get_message", "ok"}:             1,
		{"topic", "get_topic", "ok"}:               1,
		{"issue", "get_issue", "ok"}:               1,
		{"topic", "route_messages", "forbidden"}:   1,
		{"topic", "_unknown", "forbidden"}:         1,
		{"topic", "get_topic", "invalid_argument"}: 1,
	}
	key := func(attrs attribute.Set) [3]string {
		role, _ := attrs.Value("superagent.role")
		tool, _ := attrs.Value("superagent.tool")
		outcome, _ := attrs.Value("superagent.outcome")
		return [3]string{role.AsString(), tool.AsString(), outcome.AsString()}
	}
	var collected metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(t.Context(), &collected))
	recorded := map[[3]string]uint64{}
	for _, scope := range collected.ScopeMetrics {
		for _, sample := range scope.Metrics {
			if sample.Name != "superagent.mcp.tool.duration" {
				continue
			}
			for _, point := range sample.Data.(metricdata.Histogram[float64]).DataPoints {
				require.Positive(t, point.Sum)
				require.Equal(t, 3, point.Attributes.Len())
				recorded[key(point.Attributes)] += point.Count
			}
		}
	}
	require.Equal(t, wanted, recorded)

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		spans := map[[3]string]uint64{}
		for _, span := range recorder.Ended() {
			if span.Name() != "POST /v1/mcp" {
				continue
			}
			attrs := attribute.NewSet(span.Attributes()...)
			labels := key(attrs)
			if labels[1] == "" {
				continue
			}
			assert.NotContains(c, attrs.Encoded(attribute.DefaultEncoder()), unknown)
			gotID, ok := attrs.Value("superagent.topic.id")
			if labels[0] == "router" {
				assert.False(c, ok)
			} else {
				assert.Equal(c, topicID, gotID.AsString())
			}
			if labels[1] == "create_issue" {
				issue, _ := attrs.Value("superagent.target.issue.id")
				assert.Equal(c, work.Issue.ID, issue.AsString())
			}
			spans[labels]++
		}
		assert.Equal(c, wanted, spans)
	}, time.Second, time.Millisecond)
}

func TestMCPMutationTelemetryByAuthenticatedCaller(t *testing.T) {
	boundary := newRouteBoundary(t)
	f := fixtureAt(t)
	f.execution(boundary)
	recorder := tracetest.NewSpanRecorder()
	traces := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	reader := sdkmetric.NewManualReader()
	metrics := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		require.NoError(t, traces.Shutdown(context.Background()))
		require.NoError(t, metrics.Shutdown(context.Background()))
	})
	cfg, err := core.LoadConfig(f.config)
	require.NoError(t, err)
	server, err := core.StartWithTelemetry(cfg, core.TelemetryOptions{Traces: traces, Metrics: metrics})
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- server.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, server.Shutdown(ctx))
		require.NoError(t, <-done)
	})
	p := &process{endpoint: server.Endpoint()}
	router := waitBinding(t, p, "router", "", "ready")
	message := submit(t, p, f, input("usage-by-caller"))
	decision := decide(t, p, roleToken(t, f, router), newRoute("usage-route", []string{message.MessageID}, f.dir))
	topicID := decision.Targets[0].TopicID
	topicToken := roleToken(t, f, waitBinding(t, p, "topic", topicID, "ready"))
	topicEvent := waitDelivered(t, p, message.MessageID, "topic", topicID)
	start := issueInput(f.dir, "usage-issue")
	start.Start = true
	created := roleWork[core.WorkReceipt](t, p, topicToken, "create_issue", start)
	issue := created.Issue
	issueBinding := waitBinding(t, p, "issue", created.SessionBinding.ID, "ready")
	issueToken := roleToken(t, f, issueBinding)
	topicArgs := map[string]any{
		"operationId": "topic-edit", "issueId": issue.ID, "expectedRevision": issue.Revision,
		"title": "Edited by Topic", "triggerEventId": topicEvent,
	}
	topicEdit := roleWork[core.WorkReceipt](t, p, topicToken, "update_issue", topicArgs)
	issueArgs := map[string]any{
		"operationId": "issue-edit", "expectedRevision": topicEdit.Issue.Revision, "title": "Edited by Issue Agent",
	}
	issueEdit := roleWork[core.WorkReceipt](t, p, issueToken, "update_issue", issueArgs)
	require.Equal(t, "Edited by Issue Agent", issueEdit.Issue.Title)
	status, body := requestMCPTool(t, p.endpoint, "update_issue", topicToken, topicArgs)
	errorCode(t, status, body, http.StatusConflict, "conflict")
	// Reusing an operation ID with the current revision runs the update again.
	topicArgs["expectedRevision"] = issueEdit.Issue.Revision
	current := roleWork[core.WorkReceipt](t, p, topicToken, "update_issue", topicArgs).Issue
	require.Equal(t, "Edited by Topic", current.Title)
	require.Equal(t, issueEdit.Issue.Revision+1, current.Revision)

	labels := func(attrs attribute.Set) [3]string {
		role, _ := attrs.Value("superagent.role")
		outcome, _ := attrs.Value("superagent.outcome")
		action, _ := attrs.Value("superagent.action")
		return [3]string{role.AsString(), outcome.AsString(), action.AsString()}
	}
	wanted := map[[3]string]uint64{
		{"topic", "ok", "edit"}:       2,
		{"topic", "conflict", "edit"}: 1,
		{"issue", "ok", "edit"}:       1,
	}
	var collected metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(t.Context(), &collected))
	recorded := map[[3]string]uint64{}
	for _, scope := range collected.ScopeMetrics {
		for _, sample := range scope.Metrics {
			if sample.Name != "superagent.mcp.tool.duration" {
				continue
			}
			for _, point := range sample.Data.(metricdata.Histogram[float64]).DataPoints {
				tool, _ := point.Attributes.Value("superagent.tool")
				if tool.AsString() == "update_issue" {
					require.Positive(t, point.Sum)
					require.Equal(t, 4, point.Attributes.Len())
					recorded[labels(point.Attributes)] += point.Count
				}
			}
		}
	}
	require.Equal(t, wanted, recorded)
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		recorded := map[[3]string]uint64{}
		for _, span := range recorder.Ended() {
			if span.Name() != "POST /v1/mcp" {
				continue
			}
			attrs := attribute.NewSet(span.Attributes()...)
			tool, _ := attrs.Value("superagent.tool")
			if tool.AsString() != "update_issue" {
				continue
			}
			key := labels(attrs)
			recorded[key]++
			want := map[attribute.Key]string{"superagent.topic.id": topicID}
			if key[0] == "topic" {
				want["superagent.caller.id"] = topicID
				want["superagent.event.id"] = topicEvent
				want["superagent.target.issue.id"] = issue.ID
				assert.True(c, span.Parent().IsValid())
				assert.Equal(c, span.Parent().TraceID(), span.SpanContext().TraceID())
			} else {
				want["superagent.caller.id"] = issue.ID
				want["superagent.issue.id"] = issue.ID
				want["superagent.session_binding.id"] = issueBinding.ID
			}
			for key, value := range want {
				got, _ := attrs.Value(key)
				assert.Equal(c, value, got.AsString(), string(key))
			}
		}
		assert.Equal(c, wanted, recorded)
	}, time.Second, time.Millisecond)
}
