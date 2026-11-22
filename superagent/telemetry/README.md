# Run Superagent telemetry locally

Run the local Collector and OpenObserve to read traces and metrics from Core,
MASS, and Observer.

## Start the stack

Set `ZO_ROOT_USER_EMAIL` and `ZO_ROOT_USER_PASSWORD` in your environment before
the first start. Use credentials reserved for this local OpenObserve instance.
Run these commands from the repository root.

```sh
export OPENOBSERVE_AUTHORIZATION="Basic $(printf '%s:%s' "$ZO_ROOT_USER_EMAIL" "$ZO_ROOT_USER_PASSWORD" | base64 | tr -d '\n')"
docker compose -p superagent-telemetry -f superagent/telemetry/compose.yaml up -d
```

Open `http://127.0.0.1:15080` and select the `default` organization. If the
default ports are in use, set the override variables in [Compose](compose.yaml)
and use the matching addresses in the steps below.

## Enable Core telemetry

Set the following environment variables before you run `superagent serve` or
`superagent compose`. The Go exporters use OTLP/HTTP with Protobuf. Set an
exporter to `none` to disable its signal explicitly.

```sh
export OTEL_TRACES_EXPORTER=otlp
export OTEL_METRICS_EXPORTER=otlp
export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:14318
bin/superagent serve --config /absolute/run/core.yaml
```

Embedded MASS uses Core's trace and metric providers. For standalone `massd`,
set the same export variables in the daemon environment. Submit a Message or
open Observer to generate requests. Use
`OTEL_METRIC_EXPORT_INTERVAL=1000` to shorten the metric export interval for a
smoke test.

## Query Core telemetry

In **Traces**, search by the Message's `trace_id` across services. A Message
submission produces `POST /v1/channel/messages` and its `superagent.message.admit`
child span. A single-source workflow continues through role input, routing, Issue
creation, Issue Session preparation, and the Report. The trace can also include
MASS RPC, Service, and SDK spans.

Use these PromQL expressions in **Metrics**.

```promql
sum by (http_route) (http_server_request_duration_count{service_name="superagent-core"})
sum by (superagent_outcome) (superagent_core_operation_duration_count{service_name="superagent-core",superagent_operation="message.admit"})
```

The first expression groups HTTP requests by route template. The second shows
each admission outcome separately.

To count authenticated MCP tool attempts over the selected interval, group the
tool duration histogram by role, tool, and result.

```promql
sum by (superagent_role, superagent_tool, superagent_outcome) (
  increase(superagent_mcp_tool_duration_count{service_name="superagent-core"}[1h])
)
```

Each authenticated `tools/call` attempt contributes one observation, including
business errors and retries. Initialization and tool discovery contribute to
the HTTP request metrics.

Filter `POST /v1/mcp` spans by `superagent.caller.id` and `superagent.role`.
For an Issue, use its Topic and SessionBinding IDs to follow role calls.
Tool attempt metrics are independent of trace sampling.

## Query process and Go runtime metrics

Set `observerTrace` in the [Core configuration](../config.example.yaml) to
query OpenObserve from Observer. In **Metrics**, filter process charts by
service and instance. For MCP failure rate, group or filter by role, tool, or
action. The denominator includes all outcomes.

Metrics are sampled with `OTEL_METRICS_EXPORTER=otlp` even when the service is
idle. Embedded MASS shares the `superagent-core` process and its process
measurements, including in Compose. Standalone `massd` uses
`service_name="massd"`. Use `service_instance_id` to select one deployment
instance. A restart creates a new instance ID.

In OpenObserve, use a rate window that spans multiple export intervals.

```promql
sum by (service_name, service_instance_id) (
  rate(process_cpu_time{service_name=~"massd|superagent-core"}[5m])
)
process_memory_usage{service_name=~"massd|superagent-core"}
go_goroutine_count{service_name=~"massd|superagent-core"}
```

A missing OS sample produces a gap. Use the [process collectors](../../massd/pkg/processmetrics/)
for instrument names and units. Use deployment monitoring for host, container,
and Agent subprocess measurements.

## Enable Observer telemetry

Set `SUPERAGENT_OBSERVER_OTLP_ENDPOINT=http://127.0.0.1:14320` when starting
Core. Build the Observer with `make build-superagent`. Use a loopback HTTP
origin with an explicit port. Browser telemetry passes through the same-origin
Core gateway to the Collector's separate Observer receiver. Keep the gateway
within the trusted Observer network boundary.

Open an Observer view and search **Traces** for `service_name = superagent-observer`.
An `observer.http` client span and its Core server span share a trace ID. In
**Metrics**, aggregate page instances by the attributes relevant to the query.
The view histogram measures data loads, including refreshes, by view.

```promql
sum by (superagent_observer_api) (http_client_request_duration_count{service_name="superagent-observer"})
sum by (superagent_observer_view) (superagent_observer_view_duration_count{service_name="superagent-observer"})
```

## Find a trace from a Core record

Open a Message, Internal Event, Scheduled Task, SessionBinding, or Outbox record
in Observer Diagnostics. Select its Trace ID to open the waterfall in Observer
when `observerTrace` is configured.
Core saves the trace association with its business record when tracing is
enabled and reads saved associations after export is disabled. A singleton
dispatch continues its source trace. A batch links the source traces, so search
for the linked Trace IDs when the whole batch is absent from a single waterfall.
Role tools can continue a validated source event with `triggerEventId`. A saved
Trace ID can outlive its sampled or retained spans.

## Stop the stack

```sh
docker compose -p superagent-telemetry -f superagent/telemetry/compose.yaml down
```

Compose keeps the project volume. The `--volumes` flag also removes the local
OpenObserve data for that project.

See the [OpenTelemetry OTLP exporter configuration](https://opentelemetry.io/docs/languages/sdk-configuration/otlp-exporter/)
and [OpenObserve Collector guide](https://openobserve.ai/opentelemetry/collector/)
for the transport and authentication settings.
