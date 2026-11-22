package processmetrics

import (
	"context"
	"log/slog"
	"math"
	"runtime/metrics"
	"time"

	"go.opentelemetry.io/otel/sdk/instrumentation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

var runtimeStart = time.Now()

const (
	gcPauseName         = "go.memory.gc.pause.duration"
	scheduleLatencyName = "go.schedule.duration"
)

// WithRuntimeHistograms adds cumulative Go runtime histograms to the reader.
func WithRuntimeHistograms(logger *slog.Logger) sdkmetric.ReaderOption {
	return sdkmetric.WithProducer(&runtimeHistograms{missing: sampleDiagnostics{logger: logger}})
}

type runtimeHistograms struct {
	missing sampleDiagnostics
}

func (h *runtimeHistograms) Produce(context.Context) ([]metricdata.ScopeMetrics, error) {
	definitions := [...]struct{ name, source, description string }{
		{scheduleLatencyName, "/sched/latencies:seconds", "Time spent waiting to run after a goroutine became runnable."},
		{gcPauseName, "/sched/pauses/total/gc:seconds", "Duration of individual stop-the-world GC pauses."},
	}
	var samples [len(definitions)]metrics.Sample
	for i, definition := range definitions {
		samples[i].Name = definition.source
	}
	metrics.Read(samples[:])
	now := time.Now()
	exported := make([]metricdata.Metrics, 0, len(samples))
	for i, definition := range definitions {
		sample := samples[i].Value
		if sample.Kind() != metrics.KindFloat64Histogram {
			h.missing.warn(definition.name)
			continue
		}
		dp := histogramDataPoint(sample.Float64Histogram(), now)
		exported = append(exported, metricdata.Metrics{
			Name:        definition.name,
			Unit:        "s",
			Description: definition.description + " Sum is a lower-bound estimate.",
			Data: metricdata.Histogram[float64]{
				Temporality: metricdata.CumulativeTemporality,
				DataPoints:  []metricdata.HistogramDataPoint[float64]{dp},
			},
		})
	}
	return []metricdata.ScopeMetrics{{
		Scope:   instrumentation.Scope{Name: scopeName + "/runtime"},
		Metrics: exported,
	}}, nil
}

func histogramDataPoint(sample *metrics.Float64Histogram, now time.Time) metricdata.HistogramDataPoint[float64] {
	dp := metricdata.HistogramDataPoint[float64]{StartTime: runtimeStart, Time: now}
	var bucketCount uint64
	for i, count := range sample.Counts {
		dp.Count += count
		bucketCount += count
		upper := sample.Buckets[i+1]
		if upper <= 0 {
			// Fold the runtime's negative underflow bucket into the first
			// non-negative bucket. Duration measurements are non-negative.
			continue
		}
		if !math.IsInf(upper, 1) {
			// Go uses exclusive upper bounds. OTLP uses inclusive upper bounds.
			dp.Bounds = append(dp.Bounds, math.Nextafter(upper, math.Inf(-1)))
		}
		dp.BucketCounts = append(dp.BucketCounts, bucketCount)
		bucketCount = 0
		// Runtime histograms provide counts but no sum. Use each bucket's
		// lower bound, clamped to zero because pauses are non-negative.
		dp.Sum += max(0, sample.Buckets[i]) * float64(count)
	}
	if len(dp.Bounds) == len(dp.BucketCounts) {
		dp.BucketCounts = append(dp.BucketCounts, bucketCount) // OTLP's implicit +Inf bucket.
	}
	return dp
}
