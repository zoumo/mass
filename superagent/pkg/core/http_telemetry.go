package core

import (
	"context"
	"net/http"
	"net/url"
	"reflect"

	"github.com/danielgtaylor/huma/v2"
)

const (
	telemetryPerformancePath = "/v1/telemetry/performance"
	telemetryTracePath       = "/v1/telemetry/traces"
	telemetryMetricsPath     = "/v1/telemetry/metrics"
)

type telemetryTraceInput struct {
	TraceID string `path:"traceId" doc:"32-character hexadecimal Trace ID"`
	At      string `query:"at" doc:"Optional RFC3339 time anchor"`
}

func (s *Server) registerTelemetryResources(api *httpAPI) {
	traceResponses := api.readResponses(api.schema(reflect.TypeFor[telemetryTrace]()), true)
	traceResponses["409"] = api.coreError("Trace query source is not configured")
	registerJSONRead(s, api, huma.Operation{
		Path: telemetryTracePath + "/{traceId}", OperationID: "getTelemetryTrace", Summary: "Read a Trace",
		Tags: []string{"Telemetry"}, Responses: traceResponses,
	}, func(ctx context.Context, in *telemetryTraceInput, _ bool) (telemetryTrace, error) {
		return s.observerTraces.read(ctx, in.TraceID, url.Values{"at": {in.At}})
	})

	performanceResponses := api.readResponses(api.schema(reflect.TypeFor[telemetryPerformance]()), false)
	performanceResponses["409"] = api.coreError("Performance query source is not configured")
	// The query engine validates the original multimap, including unknown,
	// duplicate and empty values and filter combinations for each metric.
	performance := huma.Operation{
		Method: http.MethodGet, Path: telemetryPerformancePath, OperationID: "getTelemetryPerformance",
		Summary: "Read performance time series", Tags: []string{"Telemetry"}, Responses: performanceResponses,
		Parameters: []*huma.Param{
			queryString("metric", "process, cpu, rss, goroutines, file_descriptors, mcp_rate, mcp_failure_rate or mcp_p95"),
			queryString("window", "15m, 1h, 6h or 24h. Defaults to 1h."),
			queryString("groupBy", "Process dimensions are instance, service and all. MCP dimensions are role, tool, action, outcome and all."),
			queryString("service", "Filter process metrics by massd or superagent-core"),
			queryString("instance", "Filter process metrics by service instance"),
			queryString("role", "Filter MCP metrics by role"),
			queryString("tool", "Filter MCP metrics by tool"),
			queryString("action", "Filter MCP metrics by action"),
			queryString("outcome", "Filter MCP metrics by outcome. Unavailable for mcp_failure_rate."),
		},
	}
	readPerformance := s.observeResourceHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, err := s.observerTraces.performance(r.Context(), r.URL.Query())
		if err != nil {
			s.fail(w, err)
			return
		}
		s.write(w, http.StatusOK, result)
	}))
	api.registerRaw(performance, readPerformance.ServeHTTP)
	api.registerRaw(headOperation(performance), readPerformance.ServeHTTP)

	for _, signal := range []struct{ path, operation, summary string }{
		{telemetryTracePath, "exportTelemetryTraces", "Export OTLP Traces"},
		{telemetryMetricsPath, "exportTelemetryMetrics", "Export OTLP Metrics"},
	} {
		wire := map[string]*huma.MediaType{observerOTLPContentType: {Schema: &huma.Schema{Type: huma.TypeString, Format: "binary"}}}
		api.registerRaw(huma.Operation{
			Method: http.MethodPost, Path: signal.path, OperationID: signal.operation, Summary: signal.summary, Tags: []string{"Telemetry"},
			Description: "Available when the loopback OTLP gateway is configured. Accepts uncompressed Protobuf up to 1 MiB with a three-second deadline and request rate limits.",
			RequestBody: &huma.RequestBody{Required: true, Content: wire},
			Responses: map[string]*huma.Response{
				"200": {Description: "OTLP export response", Content: wire},
				"400": {Description: "Invalid or unreadable OTLP export request"},
				"403": api.coreError("Request is outside the trusted Host or Origin boundary"),
				"404": {Description: "OTLP gateway disabled or invalid request target", Content: map[string]*huma.MediaType{
					"text/plain": {Schema: &huma.Schema{Type: huma.TypeString}},
				}},
				"413": {Description: "OTLP request exceeds 1 MiB"},
				"415": {Description: "Expected uncompressed application/x-protobuf"},
				"429": {Description: "OTLP export rate or concurrency limit reached"},
				"502": {Description: "OTLP backend failed, timed out or returned an oversized response"},
				"503": {Description: "OTLP backend or gateway unavailable"},
			},
		}, func(w http.ResponseWriter, r *http.Request) {
			s.observerTelemetry.serve(w, r)
		})
	}
}
