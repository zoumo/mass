package transport

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	rpc2 "github.com/sourcegraph/jsonrpc2"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	rpcwire "github.com/zoumo/mass/mass-go/pkg/jsonrpc"
)

type rpcTelemetry struct {
	tracer        trace.Tracer
	duration      metric.Float64Histogram
	inflight      metric.Int64UpDownCounter
	subscriptions metric.Int64UpDownCounter
}

func newRPCTelemetry(opts Options) *rpcTelemetry {
	traces, metrics := opts.TracerProvider, opts.MeterProvider
	if traces == nil {
		traces = tracenoop.NewTracerProvider()
	}
	if metrics == nil {
		metrics = metricnoop.NewMeterProvider()
	}
	meter := metrics.Meter("massd.transport")
	duration, _ := meter.Float64Histogram("rpc.server.call.duration", metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(.0001, .00025, .0005, .001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60))
	inflight, _ := meter.Int64UpDownCounter("mass.rpc.inflight", metric.WithUnit("{request}"))
	subscriptions, _ := meter.Int64UpDownCounter("mass.session.subscription.active", metric.WithUnit("{subscription}"))
	return &rpcTelemetry{tracer: traces.Tracer("massd.transport"), duration: duration, inflight: inflight, subscriptions: subscriptions}
}

func rpcMethod(method string) string {
	switch method {
	case "initialize",
		"runtime.create", "runtime.get", "runtime.list", "runtime.update", "runtime.start", "runtime.stop", "runtime.delete",
		"session.create", "session.get", "session.list", "session.resume", "session.close", "session.inject", "session.prompt",
		"session.answer", "session.setConfig", "session.cancel", "session.load", "session.cancelLoad", "session.history",
		"session.subscribe", "session.unsubscribe":
		return method
	default:
		return "_OTHER"
	}
}

type rpcObservationKey struct{}

// Reply attempts belong to the same goroutine as RPC completion.
type rpcObservation struct {
	status          string
	outcome         string
	responseFailure bool
}

func operationErrorType(code string) string {
	switch code {
	case "outcome_unknown", "unavailable", "resource_exhausted", "canceled", "deadline_exceeded":
		return code
	case "invalid_argument", "failed_precondition", "conflict", "not_found", "permission_denied", "unsupported", "native_rejected":
		return "rejected"
	default:
		return "failed"
	}
}

func (t *rpcTelemetry) start(ctx context.Context, request *rpc2.Request) (context.Context, func()) {
	if t == nil {
		return ctx, func() {}
	}
	started := time.Now()
	method := rpcMethod(request.Method)
	methodAttr := semconv.RPCMethod(method)
	attrs := []attribute.KeyValue{semconv.RPCSystemNameJSONRPC, methodAttr}
	// Each RPC has its own remote parent. Clear any span inherited from the connection.
	ctx = trace.ContextWithSpanContext(ctx, trace.SpanContext{})
	ctx = extractRPCTraceContext(ctx, request.Meta)
	ctx, span := t.tracer.Start(ctx, method, trace.WithSpanKind(trace.SpanKindServer),
		trace.WithTimestamp(started), trace.WithAttributes(attrs...))
	t.inflight.Add(ctx, 1, metric.WithAttributes(methodAttr))
	o := &rpcObservation{}
	ctx = context.WithValue(ctx, rpcObservationKey{}, o)
	return ctx, func() {
		ended := time.Now()
		if o.status != "" {
			status := semconv.RPCResponseStatusCode(o.status)
			attrs = append(attrs, status)
			span.SetAttributes(status)
		}
		outcome := o.outcome
		if outcome == "" {
			outcome = "rejected"
		}
		span.SetAttributes(attribute.String("outcome", outcome))
		errorType := ""
		switch {
		case o.responseFailure:
			errorType = "response_write_failed"
			span.SetAttributes(attribute.String("mass.rpc.response.outcome", errorType))
		case o.status != "" && o.status != "OK":
			errorType = o.status
		case o.status == "":
			errorType = "no_response"
		}
		if errorType != "" {
			attrs = append(attrs, attribute.String("error.type", errorType))
			span.SetAttributes(attribute.String("error.type", errorType))
			span.SetStatus(codes.Error, "")
		}
		t.duration.Record(ctx, ended.Sub(started).Seconds(), metric.WithAttributes(attrs...))
		t.inflight.Add(ctx, -1, metric.WithAttributes(methodAttr))
		span.End(trace.WithTimestamp(ended))
	}
}

func extractRPCTraceContext(ctx context.Context, raw *json.RawMessage) context.Context {
	// Bound telemetry parsing and accept only traceparent from the remote peer.
	if raw == nil || len(*raw) > 1024 {
		return ctx
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(*raw, &metadata); err != nil {
		return ctx
	}
	var parent string
	if err := json.Unmarshal(metadata[rpcwire.TraceContextFormat], &parent); err != nil || parent == "" {
		return ctx
	}
	return (propagation.TraceContext{}).Extract(ctx, propagation.MapCarrier{rpcwire.TraceContextFormat: parent})
}

func observeRPCOutcome(ctx context.Context, outcome string) {
	if o, ok := ctx.Value(rpcObservationKey{}).(*rpcObservation); ok {
		o.outcome = outcome
	}
}

func sendRPCResponse(ctx context.Context, conn *rpc2.Conn, response *rpc2.Response) error {
	err := conn.SendResponse(ctx, response)
	if o, ok := ctx.Value(rpcObservationKey{}).(*rpcObservation); ok {
		if response.Error != nil {
			if o.status == "" {
				o.status = strconv.FormatInt(response.Error.Code, 10)
			}
			if o.outcome == "" {
				o.outcome = "rejected"
				if response.Error.Code == rpc2.CodeInternalError {
					o.outcome = "failed"
				}
			}
		} else {
			o.status, o.outcome = "OK", "success"
		}
		o.responseFailure = o.responseFailure || err != nil
	}
	return err
}
