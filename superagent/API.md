# Core v1 API

This document describes the current Core implementation.

## Start and ownership

`superagent serve --config FILE` acquires exclusive ownership of
`dataDir`, opens `core.sqlite` and starts HTTP. The default directory is
`./superagent-core-data` relative to the YAML file. Startup prints one JSON
object with `endpoint`, `observerEndpoint`, and `status: ready`.
Schema 20 uses SQLite WAL, FULL synchronous durability and foreign keys.

The database has nine business tables. `threads` and `trigger_messages`
record ingress and Thread subscriptions. `topics`, `issues`, `executions`,
`session_bindings` and `scheduled_tasks` record work, ownership and timing.
`internal_deliveries` records delivery to Sessions. `outbox` records
external replies. Reports and PR associations are embedded and exposed
through read views. `capabilities/` contains private role startup files.

Choose a new empty `dataDir` when upgrading from earlier schemas or
mass-sa JSON. Startup rejects those directories and leaves their contents
intact. Keep the old directory to retain access through its original version.

SIGTERM/SIGINT stop admission and wait up to ten seconds for active HTTP
requests. Accepted records survive normal or forced exit. After a lost
response, retry ingestion by original native message identity.

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
{"status":"accepted","duplicate":false,"messageId":"msg_...","threadKey":"dws:native-conversation-id","acceptedAt":"2026-11-18T01:02:03.000000000Z","eventId":"dlv_..."}
```

Message, Thread update and the initial internal delivery commit together.
The receipt identifies the accepted Message and Thread. Its optional
`eventId` is the initial delivery ID for routable input and can be read
through `/v1/observer/routing-deliveries/{id}`. Echo handling may admit a
Message without a delivery ID.

Zero or multiple Thread subscriptions enqueue a `route_request` to Router.
Exactly one queues a `routed_message` to that Topic. Both reference the
stored original Message. Initial delivery status is `pending`. The internal
delivery becomes `sent` after massd accepts the Session input.

Admission deduplicates by source, account and native `sourceMessageId`.
Duplicate intake returns the original Message ID, Thread key, acceptance time
and initial delivery ID with `duplicate=true`. Changing text, actor, Thread,
reply address or other business content under that native identity conflicts.
Canonical comparison sorts JSON object keys and normalizes absolute source
time and quoted JSON. Collection timestamps, collector instance and raw
source event envelope IDs are excluded from comparison. `operationId` is a
required caller correlation value.

## Errors and uncertain responses

Errors are JSON `{"error":{"code":"...","message":"...","retryable":false}}`.
HTTP mapping: invalid_argument=400, unauthorized=401, forbidden=403,
not_found=404, conflict/failed_precondition/unsupported/stale_binding=409, unavailable=503.
All error bodies omit credentials and raw storage/upstream errors.

Missing native identity fails invalid_argument. Wrong authenticated source or
account fails forbidden. Storage errors never return accepted. An unavailable
storage result or lost connection must be reconciled by retrying the same
operationId; do not create another native identity. A response lost after commit
can be replayed after restart with its original native identity.

## Read-only observation

No write credential is needed or sent by these trusted-reader endpoints:

- GET/HEAD `/v1/observer/health`
- GET/HEAD `/v1/observer/messages[/{id}]`
- GET/HEAD `/v1/observer/threads[/{escaped-key}]`
- GET/HEAD `/v1/observer/topics[/{id}]`
- GET/HEAD `/v1/observer/topic-activity?topicId={id}&filter=all|message|issue`
- GET/HEAD `/v1/observer/issues[/{id}]`
- GET/HEAD `/v1/observer/executions[/{id}]`
- GET/HEAD `/v1/observer/reports[/{id}]`
- GET/HEAD `/v1/observer/timers[/{id}]`
- GET/HEAD `/v1/observer/pull-requests[/{id}]`
- GET/HEAD `/v1/observer/outbox[/{id}]`
- GET/HEAD `/v1/observer/router-messages`
- GET/HEAD `/v1/observer/routing-deliveries[/{id}]`
- GET/HEAD `/v1/observer/subscriptions[/{id}]`
- GET/HEAD `/v1/observer/relations[/{id}]`
- GET/HEAD `/v1/observer/bindings[/{id}]`
- GET/HEAD `/v1/observer/internal-deliveries[/{id}]`
- GET/HEAD `/v1/observer/session-preview/{bindingId}/history`
- GET/HEAD `/v1/observer/session-preview/{bindingId}/live`
- GET/HEAD `/observer/` and its static assets

Topic activity merges routing deliveries and saved Issue facts in descending `(at, id)` order. The filter applies before pagination. `limit` defaults to 20 and accepts 1–200. `nextCursor` binds the Topic, filter and first-page watermark. Each row identifies its source record. An `issue_snapshot` describes the saved state at `updatedAt`. Lifecycle entries use the timestamp on the saved transition.

Health reports `ingress: ready` for Core HTTP intake. It shows Router
preparation, Topic and Issue business states, unknown native stops and echoes,
and counts by internal delivery and Outbox status. Use the Trigger's local
status command and logs for channel readiness and admission health.

Router is `not_started` when execution is
omitted, or its persisted `pending/ready/blocked` preparation state. Messages
include current `routing: {round,state}`. Observer/Router projections also include
source reception/return relations. Topic-role Message queries omit other Topics'
relations. Thread details expose key, ref and timestamps. Bindings expose fixed
role, owner, generation, workspace, namespace/name, `runtimeRef`, `model`,
optional `effort`, actual Session IDs,
preparation state/error and next attempt time, never token or MCP capability files.
Internal delivery details expose sender, recipient, content references, status
and the Session acceptance timestamp.

Lists return `{"items":[...],"nextCursor":"opaque"}`. Default limit 50,
valid range 1–200. Messages and subscriptions accept an exact `threadKey`.
Issues, Timers, PR associations, routing deliveries, routing relations and subscriptions accept `topicId`. Routing deliveries also accept `messageId`. A delivery includes its sender, recipient, content references, status and return link.
Executions, Reports and PR associations accept `issueId`. Internal deliveries accept the same filter for explicit Issue references. Their preview marks recorded goal-update notifications with `goalUpdate` and includes the target revision. Supply one parent
filter at a time. The Observer also supports `topicId` on Outbox to find replies to messages in the
Topic's routing history. Outbox and routing relations accept `messageId`. Observer Issues accept
`messageId` to select Issues with an explicit matching material across all Topics. Message-filtered
Issue and routing-delivery previews include the owning or participating Topic's title when available.
Observer lists accept `preview=true` for compact rows. Message, reply and Timer
previews contain up to 240 characters. Open a record by ID for its full body
and evidence. Continuation
cursors bind collection, filter and the first page's sequence high-water mark.
Rows accepted later never enter that traversal. Ordering uses fixed nanosecond
UTC Core timestamps plus stable ID (Thread key for Threads). Thread ordering
uses immutable `createdAt`. `lastMessageAt` reflects the last accepted Message
and is omitted until the Thread receives its first source input.
Router Messages default to 20 rows and accept an exact `messageId`. Rows represent
distinct Messages with a request to or delivery from the Router. They sort by the
most recent qualifying delivery registration time and Message ID, newest first.
The delivery sequence watermark limits activity before grouping and pagination.
Rows expose the source delivery ID, current routing state, target delivery IDs,
and reason previews. Read the Message's routing deliveries for full reasons and
return references. An ignored Message has no persisted ignore reason or decision
timestamp.

A fresh first-page request refreshes the snapshot. Cursors are opaque reader
state, not capabilities; malformed or scope-mismatched cursors fail. Set
`order=newest` on Observer lists for descending time and ID order. Reuse the
same order when following a continuation cursor.

`superagent observe COLLECTION [id] --endpoint ORIGIN --limit N --cursor TOKEN`
prints the same JSON. List-filter flags are `--thread-key`, `--topic-id`,
`--issue-id` and `--message-id`. Use `--newest` for descending order. `observe health` prints
health. COLLECTION is any collection listed above. The read-only CLI never loads channel config or source token files.
Use `observe topic-activity --topic-id ID --filter message` for Topic receipts and returns. The filter also accepts `issue` or `all`.
Only a numeric HTTP origin is accepted; redirects are not followed.

All Observer writes return 405 with Allow: GET, HEAD. Host must match the
connection's actual numeric local endpoint; foreign Origin and cross-site Fetch
Metadata are rejected. No query parameter grants write authority. No CORS
credentials or browser-side write token is configured. Cache-Control is no-store.
Reads preserve Core business state.

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

Execution is optional for intake-only operation. To enable it, configure the
Router workspace, fixed role profiles and at least one Worker template.

```yaml
execution:
  massdEndpoint: ws://127.0.0.1:24368/rpc
  routerWorkspace: /absolute/workspace
  namespace: superagent
  roles:
    router:
      runtimeRef: codex-fast
      model: gpt-6.2-sol
      effort: low
    topic:
      runtimeRef: codex-standard
      model: gpt-6.2-sol
  workerTemplates:
    - runtimeRef: codex-deepseek
      model: deepseek-chat
      description: Implement and verify code.
      effort: medium
    - runtimeRef: codex-standard
      model: gpt-6.2-astra
      description: Evaluate architecture and diagnose complex problems.
  # tokenFile: /private/massd.token  # omit only for explicit local massd access
  # operatorTokenFile: /private/operator.token  # enables local input recovery
  # mcpCommand: /absolute/bin/superagent  # defaults to this executable
