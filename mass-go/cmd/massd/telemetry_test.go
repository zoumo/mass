package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTelemetrySignals(t *testing.T) {
	for _, tc := range []struct {
		name, traces, metrics, sampler string
		wantTrace, wantMetric          bool
	}{
		{"default off", "", "", "always_on", false, false},
		{"traces only", "otlp", "none", "always_on", true, false},
		{"metrics only", "none", "otlp", "always_on", false, true},
		{"unsampled traces", "otlp", "otlp", "always_off", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OTEL_TRACES_EXPORTER", tc.traces)
			t.Setenv("OTEL_METRICS_EXPORTER", tc.metrics)
			t.Setenv("OTEL_TRACES_SAMPLER", tc.sampler)
			t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "http/protobuf")
			t.Setenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "http/protobuf")
			t.Setenv("OTEL_EXPORTER_OTLP_METRICS_PROTOCOL", "http/protobuf")
			t.Setenv("OTEL_EXPORTER_OTLP_COMPRESSION", "none")
			var traces, metrics atomic.Int64
			receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/traces":
					traces.Add(1)
				case "/v1/metrics":
					metrics.Add(1)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(receiver.Close)
			t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", receiver.URL+"/v1/traces")
			t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", receiver.URL+"/v1/metrics")

			providers, err := newTelemetry(t.Context())
			require.NoError(t, err)
			ctx, span := providers.traces.Tracer("test").Start(t.Context(), "test operation")
			counter, err := providers.metrics.Meter("test").Int64Counter("test.operation.count")
			require.NoError(t, err)
			counter.Add(ctx, 1)
			span.End()
			providers.shutdown()
			require.Equal(t, tc.wantTrace, traces.Load() > 0)
			require.Equal(t, tc.wantMetric, metrics.Load() > 0)
		})
	}
}

func TestTelemetryShutdownBudget(t *testing.T) {
	// Both signal providers must receive the same deadline while the first
	// provider is still blocked flushing.
	deadlines := make(chan time.Time, 2)
	release := make(chan struct{})
	shutdown := func(ctx context.Context) error {
		deadline, _ := ctx.Deadline()
		deadlines <- deadline
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	providers := &daemonTelemetry{shutdowns: []func(context.Context) error{shutdown, shutdown}}
	start := time.Now()
	providers.shutdown()
	close(release)
	first, second := <-deadlines, <-deadlines
	require.Equal(t, first, second)
	require.WithinDuration(t, start.Add(5*time.Second), first, time.Second)
	require.WithinDuration(t, first, time.Now(), time.Second)
}
