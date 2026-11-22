# Run the MASS daemon

`massd` serves Runtime and Session operations over WebSocket JSON-RPC. Run it as a standalone daemon or embed it with [`massd.Start`](instance.go). See the [Quick start](../docs/quick-start.md) for a local demonstration and the [Superagent guide](../superagent/README.md) for embedded setup.

## Configure a standalone instance

Run `make build-go` from the repository root. Run `bin/massd --help` for flags and defaults.

`massd` reads `~/.mass/config.yaml` when present. Use `--config` to select another YAML file. Flags take precedence over file values. The daemon resolves relative `dataDir` paths from the configuration file's directory. Pass an absolute path to `--data`. For an existing installation, use its current absolute data directory.

For an authenticated loopback instance, put these settings in a YAML file and pass that file to `bin/massd --config`.

```yaml
dataDir: /absolute/owned/data
listen: 127.0.0.1:24368
tokenFile: /absolute/private/massd.token
```

Use the WebSocket URL in the startup JSON as `MASS_URL`. For authenticated connections, supply the token through `MASS_TOKEN`. Direct non-loopback listeners require TLS and a token file. A token grants instance-wide access. Read the [WebSocket deployment contract](../harness/bindings/websocket.md#认证与部署) before configuring a proxy or browser Origin.

Check HTTP `GET /readyz`, then run `bin/massctl runtime list` against the reported WebSocket URL. A ready response confirms that `massd` accepts RPCs. A successful native operation requires a usable Agent CLI and credentials in the daemon environment.

### Preserve the data directory

Give each running MASS instance its own data directory. For a consistent backup, stop the writer and copy the directory with its layout and recovery files. In the default SQLite MetaStore, preserve `metadata-authority.sqlite` and `.metadata-sqlite.json` together. For the file MetaStore, preserve the resource files and metadata recovery records. In either case, EventStore's event files and recovery materials are authoritative. `history.sqlite` is a rebuildable query database. Keep Driver recovery records under `private/` with the data directory.

Changing `metaStore.backend` requires a separately verified migration or a fresh data directory. A missing authority database with its marker still present requires restoration from verified backup. See the [storage contract](../harness/bindings/storage.md) for ownership and recovery rules and the [SQLite layout decision](docs/adr/0001-distinguish-sqlite-authority-from-legacy-projection.md) for the reason behind the separate formats.

### Upgrade the WebSocket endpoint

Upgrade `massd`, `massctl`, Go clients, and Superagent Core together. Change explicit endpoints from `/rpc` to `/jsonrpc`, including reverse proxy Upgrade routes. Check `/readyz` and `massctl runtime list` through the configured client address. To run two versions simultaneously, give each its own endpoint and data directory.

## Operate a Session

Use `bin/massctl runtime --help` and `bin/massctl session --help` to discover operations. Use `NAME -n NAMESPACE` or a raw `SESSION_ID` without `-n` to address an existing Session.

Install and sign in to the native Agent CLI in the daemon environment. Use an absolute `--cwd` on the Agent host. To observe a newly created Session, start `massctl session watch` and wait for `subscription.ready` on stderr before sending input. Use `session history` to read committed events and `--after` to continue with the returned cursor. Closing a watch or ending an RPC wait leaves admitted work running.

`session close` releases the Session connection and preserves its identity and history. Use `session resume` to reopen that Session. Claude resume requires an absolute `env.CLAUDE_CONFIG_DIR` in its Runtime configuration. If resume reports `session_recovery_missing`, restore the original recovery material before retrying. Query `session get` for the saved reason if the response is lost. Use `runtime stop` to stop the Runtime and its remaining Sessions.

Use the [Go client](pkg/client/README.md) to answer a current interaction. The [JSON-RPC contract](../harness/bindings/jsonrpc.md) defines transport, methods, and errors.

### Configure Runtime and Session defaults

Use `massctl runtime create --help` and `massctl runtime update --help` for supported input flags. A `--config-file` contains `RuntimeConfig` itself. See [process configuration](pkg/runtime/process_config.go) for accepted keys. When using `envPolicy: replace`, supply the environment entries that the native CLI needs. A complete configuration supplied to `runtime update` replaces the old configuration. Coordinate concurrent edits to the same Runtime. Updates leave the current native process running.

See [Session defaults](../harness/modules/runtime/session-defaults.md) for how the daemon applies `features`, `mcpServers`, and permission presets. See the [main Session configuration contract](../harness/modules/runtime/session-config.md) for initial `--model` and `--effort` and for transient `session set-config` calls. Wait for each configuration call before sending the next change or input when order matters.

MCP file references use paths on the daemon host. The native SDK reads them when it opens or restores a Session. Give only trusted callers permission to submit Runtime and Session configuration. Literal HTTP header values are stored in the spec and readable by authorized clients. Review the [MCP header decision](../harness/adr/0002-mcp-http-header-values.md) when choosing how to supply credentials.

### Retained history load

Start the Runtime and choose a new MASS Session name. Supply the original native Session ID and its absolute working directory. For Claude, set an absolute `env.CLAUDE_CONFIG_DIR` on the Runtime. For Codex, use the native configuration that owns the source Session.

```sh
bin/massctl -o json session load imported -n demo --runtime agent \
  --cwd /absolute/owned/project --session-id ORIGINAL_NATIVE_ID
bin/massctl -o json session get imported -n demo
```

Poll `session get` for `historyLoad`. Wait for `historyLoad.completed=true` and `historyLoad.deliveryCompleted=true` before sending input. Read the imported events with `session history`. Successful load retains the original restored connection. Use `session cancel-load --load-id` with the original receipt's ID to cancel an unfinished load. See the [load contract](../harness/modules/service/load.md) for the meaning of native completion, delivery, and cleanup evidence.

## Export telemetry

Set `OTEL_TRACES_EXPORTER=otlp` or `OTEL_METRICS_EXPORTER=otlp` and give `massd` an `OTEL_EXPORTER_OTLP_ENDPOINT` for OTLP over HTTP. Set the native CLI's environment separately if it exports its own telemetry. For proxy deployments, preserve the `Mass-Trace-Context` Upgrade response header so the Go client can forward request trace context.

In the trace backend, filter on `service.name=massd` and the RPC span name. For `session.load`, inspect the child `mass.session.load` span for background settlement. See the [local telemetry guide](../superagent/telemetry/README.md) for Collector setup and query examples.

## Verify the implementation

Use the [acceptance guide](docs/acceptance.md) to select an offline test or a real Agent check. Use the [recording guide](docs/recording.md) to capture consumer material or diagnose recording failures.