```

Both role profiles require a legal Runtime name and a nonblank model.
A Worker template also requires a nonblank description. Each
`(runtimeRef, model)` pair must be unique in the list. Several templates
may use the same Runtime with different models. Effort is optional, and model
and effort are passed literally to massd. A configured pair permits a
Topic to select it. Core checks Runtime availability during Session preparation.

Namespace is a DNS label of at most 24 bytes. The Router is unique per Core
store, in `<namespace>-router/router-g1`. Each Topic has a stable namespace derived from its ID and Session `topic-g1`.
A Worker uses its Topic namespace and a fixed
`worker-<executionId with underscores changed to hyphens>-g<generation>` name.
Each binding stores the selected Runtime, model and any effort, along with the
workspace and other Session preparation parameters. Core sends `runtimeRef`
and `config: {model, effort?}` in the MASS Session spec. The template list
and descriptions live in the startup YAML. On restart, compatible execution
configuration preserves binding identities. A changed Router profile fails
startup with `failed_precondition`. Existing Topic generation specs stay
fixed. A newly created Topic binding uses the current Topic profile. Enabling execution on an intake-only
directory processes its pending Router deliveries.

Use a fixed listener port with execution enabled and keep that endpoint stable
for existing role MCP processes. An occupied configured port fails startup.
Core ownership is still single
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
| `message_return` | Forbidden | Own routed delivery | Return request below |
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
    {"topicId": "topic_existing", "messageIds": ["msg_b"]}
  ]
}
```

