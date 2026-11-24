package service_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	metricsvc "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/protobuf/proto"

	"github.com/zoumo/mass/internal/massdtest"
	"github.com/zoumo/mass/massd/pkg/events"
	"github.com/zoumo/mass/massd/pkg/eventstore"
	"github.com/zoumo/mass/massd/pkg/resource"
	"github.com/zoumo/mass/massd/pkg/service"
)

type serviceTelemetryFixture struct {
	traces   *sdktrace.TracerProvider
	recorder *tracetest.SpanRecorder
	meters   *sdkmetric.MeterProvider
	reader   *sdkmetric.ManualReader
}

func newServiceTelemetryFixture(t *testing.T, options ...sdkmetric.Option) *serviceTelemetryFixture {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	traces := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	reader := sdkmetric.NewManualReader()
	meters := sdkmetric.NewMeterProvider(append(options, sdkmetric.WithReader(reader))...)
	t.Cleanup(func() {
		require.NoError(t, traces.Shutdown(context.Background()))
		require.NoError(t, meters.Shutdown(context.Background()))
	})
	return &serviceTelemetryFixture{traces, recorder, meters, reader}
}

func (f *serviceTelemetryFixture) configure(opts *service.Options) {
	opts.TracerProvider = f.traces
	opts.MeterProvider = f.meters
}

func (f *serviceTelemetryFixture) spans(name string) []sdktrace.ReadOnlySpan {
	var result []sdktrace.ReadOnlySpan
	for _, span := range f.recorder.Ended() {
		if span.Name() == name {
			result = append(result, span)
		}
	}
	return result
}

func (f *serviceTelemetryFixture) metric(t *testing.T, name string) metricdata.Aggregation {
	t.Helper()
	var data metricdata.ResourceMetrics
	require.NoError(t, f.reader.Collect(t.Context(), &data))
	for _, scope := range data.ScopeMetrics {
		for _, entry := range scope.Metrics {
			if entry.Name == name {
				return entry.Data
			}
		}
	}
	return nil
}

func TestHistoryLoadTelemetrySettlesOnce(t *testing.T) {
	for _, phase := range []string{"completed", "failed", "canceled"} {
		t.Run(phase, func(t *testing.T) {
			f := newServiceTelemetryFixture(t)
			st, err := massdtest.OpenFileStores(t.TempDir(), false)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, st.Close()) })
			svc, driver := ticket06FaultService(t, st.Meta, st.Events, f.configure)
			ctx := ticket06FaultContext(t)
			acceptedCtx, parent := f.traces.Tracer("test").Start(ctx, "session.load")
			receipt, err := svc.LoadSession(acceptedCtx, resource.SessionLoad{
				Metadata:   resource.Metadata{Namespace: "one", Name: "imported"},
				RuntimeRef: "agent", Cwd: "/remote", SessionID: "native-import",
			})
			require.NoError(t, err)
			parent.End()
			require.Empty(t, f.spans("mass.session.load"), "acceptance must leave load span open")
			var binding *ticket06FaultBinding
			select {
			case binding = <-driver.loading:
			case <-ctx.Done():
				t.Fatal("driver did not receive accepted load")
			}
			require.NoError(t, driver.marker(ctx, binding, "started"))
			ref := resource.SessionRef{SessionID: binding.id}
			require.Eventually(t, func() bool {
				current, err := svc.GetSession(ref)
				return err == nil && current.Status.HistoryLoad.NativeResumed
			}, time.Second, time.Millisecond)
			switch phase {
			case "canceled":
				_, err := svc.CancelHistoryLoad(ctx, ref, receipt.LoadID)
				require.NoError(t, err)
			case "failed":
				require.NoError(t, driver.marker(ctx, binding, "failed"))
			case "completed":
				require.NoError(t, driver.marker(ctx, binding, "native_completed"))
			}
			require.Eventually(t, func() bool {
				return len(f.spans("mass.session.load")) == 1
			}, time.Second, time.Millisecond)
			span := f.spans("mass.session.load")[0]
			require.Equal(t, parent.SpanContext(), span.Parent())
			require.True(t, span.EndTime().After(f.spans("session.load")[0].EndTime()))
			require.Contains(t, span.Attributes(), attribute.String("outcome", phase))
			points := f.metric(t, "mass.session.load.duration").(metricdata.Histogram[float64]).DataPoints
			require.Len(t, points, 1)
			require.EqualValues(t, 1, points[0].Count)
			require.Equal(t, []attribute.KeyValue{attribute.String("outcome", phase), attribute.String("provider", "other")}, points[0].Attributes.ToSlice())
		})
	}
}

