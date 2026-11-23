# Superagent

Superagent Core routes Messages to Topics and coordinates Issue Sessions through
MASS. The DWS and GitHub Triggers collect Messages. Superagent Web reads Core state.

## Start Core

1. Copy [the Core example](config.example.yaml) to a private run directory.
   Configure a source and its token file. Use a private regular file with a
   token of at least 32 non-whitespace bytes. Relative paths start at the YAML
   file's directory.
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
configure role Sessions and at least one Issue template in the example YAML.
Use a fixed Core listen port or stable `externalURL` for the role MCP endpoint.
Use the same host and OS account for Core and standalone `massd` so native
Sessions can read their credential files. See the [Session preparation contract](API.md#session-configuration-and-preparation).

Core resource reads need no login. Keep Core and the Web gateway within a
network boundary for trusted readers. When `listen` uses a wildcard, use a
concrete numeric address to reach Core.

### Reuse an existing Core data directory

Core uses schema 27 and rejects older schemas before opening the database.
Use a new, empty `dataDir` when switching from an earlier Core release. Keep
its old data directory and binary to access the prior state. Reuse an existing
schema 27 directory on restart. If two Core installations share `massd`, give
them distinct `sessions.namespace` values.

Configure each Trigger to use the new Core and set its allowed `threadIds`
or `threadIdPrefixes` in Core's source configuration.

`storageMaxPages` limits the main SQLite database. Leave room for WAL and
other filesystem overhead. When the budget runs out, ingestion returns
`unavailable` without an acceptance receipt.

## Running MASS inside Core

Replace `massd.endpoint` and `massd.tokenFile` with an `embedded` block. Keep
Core and MASS in separate directories. Set the role profiles and Issue
profiles to Runtime names available in that MASS instance. MASS registers
missing `codex` and `claude` Runtimes on startup.

```yaml
dataDir: ./superagent
massd:
  embedded:
    dataDir: ./massd
```

Start Core as above. Core prepares embedded MASS before it prints the ready
record. See [ADR 0001](docs/adr/0001-use-one-mass-rpc-boundary.md) for the
shared RPC boundary and data ownership.

To switch between embedded and standalone MASS:

1. Stop Core and the current MASS owner. Verify both exited successfully
   before starting the replacement.
2. Keep the same MASS data directory, metadata backend, and recovery material.
   Keep Core's data directory, role MCP address, and credential file paths.
3. For embedded mode, set `massd.embedded.dataDir`. For standalone mode, start
   `massd` with the same directory and set `massd.endpoint` in Core.
4. Restart Core. Compare the Session IDs, resource UIDs, and specs of existing
   bindings with the pre-cutover records. Reconcile uncertain input before
   manually resubmitting it. See [operator reconciliation](API.md#local-operator-reconciliation).

### Accessing embedded MASS with massctl

Keep the Core listener on loopback. Set `massd.embedded.expose.tokenFile` to a
private file with a dedicated MASS token. Use at least 32 bytes without
whitespace or control characters. Use a different credential for each of Core
Source, Operator, and role MCP access. Reserve the `cap_` prefix for role MCP.
The MASS token grants access to all Runtime and Session operations, including
Sessions managed by Core.

```sh
MASS_TOKEN="$(cat /absolute/run/embedded-massd.token)" \
	bin/massctl --url ws://127.0.0.1:24370/massd session list
```

For remote access, use a trusted TLS reverse proxy. Forward `/massd/`, the
Bearer header, and the WebSocket Upgrade. Set upstream `Host` to the Core
listen address with its port. Clients use `wss://<proxy-host>/massd`. If they
send `Origin`, add the exact value to `massd.embedded.expose.allowedOrigins`.
See the [WebSocket binding](../harness/bindings/websocket.md#认证与部署).

## Run one Watchdog check

```sh
bin/superagent watchdog --endpoint http://127.0.0.1:24370
```

The Watchdog checks for an unaccepted, current `ready` Report whose notification
reached MASS at least 30 minutes ago. It prints one JSON result with a `finding`,
`no_finding`, or `incomplete` status. Use `reason` to distinguish outcomes such
as no eligible Report, insufficient evidence, or a timeout.

When a Report needs review, the Watchdog starts `codex app-server` in a temporary
directory. Use `--codex-bin` to select a different executable. It uses
`OPENAI_API_KEY`, `CODEX_API_KEY`, or file-based Codex login. For file-based
login, it copies `auth.json` from `CODEX_HOME` or `~/.codex` into the temporary
Codex home for the round. User Codex configuration stays in the source home.
Each round discards any refreshed login state. Supply an API key for unattended
periodic checks.

## Run the stack with Compose

1. Copy [the Compose example](compose.example.yaml) to a private run directory.
   Keep `core` and the Trigger sections you want to run. Compose starts
   embedded MASS. Keep Core, MASS, and Trigger data in separate paths.
2. Match each Trigger's source, account, and instance to a `core.sources`
   entry. Keep the file at `tokenFile` and the variable named by `coreTokenEnv`
   in sync. Supply GitHub and DWS access as described in the
   [Trigger guide](triggers/README.md).
3. Start Compose from the repository root.

   ```sh
   bin/superagent compose --config /absolute/run/stack.yaml
   ```

4. Read `endpoint` from the ready record and request `/status` at that origin.
   The response lists Core, massd, GitHub, and DWS. Timer counts appear under
   `core.timerStates`. A Trigger failure can make `/status` report `degraded`
   while `/readyz` continues to report Core admission. For adapter details,
   use the corresponding loopback address in `triggerEndpoints`.
5. If you enabled embedded MASS exposure, use `massdEndpoint` with its
   dedicated token. Use `/massd/livez` and `/massd/readyz` at the Core origin
   to check the exposed instance.

Press Ctrl-C to stop the Triggers before Core and embedded MASS. After startup,
Compose logs a Trigger exit and keeps the other processes running. If Core
exits unexpectedly, Compose stops the Triggers and returns an error.

## Start Superagent Web

Build `superagent-web` and start its static service with Caddy 2.11.2.
Run these commands from the repository root in one terminal.

```sh
make build-superagent-web
cd superagent/web
caddy run --config Caddyfile
```

Start the same-origin gateway in a second terminal.

```sh
cd superagent/web
caddy run --config Gateway.Caddyfile
```

Open `http://127.0.0.1:24372/superagent/`. The Web service listens on
`127.0.0.1:24371` and serves `superagent/web/dist`. The gateway sends
`/superagent/` and its assets to Web. It sends `/v1/core/`, `/v1/telemetry/`,
`/v1/mcp`, `/status`, `/livez`, and `/readyz` to Core. Restart Web to serve a
new build. Core and Compose can keep running during that restart.

| Setting | Use |
|---|---|
| `SUPERAGENT_WEB_LISTEN` | Host and port of the static Web service. Set `SUPERAGENT_WEB_BIND` to change its loopback bind. |
| `SUPERAGENT_WEB_DIST` | Absolute path to the Web build when you run Caddy outside `superagent/web`. |
| `SUPERAGENT_PUBLIC_ORIGIN` | Gateway address with scheme, such as `https://superagent.example.com`. Use your TLS hostname for HTTPS. Set `SUPERAGENT_GATEWAY_BIND` to the intended interface. |
| `SUPERAGENT_WEB_UPSTREAM` | Host and port of the Web service. Defaults to `127.0.0.1:24371`. |
| `SUPERAGENT_CORE_UPSTREAM` | Host and port of the Core listener. Defaults to `127.0.0.1:24370`. |
| `SUPERAGENT_MASSD_ROUTE` | `embedded_massd` forwards `/massd/` to Core. `standalone_massd` removes the `/massd` prefix and forwards to `SUPERAGENT_MASSD_UPSTREAM`, which defaults to `127.0.0.1:24368`. |

The gateway checks the browser Origin against the incoming scheme and host. It
checks Fetch Metadata and normalizes `Host` before forwarding to Core. Keep
backend listeners private. Configure `externalURL` with the origin that role
Sessions use for MCP, including the gateway origin if they connect there.
Use a direct loopback Core endpoint for Operator commands.

If you used `observerAllowedHosts`, use `readAllowedHosts` for additional
direct Core read hosts. The gateway already sends the Core authority as `Host`.
Move `observerTrace` settings to `telemetryQuery`. Keep its credential file on
the Core host. See [Web telemetry](telemetry/README.md#enable-superagent-web-telemetry).

For local frontend development, run `npm ci` and `npm run dev` in
`superagent/web`. Open the printed Vite origin at `/superagent/`. Vite proxies
Core and embedded MASS paths to `SUPERAGENT_CORE_ORIGIN`, which defaults to
`http://127.0.0.1:24370`.

## Cut over the HTTP API

Use Core, Compose, the Superagent Web build, the gateway configuration,
Operator CLI, and Trigger binaries that use the same resource API. This
includes the separately built AntCode Trigger. Configure probes for `/livez`
and `/readyz` and use `/status` to inspect the stack.

1. Pause incoming Trigger traffic. Stop standalone Triggers and stop Compose
   or standalone Core. For standalone massd, stop it after Core if you are
   replacing its binary. Verify that the old processes have exited and
   released their data directories before starting replacements.
2. Update the binaries, Web build, gateway configuration, and caller
   configuration together. Use a schema 27 Core directory and schema 3 Trigger
   databases. Preserve the massd data directory, credential paths, and Session
   MCP origin. See the Core [data directory requirements](#reuse-an-existing-core-data-directory)
   and [Trigger cutover steps](triggers/README.md#replace-deployed-triggers).
   Run one owner per data directory and one active Outbox sender per account.
3. Start standalone massd first if you stopped it. Then start Compose or Core
   and check `/readyz` and `/status` at the printed Core origin. If you expose
   massd, check `/massd/readyz` at its public origin. Start Web and the gateway
   and open `/superagent/` at the gateway origin.
4. Start standalone Triggers and resume traffic. Verify that a Trigger Message
   can be submitted and its receipt read back by `sourceMessageId`. Inspect
   pending Outbox delivery before retrying an uncertain send. Keep the original
   native Message identity and the saved Outbox item ID across retries.

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
  See [operator reconciliation](API.md#local-operator-reconciliation).
- To trace a record across Core and MASS, see [local telemetry](telemetry/README.md).

## Checks

Run `make test-superagent` for the offline Core and Trigger process tests. They
use synthetic peers and do not call live models or channel accounts. Run
`make check-superagent` for the full local checks, including Superagent Web browser tests with Caddy and Chrome.
See the [Makefile](../Makefile).