Each target requires exactly one `topicId` or `create` and a nonempty subset
of the requested Messages. The Router can select multiple Topics per Message.
Core checks the current routing round, which is derived from return requests.
A new selection requires pending routing. An identical active Message/Topic
pair reuses its `routed_message` delivery. New Topic creation, preparation,
Thread subscription, Message routing state and delivery registration commit
in one transaction. Success returns the resolved Topic IDs and each target's
`deliveryIds` map keyed by Message ID. Reuse `operationId` when retrying an
identical route, including Topic creation.

`kind: ignore` requires a reason and no targets. Core sets `routing_state`
to `ignored` when no target is active. If an active target remains, the
state is `routed`.

## Return and terminal-Topic behavior

Topic calls `message_return` with `{deliveryId,reason}`. The ID identifies
one `routed_message` addressed to the calling Topic. Core atomically records
a `route_request` to Router with the Message reference, original delivery
reference and reason. An unconfirmed original becomes `superseded`. A
confirmed original retains `sent` and the return is visible through its
request. Retrying by the same delivery ID returns the original request.
Other targets and Thread subscriptions continue to apply. The Router can
explicitly select the same Topic again.

Router and Topic can use `source_query` with collection `routing-deliveries`
and an optional `messageId` to read delivery history. Topic sees only its own
receipts and returns. Its `messages` collection returns each associated
Message once, including after a reroute.

