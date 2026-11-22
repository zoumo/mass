# Superagent

Superagent Core routes Messages to Topics and coordinates Issue Sessions through
MASS. The DWS and GitHub Triggers collect Messages. The Observer reads Core state.

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

3. Read `endpoint` and `observerEndpoint` from the ready record. Check Core intake
   from another terminal. Pass the printed numeric origin if you changed `listen`.

   ```sh
   bin/superagent observe health --endpoint http://127.0.0.1:24370
   ```

   `ingress: ready` confirms Core intake. Query the local Trigger status for
   channel readiness. For more read commands, run `bin/superagent observe --help`.

Omit `sessions` to collect Messages without starting Agents. To enable execution,
configure role Sessions and at least one Issue template in the example YAML.
Use a fixed Core listen port or stable `externalURL` for the role MCP endpoint.
Use the same host and OS account for Core and standalone `massd` so native
Sessions can read their credential files. See the [Session preparation contract](API.md#session-configuration-and-preparation).

Observer reads need no login. Keep them within the network boundary for trusted
readers. When `listen` uses a wildcard, use a concrete numeric address that
reaches Core. See the [Observer Host and Origin rules](API.md#read-only-observation).

### Reuse an existing Core data directory

Stop Core before backing up its data. Schema 24 upgrades to 25 in a transaction
that removes MCP tool-call history and mutation receipts. The upgrade preserves
business records, internal events, and saved trace associations. Use an empty
`dataDir` for schema 23 or earlier, JSON v4 state, or a migration from Core's
stdio role MCP. Read retained state with its original version. If both Core
directories share `massd`, use distinct `sessions.namespace` values.

Core checks source and account configuration against admitted Messages at
startup. Preserve both when replacing a Trigger. `storageMaxPages` limits the
main SQLite database, including initialization. Leave room for WAL and other
filesystem overhead. When the budget runs out, ingestion returns `unavailable`
without an acceptance receipt.

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

4. Read `observerEndpoint` and `triggerEndpoints` from the ready record. Query
   each Trigger's status. Check DWS `consumerReady` and the GitHub stream's
   `lastSuccessAt`. If you enabled embedded MASS exposure, use `massdEndpoint`
   with its dedicated token.

Press Ctrl-C to stop the Triggers before Core and embedded MASS. After startup,
Compose logs a Trigger exit and keeps the other processes running. If Core
exits unexpectedly, Compose stops the Triggers and returns an error.

## Inspect delivery and recover input

- Use Observer or `superagent observe` to read Message admission, routing,
  Session input, Report acceptance, and Outbox delivery. Each records a
  different outcome. See [the Core API](API.md).
- Use `inspect-session-binding` before `recover-input` to reconcile an uncertain
  dispatch, settle a retired binding, or replace a Session.
  See [operator reconciliation](API.md#local-operator-reconciliation).
- To trace a record across Core and MASS, see [local telemetry](telemetry/README.md).

## Checks

Run `make test-superagent` for the offline Core and Trigger process tests. They
use synthetic peers and do not call live models or channel accounts. Run
`make check-superagent` for the full local checks, including Observer tests.
See the [Makefile](../Makefile).
