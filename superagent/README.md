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
bin/superagent observe events --endpoint http://127.0.0.1:24370
bin/superagent observe channel-health --endpoint http://127.0.0.1:24370
bin/superagent observe deliveries --endpoint http://127.0.0.1:24370
bin/superagent observe issues --topic-id TOPIC_ID
bin/superagent observe reports --issue-id ISSUE_ID
bin/superagent observe timers --topic-id TOPIC_ID
bin/superagent observe pull-requests --issue-id ISSUE_ID
bin/superagent observe outbox --message-id MESSAGE_ID
```

Use a new empty data directory or an existing superagent schema 2–12 directory.
Superagent upgrades schema 2–11 transactionally. Old mass-sa JSON v4
directories, foreign files, invalid databases and unsupported schema versions
(including schema 1) are refused. The old mass-sa executable is retired. Its source and Observer
fixtures remain available as reusable historical assets. Existing massd data
and commands retain their own directories and lifecycle.

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
address can bind another interface. `0` as the port requests an ephemeral port.
The startup JSON prints the actual listener, which may be a wildcard address;
clients must use a concrete numeric interface address and the matching Host.
All ingress requires a bound source token. Observer is a read-only trusted
network surface with Host/Origin checks, not multi-user authentication; expose
it only to trusted readers when explicitly binding a wider interface.

For heartbeat and Outbox, set `sources[].instanceId` to the Trigger's stable instance ID.
See [DWS outbound echoes](API.md#dws-outbound-echoes) for the read-only message
projection and the local `release-echo` command.
Configure a separate token for each instance of a source. Channel health shows
the latest report, its backlog and its expiration. See [the heartbeat contract](API.md#trigger-heartbeat).
The [Outbox contract](API.md#trigger-outbox) defines consumer claims, task
recovery and result writeback. All send history lives in Core.

Optional `execution` config selects massdEndpoint, fixed runtimeRef,
routerWorkspace and namespace. See [API.md](API.md#execution-configuration-and-fixed-session-preparation).
Core persists role identity and configuration before creation, reconciles lost
create responses by querying the same Session, and retains failed preparation
on the same Topic. With execution enabled, the first listener address is fixed
in the store so existing MCP processes retain a stable endpoint across restart.
The private capabilities directory is never returned by Observer. Core batches
each Session's input, delivers context through Inject and uses StartOrSteer
when a batch contains an action. Input delivery leaves Topic business state
unchanged. See [Issue scheduling, reports and acceptance](API.md#issue-execution-report-and-acceptance)
for the Topic and Worker role tools.
See [Topic Timers](API.md#topic-timers) for one-shot scheduling, lifecycle fences
and followup delivery.
See [PR registration](API.md#pull-request-registration-and-subscriptions) for
role scope, the source Message contract and URL rules.
Local operators can inspect a role with `superagent inspect-binding BINDING_ID`
and use `recover-input` to reconcile input, settle a retired binding or replace
a Session after verifying release.
See [the input recovery contract](API.md#local-operator-reconciliation).

A source/account binding is permanent in its data directory. Multiple collectors
for one account share the source and use distinct instance-bound tokens.
Rebinding an existing source to another account on
restart fails. Channel credentials/profile resolution belong to external
adapters. This module never invokes DWS.

Optional `storageMaxPages` bounds main database growth in 4096-byte SQLite pages
(zero is unlimited); WAL and filesystem overhead are additional. An exhausted
budget returns unavailable without an accepted receipt. Schema initialization
also has to fit. It is an operational storage budget, not a message limit.

## Ingress and queries

See [API.md](API.md) for exact payloads, errors, retry and cursor semantics.
Message IDs and operation receipts are durable before success is returned.
Success means local acceptance, not model execution, consumption or completion.
A lost response is recovered by retrying the same operationId and parameters.
When execution is configured, accepted input is automatically dispatched through
massd after fixed-identity Session preparation. No DWS call is made by Core.

Open the printed `/observer/` URL for the Topic workspace. Its activity
combines routing decisions, Topic reception and return, and channel reply
states. The Issue view shows requirements, Worker execution, Reports and
explicit business acceptance. Trigger messages display the channel original,
Thread and routing or echo history. Diagnostics opens the collection
lists and detail drawers. The overview shows channel connection and admission, Router
preparation, business state, and input and reply evidence. Message drawers
show the full body and provide links to Thread, routing history and replies.
Topic and Issue drawers link to PR associations, their subscription and Thread
history. A newly registered Thread shows when it has received no source input.
Topics, route decisions, subscriptions, reception/return history, Session
bindings and actual input receipts are visible in separate collections. Issue
drawers link to their Execution history, startup evidence and Report timeline.
Message and Thread drawers link to reply history. Outbox details show the
target, author, attempts, channel task and observed native send results.
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
Vue typecheck/build and Chrome browser tests. `make test`, `make build`,
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
Outbox process tests cover lease expiry with a test-only clock, independent
consumer restart, task recovery and result receipts lost at commit boundaries.
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
