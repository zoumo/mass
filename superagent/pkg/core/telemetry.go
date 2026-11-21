package core

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

// TelemetryOptions supplies providers owned by the process that starts Core.
// An omitted provider disables its signal.
type TelemetryOptions struct {
	Traces  trace.TracerProvider
	Metrics metric.MeterProvider
}

type coreTelemetry struct {
	tracer            trace.Tracer
	httpDuration      metric.Float64Histogram
	operationDuration metric.Float64Histogram
	mcpToolDuration   metric.Float64Histogram
	queueDepth        metric.Int64ObservableGauge
	queueDuration     metric.Float64Histogram
	previewActive     metric.Int64UpDownCounter
	previewClosed     metric.Int64Counter
	enabled           bool
	tracesEnabled     bool
	metricsEnabled    bool
}

func newCoreTelemetry(options TelemetryOptions) *coreTelemetry {
	traces, metrics := options.Traces, options.Metrics
	t := &coreTelemetry{enabled: traces != nil || metrics != nil, tracesEnabled: traces != nil, metricsEnabled: metrics != nil}
	if traces == nil {
		traces = tracenoop.NewTracerProvider()
	}
	if metrics == nil {
		metrics = metricnoop.NewMeterProvider()
	}
	t.tracer = traces.Tracer("superagent.core")
	meter := metrics.Meter("superagent.core")
	t.httpDuration, _ = meter.Float64Histogram("http.server.request.duration",
		metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10))
	t.operationDuration, _ = meter.Float64Histogram("superagent.core.operation.duration",
		metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(0.005, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30))
	t.mcpToolDuration, _ = meter.Float64Histogram("superagent.mcp.tool.duration",
		metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(0.001, 0.005, 0.025, 0.1, 0.5, 1, 5, 10, 30))
	t.queueDepth, _ = meter.Int64ObservableGauge("superagent.core.queue.depth", metric.WithUnit("{item}"))
	t.queueDuration, _ = meter.Float64Histogram("superagent.core.queue.duration",
		metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(0.2, 0.5, 1, 2, 5, 10, 30, 60, 120, 300))
	t.previewActive, _ = meter.Int64UpDownCounter("superagent.core.preview.active", metric.WithUnit("{connection}"))
	t.previewClosed, _ = meter.Int64Counter("superagent.core.preview.closed", metric.WithUnit("{connection}"))
	return t
}

func (t *coreTelemetry) startOperation(ctx context.Context, operation, role string, opts ...trace.SpanStartOption) (context.Context, func(string)) {
	if !t.enabled {
		return ctx, func(string) {}
	}
	start := time.Now()
	attrs := []attribute.KeyValue{attribute.String("superagent.operation", operation)}
	if role != "" {
		attrs = append(attrs, attribute.String("superagent.role", role))
	}
	ctx, span := t.tracer.Start(ctx, "superagent."+operation, append(opts, trace.WithTimestamp(start), trace.WithAttributes(attrs...))...)
	if t.tracesEnabled {
		ctx = context.WithValue(ctx, traceAssociationContextKey{}, true)
	}
	return ctx, func(outcome string) {
		end := time.Now()
		result := attribute.String("superagent.outcome", outcome)
		span.SetAttributes(result)
		switch outcome {
		case "failed", "blocked", "unknown", "timeout", "retryable_failure", "permanent_failure":
			span.SetStatus(codes.Error, "")
		}
		t.operationDuration.Record(ctx, end.Sub(start).Seconds(), metric.WithAttributes(append(attrs, result)...))
		span.End(trace.WithTimestamp(end))
	}
}

func operationOutcome(err error, success string) string {
	if err == nil {
		return success
	}
	var e *Error
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.As(err, &e) && e.Code != "unavailable":
		return "rejected"
	default:
		return "failed"
	}
}

func messageIDAttribute(id string) attribute.KeyValue {
	return attribute.String("superagent.message.id", id)
}

type telemetryResponse struct {
	http.ResponseWriter
	status int
	finish func(int)
}

// Unwrap preserves streaming and write-deadline support through ResponseController.
func (w *telemetryResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *telemetryResponse) WriteHeader(status int) {
	if w.status == 0 && status >= 200 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *telemetryResponse) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(p)
}

func (w *telemetryResponse) finishRequest() {
	if w.finish == nil {
		return
	}
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	w.finish(status)
	w.finish = nil
}

func (t *coreTelemetry) startHTTP(w http.ResponseWriter, r *http.Request) (*telemetryResponse, *http.Request) {
	return t.startHTTPWithParent(w, r, time.Now(), trace.SpanContext{})
}

