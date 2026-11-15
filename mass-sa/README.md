# mass-sa

An independent Go module coordinating project work through public MASS
WebSocket JSON-RPC and the authenticated Multica CLI. It imports no mass-go
packages. This demo uses a single process and an atomic local JSON state file.

## Build and check

```sh
GOWORK=off go build -o /absolute/run/bin/mass-sa ./cmd/mass-sa
GOWORK=off go test -race ./...
GOWORK=off go vet ./...
GOWORK=off go build ./...
gofmt -l .
```

Tests use real mass-sa serve and stdio MCP subprocesses with fake MASS
WebSocket and Multica CLI peers. `TestTicketStoryPublicLoop` connects ingress,
short reply, root/child/grandchild work, parent close/recovery, explicit acceptance,
output recovery and cancellation. Fault, permission and unknown-result cases
use the existing public harnesses in adjacent tests. These are offline evidence,
not proof of real model or Multica service behavior.

## Model

- **Trigger** records an external event and returns a durable receipt before
  model execution. **Thread** identifies its source discussion; its association
  with Tickets survives execution changes.
- **Ticket** owns the objective, acceptance, business state, Issue and stable
  root namespace. It can wait without a Session. A root has a root Multica
  Issue; optional descendants have Issues under their immediate parent's Issue.
- **Assignment** grants one current executor control of a Ticket. Old
  Assignments and reports remain auditable; old capabilities are revoked.
- **Session** owns execution history. Multiple rounds share it. Close releases
  resources without cancelling the Ticket; explicit recovery normally resumes
  the same identity with a fresh Assignment and rotated MCP capability.
- **Output** is a durable business fact or explicit Router reply. Recording,
  consumer observation and actual channel delivery are separate facts.

Idle, initialization, ordinary Agent final text, a report or child completion
does not accept the overall work. Router `finish_ticket` explicitly confirms
`blocked`, `waiting_user`, `completed`, `failed`, `cancelled` or `reopened`.
Completion requires a confirmed report from the current Assignment. Ticket
`businessState` stores the accepted business state; terminal states are completed,
failed and cancelled. Multica `status` records its separate platform status.

State format is version 3. Use a new state directory; older formats are rejected
without rewriting data. Earlier run evidence remains separate from this version.

## Configuration and start

[config.example.yaml](config.example.yaml) contains fictional resource IDs,
paths and execution names. Keep actual configuration outside the checkout,
for example at `~/.mass/mass-sa-demo.yaml`. The application only reads this file;
state and capability files use their configured paths separately. Relative paths
resolve against the configuration file, so the example state path stays under
`~/.mass/mass-sa-demo/` when copied there.

For first setup, copy without overwriting an existing local configuration:

```sh
mkdir -p "$HOME/.mass"
cp -n config.example.yaml "$HOME/.mass/mass-sa-demo.yaml"
chmod 600 "$HOME/.mass/mass-sa-demo.yaml"
```

Edit the local copy with your cwd, Runtime, models and Multica targets, then make
it owner-readable only:

```sh
chmod 400 "$HOME/.mass/mass-sa-demo.yaml"
mass-sa serve --config "$HOME/.mass/mass-sa-demo.yaml"
```

Use absolute isolated working/data directories and unused numeric loopback ports
for acceptance. Tokens are referenced through tokenFile/tokenEnv, never embedded
in configuration or evidence.

Configuration rejects unknown fields, duplicate keys, wrong types and multiple
YAML documents. Each Project selects cwd, Runtime, literal Router/Worker model
and effort, explicit Multica workspace/project UUIDs and inbox batching bounds.
Additional Worker Runtime choices must appear in `worker.runtimeRefs`.

```sh
mass-sa serve --config /absolute/run/config.yaml
mass-sa trigger accept --config /absolute/run/config.yaml --project mass \
  --source demo/account --source-event-id request-1 --thread discussion-1 \
  --summary 'Deliver reviewed evidence' --content '{"text":"normalized request"}'
mass-sa trigger get '<trigger-id>' --config /absolute/run/config.yaml --project mass
mass-sa ticket get '<ticket-id>' --config /absolute/run/config.yaml --project mass
mass-sa ticket inspect '<root-ticket-id>' --config /absolute/run/config.yaml --project mass
```

`ticket create --objective ... --acceptance ...` is also available for operator
creation without a Trigger. It creates and reads back one Issue, with no
initial Assignment or Session. `ticket reconcile <id>` performs read-only
confirmation of a pending business transition; it never repeats its status write.

