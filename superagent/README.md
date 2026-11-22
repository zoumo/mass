# superagent

`superagent` includes Superagent Core and the GitHub and DWS Triggers. Core
accepts native messages, routes them through one global Router and independent
Topics, prepares fixed massd Sessions, batches durable inputs, and exposes
read-only observation. Core and each Trigger own separate SQLite databases.
Topics can schedule Issue Sessions, receive Reports, accept, cancel and reopen work.
Topics can schedule one-time followups via Timer tools. Router and Topic can
reply to a source Message through a durable Outbox. Topic and current Issue SessionBindings
roles can explicitly register PRs and subscribe their Topic to the resource Thread.
Execution is opt-in through configuration; intake-only operation starts no Agent.

From the repository root:

```sh
make build-superagent
bin/superagent serve --config /absolute/run/core.yaml
bin/superagent observe messages --endpoint http://127.0.0.1:24370
bin/superagent observe threads --endpoint http://127.0.0.1:24370
bin/superagent observe routing-events --endpoint http://127.0.0.1:24370
bin/superagent observe internal-events --endpoint http://127.0.0.1:24370
bin/superagent observe issues --topic-id TOPIC_ID
bin/superagent observe reports --issue-id ISSUE_ID
bin/superagent observe timers --topic-id TOPIC_ID
bin/superagent observe pull-requests --issue-id ISSUE_ID
bin/superagent observe outbox --message-id MESSAGE_ID
```

The default data directory is `./superagent-core-data`, relative to the YAML
file. Core upgrades a schema 24 directory to schema 25 on startup. The
transaction removes MCP tool-call history and mutation receipts and preserves
business records, internal events and saved Trace associations. Use an empty
`dataDir` for a new installation or when moving from schema 23 or earlier or
JSON v4 state. Use the original executable to inspect retained MVP history.

