package subcommand

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	metricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/protobuf/proto"
)

func TestCoreTelemetryExportsProcessMetrics(t *testing.T) {
	t.Setenv("OTEL_TRACES_EXPORTER", "none")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_PROTOCOL", "http/protobuf")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_COMPRESSION", "none")
	t.Setenv("OTEL_METRIC_EXPORT_INTERVAL", "600000")
	received := make(chan *metricspb.ExportMetricsServiceRequest, 1)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/metrics" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		data, err := io.ReadAll(r.Body)
		batch := new(metricspb.ExportMetricsServiceRequest)
		if err != nil || proto.Unmarshal(data, batch) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		select {
		case received <- batch:
		default:
			w.WriteHeader(http.StatusConflict)
		}
	}))
	t.Cleanup(receiver.Close)
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", receiver.URL+"/v1/metrics")
	logger := slog.New(slog.DiscardHandler)
	instances := make(map[string]struct{})
	for _, exporter := range []string{"none", "otlp", "otlp"} {
		t.Setenv("OTEL_METRICS_EXPORTER", exporter)
		telemetry, err := newTelemetryRuntime(t.Context(), logger)
		require.NoError(t, err)
		telemetry.shutdown()
		if exporter == "none" {
			require.Nil(t, telemetry.options.Metrics)
			require.Empty(t, received)
			continue
		}
		require.Len(t, received, 1)
		resourceMetrics := (<-received).GetResourceMetrics()
		require.Len(t, resourceMetrics, 1)
		resource := resourceMetrics[0]
		attributes := make(map[string]string)
		for _, kv := range resource.GetResource().GetAttributes() {
			attributes[kv.GetKey()] = kv.GetValue().GetStringValue()
		}
		require.Equal(t, "superagent-core", attributes["service.name"])
		instance := attributes["service.instance.id"]
		require.NotEmpty(t, instance)
		require.NotContains(t, instances, instance)
		instances[instance] = struct{}{}
		var names []string
		for _, scope := range resource.GetScopeMetrics() {
			for _, m := range scope.GetMetrics() {
				names = append(names, m.GetName())
			}
		}
		require.Subset(t, names, []string{
			"process.cpu.time", "process.memory.usage", "go.goroutine.count",
			"go.schedule.duration", "go.memory.gc.pause.duration",
		})
	}
}
