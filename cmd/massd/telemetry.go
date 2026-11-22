package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/zoumo/mass/massd/pkg/processmetrics"
)

type daemonTelemetry struct {
	traces    trace.TracerProvider
	metrics   metric.MeterProvider
	shutdowns []func(context.Context) error
}

func newTelemetry(ctx context.Context) (*daemonTelemetry, error) {
	traces, err := otlpEnabled("OTEL_TRACES_EXPORTER")
	if err != nil {
		return nil, err
	}
	metrics, err := otlpEnabled("OTEL_METRICS_EXPORTER")
	if err != nil {
		return nil, err
	}
	for _, signal := range []struct {
		name    string
		enabled bool
	}{{"TRACES", traces}, {"METRICS", metrics}} {
		if signal.enabled {
			if err := validateOTLPEndpoint(signal.name); err != nil {
				return nil, err
			}
		}
	}

	t := &daemonTelemetry{traces: tracenoop.NewTracerProvider(), metrics: metricnoop.NewMeterProvider()}
	if !traces && !metrics {
		return t, nil
	}
	// OTel's internal diagnostics may contain endpoint or response contents.
	// Emit only a fixed diagnostic for errors from the provider's workers.
	otel.SetLogger(logr.Discard())
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(error) {
		slog.Warn("OpenTelemetry operation failed")
	}))
	res := resource.NewSchemaless(
		attribute.String("service.name", "massd"),
		attribute.String("service.instance.id", uuid.NewString()),
	)
	if traces {
		exporter, err := otlptracehttp.New(ctx)
		if err != nil {
			return nil, errors.New("cannot initialize OTLP trace exporter")
		}
		provider := sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithBatcher(exporter))
		t.traces = provider
		t.shutdowns = append(t.shutdowns, provider.Shutdown)
	}
	if metrics {
		exporter, err := otlpmetrichttp.New(ctx)
		if err != nil {
			t.shutdown()
			return nil, errors.New("cannot initialize OTLP metric exporter")
		}
		reader := sdkmetric.NewPeriodicReader(exporter, processmetrics.WithRuntimeHistograms(slog.Default()))
		provider := sdkmetric.NewMeterProvider(sdkmetric.WithResource(res), sdkmetric.WithReader(reader))
		processmetrics.Register(provider, slog.Default())
		t.metrics = provider
		t.shutdowns = append(t.shutdowns, provider.Shutdown)
	}
	return t, nil
}

func otlpEnabled(key string) (bool, error) {
	switch os.Getenv(key) {
	case "", "none":
		return false, nil
	case "otlp":
		return true, nil
	default:
		return false, fmt.Errorf("%s must be none or otlp", key)
	}
}

func validateOTLPEndpoint(signal string) error {
	for _, setting := range []string{"ENDPOINT", "PROTOCOL"} {
		key := "OTEL_EXPORTER_OTLP_" + signal + "_" + setting
		value := os.Getenv(key)
		if value == "" {
			key = "OTEL_EXPORTER_OTLP_" + setting
			value = os.Getenv(key)
		}
		if value == "" {
			continue
		}
		if setting == "PROTOCOL" {
			if value != "http/protobuf" {
				return fmt.Errorf("%s must be http/protobuf", key)
			}
			continue
		}
		endpoint, err := url.Parse(value)
		if err != nil || endpoint.Hostname() == "" || endpoint.User != nil ||
			(endpoint.Scheme != "http" && endpoint.Scheme != "https") {
			return fmt.Errorf("%s must be an HTTP or HTTPS URL without user info", key)
		}
	}
	return nil
}

func (t *daemonTelemetry) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var workers sync.WaitGroup
	for _, shutdown := range t.shutdowns {
		workers.Go(func() {
			if err := shutdown(ctx); err != nil {
				slog.Warn("OpenTelemetry shutdown or flush failed")
			}
		})
	}
	workers.Wait()
}
