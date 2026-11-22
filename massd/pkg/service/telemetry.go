package service

import (
	"context"
	"errors"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/zoumo/mass/massd/pkg/events"
	"github.com/zoumo/mass/massd/pkg/runtime"
	"github.com/zoumo/mass/massd/pkg/store"
)

type serviceTelemetry struct {
	tracer       trace.Tracer
	eventFailure metric.Int64Counter
	loadDuration metric.Float64Histogram
}

func (s *Service) configureTelemetry(opts Options) {
	if opts.TracerProvider != nil {
		s.telemetry.tracer = opts.TracerProvider.Tracer("massd.service")
	}
	if opts.MeterProvider == nil {
		return
	}
	meter := opts.MeterProvider.Meter("massd.service")
	s.telemetry.eventFailure, _ = meter.Int64Counter("mass.session.event.failure", metric.WithUnit("{failure}"))
	s.telemetry.loadDuration, _ = meter.Float64Histogram("mass.session.load.duration", metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(.01, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120, 300))
	_, _ = meter.Int64ObservableGauge("mass.session.count", metric.WithUnit("{session}"),
		metric.WithInt64Callback(s.observeSessionCount))
}

func (t serviceTelemetry) start(ctx context.Context, operation string) (context.Context, trace.Span) {
	if t.tracer == nil {
		return ctx, tracenoop.Span{}
	}
	return t.tracer.Start(ctx, operation)
}

func providerAttribute(provider string) attribute.KeyValue {
	switch provider {
	case "codex", "claude":
	default:
		provider = "other"
	}
	return attribute.String("provider", provider)
}

func operationOutcome(err error) string {
	var op *runtime.OperationError
	if errors.As(err, &op) {
		switch op.Code {
		case "outcome_unknown", "unavailable", "resource_exhausted":
			return op.Code
		case "canceled":
			return "canceled"
		case "deadline_exceeded":
			return "timeout"
		case "invalid_argument", "failed_precondition", "conflict", "not_found", "permission_denied", "unsupported", "native_rejected":
			return "rejected"
		}
	}
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, runtime.ErrInvalidArgument), errors.Is(err, runtime.ErrFailedPrecondition),
		errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrConflict):
		return "rejected"
	default:
		return "failed"
	}
}

func finishOperation(span trace.Span, err error, outcome string) {
	if outcome == "" {
		outcome = operationOutcome(err)
	}
	span.SetAttributes(attribute.String("outcome", outcome))
	if err != nil {
		span.SetStatus(codes.Error, "")
		span.SetAttributes(attribute.String("error.type", operationOutcome(err)))
	}
	span.End()
}

func (s *Service) observeSessionCount(_ context.Context, observer metric.Int64Observer) error {
	type dimensions struct {
		provider string
		state    events.SessionState
	}
	counts := make(map[dimensions]int64)
	s.mu.Lock()
	for _, run := range s.runs {
		provider := providerAttribute(run.provider).Value.AsString()
		for _, l := range run.lanes {
			state := events.Unknown
			switch {
			case l.closing:
				state = "closing"
			case l.load != nil && !l.load.complete.Load():
				state = "opening"
			case l.stateObserved:
				switch l.observedState {
				case events.Idle, events.Running, events.RequiresAction:
					state = l.observedState
				}
			}
			counts[dimensions{provider, state}]++
		}
	}
	s.mu.Unlock()
	for _, provider := range []string{"codex", "claude", "other"} {
		for _, state := range []events.SessionState{events.Unknown, events.Idle, events.Running, events.RequiresAction, "opening", "closing"} {
			count := counts[dimensions{provider, state}]
			observer.Observe(count, metric.WithAttributes(providerAttribute(provider), attribute.String("state", string(state))))
		}
	}
	return nil
}

func (t serviceTelemetry) recordEventFailure(stage string) {
	if t.eventFailure != nil {
		t.eventFailure.Add(context.Background(), 1, metric.WithAttributes(attribute.String("stage", stage)))
	}
}

func (t serviceTelemetry) finishLoad(h *historyLoad, provider string, err error, outcome string) {
	if t.loadDuration != nil {
		t.loadDuration.Record(h.ctx, time.Since(h.started).Seconds(),
			metric.WithAttributes(providerAttribute(provider), attribute.String("outcome", outcome)))
	}
	finishOperation(h.span, err, outcome)
}