func TestIdleCloseTelemetryAndSessionCount(t *testing.T) {
	telemetry := newServiceTelemetryFixture(t)
	f := newIdleFixture(t, time.Second, telemetry.configure)
	f.driver.releaseClose()
	f.waitIdle(t)
	idle := attribute.NewSet(attribute.String("provider", "other"), attribute.String("state", "idle"))
	sessionCount := func() int64 {
		points := telemetry.metric(t, "mass.session.count").(metricdata.Gauge[int64]).DataPoints
		for _, point := range points {
			if point.Attributes.Equals(&idle) {
				return point.Value
			}
		}
		t.Fatal("idle state metric missing")
		return 0
	}
	require.EqualValues(t, 1, sessionCount())
	f.clock.Advance(time.Second)
	require.Eventually(t, func() bool {
		return len(telemetry.spans("mass.session.idle_close")) == 1
	}, time.Second, time.Millisecond)
	require.Zero(t, sessionCount())
	spans := telemetry.spans("mass.session.idle_close")
	require.Len(t, spans, 1)
	require.False(t, spans[0].Parent().IsValid())
	require.Contains(t, spans[0].Attributes(), attribute.String("outcome", "success"))
	_, err := f.svc.StopRuntime(t.Context(), "agent")
	require.NoError(t, err)
	require.Nil(t, telemetry.metric(t, "mass.session.event.failure"), "normal EOF is not a stream fault")
}

func TestEventFailureTelemetry(t *testing.T) {
	for _, stage := range []string{"driver_stream", "history_commit", "realtime_publish"} {
		t.Run(stage, func(t *testing.T) {
			const authorization = "Bearer fake-collector-token"
			exports := make(chan []byte, 4)
			receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil || r.URL.Path != "/v1/metrics" || r.Header.Get("Content-Type") != "application/x-protobuf" ||
					r.Header.Get("Authorization") != authorization {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				exports <- body
			}))
			t.Cleanup(receiver.Close)
			exporter, err := otlpmetrichttp.New(t.Context(), otlpmetrichttp.WithEndpointURL(receiver.URL+"/v1/metrics"),
				otlpmetrichttp.WithCompression(otlpmetrichttp.NoCompression),
				otlpmetrichttp.WithHeaders(map[string]string{"Authorization": authorization}))
			require.NoError(t, err)
			periodic := sdkmetric.NewPeriodicReader(exporter, sdkmetric.WithInterval(time.Hour))
			f := newServiceTelemetryFixture(t, sdkmetric.WithReader(periodic))
			st, err := massdtest.OpenFileStores(t.TempDir(), false)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, st.Close()) })
			var history eventstore.EventStore = st.Events
			if stage == "history_commit" {
				history = &ticket06AppendFailure{EventStore: st.Events}
			}
			svc, driver := ticket06FaultService(t, st.Meta, history, f.configure)
			_, binding := ticket06FaultLoad(t, svc, driver)
			ref := resource.SessionRef{SessionID: binding.id}
			require.Eventually(t, func() bool {
				current, err := svc.GetSession(ref)
				return err == nil && current.Status.HistoryLoad.NativeResumed
			}, time.Second, time.Millisecond)
			if stage == "driver_stream" {
				driver.pipe.End(nil)
			} else {
				if stage == "realtime_publish" {
					broadcast, err := svc.EventBroadcast(ref)
					require.NoError(t, err)
					require.NoError(t, broadcast.AddHandler(&ticket06FailingPublication{}))
				}
				require.NoError(t, driver.emit(t.Context(), binding, "failed", events.AgentMessage{
					MessageID: "failed", Content: []events.ContentBlock{events.Text("private event content")},
				}, true))
			}
			wantStage := attribute.NewSet(attribute.String("stage", stage))
			require.Eventually(t, func() bool {
				sum, ok := f.metric(t, "mass.session.event.failure").(metricdata.Sum[int64])
				return ok && len(sum.DataPoints) == 1 && sum.DataPoints[0].Value == 1 &&
					sum.DataPoints[0].Attributes.Equals(&wantStage)
			}, time.Second, time.Millisecond)
			require.NoError(t, f.meters.ForceFlush(t.Context()))
			batch := new(metricsvc.ExportMetricsServiceRequest)
			select {
			case body := <-exports:
				require.NoError(t, proto.Unmarshal(body, batch))
			case <-time.After(time.Second):
				t.Fatal("no metric batch received")
			}
			var stages []string
			for _, resourceMetrics := range batch.GetResourceMetrics() {
				for _, scope := range resourceMetrics.GetScopeMetrics() {
					for _, m := range scope.GetMetrics() {
						if m.GetName() != "mass.session.event.failure" {
							continue
						}
						for _, point := range m.GetSum().GetDataPoints() {
							require.EqualValues(t, 1, point.GetAsInt())
							attrs := point.GetAttributes()
							require.Len(t, attrs, 1)
							require.Equal(t, "stage", attrs[0].GetKey())
							stages = append(stages, attrs[0].GetValue().GetStringValue())
						}
					}
				}
			}
			require.Equal(t, []string{stage}, stages)
			require.NotContains(t, batch.String(), "private event content")
			require.NotContains(t, batch.String(), "required delivery failed")
			require.NotContains(t, batch.String(), "fake-collector-token")
			if stage == "history_commit" {
				require.NoError(t, svc.ReconcileHistory(t.Context(), ref))
			}
		})
	}
}
