# Core v1 API

This document describes the public behavior implemented through ticket 12. The
approved product spec remains authoritative for later slices.

## Start and ownership

`superagent serve --config FILE` opens `dataDir/core.sqlite`, acquires
`dataDir/.core.lock` for its entire lifetime, checks application/schema metadata,
and binds HTTP. Startup success prints one JSON object with `endpoint`,
`observerEndpoint`, and `status: ready`. SQLite schema version is 12. Foreign keys,
WAL, FULL synchronous durability and unique source/native-message constraints
are enabled. Only one Core can own a directory, including during shutdown.

A new directory may be empty or contain only Core-owned SQLite/lock files from
an interrupted initialization. Foreign contents (including old state.json),
foreign databases, corrupt SQLite and unknown schema versions fail explicitly.
Schema 2–11 databases are upgraded transactionally. The upgrade from
schema 2 adds the health table. The upgrade from schema 3 preserves Messages,
routes, bindings and operation receipts and moves delivery state to its own
collection. A legacy in-flight event retains its event ID and records a gap
because the older Core did not save its wire batch.
Schema 5 adds Issue, Execution and Report storage while retaining existing
Topics, role configurations, event bodies and operation receipts.
Schema 6 initializes the separate requirements version of each existing Issue
from its saved Issue revision, preserving the validity of previously accepted
Report versions. Execution startup versions and old operation receipts stay
as written.
Schema 8 indexes Reports by Execution. Existing Executions have no prior
recorded work observation until Core observes a newly submitted input.
Schema 9 adds the initially empty Timer collection and due-time index.
Schema 10 adds PR associations and decouples Thread references from collector
accounts. Existing Thread keys, sequence positions, subscriptions and Message
receipts retain their values.
Schema 11 adds Outbox, send attempts, claim history and the authenticated
admitting instance on new Messages. Existing Message records and receipt
IDs are preserved. Older Messages have no authenticated instance provenance.
Persisted native Session specs retain their original system prompts.
Schema 1 ingress-only directories are refused without migration. The private
`capabilities/` directory is also Core-owned.

SIGTERM/SIGINT stop admission and wait up to ten seconds for active HTTP requests.
A shutdown timeout exits nonzero. Accepted records survive normal or forced
exit; a request interrupted before a receipt must retry its original operation.

## Submit Message

`POST /v1/channel/messages` with `Content-Type: application/json` and
`Authorization: Bearer TOKEN`. The token identifies exactly one configured
source/account; body fields cannot select another identity.

```json
{
  "operationId": "stable-adapter-operation-17",
  "message": {
    "source": "dws",
    "accountId": "configured-native-account",
    "sourceMessageId": "native-message-id",
    "threadRef": { "source": "dws", "id": "native-conversation-id" },
    "actor": "native-sender-id",
    "text": "Original input text",
    "addressed": "direct",
    "channel": {
      "type": "dws",
      "conversationId": "native-conversation-id",
      "messageId": "native-message-id",
      "actorId": "native-sender-id"
    }
  }
}
```

Required Message fields are source, accountId, sourceMessageId, threadRef,
actor and nonblank text. No identity is synthesized. Optional fields: absolute
RFC3339 `at`, tri-state `human`, `addressed`, `channel`, absolute HTTP(S) `url`
without credentials, JSON object `meta`, JSON `quotedMessage`, `sourceEventId`,
`collectedAt` and `collectorInstance`. Core generates `receivedAt`.
No unknown request fields or trailing JSON values are accepted.

`channel` is optional: absence means there is no supplied reply address, and
Core never guesses one. A provided channel needs a `type`; its `address` object
can preserve other adapters' native addressing fields. Only `type=dws` checks
matching conversationId/messageId and optional actorId. Source `dws` is
single-chat-only (`addressed=direct`), with or without a reply address. DWS
profiles and reusable credentials are never part of this payload contract.

