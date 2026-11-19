# superagent

`superagent` is the new business entrypoint above massd. This independent Go
module owns a single local SQLite database. The delivered slice accepts native
messages, routes them through one global Router and independent Topics, prepares
fixed massd Sessions, batches durable inputs and exposes read-only observation.
Topics can schedule Issue Workers, receive Reports, accept, cancel and reopen work.
Topics can schedule one-time followups via Timer tools. Router and Topic can
reply to a source Message through a durable Outbox. Topic and current Worker
roles can explicitly register PRs and subscribe their Topic to the resource Thread.
Execution is opt-in through configuration; intake-only operation starts no Agent.

From the repository root:

```sh
make build-superagent
bin/superagent serve --config /absolute/run/core.yaml
bin/superagent observe messages --endpoint http://127.0.0.1:24370
bin/superagent observe threads --endpoint http://127.0.0.1:24370
bin/superagent observe routing-deliveries --endpoint http://127.0.0.1:24370
bin/superagent observe internal-deliveries --endpoint http://127.0.0.1:24370
bin/superagent observe issues --topic-id TOPIC_ID
bin/superagent observe reports --issue-id ISSUE_ID
bin/superagent observe timers --topic-id TOPIC_ID
bin/superagent observe pull-requests --issue-id ISSUE_ID
bin/superagent observe outbox --message-id MESSAGE_ID
```

The default data directory is `./superagent-core-data`, relative to the YAML
file. Use an empty directory for your first schema 20 startup. Startup rejects
earlier SQLite schemas (including 19) and JSON v4 state directories while preserving the original files.
Set `dataDir` to another empty directory if the configured path contains
earlier data. Use the original executable to inspect retained MVP history.

Configuration uses [Viper](https://github.com/spf13/viper), CLI commands use
[Cobra](https://github.com/spf13/cobra), and SQLite uses the pure-Go
[modernc driver](https://pkg.go.dev/modernc.org/sqlite). Dependencies are pinned
in go.mod/go.sum. Core consumes the public mass-go client/resource/runtime packages through the
monorepo module path; no private mass-go package or direct SDK operation is used.
The transitive SDK remains pinned to v0.0.0-20261117073811-4890981672bf.

## Configuration

Copy `config.example.yaml` into an isolated run directory and provide your own
private token file (at least 32 non-whitespace bytes; mode 0600). Tokens and
DWS credentials are never embedded in startup output or browser configuration.
Configuration paths resolve relative to the YAML file.

`listen` defaults to numeric loopback `127.0.0.1:24370`; an explicit numeric
address can bind another interface. Intake-only Core accepts port `0` for
an ephemeral listener. Execution requires a fixed port.
The startup JSON prints the actual listener, which may be a wildcard address;
clients must use a concrete numeric interface address and the matching Host.
All ingress requires a bound source token. Observer is a read-only trusted
network surface with Host/Origin checks, not multi-user authentication; expose
it only to trusted readers when explicitly binding a wider interface.

Open `/observer/` to follow a Message through its Router decisions, target Topics
and the Issues that cite it. The four views share read-only links that preserve
record IDs and filters. Diagnostics provides Thread, Timer, PR, Outbox and
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

Optional `execution` config sets the massd endpoint, Router workspace,
namespace, fixed Router and Topic profiles, and the ordered Worker template
list. Each profile has a `runtimeRef`, `model` and optional `effort`.
Set those values to the literal names supported by your massd and its drivers.
Topic uses `execution_template_query` to choose a Runtime and model when
creating an Issue. Core copies the template's effort into the Issue and Worker
binding. A later `issue_start` can use the saved profile or select a new
pair for an Issue without a current Execution. Changing the Router profile
requires a new Core data directory. See the [execution configuration](API.md#execution-configuration-and-fixed-session-preparation).
Core persists role identity and configuration before creation, reconciles lost
create responses by querying the same Session, and retains failed preparation
on the same Topic. Keep the configured listener address stable across restarts so existing MCP
processes can reconnect.
The private capabilities directory is never returned by Observer. Core batches
each Session's input, delivers context through Inject and uses StartOrSteer
when a batch contains an action. Input delivery leaves Topic business state
unchanged. See [Issue scheduling, reports and acceptance](API.md#issue-execution-report-and-acceptance)
for the Topic and Worker role tools.
See [Topic Timers](API.md#topic-timers) for one-shot scheduling, lifecycle fences
and followup delivery through normal Message intake.
See [PR registration](API.md#pull-request-registration-and-subscriptions) for
role scope, the source Message contract and URL rules.
Local operators can inspect a role with `superagent inspect-binding BINDING_ID`
and use `recover-input` to reconcile input, settle a retired binding or replace
a Session after verifying release.
See [the input recovery contract](API.md#local-operator-reconciliation).

Multiple collectors for one account share the source and use distinct
instance-bound tokens. Startup checks configured sources and accounts
against admitted Messages. Channel credentials/profile resolution belong to external
adapters. This module never invokes DWS.

Optional `storageMaxPages` bounds main database growth in 4096-byte SQLite pages
(zero is unlimited); WAL and filesystem overhead are additional. An exhausted
budget returns unavailable without an accepted receipt. Schema initialization
also has to fit. It is an operational storage budget, not a message limit.

## Ingress and queries

See [API.md](API.md) for exact payloads, errors, retry and cursor semantics.
Core commits the Message and its applicable internal delivery before returning
an acceptance receipt. Retry a lost response with the same native message
identity and business content. Session acceptance, business completion and
channel delivery have separate states.
When execution is configured, accepted input is automatically dispatched through
massd after fixed-identity Session preparation. No DWS call is made by Core.

Open the printed `/observer/` URL and select Topic for its recent deliveries,
returns and Issue activity. The Issue view shows goals, the saved execution profile, Worker executions,
Reports and explicit business acceptance. Session binding details show the
runtime, model and configured effort for the selected binding generation. Message shows the channel original,
Thread and routing or echo history. Diagnostics opens collection lists and
record details, including full message bodies, Session preparation, live
Session previews and channel receipts. Topic and Issue link to the PR URLs
they follow and their subscriptions.
The PR details link to Thread history. A newly registered Thread shows when it
has received no source input.
The diagnostics list Topics, subscriptions, routing history, Session bindings
and internal delivery acceptance. Issue
drawers link to their Execution history, startup evidence and Report timeline.
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

Root `make check` covers mass-go plus this independent module and Observer:
Go lint, race tests (including race-built real subprocesses), vet, build, format,
React/TypeScript typecheck and Vite build and Chrome browser tests. `make test`, `make build`,
`make lint` and `make fmt` also include superagent explicitly.

```sh
make check-superagent
make observer-check
```

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
Role stdio subprocesses verify startup identity, source scope and work commands.
The Issue lifecycle test uses real Core and massd with a controlled native CLI
peer for execution, delivery, final output and interrupt observation. Unknown schema tests
synthesize only SQLite version metadata, never business input rows.
Browser fixtures seed through authenticated public ingress and use actual Core
processes and embedded production assets. Runtime files and browser build output
are ignored. Tests read back process/listener release before cleanup.

The SQLite owner lock uses Unix flock. This MVP build supports macOS/Linux;
no Windows executable or multi-node database ownership is claimed.