Subscription selection includes completed/canceled Topics. Receiving, preparing
or waking a Topic preserves its business status. The `topic_complete` command
is described below. Routing keeps Issue ownership within its original Topic.

## Issue, Execution, Report and acceptance

All changes require an `operationId` scoped to the authenticated binding and
tool. The HTTP role bridge and stdio MCP call the same transactions.
Creation commands and Reports use stable business IDs. Reusing their
operation ID with different parameters returns `conflict`.
Revision-guarded updates return `conflict` when the expected revision is stale.
After a lost response, read the current resource and any associated
delivery before submitting another mutation.

### Topic tools

A new Topic can supply `create.goal` in `route_decide`. Router reads it in
`topic_query` summaries. If omitted, Core uses the description or title.

| Tool | Arguments | Result |
| --- | --- | --- |
| `execution_template_query` | `{}` | `{items: [{runtimeRef, model, description, effort?}, ...]}` in YAML order. Only the Topic role can call this tool. |
| `issue_create` | `operationId,title,goal,workspace,runtimeRef,model`, optional `materials,start` | `{issue, execution?}`. The Issue belongs to the authenticated Topic and defaults to `pending`. |
| `issue_start` | `operationId,issueId,expectedRevision`, optional `resolve` and `runtimeRef,model` as a pair | `{issue, execution}`. Schedules an Execution after all blockers are resolved. The call can resolve blockers and start in one transaction. |
| `issue_update_requirements` | `operationId,issueId,expectedRevision,goal`, optional `notifyWorker` | `{issue, notificationEventId?}`. Saves the goal and can explicitly continue the active Execution. |
| `issue_update_blockers` | `operationId,issueId,expectedRevision`, at least one of `add,resolve`, optional `notifyWorker`, and `unblockedStatus` when resolving the last blocker | `{issue, notificationEventId?}`. Updates the specified blockers and records the resolution basis. |
| `issue_worker_update` | `operationId,issueId,expectedRevision,mode`, optional `note,materials` | `{issue, notificationEventId}`. Sends background or explicitly continues the current Execution. |
| `work_query` | `collection: issues/executions/reports/timers/pull-requests`, with `id` for detail or `cursor,limit` for a list. Execution, Report and PR lists accept `issueId`. | Records in the bound Topic. |
| `pr_register` | `operationId,url`, optional `issueId` | Saved PR association and subscription ID. |
| `issue_complete` | `operationId,issueId,expectedRevision,reason,evidence` | `{issue, execution?}`. Saves acceptance and retires the current Execution. |
| `issue_cancel` | `operationId,issueId,expectedRevision,reason` | `{issue, execution?}`. Cancels a nonterminal Issue and retires its current Execution. |
| `issue_reopen` | `operationId,issueId,expectedRevision,reason` | `{issue}`. Reopens a terminal Issue with its blockers. A later explicit start is required. |
| `topic_complete` | `operationId,expectedRevision,reason,evidence` | Topic with its saved acceptance. Every Issue must already be terminal. |
| `topic_cancel` | `operationId,expectedRevision,reason` | Cancels the Topic and its nonterminal Issues, retires the Topic binding and requests stops. |
| `topic_reopen` | `operationId,expectedRevision,reason` | Reopens the Topic on its current binding. |