Thread identity compares source/id independently. The key is each UTF-8
component URL-path escaped with colon escaped too, joined by one literal colon.
Ordinary DWS keys remain `dws:conversationId`; `dws:a%3Ab%2Fc%25` reverses to
conversation ID `a:b/c%`. Case is retained. No hash, account path or collector
instance becomes part of the Thread identity.
For PR Messages, send `threadRef: {"source":"pr:url","id":"https://..."}`.
The `pr:url` reference kind uses the canonical resource URL as the Thread key.
The outer `source` and `accountId` still match the authenticated collector and
scope native message deduplication. A PR Thread requires a non-DWS source and
cannot carry a DWS reply address. Use the optional Message `url` for a source
link, such as a comment permalink, when available. See
[PR URL rules](#pull-request-registration-and-subscriptions).

Success HTTP 200:

```json
{"status":"accepted","duplicate":false,"messageId":"msg_...","threadKey":"dws:native-conversation-id","acceptedAt":"2026-11-18T01:02:03.000000000Z","eventId":"evt_..."}
```

Message, Thread update, stable receipt, operation receipt, routing round 1,
source relation (when direct) and one `message.received` Event commit together.
Zero Thread subscriptions target Router; exactly one targets that Topic; more
than one target Router without broadcast. Event intent is `act`, initial status
is `pending`. The coordinator sends committed input asynchronously; the ingress
receipt proves only local acceptance.

Operation deduplication is scoped by source + operation type + operationId.
The same operation and normalized business parameters return its exact stored
receipt. Different parameters conflict. A new operationId with an already
accepted source/sourceMessageId returns its original IDs and acceptedAt with
`duplicate=true`; that new operation receipt also becomes durable. Changing
text, actor, Thread, reply address or other business content conflicts.
Canonical comparison sorts JSON object keys and normalizes absolute source
time/quoted JSON; collectedAt, collectorInstance and raw sourceEventId envelope
references are excluded. Original fields are retained from first acceptance.
No receive timestamp or process instance participates in identity.

## Trigger heartbeat

`POST /v1/channel/heartbeat` uses `Content-Type: application/json` and
`Authorization: Bearer TOKEN`. Configure the same source, account and stable
Trigger instance on both sides. A Core source entry gets its token from the
private file. The Trigger supplies that token with its existing Core credential.

```yaml
sources:
  - source: dws
    accountId: example-account
    instanceId: collector-1
    tokenFile: ./source.token
```

A credential with no configured `instanceId` can submit Messages only. The same
instance-bound credential can use the [Outbox protocol](#trigger-outbox). To run
several instances for one source/account, repeat the pair in separate entries
with distinct instance IDs and token files/values. A token can report only its
configured source/account/instance tuple. Message deduplication remains scoped
to the source and native message, so multiple instances still share it.

```json
{
  "operationId": "heartbeat-c980553c-f5b0-439c-9cb8-956fe4fff3b6",
  "source": "dws",
  "accountId": "example-account",
  "instanceId": "collector-1",
  "connection": "ready",
  "consumerReady": true,
  "readyConsumers": 1,
  "consumers": 1,
  "admission": "accepted",
  "backlog": 0,
  "capacity": 100,
  "lastReceivedAt": "2026-11-18T01:02:03Z"
}
```

- Instance and operation IDs must match `[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}`.
  Send only the listed fields and optional `lastErrorAt`, `lastError` and
  `stopReason`. The request body is limited to 8 KiB. Exclude local status
  counters, health reporting metadata, credentials, message content and logs.
- Connection is `connecting`, `ready` or `disconnected`. There must be at least
  one consumer. `consumerReady` must be true exactly when the connection is
  ready and all consumers are ready. A disconnected instance has zero ready
  consumers and may report a `stopReason`.
- Admission is `waiting`, `pending`, `accepted` or `failed`. Capacity is
  positive and backlog is between zero and capacity. `failed` requires
  `lastError` and `lastErrorAt`. When present, these fields must be paired.
  Both optional timestamps are absolute RFC3339 values normalized to UTC.
- Allowed stop reasons are `buffer_full`, `shutdown`, `consumer_ready_timeout`,
  `consumer_pipe_failed`, `consumer_start_failed`, `consumer_exited`,
  `consumer_failed`, `dws_connection_error`, `dws_stderr_invalid`,
  `dws_ndjson_too_large_or_unreadable` and `core_rejected`.
  `lastError` also accepts `input_rejected`, `event_before_ready`,
  `invalid_message`, `invalid_request`, `core_unreachable_or_outcome_unknown`,
  `core_response_unknown`, `core_receipt_unknown`, and Core error codes
  `invalid_argument`, `unauthorized`, `forbidden`, `not_found`, `conflict`,
  `stale_binding`, `failed_precondition`, `unsupported`, `unavailable` and
  `outcome_unknown`. Map local rejection details to `input_rejected` and
  `core_rejected_*` stop codes to `core_rejected`.

HTTP 200 confirms the latest instance snapshot and operation receipt committed
in one transaction:

```json
{"status":"accepted","healthId":"ch_...","acceptedAt":"2026-11-18T01:02:04Z","expiresAt":"2026-11-18T01:02:34Z"}
```

`healthId` is stable for the bound source/instance. Core sets `acceptedAt` and
`expiresAt` using its own clock. Deduplication is scoped to
source/instance/heartbeat/operationId. Retry an uncertain operation with the
same ID and normalized payload to get its original receipt. A changed payload
with the same ID returns `conflict`. Replays do not change the latest snapshot
or refresh expiration, even after restart.

Publish each new observation with a new operation ID, including periodic
reports when the state is unchanged. A 10-second reporting interval leaves
room for retries within the 30-second freshness window. Serialize reports for
an instance and retry the current operation before sending a newer snapshot.
New operations replace the snapshot in Core commit order. Heartbeats do not
enqueue routing work.

## Errors and uncertain responses

Errors are JSON `{"error":{"code":"...","message":"...","retryable":false}}`.
HTTP mapping: invalid_argument=400, unauthorized=401, forbidden=403,
not_found=404, conflict/failed_precondition/unsupported/stale_binding=409, unavailable=503.
All error bodies omit credentials and raw storage/upstream errors.

Missing native identity fails invalid_argument. Wrong authenticated source or
account fails forbidden. Storage errors never return accepted. An unavailable
storage result or lost connection must be reconciled by retrying the same
operationId; do not create another native identity. A response lost after commit
can be replayed after restart without another logical Message or Event.

## Read-only observation

No write credential is needed or sent by these trusted-reader endpoints:

- GET/HEAD `/v1/observer/health`
- GET/HEAD `/v1/observer/channel-health[/{healthId}]`
- GET/HEAD `/v1/observer/messages[/{id}]`
- GET/HEAD `/v1/observer/threads[/{escaped-key}]`
- GET/HEAD `/v1/observer/events[/{id}]`
- GET/HEAD `/v1/observer/topics[/{id}]`
- GET/HEAD `/v1/observer/issues[/{id}]`
- GET/HEAD `/v1/observer/executions[/{id}]`
- GET/HEAD `/v1/observer/reports[/{id}]`
- GET/HEAD `/v1/observer/timers[/{id}]`
- GET/HEAD `/v1/observer/pull-requests[/{id}]`
- GET/HEAD `/v1/observer/outbox[/{id}]`
- GET/HEAD `/v1/observer/decisions[/{id}]`
- GET/HEAD `/v1/observer/subscriptions[/{id}]`
- GET/HEAD `/v1/observer/relations[/{id}]`
- GET/HEAD `/v1/observer/bindings[/{id}]`
- GET/HEAD `/v1/observer/session-preview/{bindingId}/history`
- GET/HEAD `/v1/observer/session-preview/{bindingId}/live`
- GET/HEAD `/v1/observer/deliveries[/{id}]`
- GET/HEAD `/v1/observer/batches[/{id}]`
- GET/HEAD `/v1/observer/attempts[/{id}]`
- GET/HEAD `/v1/observer/recoveries[/{id}]`
- GET/HEAD `/observer/` and its static assets

Health reports `ingress: ready` for the Core HTTP intake endpoint. The SQLite
read snapshot includes Router preparation, fresh connection and admission
counts, expired heartbeats, Topic and Issue business states, unbound events,
unknown native stops and echoes, and input delivery and Outbox counts by
state. Delivery counts include `needsAttention`.
Channel health lists one latest committed snapshot per reporting instance and
includes the Core acceptance/expiry times. Before the first heartbeat there is
no report. On each read, `status` is `stale` if the report expired, otherwise
`buffer_full` if backlog reached capacity or the stop reason is `buffer_full`,
otherwise `admission_failed` if admission failed, otherwise the last reported
connection. The `stale` boolean and raw reported connection/admission fields
are also returned. A fresh report reflects the Trigger's observation at the
last heartbeat. Stale status preserves the reported facts for inspection.
Channel health pagination orders by first acceptance time and stable ID.
Refresh can show a newer report for the same row or a change in freshness.

Router is `not_started` when execution is
omitted, or its persisted `pending/ready/blocked` preparation state. Messages
include current `routing: {round,state}`. Observer/Router projections also include
source reception/return relations. Topic-role Message queries omit other Topics'
relations. Thread details expose key, ref and timestamps. Bindings expose fixed
role, owner, generation, workspace, namespace/name, Runtime, actual Session IDs,
preparation state/error and next attempt time, never token or MCP capability files.
Events expose logical target, routing round, status, actual input receipt and
sanitized error. A delivered receipt does not prove consumption or completion.

Lists return `{"items":[...],"nextCursor":"opaque"}`. Default limit 50,
valid range 1–200. Messages and subscriptions accept an exact `threadKey`.
Issues, Timers, PR associations, routing relations and subscriptions accept `topicId`.
Executions, Reports and PR associations accept `issueId`. Supply one parent
filter at a time. The Observer also supports `topicId` on Decisions and Outbox
to find decisions and replies for messages in the Topic's routing history.
Outbox, routing relations, Decisions and Events accept `messageId` in the Observer.
Observer lists accept `preview=true` for compact rows. Message, reply and Timer
previews contain up to 240 characters. Open a record by ID for its full body
and evidence. Continuation
cursors bind collection, filter and the first page's sequence high-water mark.
Rows accepted later never enter that traversal. Ordering uses fixed nanosecond
UTC Core timestamps plus stable ID (Thread key for Threads). Thread ordering
uses immutable `createdAt`. `lastMessageAt` reflects the last accepted Message
and is omitted until the Thread receives its first source input.
A fresh first-page request refreshes the snapshot. Cursors are opaque reader
state, not capabilities; malformed or scope-mismatched cursors fail. Set
`order=newest` on Observer lists for descending time and ID order. Reuse the
same order when following a continuation cursor.

`superagent observe COLLECTION [id] --endpoint ORIGIN --limit N --cursor TOKEN`
prints the same JSON. List-filter flags are `--thread-key`, `--topic-id`,
`--issue-id` and `--message-id`. Use `--newest` for descending order. `observe health` prints
health. COLLECTION is any collection listed above. The read-only CLI never loads channel config or source token files.
Only a numeric HTTP origin is accepted; redirects are not followed.

All Observer writes return 405 with Allow: GET, HEAD. Host must match the
connection's actual numeric local endpoint; foreign Origin and cross-site Fetch
Metadata are rejected. No query parameter grants write authority. No CORS
credentials or browser-side write token is configured. Cache-Control is no-store.
Reads never enqueue work, acknowledge Events or change Message/Thread state.

Session preview resolves a recorded Core binding, then verifies the Session
identity through massd. The history endpoint reads up to 50 durable events and
accepts the previous page's `nextCursor` as `cursor`. It also returns
`observedAt` and massd's `syncedEventId`. The live endpoint streams
`application/x-ndjson`. Frames report `connected`, `heartbeat`, or a
`preview` event. A `history_changed` frame tells the client to query stored
history again. On reconnect the client reads history and its cursor again.
Activity between live connections can be missing. Stored history belongs to
the Session, which can span several binding generations. The projection omits
raw tool values, Runtime configuration and event metadata. Text previews
longer than 16 KiB carry a truncation flag. HEAD reads the binding and returns
headers without opening a massd connection.


## Execution configuration and fixed Session preparation

Execution is optional for intake-only operation. To enable it, configure an
existing massd Runtime and an existing absolute Router workspace:

```yaml
execution:
  massdEndpoint: ws://127.0.0.1:24368/rpc
  runtimeRef: agent
  routerWorkspace: /absolute/workspace
  namespace: superagent
  # tokenFile: /private/massd.token  # omit only for explicit local massd access
  # operatorTokenFile: /private/operator.token  # enables local input recovery
  # mcpCommand: /absolute/bin/superagent  # defaults to this executable
```

Namespace is a DNS label of at most 24 bytes. The Router is unique per Core
store, in `<namespace>-router/router-g1`. Topic `<topicId>` has namespace
`<namespace>-<topicId with underscores changed to hyphens>`, Session `topic-g1`.
A Worker uses its Topic namespace and a fixed
`worker-<executionId with underscores changed to hyphens>-g<generation>` name.
Runtime, workspace, fixed prompt, role, owner, generation, MCP configuration and
preparation operation are persisted before any Session creation. The massd endpoint is pinned too, so a restart cannot silently create parallel
Sessions in another massd. Execution
configuration must match the existing binding on restart. Enabling execution
on an intake-only directory also binds its pending Router events.

Core persists the first execution listener, including an assigned ephemeral
port. Restart rebinds that exact endpoint so existing stdio MCP processes can
continue using their captured capability. A different configured endpoint is
refused; an occupied endpoint fails visibly. Core ownership is still single
process. Preparation queries massd by fixed namespace/name before creating,
never includes initial business input in create, and re-queries the same name
when a create receipt is lost. Full Session spec, resource UID and native
Session identity must match; conflicts become `blocked` and are never adopted.
Offline matching Sessions use public resume without new input.

Preparation failure retains the original binding, fixed name and visible pending
operation, retried after two seconds in this slice. On restart, ready bindings
are reconciled before dispatch resumes. New Topic Sessions have the issue and
acceptance instructions in their fixed prompt. Earlier Topics retain their
persisted Session spec and can discover the new tools through MCP.
MCP capability files are private, atomically replaced and read once per MCP
process. Authentication is checked by Core on every role call. Runtime/model/
effort are never options in a route decision.

## Role stdio MCP and source scope

massd launches `superagent mcp --capability-file FILE`. The file contains an
endpoint, token, binding ID, role, owner and generation. Worker files also carry
Topic and Issue IDs, and use the Execution ID as owner. The MCP process captures
these values once, verifies them with Core, and never reloads a new identity.
A mismatched startup identity fails `stale_binding`. External Message text,
meta, tool arguments and native-subagent suggestions cannot select permissions.

The transport is newline-delimited JSON-RPC 2.0 using sourcegraph/jsonrpc2,
with initialize, notifications/initialized, ping, tools/list and tools/call.
Supported protocol versions are 2024-11-05, 2025-03-26, 2025-06-18 and
2025-11-25. Unknown versions negotiate 2024-11-05. Tool calls require initialize;
unknown request/argument fields are rejected. tools/call returns text and
structuredContent on success; JSON-RPC errors include the Core error as data.

| Tool | Router | Topic | Arguments |
| --- | --- | --- | --- |
| `binding_get` | Own binding | Own binding | `{}` |
| `source_query` | All source Messages/Threads and own replies | Associated Messages, subscribed Thread metadata and own replies | `collection: messages/threads/outbox`, optional `id`, or list `threadKey/cursor/limit`. Outbox also accepts `messageId` |
| `topic_query` | Topic summaries only | Only own Topic | `collection: topics`, optional `id` or `cursor/limit` |
| `route_decide` | Allowed | Forbidden | Route request below |
| `message_return` | Forbidden | Own current reception | Return request below |
| `message_reply` | Explicit Message or oldest in supplied range | Explicit associated Message or oldest associated Message | `operationId`, `text`, optional `messageId`, Router-only `messageIds` |

Detail queries cannot combine list parameters. Topic summaries include only
ID, title, description, status, revision and timestamps. There is no Issue,
Report or Worker Session expansion. Topic source queries do not expose unrelated
messages from a shared Thread. Role list cursors additionally bind the exact
binding; Observer or another role cannot reuse them to change scope. A scoped
missing detail returns not_found without revealing its existence.

The authenticated HTTP bridge is `POST /v1/role/TOOL`, using the same tool names,
arguments and domain commands. Success is `{"result": ...}`; failures use the
standard error envelope. Channel tokens cannot call role tools; role tokens
cannot submit channel messages. Neither surface is part of Observer.

## Atomic RouteDecision

```json
{
  "operationId": "router-decision-17",
  "messageIds": ["msg_a", "msg_b"],
  "rounds": {"msg_a": 1, "msg_b": 2},
  "kind": "route",
  "reason": "Two explicit independent requests",
  "targets": [
    {"create": {"title": "First request", "workspace": "/absolute/existing/workspace"}, "messageIds": ["msg_a"]},
    {"topicId": "topic_existing", "messageIds": ["msg_b"], "redeliveryReason": "New information justifies this scope"}
  ]
}
```

Each target requires exactly one topicId or create, plus a unique nonempty
subset of decision messageIds. Every source Message must have a current pending
round matching rounds; every source must be covered. A Message can explicitly
appear in several independent targets. Duplicate existing-Topic target entries,
unknown sources/targets, stale rounds and conflicting decisions are rejected.
The complete request uses one SQLite transaction: Topic, binding/preparation,
Message–Topic reception, Thread subscription, first input events, decision,
source routing state and operation receipt all commit or roll back together.
No model, workspace creation or massd call occurs inside that transaction.

Success returns the request fields plus decision `id`, `createdAt` and resolved
Topic IDs in targets. A new Topic starts active at revision 1. A repeated
operationId with identical parameters returns the original decision, even after
its selected workspace is removed; changing parameters conflicts. Dedupe is
scoped to binding and operation type. Overlapping decisions compare all source
rounds atomically and leave no partial new Topic/delivery on conflict.

`kind: ignore` requires a reason and no targets. It stores an observable decision
and marks source routing ignored; it does not complete any Topic. Clarification
is a future Outbox reply plus a later source Message, not a separate matching
state machine in this slice.

## Return and terminal-Topic behavior

Topic calls `message_return` with
`{operationId,messageId,round,reason}`. round identifies its current reception.
Core verifies ownership and active reception, appends the reason/history,
marks that reception returned, increments the Message's current routing round,
and queues a stable `message.returned` Router event in the same transaction.
The Thread subscription remains. Identical retries return the saved relation.
This path bypasses single-subscription auto-routing. A later Router decision
that selects a previously returning Topic must provide a new nonblank
redeliveryReason differing from the latest return reason.

Subscription selection includes completed/canceled Topics. Receiving, preparing
or waking a Topic preserves its business status. The `topic_complete` command
is described below. Routing keeps Issue ownership within its original Topic.

## Issue, Execution, Report and acceptance

All changes require an `operationId` scoped to the authenticated binding and
tool. An identical request returns the original committed receipt, including
after the business state changes. Changing its parameters returns `conflict`.
Use the read tools for live state. The HTTP role bridge and stdio MCP call the
same transactions.

### Topic tools

| Tool | Arguments | Result |
| --- | --- | --- |
| `issue_create` | `operationId,title,requirements,workspace`, optional `materials,start` | `{issue, execution?}`. Topic ownership is automatic, revision starts at 1, and default status is `pending`. |
| `issue_start` | `operationId,issueId,expectedRevision`, optional `resolveBlockerIds` | `{issue, execution}`. Schedules the one current Execution and can explicitly resolve blockers in the same transaction. |
| `issue_update_requirements` | `operationId,issueId,expectedRevision,requirements`, optional `notifyWorker` | `{issue, notificationEventId?}`. Saves new requirements and optionally queues an input for the current Execution. |
| `issue_update_blockers` | `operationId,issueId,expectedRevision`, at least one of `add,resolveBlockerIds`, and `unblockedStatus` when resolving the last reason | `{issue}`. Modifies only the specified reasons. |
| `work_query` | `collection: issues/executions/reports/timers/pull-requests` with `id` for detail, or `cursor,limit` for a list. Execution, Report and PR lists also accept `issueId`. | Records in the bound Topic only. |
| `pr_register` | `operationId,url`, optional `issueId` | Saved PR association and subscription ID. |
| `issue_complete` | `operationId,issueId,expectedRevision,reason,evidence` | `{issue, execution?}`. Saves acceptance and retires the current Execution, if present. |
| `issue_cancel` | `operationId,issueId,expectedRevision,reason` | `{issue, execution?}`. Cancels a nonterminal Issue and retires its current Execution. |
| `issue_reopen` | `operationId,issueId,expectedRevision,reason` | `{issue}`. Reopens a terminal Issue to pending. A later explicit start is required. |
| `topic_complete` | `operationId,expectedRevision,reason,evidence` | Topic with its saved acceptance. Every Issue must already be terminal. |
| `topic_cancel` | `operationId,expectedRevision,reason` | Cancels the Topic and its nonterminal Issues, retires the Topic binding and requests stops. |
| `topic_reopen` | `operationId,expectedRevision,reason` | Reopens only the Topic on its current binding. Issue states and outcomes stay intact. |

Supply a nonblank title and requirements and an absolute existing workspace.
Each material is `{label?,uri}` or `{label?,messageId}`. URI or path references
are stored as text. A Message must already have a reception in that Topic.
The fuller plan can stay in the workspace. A Topic must be active to create,
start, edit or complete an Issue. For a stale revision Core returns `conflict`
with `resource` and `currentRevision`.

`start: true` uses the same scheduling transaction as `issue_start`.
Core records the current Execution, Worker binding/preparation, one
`issue.started` act Event and operation receipt together. The fixed start
Event instructs the Worker to fetch its scoped requirements. The binding and
Event carry the Execution generation. Starting increments the Issue revision
and records the current requirements version in the Execution and event. A
different start while a current Execution exists returns conflict.
The receipt proves scheduling. Read `preparation` and `startup` on the
Execution to inspect the bound Session and actual delivery receipt.

Completion requires a reason and at least one nonblank evidence reference,
such as a Report ID or artifact path. Core stores the acceptance author binding,
timestamp and references. Topic reviews the evidence. A Report, ordinary final,
Session idle or all Issues reaching a terminal state leaves Topic completion
to the explicit command.

### Requirements and blocking reasons

`expectedRevision` compares against `issue.revision`. `requirementsRevision`
starts at 1 for a new Issue and increases only when the requirements are saved.
Worker uses the latter value in a Report. Starting an Execution and editing
blockers each advance the Issue revision.

`notifyWorker: true` requires a current Execution, which may still be preparing.
Core saves the requirements, queues one `issue.requirements_updated` act Event
and stores the operation receipt atomically. The Event's `revision` is the
requirements version and its `targetGeneration` is the intended Execution
generation. The payload includes the Issue, Topic, Execution and requirements
at that version. Unified delivery supplies truncation, authorized full content
lookup and error state. A save without notification makes the new requirements
available through `issue_get_current`. It creates no delivery. On notification
input, Worker reads current requirements before acting, including when a newer
save has already occurred.

Each blocker has a stable ID and a reason. An `add` entry is
`{kind:"human",reason}` or `{kind:"dependency",reason,dependencyIssueId}`.
The entire edit is rejected if any target is missing, outside the Topic, the
Issue itself, or would close a dependency cycle. Resolve a reason by its ID.
As long as any reason remains, the business state is `blocked`. Resolving the
last reason through the blocker command requires `unblockedStatus` of
`pending` or `active`. The blocker command leaves Worker's Execution alone.
With `issue_start`, only the specified `resolveBlockerIds` are removed. If
blockers remain, the Worker starts and the business state stays `blocked`.
If the start resolves the last blocker, the business state becomes `active`.

The Issue lifecycle emits an `issue.dependency_changed` context Event to its
Topic when a referenced target completes, cancels or reopens. Its payload
contains the target, prior and new business states, and the IDs of dependent
Issues. Each transition and its dependency fact commit together with the
operation receipt. These facts preserve dependent Issues and leave all
orchestration to the Topic.

Issue reads expose blocker records and the projected
`latestRequirementsNotification` with its requirements version, Event ID and
delivery state. It continues to identify the last explicitly notified version
after a later silent save. Topic and Observer Issue details with a current
Execution include a bounded, read-only MASS `workerSession` observation.
It reports the Session state and observation time, or `unknown` with a gap.
Business state remains independent of the observed Session state.

### Worker tools

| Tool | Arguments | Result |
| --- | --- | --- |
| `issue_get_current` | `{}` | Bound Issue and current Execution. |
| `issue_material_get` | `{messageId}` | Original Message if explicitly listed in the bound Issue materials. |
| `report_submit` | `operationId,kind,summary`; optional `details,evidence,questions,requirementsRevision` | Appended Report with its Topic notification Event ID. |
| `pr_register` | `operationId,url` | Saved PR association scoped to the current Topic, Issue and Execution. |
| `event_get`, `event_ack` | `{eventId}` | Read or optionally ACK an event delivered to the current binding. |

Report kinds are `progress`, `ready`, `blocked` and `failed`. Evidence and
questions are string arrays. The optional requirements revision identifies the
version the Worker used. Core supplies the Topic, Issue, Execution and generation
from the authenticated binding. Unknown identity fields in arguments are
rejected. Topic, source-wide queries and Outbox are outside Worker scope.

The Report, one `issue.reported` Event targeting its Topic and the operation
receipt commit in one transaction. Current Reports use act. Once retired, the
credential can append only to that same historical Execution. New late Reports
carry `historical: true` and use context. The private stdio bootstrap can still
verify the frozen identity for this append path. Current Issue and material
queries, PR registration, event reads and ACKs all reject the retired binding. Report processing
does not change Issue or Topic business state.

### Worker work observation and report reminder

Core opens a live observation before dispatching an eligible Worker
StartOrSteer and binds that observation to its input attempt. For Codex, the
public prompt receipt supplies a native turn ID in `submission` when
`nativeAccepted` is true and the result is `applied_new_work` or
`applied_current_work`. The pinned SDK forwards a root `turn/completed` as a
live `session.state_update` with `_meta.codex.method` and
`_meta.codex.turnId`. The Session ID, namespace, name, root turn ID and
terminal reason must agree with the input and its Worker binding. A terminal
event can arrive before its receipt. A newer admitted prompt fences the
older attempt; a later completion of the earlier turn cannot settle the newer
attempt. Child-thread notifications and standalone idle updates cannot
qualify. This proves the end of the referenced root turn. Codex provides no
aggregate completion guarantee for work delegated outside that root turn.

The Claude receipt confirms a best-effort local submission. Claude's result
events do not carry a matching native work reference, so Core reports
`native_work_identity_unavailable` for these submissions. Subscription
failure or interruption leaves work unknown and the original attempt is not
resubscribed for inference. Core restart marks unfinished observation as
`core_observation_interrupted`. A newly accepted input can establish fresh
observation. Core does not reconstruct terminal facts from history, idle,
final text, control acknowledgment or EOF.

When the current Execution has no Report, a correlated end and one
`issue.report_reminder` Event commit together. The Event always targets the
same Worker binding generation and asks for a Report about existing work.
The Worker keeps the original Execution and can use the Event ID as its
Report operation ID. The marker stays set after a reminder is delivered,
superseded or redelivered. A Report for the current Execution supersedes an
unsent reminder in the Report transaction. Core also checks the Report and
binding immediately before dispatch. In-flight sends settle their actual
receipt, and an uncertain transport retry preserves the Event ID. Historical
Reports are scoped to their original Execution.

Execution reads project `workObservation` with the binding and Session
identities, attempt ID, work reference if known, continuity, the observed end
or the observation gap. They also project `reminderScheduled`,
`reminderEligibility` and `reminder` with its delivery status when scheduled.
Observer links to the Event and its delivery evidence. Neither observation
nor reminder changes Issue, Topic or stop status.

### Retirement and stop observation

Issue completion and cancellation supersede pending Worker input and revoke
the Execution's authority in the business transaction. Cancellation also
persists a stop request for the retired Execution. Completion requests a
stop when it finds a claimed batch. In-flight sends retain
their actual receipts. Topic cancellation performs these changes for each
unfinished Issue, retires the Topic binding and requests a stop for it. Existing
acceptance evidence, reports, workspaces and Session history remain accessible.
Cancellation does not undo external effects.

Core serializes stop with the binding's current dispatch, verifies the original
MASS Session identity and uses public `session.cancel`. Execution details
show `stop.status` (`requested`, `dispatching`, `observed`, `blocked` or
`unknown`), request and observation times, receipt/error, Session observation
and any gap. Stop control is scheduled ahead of the binding's next input and
does not wait for the input aggregation window. An interrupted in-flight stop
remains unknown across restart. The stop observer records Session state
separately from the Worker's input observation. Cancel acknowledgment,
no-active-work, idle and EOF leave `stop.workState` unknown. A verified resource release
appears as `close` on the old binding and in Execution preparation. Its method,
time and massd resource version are available separately from the turn outcome.

Reopening preserves historical acceptance and blocker records. An Issue moves
to pending and has no Execution until a later explicit start. A start requires
proof that prior preparation never began or that the original native owner
was released. It assigns a fresh Execution ID, generation and credential.
A canceled Topic still accepts new Messages through its subscriptions.
Messages wait durably while the Topic has no current Session. After settlement,
they reach the next Topic generation with the same IDs. The Topic stays
canceled until that owner explicitly calls `topic_reopen`. The previous Topic
credential can replay its exact cancellation receipt.

## Durable input delivery

An Event has a stable ID, type, Core acceptance time, logical role/owner,
`act` or `context` intent and a `contentRef`. An optional `revision` records
business revision. A positive `targetGeneration` fences a command to its
binding generation. A Topic command can also include `topicGeneration` (zero
for a new Topic) to require the current active lifecycle. Source events retain
the Message ID and routing round.
Each Delivery stores the actual binding generation and the massd Session ID
and resource UID used when dispatch is claimed. The Event query projects its
latest delivery. The Delivery collection retains previous bindings' records.

The input is JSON with `schemaVersion: 2`, `bindingId`, `generation` and
ordered `events`. The wire `eventId` equals the Event query `id`. The first
event opens a fixed 200 ms window in Core acceptance order. The window ends
when time expires, when the batch reaches 20 events or when its next member
would exceed 64 KiB of encoded JSON. Source timestamps have no effect on
order. Text longer than 8 KiB keeps a UTF-8 preview and sets `truncated: true`.
An event with oversized optional fields is sent alone with its preview and
content reference. Use `source_query` to read the full referenced Message.
Large structured payloads carry a string `payloadPreview`. Use `event_get`
to read their original JSON.

All-context batches use public `session.inject`. Any `act` selects
`session.prompt`, which calls StartOrSteer. Mixed batches preserve the event
intents. Each Session writes one batch at a time and can send another once the
input RPC settles, including during the same model turn. Separate Sessions
can receive input concurrently. The coordinator reserves up to four concurrent
Worker preparation/input calls for future Worker producers.

Core commits the batch and a dispatching Attempt before the RPC. Settlement
stores the observed outcome, receipt, error and observation gap. A delivered
batch requires `sent: true` and no explicit native rejection. Prompts also
require an applied result. `not_applied`, native rejection and unsupported are
`blocked`. Unsupported Inject stays blocked. A prompt error without a public
receipt retains its error and an absent receipt.

Known pre-dispatch rejection or explicit temporary non-submission is
`pending`. A missing `sent` field does not establish non-submission. A
lost RPC response or a crash after claim is `transport_unknown`. Core
retries the saved batch and records possible duplication. Stale routing
rounds, inactive receptions and invalidated commands are superseded. If
batch membership changes, Core rebuilds it with the still-current event IDs.
Successful deliveries already committed to Core survive restart without replay.
`blocked_unknown` seals the binding after a MASS-reported `outcome_unknown`,
an existing input fence or an ambiguous error with missing submission evidence.

Retry times are persisted. The delay starts at 1, 2, 4, 8, 16 and 32 seconds
with up to 20% jitter. The maximum is 60 seconds. After 10 failures or ten
minutes of failure, Core marks `needsAttention` and retries eligible failures
at 54–60 second intervals. Batch details retain event IDs, JSON byte count,
method, attempt count, latest attempt ID and receipt.

### Role ACK and full content

Use `event_get` and `event_ack` over stdio MCP or
`POST /v1/role/event_get` and `POST /v1/role/event_ack` with
`{"eventId":"evt_..."}` and the existing role credential. Access is restricted
to events directed to the current binding. ACK requires a claimed dispatch
and records the first ACK time for the binding/event pair. It is optional
and independent of delivery, retries and business completion. Message
references follow the existing `source_query` authorization.

### Local operator reconciliation

Set `execution.operatorTokenFile` to a distinct private credential with at
least 32 non-whitespace bytes and bind Core to loopback. Inspection uses
`GET /v1/operator/bindings/{bindingId}` to read the saved binding, its stop
evidence and a live identity-checked massd snapshot. Recovery uses
`POST /v1/operator/input-recovery`. Both require the local operator credential.

```sh
superagent inspect-binding BINDING_ID --endpoint http://127.0.0.1:24370 \
  --operator-token-file /private/operator.token
superagent recover-input BINDING_ID --endpoint http://127.0.0.1:24370 \
  --operator-token-file /private/operator.token --operation-id incident-123 \
  --mode retry --reconciliation 'Compared original native trace and affected work' \
  --accept-possible-duplicate
```

`retry` targets a current blocked Router, Topic or Worker binding. The operator
records facts checked and accepts the possibility of duplication. Core
checks the persisted Session identity/spec through massd. It closes an
applied online owner and verifies offline readback before resuming. A Session
already offline or unknown uses public resume on the same authority, without
recording a close. If the previous native owner was unconfirmed, that gap
survives same-authority recovery and fences any subsequent replacement. Core
reschedules unknown input after reconciliation on the same authority.
Input still uses normal massd admission. The recovery
record retains the operator note and observed resource versions. Seeing
idle alone is insufficient.

`settle` releases a retired Worker or Topic binding. For a Worker, the next
explicit start creates the new Execution. For a Topic, settlement prepares its
next owner so queued source Messages can reach it. Core prefers reusing an
identity-checked local Claude Session on macOS/Linux after its online native
process has closed and massd verifies the same resource offline. A new MCP
process loads a new generation credential. The previous MCP process retains
its startup identity. If a failed resume leaves the same resource verifiably
offline, preparation switches to a fresh Session. An unknown resume leaves
the binding blocked and exposes the error.

`replace` requests a fresh Session after verified release. A current Worker
replacement retires the old Execution and explicitly schedules its successor.
A retired Worker replacement records the release policy for its next explicit
start. Router and Topic replacement retain valid unsubmitted facts with their
stable IDs, supersede stale commands and append a `binding.recovered` event.
A canceled Topic receives that recovery event as context. Delivered history
stays on its previous binding.

Codex Session close releases the SDK attachment to its shared app server.
To settle or replace its authority, pass `--stop-runtime` after inspecting the
target. This stops the shared local Runtime and interrupts its other Sessions.
Core checks resolved ownership, the completed Runtime stop and resource readback
before accepting the release. A restart-time unknown owner or an unconfirmed
close stays blocked. The same settled stop can account for additional retired
bindings whose saved Runtime instance and offline Session readback match. Each
verified binding gets its own release evidence. Session status alone cannot determine a particular turn's
outcome or account for tools outside the locally owned Runtime.

Core records the operation before issuing lifecycle calls. A pending recovery
holds the current binding in `recovering`. Inspect
`superagent observe recoveries OPERATION_ID` for failures or retry with the
same arguments. A completed operation returns its saved record on replay.

### In-process producers

`core.EnqueueEvent(ctx, tx, event)` appends a logical event and its current
delivery inside the caller's Core transaction. Producers supply a stable ID
and use a positive `TargetGeneration` for generation-bound commands. They
can call `core.SupersedeEvent(ctx, tx, eventID)` to fence future dispatches
within a business transaction. An already-claimed attempt retains its
actual outcome.

## Topic Timers

Only the current Topic binding may manage its Timers. The Topic must be active.
These tools are available over stdio MCP and `POST /v1/role/{tool}`.

| Tool | Arguments | Result |
| --- | --- | --- |
| `timer_set` | `operationId,dueAt,content`, optional `materials` | A new `scheduled` Timer at revision 1. |
| `timer_update` | `operationId,timerId,expectedRevision,dueAt,content`, optional `materials` | The next revision. Replaces the due time, content and materials. Omit materials to clear them. |
| `timer_cancel` | `operationId,timerId,expectedRevision` | The next revision with status `canceled`. |

`dueAt` accepts RFC3339 with `Z` or a numeric offset and up to nine fractional
digits. The record stores the UTC instant. Content must be nonblank. Materials
use the Issue material shape and the same Topic scope for Message references.
`operationId` replays the original committed receipt for identical parameters.
Reusing it with different parameters or using a stale revision fails with
`conflict`. Changes to a current `fired`, `canceled` or `invalidated` Timer
fail with `failed_precondition`. Use a new `timer_set` for another followup.

Core scans on startup and every 250 ms while execution is enabled. A valid past
time is eligible on the next scan. The first valid transaction to commit
decides a race between firing, updating, canceling and Topic termination.
Firing keeps the selected revision, records `firedAt` and commits one stable
`timer.followup` act Event in the same transaction. Its payload contains the
fired Timer. Normal input batching and StartOrSteer deliver the Event to the
Topic, which can query current state before deciding what to do. The Timer
creates no external Message or Thread and requests no Issue start.

Completing or canceling the Topic atomically invalidates scheduled Timers.
Fired Timers keep their recorded trigger and Event ID. Unsent followups are
superseded, including claimed batches that fail the final delivery check.
An admitted call can still settle with its actual receipt. Reopening keeps
these historical Timer states. On restart, scheduled Timers remain eligible
and committed followups keep their Event ID and normal delivery retry semantics.

`work_query` with `collection: "timers"` lists Timers in the bound Topic or
reads one by `id`. Observer and `observe timers --topic-id TOPIC_ID` expose the
current revision, due and fired times, state and followup delivery projection.
Use the followup Event for the full payload and its delivery records.

## Pull request registration and subscriptions

Call `pr_register` from Topic or Worker stdio MCP, or through the corresponding
authenticated role bridge. A Topic supplies `issueId` only when associating a PR
with an Issue it owns. A Worker uses its current binding for all three IDs.
Core checks current Worker ownership before replaying old PR receipts.

Core saves the association, Thread, Topic subscription and operation receipt
in one transaction. The record includes `id`, `canonicalUrl`, `threadKey`,
`topicId`, optional `issueId` and `executionId`, `subscriptionId` and
`createdAt`. Replays under the same operation ID compare the canonical URL
and request scope. A new operation for the same URL and full association scope
returns the original record. Topic-level, Issue-level and Execution-level
associations can coexist and share the same Topic–Thread subscription.

Use an explicit absolute HTTP(S) resource URL without embedded credentials.

- For public GitHub, Core accepts `github.com/{owner}/{repo}/pull/{number}`.
  It emits HTTPS with lowercase host, owner and repository, positive decimal
  number and no trailing slash or default port. It removes numbered
  `#issuecomment-N`, `#discussion_rN` and `#pullrequestreview-N` fragments and
  the query parameters `utm_source`, `utm_medium`, `utm_campaign`, `utm_term`,
  `utm_content`, `utm_id`, `gclid` and `fbclid`. Supply the resource URL
  directly for other anchors, query fields or GitHub subpages.
- For other hosts, the caller declares the supplied URL canonical. Core
  validates it and preserves it verbatim, including its path, query and
  fragment. Core performs no redirects, short-link resolution or
  network requests.

Message intake and registration share these URL rules. Admission after one
subscription routes directly to its Topic. Admission with zero or multiple
subscriptions targets Router. When a Message arrived earlier, registration
reuses its Thread and any existing Topic subscription. The earlier routing
history stays as recorded. PR registration does not scan Reports. Source
updates leave Issue and Topic states under explicit business control, and
cancellation leaves the PR and subscription in place. The integration covers
the source contract using synthetic Messages.

## Message replies

`message_reply` atomically creates one pending Outbox item and records the
operation receipt. Use the same `operationId` and parameters to recover the
original receipt. Use `source_query` with `collection: "outbox"` to read current
state. A successful queue receipt confirms local persistence.

Router may give one explicit `messageId` or the distinct `messageIds` of its
current routing input. Topic may give an explicit associated Message or omit
the target to use its oldest associated Message. Core orders the candidates by
its own `receivedAt` then the stable SQLite acceptance sequence. It checks the
chosen Message's channel after selection. No match, missing native address or
an out-of-scope target returns an error. Worker has no reply capability.

The item contains `id`, `messageId` (the Core target), `channel` (the original
Message's native address), `text`, `createdAt`, `authorRole`, optional
`authorId`, `authorBindingId`, `source`, `accountId`, `instanceId` and
`status`. The source/account/instance are fixed at creation. For a Message
admitted with an instance-bound credential, only that instance can send
its replies. A legacy Message or an admission with an ingress-only token
may use the sole configured consumer for its persisted source/account.
An absent or ambiguous consumer fails with `failed_precondition`. The
untrusted `collectorInstance` field does not select a consumer. Rotating the
credential for the same source/account/instance preserves access.

## Trigger Outbox

Use the instance-bound Bearer token for every request. The routes below are
relative to `/v1/channel/outbox`. Each POST accepts one JSON object and
returns a JSON receipt after commit.

| Method and path | Operation | Request and result |
| --- | --- | --- |
| GET base path | listOutbox | Optional `cursor` and `limit` (default 50, max 200). Returns `{items, nextCursor?}` with eligible items in creation order. Restart polling from the first page to see newly eligible items. |
| GET `/items/{outboxId}` | getOutbox | Full item with attempts, task IDs, recorded results and claim history. Claim IDs are omitted. Includes items that are waiting, sent or failed. |
| POST `/begin-attempt` | beginAttempt | `operationId`, `outboxId`. Returns `{outboxId, attemptId, claimId, generation, mode, leaseExpiresAt, taskId?}`. Mode `send` reserves a new attempt. Mode `recover` claims an existing task and names the original `taskId`. |
| POST `/confirm-send` | confirmSend | `operationId`, `outboxId`, `attemptId`, `claimId`. Returns the grant after durably marking that a channel call may start. Requires a live, unused send claim. |
| POST `/renew-lease` | renewLease | Same fields as confirmSend. Returns the current claim with its new deadline. An expired or released claim cannot be renewed. |
| POST `/record-task` | recordTask | Same fields as confirmSend plus `taskId`. Returns the attempt with its native task reference. Use the original send claim even if its lease expired. |
| POST `/finish-attempt` | finishAttempt | Same fields as confirmSend plus `outcome` and result fields below. Returns the attempt with recorded result history. |

All POSTs deduplicate by source, instance, operation and `operationId`. Operation IDs
must match `[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}`. Replays with the same
parameters return the original receipt. Changed parameters return `conflict`.
A claim receipt is private to its consumer. Competing processes use distinct
operation IDs. Reads are non-consuming. A claim may be unavailable by the
time the caller acts on a list entry. Competing active claims return
`conflict`; ineligible work returns `failed_precondition`; an item or claim
outside the authenticated scope returns `not_found`.

**Send and recovery flow**

1. Begin and inspect the claim `mode`. With `recover`, query the original
   `taskId` directly. With `send`, confirm once and call the channel only
   after a successful live confirmation. Keep the actual address from
   `item.channel`.
2. Treat a confirmation replay as a receipt for an earlier transaction.
   Only the still-running claimant that knows it has yet to call the channel
   may proceed, within its live lease. After process loss, start again from a
   fresh list/claim operation. A lease coordinates Core claims. The channel
   can still apply an in-flight send after Core's lease expires.
3. When a new send returns a task ID, record it before polling it. A task
   keeps the item `sending`. Skip new sends while recovering an unresolved
   known task. Renew live claims every 20 seconds for long sends or task
   checks. Each claim has a 60-second lease. If task polling is temporarily
   unavailable, continue querying, allow expiry, or report `unknown` to release
   the current claim and preserve the unsuccessful poll in history.
4. On a definite success, retry its original finish operation until Core
   acknowledges it. Under `outcome: "sent"`, send the returned
   `conversationId` and actual native `messageId` when available. A native
   `messageId` requires the actual `conversationId`. Never use the Core
   target ID, native target Message ID or task ID as the result ID.
5. For `retryable_failure`, `permanent_failure` or `unknown`, send a stable
   `errorCode` with the same identifier format as operationId, and omit native
   result references. Map known permission and invalid/unavailable target
   errors to `permanent_failure`. Use `unknown` for an unresolved send outcome.
   If a task ID is already recorded, `unknown` leaves the attempt in
   `state: "task"` and the reply `sending`. The next claim recovers the
   same attempt and task ID. An inconclusive poll does not consume another
   send or count against the send retry limit. Use `retryable_failure` only
   when the channel confirms that the attempt did not send.

Expiry of a reservation before confirmation permits a fresh claim without
consuming a send attempt. An expired confirmed send with a persisted task
is available for task recovery. An expired confirmed send without a task
or result becomes `unknown`. For a send without a known task, Core waits
60 seconds from expiry or an explicit unknown result before permitting another
send and retains `possibleDuplicate`. Retryable failures can be claimed again. After ten
failed/unknown sends, or a permanent failure of the current attempt, Core
stops automatic sends and records one `outbox.failed` event for the original
author role and owner. Topic business state stays independent of delivery.

Late results enter the identified attempt's history. A verified success can
preempt a reserved send and changes the overall reply to `sent`. An
already-confirmed send keeps its own attempt, task and results. Recovery can
still query such a task. The latest attempt's ID and outcome are independent
of any earlier success. Each recorded result keeps its acceptance time and
claim generation. A later success may add a real message identity that was
unavailable in the first receipt. Every recorded success preserves the
exact source (from the item), conversation ID and actual Message ID.

### Reply history

Observer and the author's `source_query` expose the five item states
`pending`, `sending`, `sent`, `failed`, `unknown`. Use `messageId` or
`threadKey` with the Outbox list to follow a Message or Thread's replies.
Detail includes attempt state, task ID, claim generations and deadlines,
success identities, possible duplicates, the next eligible retry time and
the failure Event ID if present. Observer exposes reads only.

## DWS outbound echoes

On `POST /v1/channel/messages`, an instance-bound DWS adapter can set
`selfSent: true` beside `operationId` and `message` when the original sender's
openDingTalk ID exactly matches its configured own identity. This is a hold
hint. The existing acceptance receipt and native deduplication are preserved.

Core checks successful Outbox history for the same source, actual conversation
ID and actual native message ID. An exact match records `echo.status:
"outbound_echo"` and the matching Outbox/attempt. It does not enqueue role
delivery. SQLite schema 12 backfills successful DWS identities from existing
attempt history and indexes future receipts transactionally.

With a self-sent hint and no exact match, Core may hold routing for up to
60 seconds based on the conversation's confirmed in-flight sends, unresolved
tasks or sends in the unknown recovery window. With no such candidate the
Message is routed normally. Held Messages are already durably accepted.
Each hold records its candidate Outbox and attempt IDs at acceptance.
Definite failures or other fully identified sends clear the hold and route
the input once if every candidate has been resolved before the deadline.
An inconclusive hold becomes `echo_unknown` and still has no routing delivery.
An exact result arriving later can settle it as `outbound_echo`.

Use `GET /v1/observer/messages/{id}` for `echo.status`, the deadline, candidate
links, the precise match and any routing event ID. The original acceptance
event is retained for auditing. A locally authenticated operator can route
an `echo_unknown` Message once after reconciliation:

```sh
bin/superagent release-echo MESSAGE_ID --endpoint http://127.0.0.1:24370 \
  --operator-token-file /absolute/private/operator.token --operation-id OPERATION_ID \
  --reason "Verified as user input"
```

The CLI uses `POST /v1/operator/echo-release` with `messageId`, `operationId`
and `reason`, authenticated with `execution.operatorTokenFile` on the
loopback listener. A replay of that release returns its recorded routing
event. A later match on an already routed Message is recorded for inspection
without another delivery.

## Harness relationship

The existing MASS harness remains authoritative for massd Runtime/Session and
input receipts. The approved superagent spec and this API describe the new
business Core, SQLite schema and role surface, which the old mass-sa harness
does not yet model. This ticket does not rewrite harness files or add an old
mass-sa/JSON compatibility layer.