Configuration uses [Viper](https://github.com/spf13/viper), CLI commands use
[Cobra](https://github.com/spf13/cobra), and SQLite uses the pure-Go
[modernc driver](https://pkg.go.dev/modernc.org/sqlite). Dependencies are pinned
in the root [`go.mod`](../go.mod) and `go.sum`. The [command package](../cmd/superagent)
builds Core and both [Triggers](triggers/README.md). Core uses the public MASS
Go packages and calls [`massd.Start`](../massd/instance.go) for in-process assembly.

## Configuration

See [Run Superagent telemetry locally](telemetry/README.md) for the optional
OpenTelemetry exporters and the local OpenObserve stack.

For standalone Core, copy `config.example.yaml` into an isolated run directory.
Provide a private source token file with at least 32 non-whitespace bytes and
file mode 0600. Core resolves relative paths from the YAML file's directory.

`listen` defaults to numeric loopback `127.0.0.1:24370`; an explicit numeric
address can bind another interface. Intake-only Core accepts port `0` for
an ephemeral listener. Execution requires a fixed port.
The startup JSON prints the actual listener, which may be a wildcard address;
clients must use a concrete numeric interface address and the matching Host.
All ingress requires a bound source token. Observer is a read-only trusted
network surface with Host/Origin checks, not multi-user authentication; expose
it only to trusted readers when explicitly binding a wider interface.

Open `/observer/` for Topic conversations. The sidebar lists Topics by their
latest conversation message. Select a Topic to follow Router routing, Core
Thread delivery, Issue creation and dispatch, and Issue Reports. The Message,
Router, Topic and Issue views provide read-only links to the related records.
Diagnostics provides Thread, Timer, PR, Outbox and
Session records. Existing `?view=messages|topics|issues` links open the
corresponding view. Add `mode=records` to open its diagnostic collection.

For Outbox, set `sources[].instanceId` to the Trigger's stable instance ID.
See [DWS outbound echoes](API.md#dws-outbound-echoes) for the read-only message
projection and the local `release-echo` command.
Configure a separate token for each instance of a source. Use the Trigger’s
local status command and logs for consumer health and backlog.
Run one active Outbox consumer per account. When an account has multiple
instance-bound credentials, select the consumer with `outboxConsumer: true`.
The [Outbox contract](API.md#trigger-outbox) covers task recovery and result
writeback.

`massd` selects an embedded or external MASS instance. `sessions` configures the Router
workspace, namespace, fixed Router and Topic profiles, and ordered Issue
templates. Each profile has a `runtimeRef`, `model` and optional `effort`.
Set those values to the literal names supported by your massd and its drivers.
Topic uses `get_topic` with `include: ["executionTemplates"]` to choose a Runtime and model when
creating an Issue. Core copies the template's effort into the Issue and its
SessionBinding. A later `start_issue` can use the saved profile or select a new
pair for an Issue without a current SessionBinding. Changing the Router profile
requires a new Core data directory. See the [Session configuration](API.md#session-configuration-and-preparation).
Core persists role identity and configuration before creation, reconciles lost
create responses by querying the same Session, and retains failed preparation
on the same Topic. Keep the configured listener address stable across restarts
so existing Sessions can reconnect to Core over HTTP.
Observer responses omit the contents of the private MCP credentials directory. Core batches
each Session's input, delivers context through Inject and uses StartOrSteer
when a batch contains an action. Input delivery leaves Topic business state
unchanged. See [Issue scheduling, reports and acceptance](API.md#issue-sessionbinding-report-and-acceptance)
for the Topic and Issue role tools.
See [Topic Timers](API.md#topic-timers) for one-shot scheduling, lifecycle fences
and followup delivery through normal Message intake.
See [PR registration](API.md#pull-request-registration-and-subscriptions) for
role scope, the source Message contract and URL rules.
Local operators can inspect a role with `superagent inspect-session-binding SESSION_BINDING_ID`
and use `recover-input` to reconcile input, settle a retired binding or replace
a Session after verifying release.
See [the input recovery contract](API.md#local-operator-reconciliation).

Multiple collectors for one account share the source and use distinct
instance-bound tokens. Startup checks configured sources and accounts
against admitted Messages. Triggers resolve their upstream credentials and
profiles. The DWS Trigger makes DWS calls.

Optional `storageMaxPages` bounds main database growth in 4096-byte SQLite pages
(zero is unlimited); WAL and filesystem overhead are additional. An exhausted
budget returns unavailable without an accepted receipt. Schema initialization
also has to fit. It is an operational storage budget, not a message limit.

## Running MASS inside Core

Use `config.example.yaml` to configure the source and role Sessions. Replace
`massd.endpoint` and its optional `massd.tokenFile` with an `embedded` block.
Use separate sibling directories for Core and MASS.

```yaml
dataDir: ./superagent
massd:
  embedded:
    dataDir: ./massd
    # metaStore: { backend: sqlite }
    # sessionIdleTimeout: 10m
    # writeTimeout: 10s
```

Set the role profiles and Issue templates to available Runtime names. MASS
registers missing `codex` and `claude` Runtimes at startup and preserves
existing entries. `sessionIdleTimeout: 0s` disables idle closure.

```sh
make build-superagent
bin/superagent serve --config "$HOME/.mass/core.yaml"
```

Core owns the only HTTP listener. It prepares MASS before printing the
ready record. Keep the Core MCP address and both data directories stable
when restarting.

To switch between embedded and standalone MASS, stop Core and its MASS
instance and wait for a successful exit. Keep both data directories, MASS
recovery files, and Core's MCP credential paths and address. Start the new
owner of the same MASS directory. Set `massd.embedded.dataDir` for embedded
mode or set `massd.endpoint` and start standalone massd for external mode.
Use the same metadata backend as before. After Core restarts, check that
existing bindings point to the same Session IDs and UIDs and that Session
specs match. If Core reports an unknown native result, follow the
[recovery contract](API.md#local-operator-reconciliation) before resubmitting input.

### Accessing embedded MASS with massctl

To enable `/massd/` on the Core HTTP listener, add this block under
`massd.embedded` and keep `listen` on a loopback IP address.

```yaml
expose:
  tokenFile: ./embedded-massd.token
  # allowedOrigins: [https://ops.example.com]
```

Create a regular token file with mode `0600`, readable by the Core process.
Generate a dedicated token of at least 32 bytes without whitespace or control
characters. Keep it distinct from Core Source, Operator, and role MCP
credentials. The `cap_` prefix is reserved for role MCP credentials. Relative
token paths are resolved beside the Core config. This bearer grants full MASS
Runtime and Session access, including to Sessions managed by Core.

```sh
export MASS_TOKEN="$(cat "$HOME/.mass/embedded-massd.token")"
bin/massctl --url ws://127.0.0.1:24370/massd session list
```

`massctl` connects to `/massd/jsonrpc`. `/massd/livez` and `/massd/readyz`
provide unauthenticated probes through the same Core listener. To allow
non-local access, terminate TLS at a trusted reverse proxy. Limit the proxy
route to `/massd/` and forward it to Core's loopback listener. Preserve the
`/massd/` path and `Authorization` header. Forward the WebSocket Upgrade and
keep the connection open for bidirectional traffic. Set the upstream `Host`
to Core's listening address, including its port.
Clients in this deployment use `wss://<proxy-host>/massd`. Requests carrying
`Origin` must match a configured `allowedOrigins` value exactly. Native
clients such as `massctl` can omit it.

On shutdown, Core closes new MASS connection admission and stops its own
scheduling. MASS settles accepted work, attempts to send final events to
healthy connections, then closes those connections and releases its data
directory.

## Run the stack with Compose

Copy the [Compose example](compose.example.yaml) to `~/.mass/stack.yaml`. Keep
`core` and include `triggers.github` or `triggers.dws` for each collector you
want to run. Compose resolves paths relative to `stack.yaml`.

Core starts embedded MASS. The default data directory is `./massd-data`.
Set `core.massd.embedded.dataDir` to use a different directory. Use separate
paths for Core, MASS, and the Trigger databases. To have Core run Agents, add
`core.sessions` from the [Core configuration example](config.example.yaml).
Keep the Core listener on a fixed port for role MCP, or set a stable
`core.externalURL` when you use a reverse proxy.

Match each Trigger's source, account, and instance to a `core.sources` entry.
Create a separate private token file for each entry. Set the environment
variable named in that Trigger's `coreTokenEnv` to the same token. Supply
the GitHub credential through `githubTokenEnv` and configure the DWS profile
through the DWS CLI. Use a separate `feedback.statePath` if you enable DWS
feedback. See the [Trigger configuration and cutover guide](triggers/README.md).

Run these commands from the repository root.

```sh
make build-superagent
bin/superagent compose --config "$HOME/.mass/stack.yaml"
```

Compose prints one JSON record when Core and the configured Trigger status
listeners respond. The record includes `endpoint` and `observerEndpoint`.
If you set `core.massd.embedded.expose`, `massdEndpoint` gives the authenticated
WebSocket URL on Core. See [Accessing embedded MASS with massctl](#accessing-embedded-mass-with-massctl).

Use `observerEndpoint` to inspect Core. Use each origin in `triggerEndpoints`
with the corresponding `triggers ... status --endpoint` command. Check
`consumerReady` for DWS subscriptions and `lastSuccessAt` for each GitHub
repository stream.

Press Ctrl-C to stop the stack. Compose stops the Triggers first. Core then
stops scheduling and shuts down MASS. If a Trigger exits after startup,
Compose logs the exit and continues running the rest of the stack. If Core
exits unexpectedly, Compose stops the Triggers and returns an error.

To run Core independently, use `superagent serve --config core.yaml`.
For a separate MASS instance, configure `massd.endpoint` in `core.yaml`.
Set `coreEndpoint` in each standalone Trigger configuration. See the
[standalone Trigger commands](triggers/README.md).

## Ingress and queries

See [API.md](API.md) for exact payloads, errors, retry and cursor semantics.
Core commits the Message and its applicable internal event before returning
an acceptance receipt. Retry a lost response with the same native message
identity and business content. Session acceptance, business completion and
channel delivery have separate states.
When execution is configured, accepted input is automatically dispatched through
massd after fixed-identity Session preparation. No DWS call is made by Core.

Configure OTel export and `observerTrace` to query process and MCP metrics in
Observer. Metrics reads the time series from OpenObserve and supports filters
and grouping by metric dimension. See the [telemetry setup](telemetry/README.md).

Open the printed `/observer/` URL and select Topic for its recent events,
returns and Issue activity. The Issue view shows the goal, configured Runtime
and model, current Session, Reports and recorded acceptance. SessionBinding
details show its Runtime, model, configured effort and input status. Message shows the channel original,
Thread and routing or echo history. Diagnostics opens collection lists and
record details, including full message bodies, Session preparation, live
Session previews and channel receipts. Topic and Issue link to the PR URLs
they follow and their subscriptions.
The PR details link to Thread history. A newly registered Thread shows when it
has received no source input.
Diagnostics lists Topics, subscriptions, routing history, SessionBindings and
internal event acceptance. Issue details link to the SessionBindings and the
Report timeline.
Message and Thread drawers link to reply history. Outbox details show the
target, blocker reference, status, channel task and native send results.
Topic and Issue details display the recorded acceptance. Session preparation
links to stored massd history and a live preview. A disconnect is visible and
the next connection rereads stored history. Browser requests use GET for
queries and live observation. The server also accepts HEAD. Bodies render
as plain text. Known configured source tokens are redacted in queries,
including if they occur in untrusted input; arbitrary text is not guaranteed
secret-free. Configured massd tokens and role credentials are redacted too. SQLite remains
the authoritative original record.

Lists poll every five seconds while visible. Each refresh starts from a new
SQLite high-water mark and uses its cursors to reload the displayed pages.
Failed reads retain the last successful snapshot with a stale label. Browser
reconnection refreshes the current filter, displayed pages and selected
record. Navigation retains the selected record and filter in the URL.

## Checks

Run these checks from the repository root. `make check` covers MASS,
Superagent, and Observer. `make check-superagent` runs Go lint, race tests, vet,
build, and format checks for Superagent. It also typechecks and builds Observer
and runs its Chrome browser tests.

```sh
make check-superagent
make test-superagent
make observer-check
```

`make test-superagent` runs the Compose process tests and the Trigger suites.
It builds the normal and `coretest` Superagent executables with `-race` from the
same checkout. The Trigger suites run those binaries with the local DWS and
GitHub fixtures.

Process tests use temporary data/config and synthetic private source tokens;
child Core processes receive empty HOME/PATH and never invoke models or DWS.
A `coretest` build-only gate pauses immediately before COMMIT or after COMMIT
before response, allowing SIGKILL and public restart readback to verify ingress,
routing and Report-notification atomicity.
Outbox process tests cover account isolation, durable task and result writes,
retry timing, quoted blocker resolution and continuation across Core restart.
Normal builds contain no crash-gate environment behavior. Routing tests use
real Core subprocesses, the public MASS client/WebSocket/Service and a controlled
Driver. A WebSocket proxy discards actual Session-create responses after Service
commit, then public queries verify recovery and conflicting configuration refusal.
Role HTTP MCP tests verify startup identity, source scope and work commands.
The Issue lifecycle test uses real Core and massd with a controlled native CLI
peer for execution, delivery, final output and interrupt observation. Unknown schema tests
synthesize only SQLite version metadata, never business input rows.
Browser fixtures seed through authenticated public ingress and use actual Core
processes and embedded production assets. Runtime files and browser build output
are ignored. Tests read back process/listener release before cleanup.

The SQLite owner lock uses Unix flock. This MVP build supports macOS/Linux;
no Windows executable or multi-node database ownership is claimed.
