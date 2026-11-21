# Run Superagent telemetry locally

The local Docker Compose stack runs the OpenTelemetry Collector and OpenObserve.
It publishes the OpenObserve UI and two OTLP/HTTP receivers on loopback. The
Compose project name determines which test data volume to use.

## Start the stack

Set `ZO_ROOT_USER_EMAIL` and `ZO_ROOT_USER_PASSWORD` in your environment before
the first start. Use credentials reserved for this local OpenObserve instance.
Run these commands from the repository root.

```sh
export OPENOBSERVE_AUTHORIZATION="Basic $(printf '%s:%s' "$ZO_ROOT_USER_EMAIL" "$ZO_ROOT_USER_PASSWORD" | base64 | tr -d '\n')"
docker compose -p superagent-telemetry -f superagent/telemetry/compose.yaml up -d
```

Open OpenObserve at `http://127.0.0.1:15080` and select the `default`
organization. Change the host ports with `SUPERAGENT_OPENOBSERVE_PORT`,
`SUPERAGENT_OTLP_PORT`, and `SUPERAGENT_OBSERVER_OTLP_PORT` if needed.

## Enable Core telemetry

Set the following environment variables when you run `superagent serve` or
`superagent compose`. Each exporter is disabled by default. Set it to `none`
to disable that signal explicitly. The Go exporters use OTLP/HTTP with Protobuf
and honor the standard `OTEL_EXPORTER_OTLP_*` settings.

```sh
export OTEL_TRACES_EXPORTER=otlp
export OTEL_METRICS_EXPORTER=otlp
export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:14318
bin/superagent serve --config /absolute/run/core.yaml
```

To start Core and massd together, use the same `OTEL_*` variables with
`superagent compose`. massd inherits the variables from `superagent compose`.
Set `SUPERAGENT_OBSERVER_OTLP_ENDPOINT` separately to enable browser telemetry.

```sh
SUPERAGENT_OBSERVER_OTLP_ENDPOINT=http://127.0.0.1:14320 \
  bin/superagent compose --massd-bin ./bin/massd \
    --massd-config /absolute/run/massd.yaml --core-config /absolute/run/core.yaml
```

The Core resource has `service.name=superagent-core`. Submit a Message or
open Observer to generate requests. Use `OTEL_METRIC_EXPORT_INTERVAL=1000`
to shorten the export interval for a smoke test.

## Query Core telemetry

In **Traces**, search by the Message's `trace_id` across services. A Message
submission produces `POST /v1/channel/messages` and its `superagent.message.admit`
child span. A single-source workflow continues through role input, routing, Issue
creation, Issue Session preparation and the Report. To include massd's RPC,
Service and SDK spans, run a matching massd build with traces enabled and send
its OTLP data to the same Collector.

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

In OpenObserve, filter `POST /mcp` spans by `superagent.caller.id` and
`superagent.role`. Issue spans include the owning `superagent.topic.id` and
`superagent.session_binding.id`. When a Topic acts on a verified Issue, its span
includes `superagent.target.issue.id`. Supported event-driven actions carry
`superagent.event.id` and can continue that event's trace. Metric labels use
role, tool, outcome, and bounded action values. The histogram counts attempts
independently of Trace sampling or export.

## Query process and Go runtime metrics

Set `observerTrace` in the Core configuration to enable **Metrics** in
Observer. Select a process metric or view all process charts. Filter by service
and instance and group by service, instance or the total. The MCP metrics use
the exported histogram to show call rate, failure rate and P95 handling time.
Select a role, tool, action or outcome filter and group by an available
dimension. Failure rate groups and filters by role, tool or action so the
denominator includes all outcomes.
CPU uses a five-minute rate. Each point for the other metrics uses the
latest sample within the chart step or 90 seconds, whichever is longer.

With `OTEL_METRICS_EXPORTER=otlp`, `massd`, `superagent serve`, and
`superagent compose` report measurements even when idle. Use
`service_name="massd"` or `service_name="superagent-core"` to select a service.
Filter by `service_instance_id` to select a process. In Compose, massd and Core
report separate sets of measurements. Each restart creates a new instance ID.

| OTLP metric | What it measures |
| --- | --- |
| `process.cpu.time` | Cumulative CPU seconds of the service process in `cpu.mode=user` and `cpu.mode=system`. Sum the per-mode rates for each instance to obtain CPU cores used. |
| `process.memory.usage` | Resident memory in bytes. |
| `process.unix.file_descriptor.count` | Open file descriptors. |
| `mass.process.unix.file_descriptor.limit` | The finite soft limit for open file descriptors. Compare it with the open count to track remaining capacity. |
| `process.uptime` | Time since the OS started the process, in seconds. |
| `go.memory.used`, `go.memory.allocated`, `go.memory.gc.goal` | Go runtime memory use by category, cumulative heap allocation, and the GC heap target, in bytes. |
| `go.goroutine.count` | Live goroutines. |
| `go.memory.gc.cycles` | Cumulative number of completed GC cycles. |
| `go.schedule.duration`, `go.memory.gc.pause.duration` | Cumulative histograms of scheduler wait and GC stop-the-world pause durations in seconds. Use buckets for percentiles. The sums estimate lower bounds. |

In OpenObserve, use a rate window that spans multiple export intervals.

```promql
sum by (service_name, service_instance_id) (
  rate(process_cpu_time{service_name=~"massd|superagent-core"}[5m])
)
process_memory_usage{service_name=~"massd|superagent-core"}
go_goroutine_count{service_name=~"massd|superagent-core"}
```

These measurements cover the service processes on Linux and macOS. Use the
deployment's monitoring for host, container, filesystem, and Agent subprocess
metrics. A missing OS sample produces a gap for that measurement.

## Enable Observer telemetry

Set `SUPERAGENT_OBSERVER_OTLP_ENDPOINT=http://127.0.0.1:14320` when starting
Core. Build Superagent with the embedded Observer using `make build-superagent`.
This setting enables the Observer SDK at runtime. Use a loopback HTTP origin
with an explicit port. Browser batches go through the same-origin
`/v1/telemetry/traces` and `/v1/telemetry/metrics` gateway to the Collector's
Observer receiver. The gateway strips incoming credentials and caps batch
size, rate, and concurrency. The receiver applies a separate attribute
allowlist. Expose the Core endpoint only to the trusted Observer audience.

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
With Core tracing enabled, the database records W3C Trace Context in one
`trace_associations` table. The association commits with the business record.
Each dispatch has a span. A singleton dispatch inherits its source context.
A batch uses Span Links for its sources. Role tools use `triggerEventId` to
continue from the validated input event. Legacy and proactive calls may omit it.
Core can read existing associations when tracing is disabled. Sampling and
OpenObserve retention determine whether a trace is still queryable.

## Stop the stack

```sh
docker compose -p superagent-telemetry -f superagent/telemetry/compose.yaml down
```

Compose keeps the project volume. The `--volumes` flag also removes the local
OpenObserve data for that project.

See the [OpenTelemetry OTLP exporter configuration](https://opentelemetry.io/docs/languages/sdk-configuration/otlp-exporter/)
and [OpenObserve Collector guide](https://openobserve.ai/opentelemetry/collector/)
for the transport and authentication settings.