func (t *coreTelemetry) startHTTPWithParent(w http.ResponseWriter, r *http.Request, start time.Time, parent trace.SpanContext) (*telemetryResponse, *http.Request) {
	ctx := r.Context()
	if t.tracesEnabled {
		if parent.IsValid() {
			ctx = trace.ContextWithRemoteSpanContext(ctx, parent)
		} else {
			// Ingress accepts W3C trace identity. Untrusted vendor state stays outside Core.
			carrier := propagation.MapCarrier{"traceparent": r.Header.Get("Traceparent")}
			ctx = (propagation.TraceContext{}).Extract(ctx, carrier)
		}
		ctx = context.WithValue(ctx, traceAssociationContextKey{}, true)
	}
	method := boundedHTTPMethod(r.Method)
	route := telemetryRoute(r.URL.Path)
	spanName := method
	if method == "_OTHER" {
		spanName = "HTTP"
	}
	attrs := []attribute.KeyValue{semconv.HTTPRequestMethodKey.String(method), semconv.URLScheme("http")}
	if route != "" {
		spanName += " " + route
		attrs = append(attrs, semconv.HTTPRoute(route))
	}
	ctx, span := t.tracer.Start(ctx, spanName, trace.WithSpanKind(trace.SpanKindServer), trace.WithAttributes(attrs...), trace.WithTimestamp(start))
	observed := &telemetryResponse{ResponseWriter: w, finish: func(status int) {
		end := time.Now()
		attrs = append(attrs, semconv.HTTPResponseStatusCode(status))
		if status >= 500 {
			span.SetStatus(codes.Error, "")
			attrs = append(attrs, semconv.ErrorTypeKey.String(strconv.Itoa(status)))
		}
		span.SetAttributes(attrs...)
		t.httpDuration.Record(ctx, end.Sub(start).Seconds(), metric.WithAttributes(attrs...))
		span.End(trace.WithTimestamp(end))
	}}
	return observed, r.WithContext(ctx)
}

func boundedHTTPMethod(method string) string {
	switch method {
	case http.MethodConnect, http.MethodDelete, http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPatch,
		http.MethodPost, http.MethodPut, http.MethodTrace, "QUERY":
		return method
	default:
		return "_OTHER"
	}
}

// telemetryRoute returns a constant or a path built from an allowlisted
// collection. Object ids, unknown paths, and query strings never become labels.
func telemetryRoute(path string) string {
	switch path {
	case "/v1/channel/messages", "/v1/channel/outbox", "/v1/channel/outbox/result",
		"/v1/operator/input-recovery", "/v1/operator/echo-release", "/v1/observer/health",
		"/v1/observer/topic-activity", observerPerformancePath,
		"/observer", "/observer/", mcpPath:
		return path
	}
	switch {
	case strings.HasPrefix(path, "/v1/channel/outbox/items/"):
		return "/v1/channel/outbox/items/{id}"
	case strings.HasPrefix(path, "/v1/operator/session-bindings/"):
		return "/v1/operator/session-bindings/{id}"
	case strings.HasPrefix(path, observerTracePath):
		return observerTracePath + "{id}"
	case strings.HasPrefix(path, "/v1/observer/session-preview/"):
		_, view, _ := strings.Cut(strings.TrimPrefix(path, "/v1/observer/session-preview/"), "/")
		if view == "history" || view == "live" {
			return "/v1/observer/session-preview/{id}/" + view
		}
	case strings.HasPrefix(path, "/v1/observer/"):
		collection, _, detail := strings.Cut(strings.TrimPrefix(path, "/v1/observer/"), "/")
		switch collection {
		case "messages", "threads", "internal-events", "topics", "issues", "reports", "timers",
			"pull-requests", "router-messages", "routing-events", "subscriptions", "relations", "session-bindings", "outbox":
			if detail {
				return "/v1/observer/" + collection + "/{id}"
			}
			return "/v1/observer/" + collection
		}
	case strings.HasPrefix(path, "/observer/"):
		return "/observer/{asset}"
	}
	return ""
}

func (t *coreTelemetry) preview(ctx context.Context) func(string) {
	t.previewActive.Add(ctx, 1)
	return func(outcome string) {
		t.previewActive.Add(ctx, -1)
		t.previewClosed.Add(ctx, 1, metric.WithAttributes(attribute.String("superagent.outcome", outcome)))
	}
}

func (t *coreTelemetry) registerQueueDepth(provider metric.MeterProvider, store *Store) metric.Registration {
	if !t.metricsEnabled {
		return nil
	}
	registration, _ := provider.Meter("superagent.core").RegisterCallback(func(ctx context.Context, observer metric.Observer) error {
		ctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		defer cancel()
		var internalEvents, outbox int64
		err := store.db.QueryRowContext(ctx, `SELECT
 (SELECT count(*) FROM internal_events WHERE status IN ('pending','dispatching')),
 (SELECT count(*) FROM outbox WHERE status='pending')`).Scan(&internalEvents, &outbox)
		if err != nil {
			return nil
		}
		observer.ObserveInt64(t.queueDepth, internalEvents, metric.WithAttributes(attribute.String("superagent.queue", "internal_event")))
		observer.ObserveInt64(t.queueDepth, outbox, metric.WithAttributes(attribute.String("superagent.queue", "outbox")))
		return nil
	}, t.queueDepth)
	return registration
}

func (t *coreTelemetry) recordQueueDuration(ctx context.Context, queue string, createdAt time.Time) {
	if !t.metricsEnabled || createdAt.IsZero() {
		return
	}
	t.queueDuration.Record(ctx, max(0, time.Since(createdAt).Seconds()), metric.WithAttributes(attribute.String("superagent.queue", queue)))
}
