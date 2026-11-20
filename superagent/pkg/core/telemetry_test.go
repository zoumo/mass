package core_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

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