Supply a nonblank Issue goal and an absolute existing workspace.
`requirements` is a legacy input alias for `goal`. Each material is
`{label?,uri}` or `{label?,messageId}`. The Message must already belong to the
Topic's reception history. A stale `expectedRevision` returns `conflict` with
`resource` and `currentRevision`.

For each explicit selection, Core validates the complete Runtime and model
pair against the current list and takes effort from that entry. An incomplete
or unlisted pair returns `invalid_argument`. An `issue_start` with neither
field uses the saved Issue profile, even if the template changed or was
removed. Issue reads include `executionProfile: {runtimeRef, model, effort?}`,
including through the Worker's `issue_get_current`. An identical create retry
returns the original Issue identity and, for `start: true`, its first Execution.

`start: true` uses the same scheduling transaction as `issue_start`. Core
saves the Issue's profile, the new Execution, its Session binding, and a
`worker_started` internal delivery together. Another start with a current
Execution returns `conflict`. The Execution records the goal revision at
startup. Its details project the Session binding as `preparation`, so
historical Executions retain their own Runtime, model and effort after
another start changes the Issue's profile. Execution details also project
the `startup` delivery state.

Completion requires a reason and at least one evidence reference, such as a
Report ID or artifact path. Core stores the acceptance author and timestamp.
Topic verifies the result before issuing the explicit acceptance command.

### Goal, blockers and Worker inputs

`expectedRevision` compares with `issue.revision`. `goalRevision` starts at 1
and advances only when the goal text changes. The Issue revision also tracks
changes to blockers, materials and lifecycle state.

An `add` entry is `{kind:"human",reason}` or
`{kind:"dependency",reason,dependencyIssueId}`. Dependency targets must exist
in the same Topic, and the edit must preserve acyclic dependencies.
Each `resolve` entry is `{blockerId,reason,messageId?}`. Core validates the
current Issue and open blocker IDs and retains the basis in `resolvedBlockers`.
An associated Message can serve as the basis and becomes Issue material.
Any remaining blocker keeps the Issue blocked. On the final resolution, the
Topic chooses `pending` or `active`. Pending retires any current Execution
so the Topic can arrange a later start. Active requires a current Execution.
Operator replacement retires a blocked Execution after resource release.
The Topic can arrange a fresh Execution once its blockers are resolved.
For an active Execution, the Topic can use `notifyWorker: true` in the same
operation, or use `issue_worker_update` later.

Use `mode: "context_supplement"` with a note or material to send background
to the current Worker via Inject. Use `mode: "continue"` on an active Issue
with all blockers resolved to send `worker_update` via StartOrSteer.
An explicit `notifyWorker` on a goal or blocker edit sends the same kind of
work update. Core rechecks the target Execution, goal revision and blockers
before admitting that update. The receipt field `notificationEventId` holds
the stable internal delivery ID.

When an Issue completes, cancels or reopens, Core can register an
`issue.dependency_changed` fact in a `context_supplement` for its Topic.
The Topic reads current dependencies and decides whether and how to proceed.

Issue detail includes the most recent explicit goal notification in
`latestRequirementsNotification`, with delivery status and goal version.
A current Execution also has a read-only MASS `workerSession` observation,
including observation time or an unknown-state reason.

### Worker tools

| Tool | Arguments | Result |
| --- | --- | --- |
| `issue_get_current` | `{}` | Bound Issue and current Execution. |
| `issue_material_get` | `{messageId}` | Original Message listed in the Issue materials. |
| `report_submit` | `operationId,kind,summary,goalRevision`, optional `details,evidence,questions` | Appended Report with its Topic notification ID. |
| `pr_register` | `operationId,url` | PR link on the current Issue and its parent Topic. |
| `delivery_get` | `{delivery_id}` | Full content and references of an internal delivery addressed to the authenticated binding. |