Management API uses the configured operator credential at
`/v1/management`: POST `/triggers`, GET `/triggers/<id>?projectId=mass`,
POST `/tickets`, GET `/tickets/<id>?projectId=mass`, POST
`/tickets/<id>/reconcile`, GET `/outputs` and POST `/outputs/ack`.
GET `/tickets/<root-id>/inspect` shows root and descendant Tickets, workers,
Assignments, routes, inputs, batches and reconciliation needs. The CLI exposes
the same read model through `ticket inspect`.
Trigger ingress requires projectId, source, sourceEventId, threadId, kind,
summary and JSON content. Same identity/content returns the original receipt;
changed content conflicts. Receipt means persistence, not execution or completion.

Operator `event --root-ticket <id>` (POST `/events`) injects an explicit scoped
inbox input; `route bind|list|revoke --root-ticket <id>` and `worker list
--root-ticket <id>` inspect/control that scope. These require existing work.
External adapters use Trigger ingress for source deduplication and ownership.

## Router and Worker MCP

Serve configures private stdio MCP processes for Sessions. The entry point is
`mass-sa mcp --project mass --role router|worker --capability /private/file`.
Keep capability contents private; the file binds endpoint, role and identity.
Every tool call rechecks authorization.

Router tools include `decide_trigger`, `reply_trigger`, `associate_thread`,
`subscribe_output`, `create_ticket`, `create_worker`, `get_ticket_context`,
worker inspection/messaging, `stop_worker` and `finish_ticket`.

1. Decide a Trigger with `ignore`, `associate` (ticketId) or `create`
   (title/objective/acceptance). To authorize optional delegation, use
   `create_ticket(authorization=...)`, then associate the Trigger.
2. Send an early `reply_trigger` with stable requestId. `accepted` needs a
   confirmed associated Ticket and generates its text; omit text. It does not
   claim queued work is executing. `clarification` and `no_execution` need text.
3. `create_worker(ticketId, name)` derives scope and first creates its Session.
   Router inspection/messaging/stop tools take `rootTicketId`; Worker tools derive
   that scope from their capability and accept no scope override.
4. Send later work to the current worker. Ordinary associated Trigger inputs
   route directly; `replyRequested: true` asks the same Project Router to
   communicate. Multiple Ticket associations make a Thread ambiguous and
   route to the Router without broadcast.
5. Review confirmed report/evidence via `get_ticket_context`, then explicitly
   accept or disposition the work. `summary` replies require ticketId and a
   confirmed factId from that context.

Calling `create_worker` on an active Ticket/name continues its Assignment.
After `stop_worker`, creation checks identity, cwd, execution configuration,
history and pending input, then resumes the original Session with a new
Assignment. `action: retry` explicitly renews an attempt. `action: replace`
with a new name changes Session/executor/configuration after the old Session
is confirmed idle/offline and closed. Runtime/model/effort reach MASS literally
and its returned configuration must match. Busy or unknown execution fences
retry/replacement. MCP capability path is retained and its token rotates;
a process that cached the old token loses authority.

A successful, identity-checked MASS resume can return `unknown` before its
first execution-state observation. For a new current Assignment whose old
execution was settled and closed, mass-sa records permission for one fresh
input batch. It preserves the public unknown observation and consumes this
permission durably before attempting the prompt. It never grants it from a
query alone, an unconfirmed resume, a stale Assignment or an unknown prompt
outcome. After that first attempt, ordinary unknown observations keep later
inputs queued. MASS independently rejects an unknown Session with no live
controller; this permission does not bypass its input gate.

## Optional delegation and lifecycle

Workers can work directly or use their Agent's native subagent tools. Native
subagents are not mapped to platform Tickets, Issues or outputs. Static/offline
interface checks do not establish actual native subagent execution.

Platform delegation requires immutable root `authorization`, for example:

```json
{
  "resources": ["acceptance:arithmetic"],
  "tools": ["get_ticket_context", "create_child_ticket", "dispatch_child_ticket", "send_to_worker"],
  "runtimeRefs": ["isolated-runtime"],
  "models": ["example-model"],
  "efforts": ["high"],
  "maxDepth": 2,
  "maxTickets": 3
}
```

Resource identities constrain Agent instructions, not OS access. Tool and
execution allowlists are enforced. Child grants must be exact subsets; absolute
maxDepth and root-wide maxTickets cannot grow. Hard ceilings are depth 8 and
64 Tickets. Root and unknown reservations count against the budget.

Authorized Workers call `create_child_ticket` with stable requestId, independent
title/objective/acceptance, narrower authorization and exact
`execution: {runtimeRef, model, effort}`. Omit workerName to queue; include it
for immediate dispatch. `dispatch_child_ticket(ticketId, name)` starts a direct
child. Parent/root/Project/namespace are derived from the current capability.
Children cannot be reparented. Namespace groups Sessions and grants no tools.

