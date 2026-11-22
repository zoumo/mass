package processmetrics

import (
	"bytes"
	"context"
	"log/slog"
	"math"
	"runtime"
	runtimemetrics "runtime/metrics"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"golang.org/x/sys/unix"
)

func TestCurrentProcessMetrics(t *testing.T) {
	t.Setenv("HOST_PROC", t.TempDir())
	logger := slog.New(slog.DiscardHandler)
	reader := sdkmetric.NewManualReader(WithRuntimeHistograms(logger))
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	Register(provider, logger)
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(t.Context())) })

	first := collectMetrics(t, reader)
	cpu := first["process.cpu.time"].Data.(metricdata.Sum[float64])
	require.True(t, cpu.IsMonotonic)
	require.Equal(t, metricdata.CumulativeTemporality, cpu.Temporality)
	require.Len(t, cpu.DataPoints, 2)
	var modes []string
	for _, point := range cpu.DataPoints {
		mode, ok := point.Attributes.Value("cpu.mode")
		require.True(t, ok)
		require.GreaterOrEqual(t, point.Value, 0.0)
		modes = append(modes, mode.AsString())
	}
	require.ElementsMatch(t, []string{"user", "system"}, modes)

	rss := first["process.memory.usage"].Data.(metricdata.Sum[int64])
	require.False(t, rss.IsMonotonic)
	require.Positive(t, rss.DataPoints[0].Value)
	fds := first["process.unix.file_descriptor.count"].Data.(metricdata.Sum[int64])
	require.False(t, fds.IsMonotonic)
	require.Positive(t, fds.DataPoints[0].Value)
	uptime := first["process.uptime"].Data.(metricdata.Gauge[float64])
	require.Positive(t, uptime.DataPoints[0].Value)

	for name, unit := range map[string]string{
		"go.goroutine.count":                 "{goroutine}",
		"go.memory.used":                     "By",
		"go.memory.allocated":                "By",
		"go.memory.gc.goal":                  "By",
		"go.memory.gc.cycles":                "{gc_cycle}",
		"process.cpu.time":                   "s",
		"process.memory.usage":               "By",
		"process.unix.file_descriptor.count": "{file_descriptor}",
		"process.uptime":                     "s",
	} {
		require.Contains(t, first, name)
		require.Equal(t, unit, first[name].Unit, name)
	}
	firstCycles := first["go.memory.gc.cycles"].Data.(metricdata.Sum[int64])
	require.True(t, firstCycles.IsMonotonic)
	runtime.GC()
	second := collectMetrics(t, reader)
	secondCycles := second["go.memory.gc.cycles"].Data.(metricdata.Sum[int64])
	require.Greater(t, secondCycles.DataPoints[0].Value, firstCycles.DataPoints[0].Value)
	for _, name := range []string{gcPauseName, scheduleLatencyName} {
		require.Equal(t, "s", second[name].Unit)
		histogram := second[name].Data.(metricdata.Histogram[float64])
		require.Equal(t, metricdata.CumulativeTemporality, histogram.Temporality)
		point := histogram.DataPoints[0]
		before := first[name].Data.(metricdata.Histogram[float64]).DataPoints[0]
		require.GreaterOrEqual(t, point.Count, before.Count)
		if name == gcPauseName {
			require.Greater(t, point.Count, before.Count)
		}
		require.Equal(t, before.StartTime, point.StartTime)
		require.Equal(t, before.Bounds, point.Bounds)
		require.Len(t, point.BucketCounts, len(point.Bounds)+1)
		require.True(t, slices.IsSorted(point.Bounds))
		require.GreaterOrEqual(t, point.Bounds[0], 0.0)
	}
}

func TestUnavailableProcessSampleLeavesOtherMetrics(t *testing.T) {
	logs := new(bytes.Buffer)
	logger := slog.New(slog.NewTextHandler(logs, nil))
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(t.Context())) })
	missing := &sampleDiagnostics{logger: logger}
	sample := processSample{
		userCPU:   3,
		systemCPU: 2,
		cpuOK:     true,
		fds:       5,
		fdsOK:     true,
		fdLimit:   unix.RLIM_INFINITY,
		fdLimitOK: true,
		uptime:    10,
		uptimeOK:  true,
	}
	meter := provider.Meter(scopeName)
	require.NoError(t, registerProcess(meter, func(_ context.Context) processSample { return sample }, missing))

	metrics := collectMetrics(t, reader)
	require.NotContains(t, metrics, "process.memory.usage")
	require.NotContains(t, metrics, "mass.process.unix.file_descriptor.limit")
	require.Contains(t, metrics, "process.cpu.time")
	require.Contains(t, metrics, "go.memory.gc.cycles")
	sample.rss, sample.rssOK = 4096, true
	sample.fdLimit = 1024
	metrics = collectMetrics(t, reader)
	require.Equal(t, sample.rss, metrics["process.memory.usage"].Data.(metricdata.Sum[int64]).DataPoints[0].Value)
	require.EqualValues(t, sample.fdLimit, metrics["mass.process.unix.file_descriptor.limit"].Data.(metricdata.Gauge[int64]).DataPoints[0].Value)
	sample.rssOK = false
	require.NotContains(t, collectMetrics(t, reader), "process.memory.usage")
	require.Equal(t, 1, strings.Count(logs.String(), "metric=process.memory.usage"))
}

func TestRuntimeHistogramBucketConversion(t *testing.T) {
	point := histogramDataPoint(&runtimemetrics.Float64Histogram{
		Buckets: []float64{math.Inf(-1), 0, 1, 2, math.Inf(1)},
		Counts:  []uint64{0, 2, 3, 5},
	}, time.Now())
	require.Equal(t, []float64{math.Nextafter(1, 0), math.Nextafter(2, 0)}, point.Bounds)
	require.Equal(t, []uint64{2, 3, 5}, point.BucketCounts)
	require.EqualValues(t, 10, point.Count)
	require.InDelta(t, 13, point.Sum, 0)
}

func collectMetrics(t *testing.T, reader *sdkmetric.ManualReader) map[string]metricdata.Metrics {
	t.Helper()
	var resourceMetrics metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(t.Context(), &resourceMetrics))
	exported := make(map[string]metricdata.Metrics)
	for _, scope := range resourceMetrics.ScopeMetrics {
		for _, m := range scope.Metrics {
			require.NotContains(t, exported, m.Name)
			exported[m.Name] = m
		}
	}
	return exported
}