Report kinds are `progress`, `ready`, `blocked` and `failed`. The Worker
supplies the goal revision it used. `requirementsRevision` remains a legacy
input alias. Core derives ownership and the generation from the authenticated
Session binding.

The Report is appended to `issue.reports` in the same transaction as its
Topic notification. Current Reports register `report_submitted` with a
reference to the Issue and Report. A late Report uses the retired Execution's
identity and revision bounds, is marked `historical` and sends
`context_supplement` to the Topic. Scoped Report queries and Observer history
still read by Report ID. Topic owns acceptance, regardless of Report kind.

### Worker work observation and report reminder

Core opens a live observation before dispatching an eligible Codex Worker
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

Core records a successful Claude write (`applied_best_effort`, `sent=true`)
as `workObservation.state=delivered`. Its end stays unconfirmed, so this
receipt alone cannot schedule a reminder. A delivered Codex input without a
turn ID or live subscription also stays `delivered` with an observation gap.
An interruption during live observation changes the state to `unknown`.
Core does not resubscribe the original attempt. Core restart marks unfinished
observation as `core_observation_interrupted`. A newly accepted input can
establish fresh observation. Core does not reconstruct terminal facts from history, idle,
final text, control acknowledgment or EOF.

When the current Execution has no Report, a correlated end and one
`report_reminder` commit together. It targets the same Worker Execution and
asks for a Report about existing work. The Worker can use the delivery ID
as its Report operation ID. The marker stays set after delivery, supersession
or replay. A Report for the current Execution supersedes an unconfirmed
reminder in the same transaction. Core rechecks that Execution and its
Reports before dispatch. A retry preserves the delivery ID.

Execution reads project `workObservation` with the binding and Session
identities, attempt ID, work reference if known, continuity, the observed end
or the observation gap. They also project `reminderScheduled`,
`reminderEligibility` and `reminder` with its delivery status when scheduled.
Observer links to the delivery and separately shows Issue, Topic and stop status.

### Retirement and stop observation

Issue completion and cancellation supersede pending Worker input and revoke
the Execution's authority in the business transaction. Cancellation also
persists a stop request for the retired Execution. Completion requests a
stop when it finds admitted input. A late acceptance leaves an internal
delivery that was already superseded in that state. Topic cancellation performs these changes for each
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
to pending or stays blocked by open blockers. A later explicit start creates
the next Execution. A start requires
proof that prior preparation never began or that the original native owner
was released. It assigns a fresh Execution ID, generation and credential.
When the previous Session was verified reusable, Core can reuse it only with
the same workspace, Runtime, model, effort and namespace.
A canceled Topic still accepts new Messages through its subscriptions.
Messages wait durably while the Topic has no current Session. After settlement,
they reach the next Topic generation with the same IDs. The Topic stays
canceled until that owner explicitly calls `topic_reopen`. The previous Topic
credential can replay its exact cancellation receipt.

## Durable input delivery

Internal deliveries use `schemaVersion: 3` with ordered `events`.
Each event carries `delivery_id`, `kind`, `sender`, `recipient` and
`content`. The sender and recipient carry a role kind and, when applicable,
a `topic_id` or `execution_id`. Deduplicate by `delivery_id`. A truncated
event carries `truncated: true` and a `content_ref` for `delivery_get`.
All-`context_supplement` batches use `session.inject`; any other kind
selects `session.prompt` (StartOrSteer). Acceptance settles the rows as
`sent`. A restart can submit unconfirmed rows again using the same IDs.
Worker startup and updates, Report notifications, Report reminders and Issue
dependency changes use this path. The Observer exposes the acceptance state
and timestamp separately from work completion.

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
stable IDs, apply current delivery eligibility and enqueue a recovery notification.
A canceled Topic receives that recovery notification as context. Sent
internal deliveries keep their original ID and timestamp.

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
holds the current binding in `recovering`. Inspect the binding for its latest recovery or retry with the same
arguments. The latest completed recovery returns its saved result on replay.

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
`timer_set` uses the operation ID to replay the same Timer identity.
Updates and cancellations compare the current revision.
Reusing it with different parameters or using a stale revision fails with
`conflict`. Changes to a current `fired`, `canceled` or `invalidated` Timer
fail with `failed_precondition`. Use a new `timer_set` for another followup.

