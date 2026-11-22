package integration_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	metricsvc "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	tracesvc "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricpb "go.opentelemetry.io/proto/otlp/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/zoumo/mass/internal/testpeer/replay"
)

func withPromptTraceScenario(t *testing.T, path, provider string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	var scenario map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	require.NoError(t, dec.Decode(&scenario))
	if provider == "claude" {
		scenario["expectPromptTrace"] = true
	} else {
		for _, raw := range scenario["frames"].([]any) {
			frame := raw.(map[string]any)
			data, _ := frame["wire"].(map[string]any)
			if frame["direction"] == "c2s" && (data["method"] == "turn/start" || data["method"] == "turn/steer") {
				data["trace"] = map[string]any{"traceparent": replay.GeneratedTraceparent}
			}
		}
	}
	body, err = json.Marshal(scenario)
	require.NoError(t, err)
	result := filepath.Join(t.TempDir(), provider+"-trace.json")
	require.NoError(t, os.WriteFile(result, body, 0o600))
	return result
}

type telemetryReceiver struct {
	mu      sync.Mutex
	traces  []*tracesvc.ExportTraceServiceRequest
	metrics []*metricsvc.ExportMetricsServiceRequest
}

func (r *telemetryReceiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(req.Body)
	if err != nil || req.Header.Get("Content-Type") != "application/x-protobuf" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	switch req.URL.Path {
	case "/v1/traces":
		data := new(tracesvc.ExportTraceServiceRequest)
		if err := proto.Unmarshal(body, data); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		r.traces = append(r.traces, data)
	case "/v1/metrics":
		data := new(metricsvc.ExportMetricsServiceRequest)
		if err := proto.Unmarshal(body, data); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		r.metrics = append(r.metrics, data)
	default:
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
}

func otlpAttribute(attrs []*commonpb.KeyValue, key string) string {
	for _, attr := range attrs {
		if attr.GetKey() == key {
			return attr.GetValue().GetStringValue()
		}
	}
	return ""
}

func TestMassdOpenTelemetry(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			data := &telemetryReceiver{}
			collector := httptest.NewServer(data)
			t.Cleanup(collector.Close)
			t.Setenv("OTEL_TRACES_EXPORTER", "otlp")
			t.Setenv("OTEL_METRICS_EXPORTER", "otlp")
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", collector.URL)
			t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
			t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "")
			t.Setenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "http/protobuf")
			t.Setenv("OTEL_EXPORTER_OTLP_METRICS_PROTOCOL", "http/protobuf")
			t.Setenv("OTEL_TRACES_SAMPLER", "always_on")
			t.Setenv("OTEL_METRIC_EXPORT_INTERVAL", "100")
			t.Cleanup(func() {
				if !t.Failed() {
					data.checkLifecycle(t, provider)
				}
			})
			s := &entrypointSuite{coldRuntime: true}
			s.SetT(t)
			s.SetupTest()
			s.basic(provider)
		})
	}
}

func (r *telemetryReceiver) checkLifecycle(t *testing.T, provider string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var spans []*tracepb.Span
	for _, batch := range r.traces {
		for _, resource := range batch.GetResourceSpans() {
			require.Equal(t, "massd", otlpAttribute(resource.GetResource().GetAttributes(), "service.name"))
			for _, scope := range resource.GetScopeSpans() {
				spans = append(spans, scope.GetSpans()...)
			}
		}
		require.NotContains(t, batch.String(), `"hello"`, "prompt text must stay out of trace payloads")
	}
	parent := func(child *tracepb.Span, name string) *tracepb.Span {
		for _, span := range spans {
			if span.GetName() == name && bytes.Equal(span.GetTraceId(), child.GetTraceId()) &&
				bytes.Equal(span.GetSpanId(), child.GetParentSpanId()) {
				return span
			}
		}
		t.Fatalf("%s is missing parent %s", child.GetName(), name)
		return nil
	}
	coverage := map[string]string{
		"agent.core.open":      "session.create",
		"agent.core.resume":    "session.resume",
		"agent.session.close":  "session.close",
		"agent.start_or_steer": "session.prompt",
		"agent.core.stop":      "runtime.stop",
	}
	counts := map[string]int{}
	for _, span := range spans {
		rpc := coverage[span.GetName()]
		if rpc == "" {
			continue
		}
		counts[span.GetName()]++
		require.Equal(t, provider, otlpAttribute(span.GetAttributes(), "provider"))
		require.GreaterOrEqual(t, span.GetEndTimeUnixNano(), span.GetStartTimeUnixNano())
		root := parent(parent(span, "mass."+rpc), rpc)
		require.Empty(t, root.GetParentSpanId())
	}
	for name := range coverage {
		require.Positive(t, counts[name], name)
	}
	require.GreaterOrEqual(t, counts["agent.start_or_steer"], 2)
	for _, rpc := range []string{"session.create", "session.resume"} {
		require.Condition(t, func() bool {
			for _, span := range spans {
				if span.GetName() != "agent.core.start" {
					continue
				}
				for _, serviceSpan := range spans {
					if serviceSpan.GetName() == "mass."+rpc && bytes.Equal(span.GetTraceId(), serviceSpan.GetTraceId()) {
						start := parent(span, "mass.runtime.start")
						require.Equal(t, serviceSpan.GetSpanId(), start.GetParentSpanId())
						parent(serviceSpan, rpc)
						return true
					}
				}
			}
			return false
		}, "cold path for %s", rpc)
	}

	latest := map[string]*metricpb.Metric{}
	seen := map[string]bool{}
	for _, batch := range r.metrics {
		for _, resource := range batch.GetResourceMetrics() {
			require.Equal(t, "massd", otlpAttribute(resource.GetResource().GetAttributes(), "service.name"))
			for _, scope := range resource.GetScopeMetrics() {
				for _, metric := range scope.GetMetrics() {
					latest[metric.GetName()] = metric
					seen[metric.GetName()] = true
				}
			}
		}
	}
	require.Contains(t, seen, "mass.session.count")
	for _, name := range []string{"mass.rpc.inflight", "mass.session.subscription.active"} {
		points := latest[name].GetSum().GetDataPoints()
		require.NotEmpty(t, points, name)
		for _, point := range points {
			require.Zero(t, point.GetAsInt(), name)
		}
	}
	for _, name := range []string{"rpc.server.call.duration", "agent.sdk.operation.duration"} {
		points := latest[name].GetHistogram().GetDataPoints()
		require.NotEmpty(t, points, name)
		for _, point := range points {
			require.Positive(t, point.GetCount(), name)
		}
	}
}
