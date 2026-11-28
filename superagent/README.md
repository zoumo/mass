# Superagent

Superagent Core routes Messages to Topics and coordinates Issue Sessions through
MASS. The DWS and GitHub Triggers collect Messages. Superagent Web reads Core state.

## Start Core

1. Copy [the Core example](config.example.yaml) to a private run directory.
   Resolve relative paths from the YAML file's directory. Core creates
   `<dataDir>/credentials/admin.token` for Operators and embedded MASS, and
   `<dataDir>/credentials/triggers.token` for Triggers. Core reuses these files
   on restart.
2. Build and start Core from the repository root.

   ```sh
   make build-superagent
   bin/superagent serve --config /absolute/run/core.yaml
   ```

3. Read `endpoint` from the ready record. Check Core intake
   from another terminal. Use the printed Core endpoint if you changed `listen`.

   ```sh
   curl --fail http://127.0.0.1:24370/readyz
   curl --fail http://127.0.0.1:24370/status
   ```

   `/livez` checks whether Core can respond. `/readyz` reports whether Core
   can accept requests. `/status` reports Core and massd health. Standalone
   Core lists the built-in Triggers as `not_managed`. Run
   `bin/superagent observe --help` for Core record queries.

Omit `sessions` to collect Messages without starting Agents. To enable execution,
set `sessions.router`, `sessions.topic`, and at least one template in
`sessions.issue.templates`.
Use a fixed Core listen port or stable `externalURL` for the role MCP endpoint.
When MASS runs on another host, set an HTTPS `externalURL` that the MASS host
can reach. The MASS host must be able to read the Session workspaces and
`<dataDir>/mcp-files` at the absolute paths Core puts in the Session spec.
See the [Session preparation contract](API.md#session-configuration-and-preparation).

Core resource reads need no login. Keep the Core listener within a network
boundary for trusted readers. When `listen` uses a wildcard, use a
concrete numeric address to reach Core.

### Reuse an existing Core data directory

For a fresh rollout, point `dataDir` at a separate, empty directory. Keep the
previous directory and its binary together to access the prior state. Core
creates `core.sqlite` with schema 28 and rejects databases with earlier schema
versions.

To restart the new instance, stop its current owner and start Core with the
same `dataDir`. After `/readyz` succeeds, read a previously accepted receipt
by `sourceMessageId` with the original Source and Account ID. If two Core
installations share `massd`, give them distinct `sessions.namespace` values.

For a fresh Trigger database or a restart, follow the
[Trigger data path instructions](triggers/README.md#replace-deployed-triggers).

### Upgrade deployed Core data and credentials

Follow the [single-service migration procedure](docs/migrate-single-service.md)
to keep data, credentials, and Session history across the upgrade and rollback.

`storageMaxPages` limits the main SQLite database. Leave room for WAL and
other filesystem overhead. When the budget runs out, ingestion returns
`unavailable` without an acceptance receipt.

### Migrate Session configuration

For `superagent serve`, edit the root `sessions` section. For
`superagent compose`, edit `core.sessions`.

| Previous field | New field |
| --- | --- |
| `sessions.roles.router` | `sessions.router` |
| `sessions.routerWorkspace` | `sessions.router.workspace` |
| `sessions.roles.topic` | `sessions.topic` |
| `sessions.issueTemplates` | `sessions.issue.templates` |

Copy the existing values to the new fields. Use the same `sessions.namespace`,
Core and MASS data directories, and role MCP endpoint. Stop the current Core
and start the updated binary with the new YAML. Compare existing Session names,
binding IDs, and Session specs with their values before the restart.

### Enable a remote computer

Build `cua-mcp` with `make build-cua-mcp` and set `remoteComputer.cua.command`
to the executable and `remoteComputer.cua.configFile` to its YAML file.
Put `remoteComputer` at the root for `superagent serve` or under `core`
for `superagent compose`. Relative paths start at the configuration file's
directory. Core and MASS must have the same access to those paths.
Configure the relay, computers and credential files in the
[CUA MCP configuration](../extensions/cua/README.md).

Core adds `cua-mcp` to Topic and Issue Sessions and requests local tool
restrictions for Router, Topic and Issue. All three retain the `superagent`
business MCP. Codex keeps the `apply_patch` exception when the native model
exposes the tool.

Restart Core after changing `remoteComputer`. The MCP subprocess reads its
own YAML file at startup. If a binding already has a Session with different
remote computer options, Core blocks new input with
`remote_computer_configuration_changed`. Use
[operator reconciliation in replace mode](API.md#operator-reconciliation)
to release its current Session and prepare a new one with the updated options.

## Scheduled tasks

Topic Sessions manage their own scheduled tasks through MCP. For independent
tasks and triggering from a local script or CLI, use the Core admin token
and connect directly to Core. Independent schedules also run
in standalone Core without Session execution. Their Router events remain
pending until a Router can handle them.

See the [scheduled task API](API.md#scheduled-tasks) for schedule modes,
Operator commands and history queries.

Core parses five-field cron expressions with [robfig/cron v3](https://github.com/robfig/cron).

## Running MASS inside Core

Core starts embedded MASS in `<dataDir>/massd` when you omit the `massd`
section. Set the Session role profiles and Issue templates to Runtime names
available in that instance. MASS registers missing `codex` and `claude`
Runtimes on startup.

To connect to external MASS, set `massd.endpoint` to its WebSocket RPC
address and set `massd.tokenFile` if the service requires a bearer token.
When moving the MASS data between owners, stop Core and MASS before the
handoff. Keep the same MASS metadata and recovery material. After restarting
Core, compare the Session IDs, resource UIDs, and specs against the saved
bindings. Reconcile any uncertain input before manually resubmitting it.
See [operator reconciliation](API.md#operator-reconciliation).

### Accessing embedded MASS with massctl

Use `<dataDir>/credentials/admin.token`. It authorizes all MASS Runtime and
Session operations, including operations on Sessions managed by Core.

```sh
MASS_TOKEN="$(cat /absolute/run/superagent-core-data/credentials/admin.token)" \
	bin/massctl --url ws://127.0.0.1:24370/apis/massd session list
```

For remote access, use a trusted TLS reverse proxy. Forward `/apis/massd/`, the
Bearer header, and the WebSocket Upgrade. Set upstream `Host` to the Core
listen address with its port. Native clients use `wss://<proxy-host>/apis/massd`.
Embedded MASS rejects upgrades containing an `Origin` header.
In embedded mode, both `superagent serve` and `superagent compose` print
`massdEndpoint` with the full WebSocket RPC path in the ready record.

## Run one Watchdog check

```sh
bin/superagent watchdog --endpoint http://127.0.0.1:24370
```

The Watchdog checks for an unaccepted, current `ready` Report whose notification
reached MASS at least 30 minutes ago. It prints one JSON result with a `finding`,
`no_finding`, or `incomplete` status. Use `reason` to distinguish outcomes such
as no eligible Report, insufficient evidence, or a timeout. The result includes
`releaseUnconfirmed` if Watchdog cannot confirm that the Codex process group
has exited.

When a Report needs review, the Watchdog starts `codex app-server` in a temporary
directory. Use `--codex-bin` to select a different executable. It uses
`OPENAI_API_KEY`, `CODEX_API_KEY`, or file-based Codex login. For file-based
login, it copies `auth.json` from `CODEX_HOME` or `~/.codex` into the temporary
Codex home for the round. User Codex configuration stays in the source home.
Each round discards any refreshed login state. Supply an API key for unattended
periodic checks.

To enable periodic checks, add a top-level `watchdog` section to the Compose
file and put `codex` on Compose's `PATH`.

```yaml
watchdog:
  interval: 10m
```

Compose starts Watchdog after Core is ready. Watchdog checks current state
immediately and waits `interval` after each round, including failed rounds.
Use a positive Go duration string, such as `10m` or `1h`. Under
`component: watchdog`, the Compose logs record the outcome and resource IDs.
On shutdown, Compose cancels the active check and waits for Watchdog and the
Codex process group to exit.

Open **Watchdog** at `/watchdog` to see whether Watchdog is running
and read the latest completed check. Use the Topic and Issue links to review
its judgment and recommendation in context. Compose keeps the latest result
after Watchdog exits and starts with an empty snapshot after restart.

## Run the stack with Compose

1. Copy [the Compose example](compose.example.yaml) to a private run directory.
   Keep `core` and the Trigger sections you want to run. Keep Trigger files
   outside `core.dataDir`.
2. Supply native GitHub and DWS credentials. Compose waits for Core readiness,
   reads `<core.dataDir>/credentials/triggers.token`, and injects the token into
   each Trigger subprocess. See the [Trigger guide](triggers/README.md).
3. Start Compose from the repository root.

   ```sh
   bin/superagent compose --config /absolute/run/stack.yaml
   ```

4. Read `endpoint` from the ready record and request `/status` at that origin.
   The response lists Core, massd, GitHub, and DWS. Scheduled task counts appear under
   `core.scheduledTaskStates`. A Trigger failure can make `/status` report `degraded`
   while `/readyz` continues to report Core admission. For adapter details,
   use the corresponding address in `triggerEndpoints`. Compose reports the
   actual bound port and uses a loopback address for a wildcard listener.
5. When Core runs embedded MASS, the ready record includes `massdEndpoint`.
   Connect with the Core admin token. Use `/apis/massd/livez` and `/apis/massd/readyz`
   at the Core origin to check the embedded instance.

Press Ctrl-C to stop the Triggers before Core and embedded MASS. After startup,
Compose logs a Trigger exit and keeps the other processes running. If Core
exits unexpectedly, Compose stops the Triggers and returns an error.

## Start Superagent Web

Run `make build-superagent` and start `bin/superagent serve` or
`bin/superagent compose`. Open the printed Core endpoint at `/`, for example
`http://127.0.0.1:24370/`. The Core listener serves the Web, Core API, probes,
and embedded MASS. The binary contains the Web files. To deploy a Web update,
replace the binary and restart it.

Use your deployment ingress to terminate TLS and control access. Keep the
Core listener private. At the ingress, validate browser Origin against the
public origin and forward the request using the upstream Core authority as
`Host`. If a validated browser request includes `Origin` on a Core API request,
rewrite it to the upstream Core origin and retain `Sec-Fetch-Site`. Keep
Operator clients on a direct trusted Core connection. Set `externalURL` to
the origin role Sessions use for MCP.

If you used `observerAllowedHosts`, use `readAllowedHosts` for additional
direct Core resource read hosts. Rename `telemetryQuery` or `observerTrace` to
`openobserve` in existing Core configurations. In Compose, use
`core.openobserve`. Keep the credential file on the Core host. See the
[OpenObserve setup](telemetry/README.md#query-process-and-go-runtime-metrics).

### Develop the Web locally

Start Core on `127.0.0.1:24370`. You can use a Go-only build for the Vite
workflow.

```sh
go run ./cmd/superagent serve --config /absolute/run/core.yaml
```

In another terminal, run these commands and open the Vite origin at `/`.

```sh
cd superagent/web
npm ci
npm run dev
```

Vite proxies API and probe requests to
`SUPERAGENT_CORE_ORIGIN`, which defaults to `http://127.0.0.1:24370`.

## Cut over the HTTP API

Use the [direct cutover and rollback guide](docs/migrate-single-service.md)
when deploying this release with standalone Triggers.

## Inspect delivery and recover input

Use Superagent Web or these CLI commands to query retained Core resources.

```sh
bin/superagent observe status
bin/superagent observe trigger-messages --thread-id 'dws:example'
bin/superagent observe router-messages --message-id mass-m1
bin/superagent observe outbox-messages --message-id mass-m1
bin/superagent observe topic-activity mass-t1 --filter message
```

See [the Core API](API.md) for the resource contract.

- Use `inspect-session-binding` before `recover-input` to reconcile an uncertain
  dispatch, settle a retired binding, or replace a Session.
  See [operator reconciliation](API.md#operator-reconciliation).
- To trace a record across Core and MASS, see [local telemetry](telemetry/README.md).

## Checks

Run `make test-superagent` for the offline Core and Trigger process tests. They
use synthetic peers and do not call live models or channel accounts.
Run `make check-superagent` for the full checks, including Playwright in Chrome
against Core processes that embed the Web. Use `make check-superagent-web` to
run the browser tests alone. Use `make openapi-superagent` to export the API
contract with Go tooling. See the [Makefile](../Makefile).