Core scans on startup and every 250 ms while execution is enabled. A valid past
time is eligible on the next scan. The first valid transaction to commit
decides a race between firing, updating, canceling and Topic termination.
Firing keeps the selected revision and commits `firedAt`, the task Thread,
its Topic subscription, and one Trigger Message with its routing delivery in
the same transaction. The internal source is `core:timer`. The task ID is both
the native Thread ID and `sourceMessageId`. The Message text is the followup
reason. Its `meta` includes `type: timer.followup`, `taskId`, `topicId`,
`taskRevision`, `dueAt` and any task materials. The Message carries no
external reply address.

The usual single-subscription rule routes the Message to the Topic. Event
Delivery submits the `routed_message` through StartOrSteer. Use `source_query`
to read the Message reference and `work_query` to check the current task and
Topic work before deciding what to do.

Completing or canceling the Topic atomically invalidates scheduled Timers and
supersedes unconfirmed followup deliveries. Accepted deliveries and their
Message and Thread remain readable. Reopening preserves the task and delivery
history. On restart, due scheduled Timers can fire and valid unconfirmed
deliveries use the same delivery ID on retry.

`work_query` with `collection: "timers"` lists Timers in the bound Topic or
reads one by `id`. Observer and `observe timers --topic-id TOPIC_ID` expose the
revision, due and fired times, state, `triggerMessageId`, `threadKey` and
`followup` routing delivery.

## Pull request registration and subscriptions

Call `pr_register` from Topic or Worker stdio MCP, or through the corresponding
authenticated role bridge. A Topic supplies `issueId` only when associating a PR
with an Issue it owns. A Worker registers for its currently bound Issue and
can do so before submitting the Report. Core checks current Worker ownership
before querying or reusing the current PR association.

Core saves the canonical URL in the Topic's `pullRequests` and, when scoped
to an Issue, in that Issue's `pullRequests`. Both writes, the Thread, the Topic
subscription share a transaction. A link returns
`id`, `canonicalUrl`, `threadKey`, `topicId`, `subscriptionId` and
`createdAt`. Issue links also carry `issueId` and the initiating
`executionId` if a Worker registered that link. Registration reuses the link for the same canonical URL and Topic or Issue,
including across Worker generations. Multiple Issues can share one parent Topic–Thread
subscription. Each further Topic has its own subscription.

The read-only `pull-requests` collection projects the links from Topics and
Issues. Filter by `topicId` for the Topic's followed URLs or by `issueId` for
the Issue's URLs. The PR details link to the shared Thread and the Topic
subscription.

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

`message_reply` atomically creates one pending Outbox item. Use the same
`operationId` and parameters to retrieve the same item with its current state.
Use `source_query` with `collection: "outbox"` to read authorized replies.
A successful queue receipt confirms local persistence.

Router may give one explicit `messageId` or the distinct `messageIds` of its
current routing input. Topic may give an explicit associated Message or omit
the target to use its oldest associated Message. Core orders the candidates by
its own `receivedAt` then the stable SQLite acceptance sequence. It checks the
chosen Message's channel after selection. No match, missing native address or
an out-of-scope target returns an error. Worker has no reply capability.

An Outbox item carries `id`, `messageId`, `text` and `status`, with optional
`receipt` and `blockerRef`. The status is `pending`, `sent` or `failed`.
A Topic can set `blockerRef: {issueId, blockerId}` for one open human blocker
in its own Issue. Router replies cannot set this field.

### Quoted answers