Confirmed child creation and business changes atomically enter the parent
Ticket inbox. Unattempted notifications wait while the parent has no executor
or is offline, and resolve its current Assignment when dispatched. Child work
continues while the parent is closed. Accepted/unknown attempts retain their
original target and are not automatically replayed. Context exposes childInputs
with delivery and result references. Late facts on a terminal parent are audit
records and never reopen it.

Before a terminal parent outcome, explicitly resolve/cancel descendants first
with `finish_ticket`. Unknown creation/transition or unfinished descendants
block closure. No transfer/unlink operation is provided. Children have no
fabricated external Trigger and inherit no channel subscriptions.

## External consumer

Only `reply_trigger` publishes Agent text. Ordinary final, thinking, tool results
and Worker transcripts stay internal. Confirmed business changes persist their
own `ticket.<state>` facts even when the Router is unavailable.

```sh
mass-sa output read --config /absolute/run/config.yaml --project mass \
  --consumer adapter-1 --source demo/account --thread discussion-1 --limit 100
mass-sa output ack '<output-id>' --config /absolute/run/config.yaml --project mass \
  --consumer adapter-1 --source demo/account --thread discussion-1 \
  --outcome delivered --reference 'simulated adapter receipt'
```

Reads resume the consumer/destination checkpoint. `--after 0` explicitly rereads
retained records for audit/backfill. Outputs have stable IDs; adapters deduplicate
by ID. Acknowledgement requires prior observation. Out-of-order acknowledgements
cannot skip earlier unconfirmed destination records. Outcomes delivered/failed/
unknown consume a record, retain evidence and never schedule sends or reopen work.
Unknown sends require channel reconciliation before retry. Failed/unknown may
later reconcile to delivered; confirmed delivery cannot regress.

Facts target the first explicitly associated Trigger's source Thread or an
explicit `subscribe_output(ticketId, triggerId)` target. Thread association alone
does not broadcast. Replies target their own Trigger. Adapters mark echoes with
`echoOfOutputId` (`trigger accept --echo-of-output`); authorized echoes receive an
ignored receipt without new work. Unmarked text is not reliably deduplicated by
similarity. The management credential is an operator credential; destination
filters are not separate adapter accounts. No real IM/PR/email connector is included.

## Reliability and real acceptance

External creates and status operations reserve/fence intent before mutation.
Multica writes use an explicit workspace, creation selects the project, and
authoritative readback confirms project, hierarchy, metadata, comments and status.
No Agent is assigned; recording status uses `--no-start`. An unknown create is
isolated for investigation, never blindly retried. Restart can read back a pending
status intent but cannot repeat the write. This does not promise unconditional
exactly-once external side effects. State is single-instance with no process lock,
HA, pruning or separate message broker.

The sibling mass-go module pins remote SDK
`v0.0.0-20261115111701-ee13cff995ca` with no local SDK replace. Build massd from
its normal manifest with `GOWORK=off go build -mod=readonly -o /absolute/run/bin/massd
./cmd/massd` in mass-go, then build this module's mass-sa into the same new run.
Use `go list -m -json github.com/zoumo/agent-sdk-go` and `go version -m` to verify
the remote dependency and absence of a local replacement.

Real acceptance needs authenticated Claude, actual massd/Runtime, isolated
working/state/data directories and ports, and explicit Multica targets. Record
all executable hashes, model evidence and SDK source. Historical run5 used
unpublished candidate massd/SDK changes; published SDK dependencies are not
proven to reproduce it. Model configuration alone does not prove upstream model
selection. Preserve failures and distinguish configured/provider-observed facts.

For Claude close/resume, Runtime configuration must explicitly set
`env.CLAUDE_CONFIG_DIR` to the absolute native storage root containing that
Session's transcript. A fresh directory also needs separately provisioned
authentication; do not copy credentials into acceptance evidence. Historical
T07 candidates failed on an unscoped metadata query and a SessionStart callback
registered after native resume startup. The separately verified local SDK fixes
scope metadata to cwd and confirm exact live ID/cwd with a correlated read-only
`get_status`, preserving transcript/file checks and late identity conflict
rejection. Unified Resume adds no private callback and leaves caller hooks
unchanged. The pinned remote SDK contains these recovery fixes. Historical
local-candidate results alone do not prove a build with the remote dependency.
Do not enable personal hooks, send work before identity confirmation, delete
personal files, or substitute a new Session to disguise failed recovery. Native
CLI storage can be shared even when MASS data and cwd are isolated; record this
limit and preserve each failed run independently.

Freeze evidence, then use massctl while massd remains running: close every owned
Session and get it as offline; stop the owned Runtime and read it as stopped.
Then stop owned mass-sa/massd and check processes/listeners. A free port alone
does not prove Session/Runtime lifecycle cleanup. Retain failed-close evidence and
do not touch another run's resources. Do not overwrite historical binaries/runs.
