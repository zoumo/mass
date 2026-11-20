package subcommand

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
	"github.com/zoumo/mass/mass-go/pkg/processmetrics"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/zoumo/mass/superagent/pkg/core"
)

type telemetryRuntime struct {
	options   core.TelemetryOptions
	logger    *slog.Logger
	shutdowns []func(context.Context) error
}

func newTelemetryRuntime(ctx context.Context, logger *slog.Logger) (*telemetryRuntime, error) {
	traces, err := telemetryEnabled("TRACES")
	if err != nil {
		return nil, err
	}
	metrics, err := telemetryEnabled("METRICS")
	if err != nil {
		return nil, err
	}
	t := &telemetryRuntime{logger: logger}
	if !traces && !metrics {
		return t, nil
	}
	// SDK diagnostics can include exporter response bodies or endpoint details.
	otel.SetLogger(logr.Discard())
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(error) {
		logger.Warn("OpenTelemetry operation failed")
	}))
	res := resource.NewSchemaless(
		attribute.String("service.name", "superagent-core"),
		attribute.String("service.instance.id", uuid.NewString()),
	)
	if traces {
		exporter, err := otlptracehttp.New(ctx)
		if err != nil {
			return nil, errors.New("cannot initialize OTLP trace exporter")
		}
		provider := sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithBatcher(exporter))
		t.options.Traces = provider
		t.shutdowns = append(t.shutdowns, provider.Shutdown)
	}
	if metrics {
		exporter, err := otlpmetrichttp.New(ctx)
		if err != nil {
			t.shutdown()
			return nil, errors.New("cannot initialize OTLP metric exporter")
		}
		reader := sdkmetric.NewPeriodicReader(exporter, processmetrics.WithRuntimeHistograms(logger))
		provider := sdkmetric.NewMeterProvider(sdkmetric.WithResource(res), sdkmetric.WithReader(reader))
		processmetrics.Register(provider, logger)
		t.options.Metrics = provider
		t.shutdowns = append(t.shutdowns, provider.Shutdown)
	}
	return t, nil
}

func telemetryEnabled(signal string) (bool, error) {
	key := "OTEL_" + signal + "_EXPORTER"
	switch os.Getenv(key) {
	case "", "none":
		return false, nil
	case "otlp":
		if err := validateTelemetryEndpoint(signal); err != nil {
			return false, err
		}
		return true, nil
	default:
		return false, fmt.Errorf("%s must be none or otlp", key)
	}
}

func validateTelemetryEndpoint(signal string) error {
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
		if err != nil || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.Fragment != "" ||
			(endpoint.Scheme != "http" && endpoint.Scheme != "https") {
			return fmt.Errorf("%s must be an HTTP or HTTPS URL without user info or a fragment", key)
		}
	}
	return nil
}

func (t *telemetryRuntime) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var workers sync.WaitGroup
	for _, shutdown := range t.shutdowns {
		workers.Go(func() {
			if err := shutdown(ctx); err != nil {
				t.logger.Warn("OpenTelemetry shutdown or flush failed")
			}
		})
	}
	workers.Wait()
}