For DWS, Core matches the inbound `quotedMessage.message_id` against the
native `receipt.messageId` of a sent question. Source, account and the inbound
native conversation must match. If `quotedMessage.conversation_id` is provided,
it must also match. Topic `source_query` on an associated Message returns
`quotedReply: {outboxId, blockerRef}` for a unique match within its Topic.
The lookup takes effect as soon as the send receipt is saved, including for
answers already admitted.

A match identifies the original blocker. The Topic reads the current Issue
and decides whether the answer is sufficient. With no match, the Topic
evaluates which questions the user has clearly answered and requests
clarification when needed. Unanswered blockers stay open. Pass the reply
Message ID as the basis when calling `issue_update_blockers`. Core rejects a
matched quoted Message as the basis for any other blocker, including a
replacement blocker for the same question. To explicitly continue the current
Execution after resolving the last blocker, pass `unblockedStatus: "active"`
and `notifyWorker: true`. The resolution and `worker_update` commit together.

## Trigger Outbox

Core resolves the account and address from the original Message. For each
account, run a single active sender and stop it before starting a replacement.
Configure the Core source with the sender's `instanceId` and Bearer token. If
there is more than one instance-bound credential for an account, set
`outboxConsumer: true` on exactly one. The sole instance-bound credential is
selected by default. Ingress credentials for the same account can share
Message intake. Only the selected one can pull or update Outbox. A different
account's item IDs return `not_found`.

| Method and path | Result |
| --- | --- |
| GET `/v1/channel/outbox` | A page of eligible replies ordered by insertion. Pass optional `limit` (default 50, max 200) and `cursor`. Pull is read-only. |
| GET `/v1/channel/outbox/items/{id}` | One reply in the account, including terminal states. |
| POST `/v1/channel/outbox/result` | Update one reply and return its committed {id, status, receipt?}. |

Channel reads join `source`, `accountId` and `channel` from the original
Message. Query results include pending replies with and without known tasks.
Failed replies with an elapsed `receipt.retryAt` are eligible for retry.

Result writes accept `Content-Type: application/json` with `outboxId`,
`expected: {status, receipt?}` from the read, and `outcome`:

| Outcome | Additional fields and behavior |
| --- | --- |
| `pending` | Save `taskId` before querying. Subsequent pulls can query the same native task. |
| `sent` | Preserve the current task ID when present. Save the actual native `conversationId` and `messageId` if available. A native message ID requires its conversation ID. |
| `retryable_failure` | Save `errorCode` and schedule retry after 60 seconds. |
| `permanent_failure` | Save `errorCode` and notify the author through Event Delivery. |
| `unknown` | Save `errorCode`, mark the receipt uncertain, and schedule retry after 60 seconds. Use when the send result is unknown and its task cannot be queried. |

Final results for a recorded pending task include that task's ID. Temporary
query failures leave this task pending. Retry the identical result write until
Core acknowledges it. Core applies the update only to the expected state and
returns the saved result for an identical replay. Stale updates with a
different result return `conflict`. The receipt can retain `uncertain` across
retries when a previous send could have succeeded. After process loss before
any task or success was saved, a replacement can send the same Outbox row
again using the same channel idempotency key.

### Reply history

Observer and the author's `source_query` show pending, failed and sent replies.
List by `messageId` or `threadKey` to read the associated history. Details show
the original Message, any blocker reference and available channel receipt.

## DWS outbound echoes

On `POST /v1/channel/messages`, an instance-bound DWS adapter can set
`selfSent: true` beside `operationId` and `message` when the original sender's
openDingTalk ID matches its configured native identity.

Core checks sent Outbox receipts for the same source, account and actual
native conversation/message pair. An exact match records
`echo.status: "outbound_echo"` and the matching Outbox ID. It stays out of
role delivery. A self-sent Message without an exact match may wait up to 60
seconds when its conversation has a pending reply or an uncertain send in its
retry window. Held Messages are durably admitted. When all candidate replies
settle with known nonmatching results, Core clears the hold and routes once.
An unresolved hold becomes `echo_unknown`. An exact result arriving later can
still settle it as `outbound_echo`.

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
