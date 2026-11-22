# Core v1 API

This document describes the current Core implementation.

## Start and ownership

`superagent serve --config FILE` acquires exclusive ownership of
`dataDir`, opens `core.sqlite` and starts HTTP. The default directory is
`./superagent-core-data` relative to the YAML file. Startup prints one JSON
object with `endpoint`, `observerEndpoint`, and `status: ready`.
Schema 25 uses SQLite WAL, FULL synchronous durability and foreign keys.

The database has eight business tables. `threads` and `trigger_messages`
record ingress and Thread subscriptions. `topics`, `issues`,
`session_bindings` and `scheduled_tasks` record work, ownership and timing.
`internal_events` records delivery to Sessions. `outbox` records
external replies. Reports and PR associations are embedded and exposed
through read views.
When tracing is enabled, Core stores trace associations. Core keeps private
role credentials in `dataDir/mcp-files`.

Core upgrades schema 24 in one startup transaction. The upgrade drops
`mcp_tool_calls` and `mcp_mutation_receipts`. It preserves business records,
internal events and saved Trace associations. A failed startup validation
rolls back both table removals and the schema version change.

Choose a new empty `dataDir` for schema 23 or earlier, or for JSON v4 state.
Startup rejects those directories and preserves their contents.
Also use an empty `dataDir` when switching from Core's stdio role MCP.
Keep the old directories for access through their original versions.
If both directories use the same massd, configure a distinct
`sessions.namespace` for the new directory.

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
can preserve other adapters' native addressing fields. For `type=dws`,
`conversationId` and `messageId` must match the native Thread and Message IDs.
When present, `actorId` must match `actor`. DWS direct and self-chat Messages
use `addressed=direct`. DWS group Messages use `addressed=group` with the native
group conversation ID in `threadRef.id`. A reply-enabled group supplies
`channel.type=dws`, `channel.actorId=actor`, and both `groupOpenConversationId`
and `senderOpenDingtalkId` in `channel.address`. These entries must match
`threadRef.id` and `actor`, respectively. Inbound-only groups omit `channel`.

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
{"status":"accepted","duplicate":false,"messageId":"mass-m1","threadKey":"dws:native-conversation-id","acceptedAt":"2026-11-18T01:02:03.000000000Z","eventId":"evt_..."}
```

Message, Thread update and the initial internal event commit together.
The receipt identifies the accepted Message and Thread. Its optional
`eventId` is the initial event ID for routable input and can be read
through `/v1/observer/routing-events/{event_id}`. Echo handling may admit a
Message without a event ID.

Zero or multiple Thread subscriptions enqueue a `route_request` to Router.
Exactly one queues a `routed_message` to that Topic. Both reference the
stored original Message. Initial event status is `pending`. The event
becomes `sent` after massd accepts the Session input.

Admission deduplicates by source, account and native `sourceMessageId`.
Duplicate intake returns the original Message ID, Thread key, acceptance time
and initial event ID with `duplicate=true`. Changing text, actor, Thread,
reply address or other business content under that native identity conflicts.
Canonical comparison sorts JSON object keys and normalizes absolute source
time and quoted JSON. Collection timestamps, collector instance and raw
source event envelope IDs are excluded from comparison. `operationId` is a
required caller correlation value.

`GET /v1/channel/messages?sourceMessageId={encoded-native-id}` uses the same
source credential and returns the stored acceptance receipt with `duplicate=false`.
The lookup scopes the native ID to the authenticated source and account and
returns `not_found` when the scoped Message is absent. It returns no Message
body. POST and GET receipts include `echoStatus` when Core has classified the
Message. A duplicate POST returns the current echo classification with the
original acceptance facts.

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
- GET/HEAD `/v1/observer/performance`
- GET/HEAD `/v1/observer/traces/{traceId}`
- GET/HEAD `/v1/observer/messages[/{id}]`
- GET/HEAD `/v1/observer/threads[/{escaped-key}]`
- GET/HEAD `/v1/observer/topics[/{id}]`
- GET/HEAD `/v1/observer/topic-activity?topicId={id}&filter=all|message|issue`
- GET/HEAD `/v1/observer/topic-activity?mode=conversation`
- GET/HEAD `/v1/observer/issues[/{id}]`
- GET/HEAD `/v1/observer/reports[/{id}]`
- GET/HEAD `/v1/observer/timers[/{id}]`
- GET/HEAD `/v1/observer/pull-requests[/{id}]`
- GET/HEAD `/v1/observer/outbox[/{id}]`
- GET/HEAD `/v1/observer/router-messages`
- GET/HEAD `/v1/observer/routing-events[/{event_id}]`
- GET/HEAD `/v1/observer/subscriptions[/{id}]`
- GET/HEAD `/v1/observer/relations[/{id}]`
- GET/HEAD `/v1/observer/session-bindings[/{id}]`
- GET/HEAD `/v1/observer/internal-events[/{event_id}]`
- GET/HEAD `/v1/observer/session-preview/{sessionBindingId}/history`
- GET/HEAD `/v1/observer/session-preview/{sessionBindingId}/live`
- GET/HEAD `/observer/` and its static assets

Use `superagent observe session-bindings --issue-id ISSUE_ID` to list an
Issue's SessionBindings, or `superagent observe session-bindings ID` to read one.

`trace=true` includes saved Trace associations in supported Observer record
reads. Topic activity timelines include the associations with each event.
An Issue has a creation Trace link when Core saved its creation association.

`/traces/{traceId}` returns spans from the OpenObserve stream configured under
`observerTrace`. Specify a 32-character hexadecimal Trace ID. Lookup covers
the past seven days by default, or seven days on either side of an optional
RFC3339 `at` timestamp. The response includes at most 1,000 spans and marks
partial results. The Observer links to `/observer/?perspective=trace&id={traceId}`.

`GET /v1/observer/performance` queries OTel time series in OpenObserve. `window`
accepts `15m`, `1h`, `6h` and `24h` and defaults to `1h`.

- `metric=process` is the default and returns `cpu`, `rss`, `goroutines` and
  `file_descriptors`. Select a single chart using one of those IDs. Filter by
  `service` or `instance`. `groupBy` accepts `instance` (default), `service`
  and `all`. Service totals sum the available process instances.
- `metric=mcp_rate`, `mcp_failure_rate` and `mcp_p95` query the MCP tool duration
  histogram. Filter by `role`, `tool`, `action` or `outcome` and group by one of
  those dimensions, or use `groupBy=all`. The default group is `role`. Failure
  rate supports grouping and filtering by `role`, `tool` or `action` and uses
  all outcomes in the denominator.

The response includes `from`, `until` and `stepSeconds`. Each series has a
`dimensions` object and `points` containing `[unixSeconds, value]` pairs.
Values are cores for `cpu`, bytes for `rss`, counts for `goroutines` and
`file_descriptors`, calls per second for `mcp_rate`, a fraction from 0 to 1 for
`mcp_failure_rate`, and seconds for `mcp_p95`. An empty series list indicates
no samples. An `unavailable: true` metric indicates a query failure.

Topic activity merges routing events and saved Issue facts in descending `(at, id)` order. The filter applies before pagination. `limit` defaults to 20 and accepts 1–200. `nextCursor` binds the Topic, filter and first-page watermark. Each row identifies its source record. An `issue_snapshot` describes the saved state at `updatedAt`. Lifecycle entries use the timestamp on the saved transition.

In `mode=conversation`, omitting `topicId` lists Topics by their latest
conversation message, newest first. Topics without messages follow in
creation order, newest first. Equal timestamps sort by Topic ID descending.
Use `search` to match a substring of the title or ID. Supply `topicId` to
list that Topic's timeline in descending `(at, id)` order. Use `filter`
with `all`, `router_routed`, `thread_direct`, `issue_created`, `issue_session_dispatch`
or `report`.
Ingress rows identify the saved Message and event and include the source
`threadKey` when present. Issue creation appears on the timeline while activity
ordering uses conversation messages. Issue Session dispatch rows identify the saved
event, its Issue and the recipient `sessionBindingId`. Their
`dispatchKind` identifies startup, continuation, goal notification or context
supplement. Read the internal event for its full content and references.
Report rows identify the saved Report, Issue, SessionBinding and notification
event. Read the Report for its complete body and the Issue for recorded
acceptance. A notification retry keeps the same Report row.
An exact `eventId` within a Topic returns a single row in the standard page
envelope. `limit` defaults to 20 and accepts 1–200. Continuation cursors bind the
query scope and first-page watermarks.

Health reports `ingress: ready` for Core HTTP intake. It shows Router
preparation, Topic and Issue business states, unknown native stops and echoes,
and counts by internal event and Outbox status. Use the Trigger's local
status command and logs for channel readiness and admission health.

Router is `not_started` when execution is
omitted, or its persisted `pending/ready/blocked` preparation state. Messages
include current `routing: {round,state}`. Observer/Router projections also include
source reception/return relations. Topic-role Message queries omit other Topics'
relations. Thread details expose key, ref and timestamps. SessionBindings expose fixed
role, owner, generation, workspace, namespace/name, `runtimeRef`, `model`,
optional `effort`, actual Session IDs,
preparation state and any error, and the next attempt time. Credentials and MCP capability files stay private.
Internal event details expose sender, recipient, content references, status
and the Session acceptance timestamp.

Lists return `{"items":[...],"nextCursor":"opaque"}`. Default limit 50,
valid range 1–200. Messages and subscriptions accept an exact `threadKey`.
Issues, Timers, PR associations, routing events, routing relations and subscriptions accept `topicId`. Routing events also accept `messageId`. An event includes its sender, recipient, content references, status and return link.
SessionBindings, Reports and PR associations accept `issueId`. Internal events accept the same filter for explicit Issue references. Their preview marks recorded goal-update notifications with `goalUpdate` and includes the target revision. Supply one parent
filter at a time. The Observer also supports `topicId` on Outbox to find replies to messages in the
Topic's routing history. Outbox and routing relations accept `messageId`. Observer Issues accept
`messageId` to select Issues with an explicit matching material across all Topics. Message-filtered
Issue and routing-event previews include the owning or participating Topic's title when available.
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
most recent qualifying event registration time and Message ID, newest first.
The event sequence watermark limits activity before grouping and pagination.
Rows expose the source event ID, current routing state, target event IDs,
and reason previews. Read the Message's routing events for full reasons and
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

By default, `Host` must match the connection's numeric local endpoint.
`observerAllowedHosts` also accepts exact `Host` values for `GET` and `HEAD`
requests to the Observer UI and `/v1/observer/` API. Each entry is a DNS name
with a port when clients include one. All other routes retain the numeric
endpoint requirement. `externalURL` independently sets the Session MCP address.

Core rejects foreign Origins and cross-site Fetch Metadata. Observer reads do
not require authentication. Limit access to trusted readers at the network
boundary. Observer requests with write methods return `405` on the numeric
endpoint and `403` through an allowed DNS name. No query parameter grants write
authority. No CORS credentials or browser-side write token is configured.
`Cache-Control` is `no-store`.
Reads preserve Core business state.

Session preview resolves a recorded SessionBinding, then verifies the Session
identity through massd. The history endpoint reads up to 50 durable events and
accepts the previous page's `nextCursor` as `cursor`. It also returns
`observedAt` and massd's `syncedEventId`. The live endpoint streams
`application/x-ndjson`. Frames report `connected`, `heartbeat`, or a
`preview` event. A `history_changed` frame tells the client to query stored
history again. On reconnect the client reads history and its cursor again.
Activity between live connections can be missing. Stored history belongs to
the Session, which can span several binding generations. The projection omits
raw tool values, Runtime configuration and event metadata. Text previews
longer than 16 KiB carry a truncation flag. HEAD reads the SessionBinding and returns
headers without opening a massd connection.


## Session configuration and preparation

Omit `sessions` for intake-only operation. To start role Sessions, configure
the Router workspace, fixed role profiles and at least one Issue template.

```yaml
# externalURL: https://core.example.com  # Core's address as seen by massd
massd:
  endpoint: ws://127.0.0.1:24368/jsonrpc
  # tokenFile: /private/massd.token
# operatorTokenFile: /private/operator.token  # enables local input recovery
sessions:
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
  issueTemplates:
    - runtimeRef: codex-deepseek
      model: deepseek-chat
      description: Implement and verify code.
      effort: medium
    - runtimeRef: codex-standard
      model: gpt-6.2-astra
      description: Evaluate architecture and diagnose complex problems.
```

`massd.endpoint` accepts a WebSocket origin, proxy mount base, or full
`/jsonrpc` URL. See the [endpoint upgrade steps](../mass-go/README.md#upgrade-the-websocket-endpoint)
when switching an existing deployment. For an embedded instance, use
`massd.embedded.dataDir` in place of `massd.endpoint` and `massd.tokenFile`.
See [embedded deployment](README.md#running-mass-inside-core) for startup and
ownership handoff.

Both role profiles require a legal Runtime name and a nonblank model.
An Issue template also requires a nonblank description. Each
`(runtimeRef, model)` pair must be unique in the list. Several templates
may use the same Runtime with different models. Effort is optional, and model
and effort are passed literally to massd. A configured pair permits a
Topic to select it. Core checks Runtime availability during Session preparation.

New Router Sessions, Topic Sessions and Issue Sessions use `config.permissionPreset: fullAccess`
and `features.agentQuestions: false`. This applies to replacement Sessions and
overrides the Runtime default for native questions. Remove `permissionPreset`
from Superagent role profiles and Issue templates when upgrading. An existing
Session keeps its saved spec when resumed or reused.

Router and Topic use the Outbox to request clarification and receive answers
through incoming Messages. Issue Sessions submit a `blocked` Report when they need
information from the user, and Topic follows up through the Outbox.

Namespace is a DNS label of at most 24 bytes. The Router is unique per Core
store, in `<namespace>-router/router-g1`. Each Topic has a stable namespace derived from its ID and Session `topic-g1`.
Configure distinct execution namespaces for Core stores that share one massd.
An Issue Session uses its Topic namespace and a fixed
`issue-<issueId>-g<generation>` name.
Each binding stores the selected Runtime, model and any effort, along with the
workspace and other Session preparation parameters. Core sends `runtimeRef`
and `config: {model, effort?}` in the MASS Session spec. The template list
and descriptions live in the startup YAML. On restart, compatible execution
configuration preserves binding identities. A changed Router profile fails
startup with `failed_precondition`. Existing Topic generation specs stay
fixed. A newly created Topic binding uses the current Topic profile. Enabling execution on an intake-only
directory processes its pending Router events.

Use a fixed listener port with execution enabled and keep that endpoint stable
for existing role Sessions. An occupied configured port fails startup.
Core ownership is still single
process. Preparation queries massd by fixed namespace/name before creating,
never includes initial business input in create, and re-queries the same name
when a create receipt is lost. Full Session spec, resource UID and native
Session identity must match; conflicts become `blocked` and are never adopted.
Offline matching Sessions use public resume without new input.

Preparation failure retains the original binding, fixed name and visible pending
operation, retried after two seconds in this slice. On restart, ready bindings
are reconciled before dispatch resumes. Topic Sessions receive Issue and
acceptance instructions in their fixed prompt.
MCP capability files are private, atomically replaced and read once per MCP
process. Authentication is checked by Core on every role call. Runtime/model/
effort are never options in a route decision.

## Role MCP and source scope

Core exposes one Streamable HTTP endpoint at `/v1/mcp`. Router, Topic and Issue bindings
send JSON-RPC 2.0 messages via `POST /v1/mcp` with `Authorization: Bearer TOKEN`.
Core authenticates each request against its binding. `tools/list` advertises
that binding's tools and `tools/call` enforces its permissions. A revoked
Issue SessionBinding can discover and call `submit_report` for historical work.
A revoked Router or Topic cannot discover or call its former tools.
Channel tokens cannot authenticate as role bindings.

Clients send `Content-Type: application/json` and
`Accept: application/json, text/event-stream`. Requests can be up to 1 MiB.
Start with `initialize`, send
`notifications/initialized`, then use `tools/list`, `tools/call` or `ping`.
Send the negotiated version in `MCP-Protocol-Version` after initialization.
Core recognizes protocol versions 2024-11-05, 2025-03-26, 2025-06-18 and
2025-11-25. An unknown initialization version negotiates 2025-11-25. Core
returns one JSON response per request and an empty HTTP 202 for accepted
notifications. `GET /v1/mcp` returns HTTP 405. Core issues no MCP session ID.

Successful tool results contain `structuredContent` and a text block with the
same JSON. Tool validation and business failures set `result.isError` to true
and include the Core error under `result.structuredContent.error`. JSON-RPC
errors cover malformed requests and capability violations. Unauthenticated
requests return HTTP 401.

`initialize` returns binding metadata under `_meta["mass.dev/session-binding"]` with
`id`, `role`, `ownerId`, `generation` and the applicable `topicId` and `issueId`.
Topic and Issue Agents use `get_topic` and `get_issue` for scoped business state.
External Message text, metadata, tool arguments and native-subagent suggestions
cannot select permissions.

MASS-managed role Sessions connect directly to Core `/v1/mcp` over HTTP.
`externalURL` optionally sets the base URL for native Session clients. Core
appends `/v1/mcp`. If a reverse proxy terminates TLS, forward `/v1/mcp` to Core
and set the upstream `Host` to the local address that receives the request.
Core writes a private file containing the scoped bearer under `dataDir/mcp-files`
and passes its absolute path in `headers[].valueFrom.fileKeyRef`. Run Core and
massd on the same host under the same operating-system account. For separate
processes, set `massd.endpoint` to the RPC address reachable from Core. Without
an explicit `externalURL`, use a fixed Core listen port. A wildcard Core listener
maps to `127.0.0.1` or `::1` for native Session MCP connections. Core persists the
MCP URL and credential path in the Session spec. Keep both stable across restarts.

| Tool | Router | Topic | Arguments |
| --- | --- | --- | --- |
| `get_internal_event` | Own event | Own event | `event_id` |
| `get_message` | Source Messages | Associated Messages | `messageId`, optional `include` with `thread` and `replies`. Router can also include `routing`. |
| `list_messages` | Source history | Associated history | Optional `threadKey,cursor,limit` |
| `list_topics` | Topic summaries | | Optional `query,cursor,limit` |
| `route_messages` | Route or ignore | | Route request below |
| `send_reply` | Scoped Message | Associated Message | `operationId,messageId,content`, optional Topic-only `blockerRef` |
| `decline_message` | | Own routing event | `event_id,reason` |

Topic summaries expose the Topic ID, title, description, goal, state, revision
and timestamps. Topic Message history follows the Topic's routing associations,
including when a Thread has multiple subscribers. List cursors bind the
collection, filters and authenticated role. An unavailable scoped record
returns `not_found`.

Message views include `id`, `source`, `text`, `threadKey` and `actor`, with
`at` and `url` when available. Router can request the routing round, state and
relations. Topic also sees `quotedReply` for a matched answer in its scope.
Followup and PR Messages can include event facts under `meta`. Forwarded DWS
Messages can include `meta.forwardMessages`.

## Atomic RouteDecision

```json
{
  "operationId": "router-decision-17",
  "messageIds": ["mass-m1", "mass-m2"],
  "rounds": {"mass-m1": 1, "mass-m2": 2},
  "kind": "route",
  "reason": "Two explicit independent requests",
  "targets": [
    {"create": {"title": "First request", "workspace": "/absolute/existing/workspace"}, "messageIds": ["mass-m1"]},
    {"topicId": "mass-t42", "messageIds": ["mass-m2"]}
  ]
}
```

Each target requires exactly one `topicId` or `create` and a nonempty subset
of the requested Messages. The Router can select multiple Topics per Message.
Core checks the current routing round, which is derived from return requests.
A new selection requires pending routing. An identical active Message/Topic
pair reuses its `routed_message` event. New Topic creation, preparation,
Thread subscription, Message routing state and event registration commit
in one transaction. Success returns the resolved Topic IDs and each target's
`eventIds` map keyed by Message ID. Reuse `operationId` when retrying an
identical route, including Topic creation.

`kind: ignore` requires a reason and no targets. Core sets `routing_state`
to `ignored` when no target is active. If an active target remains, the
state is `routed`.

## Declining messages and terminal Topics

A Topic that declines a Message calls `decline_message` with `{event_id,reason}`. The ID identifies
one `routed_message` addressed to the calling Topic. Core atomically records
a `route_request` to Router with the Message reference, original event
reference and reason. An unconfirmed original becomes `superseded`. A
confirmed original retains `sent` and the return is visible through its
request. Retrying by the same event ID returns the original request.
Other targets and Thread subscriptions continue to apply. The Router can
explicitly select the same Topic again.

Router can include `routing` on `get_message` for its routing state and
relations. Observer exposes the saved routing event records.

Subscription selection includes completed/canceled Topics. Receiving, preparing
or waking a Topic preserves its business status. Use `update_topic` with the `complete` transition to record acceptance. Routing keeps Issue ownership within its original Topic.

## Issue, SessionBinding, Report and acceptance

Core assigns IDs with decimal counters starting at 1.

| Object | Example | Numbering |
| --- | --- | --- |
| Message | `mass-m42` | `trigger_messages.seq` within the Core database |
| Topic | `mass-t42` | Topic sequence within the Core database |
| Issue | `mass-t42-i3` | Issue sequence within the Topic |
| Report | `mass-t42-i3-r5` | Report submission sequence within the Issue |
| Timer | `mass-t42-tm3` | Timer sequence within the Topic, across Topic generations |
| Blocker | `mass-t42-i3-b2` | Blocker sequence within the Issue, including resolved blockers |

These IDs stay fixed after creation, including when an Issue or Topic is
reopened. Each SessionBinding gets an opaque ID. Reports use their own IDs
and reference their Topic notification with `eventId`.

All changes require an `operationId`. Creation tools such as `create_issue`
and `schedule_followup` use it within the authenticated SessionBinding to
identify the same resource on an identical retry. Reusing a creation key
with different parameters returns `conflict`.

Revision-guarded writes check the current revision and business state on every
attempt. A stale `expectedRevision` returns `conflict`, even when the operation
ID matches an earlier successful request. Reusing the operation ID with the
current revision can apply another change. After a lost response, read the
current resource and any associated internal event before submitting another
mutation.

### Topic tools

A new Topic can supply `create.goal` in `route_messages`. Router reads it in
`list_topics` summaries. If omitted, Core uses the description or title.

| Tool | Arguments | Result |
| --- | --- | --- |
| `get_topic` | Optional `include` with `executionTemplates`, `followups`, or `pullRequests` | The bound Topic and requested expansions. |
| `update_topic` | `operationId,expectedRevision`, optional `title,description,goal,pullRequests,transition,triggerEventId` | The Topic after one transactional update. |
| `create_issue` | `operationId,title,goal,runtimeRef,model`, optional `workspace,materials,start,triggerEventId` | `{issue, sessionBinding?}`. The Issue belongs to the authenticated Topic. |
| `get_issue` | `issueId`, optional `include` with `reports` or `pullRequests` | The scoped Issue and requested expansions. |
| `list_issues` | Optional `status,cursor,limit` | Issues in the bound Topic. |
| `start_issue` | `operationId,issueId,expectedRevision`, optional `resolve`, `runtimeRef,model` as a pair, and `triggerEventId` | `{issue, sessionBinding}`. Resolves supplied blockers and starts the Issue Session in one transaction. |
| `update_issue` | `operationId,issueId,expectedRevision`, optional `title,goal,materials,blockers,sessionInput,pullRequests,transition,triggerEventId` | `{issue, sessionBinding?, notificationEventId?}`. Applies the update in one transaction. |
| `get_report` | `reportId` | The scoped Report and its evidence. |

Use `materials: {add:[...]}` to append materials and `pullRequests: {add:[...]}`
to add absolute PR URLs. Use `blockers: {add?,resolve?,unblockedStatus?}` to
edit blockers and `sessionInput: {mode,content?}` to send input to the current Issue Session.
Use `transition: {action,reason,evidence?}` in `update_topic` or `update_issue`.
Actions are `complete`, `cancel`, and `reopen`. Completion requires an evidence
list. Topic completion also requires every Issue to be terminal. Issue reopen
requires a later explicit start. Topic cancellation requests stops for its
Sessions and cancels its nonterminal Issues.

Supply a nonblank Issue goal. When `workspace` is omitted, Core uses the
current Topic's workspace. Supply an existing absolute path to override it.
Each material is `{label?,uri}` or `{label?,messageId}`. The Message must
already belong to the Topic's reception history. A stale `expectedRevision`
returns `conflict` with `resource` and `currentRevision`.

For each explicit selection, Core validates the complete Runtime and model
pair against the current list and takes effort from that entry. An incomplete
or unlisted pair returns `invalid_argument`. A `start_issue` with neither
field uses the saved Issue profile, even if the template changed or was
removed. Issue reads include `executionProfile: {runtimeRef, model, effort?}`,
including through the Issue Session's `get_issue`. An identical create retry
returns the original Issue identity and, for `start: true`, its initial SessionBinding.

`start: true` uses the same scheduling transaction as `start_issue`. Core
saves the Issue's profile and `currentSessionBindingId`, creates the
SessionBinding and registers an `issue_session_started` event in one
transaction. Another start with a current SessionBinding returns `conflict`.
Each SessionBinding retains its starting goal revision, Runtime, model and
effort. Its Observer detail includes the `startup` event state.

Completion requires a reason and at least one evidence reference, such as a
Report ID or artifact path. Core stores the acceptance author and timestamp.
Topic verifies the result before issuing the explicit acceptance command.

### Goal, blockers and Issue Session inputs

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
Topic chooses `pending` or `active`. Pending retires the current SessionBinding
so the Topic can arrange a later start. Active requires a current SessionBinding.
To continue the active Issue in the same Session, call `update_issue` with
`sessionInput` in the same operation or in a later update.

Use `mode: "context_supplement"` with structured `content` or a material to send background
to the current Issue Session via Inject. Use `mode: "continue"` on an active Issue
with all blockers resolved to send `issue_session_update` via StartOrSteer.
Include `sessionInput.mode: "continue"` in a goal or blocker edit to send a
work update. Core rechecks the target SessionBinding, goal revision and blockers
before admitting that update. The receipt field `notificationEventId` holds
the stable internal event ID.

When an Issue completes, cancels or reopens, Core can register an
`issue.dependency_changed` fact in a `context_supplement` for its Topic.
The Topic reads current dependencies and decides whether and how to proceed.

Issue detail includes the most recent explicit goal notification in
`latestRequirementsNotification`, with event status and goal version.
An Issue with a current SessionBinding also has a read-only MASS `issueSession` observation,
including observation time or an unknown-state reason.

### Issue Session tools

| Tool | Arguments | Result |
| --- | --- | --- |
| `get_issue` | `{}` | Bound Issue with its current goal, materials, blockers and business state. |
| `get_message` | `messageId` | Message listed in the Issue materials. |
| `update_issue` | `operationId,expectedRevision`, optional `title,materials,pullRequests,triggerEventId` | The bound Issue after applying edits. |
| `submit_report` | `operationId,kind,summary,goalRevision`, optional `details,evidence,questions,triggerEventId` | Appended Report with its Topic notification ID. |
| `get_internal_event` | `event_id` | Complete Agent view of the event addressed to this binding. |

Report kinds are `progress`, `ready`, `blocked` and `failed`. The Issue Session
supplies the goal revision it used. Core records the authenticated
`sessionBindingId` as the source.

The Report is appended to `issue.reports` in the same transaction as its
Topic notification. Current Reports register `report_submitted` with a
reference to the Issue and Report. A Report is current when its binding and
goal revision match the current Issue and it was submitted before the binding
retired. A Report from a retired binding uses its saved goal revision bounds
and is marked `historical`. Both historical Reports and Reports for a prior
goal revision send `context_supplement`. Topic `get_report`, the Agent report
view and the Observer Reports collection compute `current` at read time.
Topic owns acceptance.

### Issue Session observation and report reminder

Core opens a live Session subscription before dispatching an Issue Session
StartOrSteer. Both Codex and Claude use the unified `session.state_update`
event. A transition to `idle` after activity in the dispatch window qualifies
once the input receipt confirms delivery. A receipt for steering existing
work can use that work's subsequent transition to `idle`. Events that arrive
before the receipt stay buffered. Subscription replay and initial or stale
idle observations do not qualify. A newer admitted prompt supersedes the
prior observation.

Core settles the input as `sent` from the receipt. Without a live subscription,
`workObservation` records `delivered` and an observation gap. An interruption
during live observation changes the state to `unknown`. Core restart marks
unfinished observation as `core_observation_interrupted`. A newly accepted
input can establish fresh observation.

When the current goal has no Report from the current SessionBinding, the
observed `idle` and one `report_reminder` commit together. It targets the same
SessionBinding and
asks for a Report about existing work. The Issue Session can use the event ID
as its Report operation ID. The marker stays set after delivery, supersession
or replay. A current Report supersedes an unconfirmed reminder in the same
transaction. Core rechecks the SessionBinding and current Report eligibility
before dispatch. A retry preserves the event ID.

SessionBinding detail projects `workObservation` with the binding and Session
identities, attempt ID, optional diagnostic work reference, continuity, and
the observed idle under `end` or the observation gap. They also project `reminderScheduled`,
`reminderEligibility` and `reminder` with its event status when scheduled.
Observer links to the event and separately shows Issue, Topic and stop status.

### Retirement and stop observation

Issue completion and cancellation supersede pending Issue Session input and revoke
the current SessionBinding in the business transaction. Cancellation also
persists a stop request on the retired binding. Completion requests a
stop when it finds admitted input. A late acceptance leaves an internal
delivery that was already superseded in that state. Topic cancellation performs these changes for each
unfinished Issue, retires the Topic binding and requests a stop for it. Existing
acceptance evidence, reports, workspaces and Session history remain accessible.
Cancellation does not undo external effects.

Core serializes stop with the binding's current dispatch, verifies the original
MASS Session identity and uses public `session.cancel`. SessionBinding details
show `stop.status` (`requested`, `dispatching`, `observed`, `blocked` or
`unknown`), request and observation times, receipt/error, Session observation
and any gap. Stop control is scheduled ahead of the binding's next input and
does not wait for the input aggregation window. An interrupted in-flight stop
remains unknown across restart. The stop observer records Session state
separately from the Issue Session's input observation. Cancel acknowledgment,
no-active-work, idle and EOF leave `stop.workState` unknown. A verified resource release
appears as `close` on the retired SessionBinding. Its method,
time and massd resource version are available separately from the turn outcome.

Reopening preserves historical acceptance and blocker records. An Issue moves
to pending or stays blocked by open blockers. A later explicit start creates
a fresh SessionBinding and credential after Core verifies that prior
preparation never began or that the original native owner was released.
When the previous Session was verified reusable, Core can reuse it only with
the same workspace, Runtime, model, effort and namespace.
A canceled Topic still accepts new Messages through its subscriptions.
Messages wait durably while the Topic has no current Session. After settlement,
they reach the next Topic generation with the same IDs. The Topic stays
canceled until that owner explicitly reopens it with `update_topic`.

## Internal event dispatch

Core sends each Session a JSON object with ordered `events`. Read `kind` to
select the action. Deduplicate by `event_id`.

For a Router `route_request`, use the `trigger_message` ID, `content.data.text`,
`threadKey`, and `routing` when supplied. Pass `routing.round` in
`route_messages.rounds` keyed by Message ID. For a reference-only event or a stale
round error, call `get_message` with the Message ID and `include: ["routing"]` to read
the current state and any return reason.

```json
{"events":[{"event_id":"evt_router","kind":"route_request","refs":[{"kind":"trigger_message","id":"mass-m1"}],"content":{"type":"markdown","data":{"text":"Verify the report."}},"threadKey":"dws:release","routing":{"round":1,"state":"pending"}}]}
```

For a Topic `routed_message`, use `content` when present. Core inlines ordinary
short Message text as Markdown when the encoded `content` fits within 2 KiB
and the batch has space. A matched short quoted reply can include `quotedReply`
and the relevant Issue's `revision`, `status`, and open `blockers`. On a
reference-only event, including an unmatched quote, call `get_message` with the `trigger_message` ID. Use the same query to read source facts such as Timer
materials or PR observations. Each
`routed_message` carries one such reference, including when it has `content`.
Pass its `event_id` to `decline_message` when the Message belongs elsewhere.

```json
{"events":[{"event_id":"evt_topic","kind":"routed_message","refs":[{"kind":"trigger_message","id":"mass-m1"}],"content":{"type":"markdown","data":{"text":"Verify the report."}}}]}
```

On `report_submitted`, check `report.current` and the Issue goal revision.
Verify the evidence against the current goal before accepting the Issue. When either view is missing,
read the relevant record with `get_report` or `get_issue`. For an Issue mutation, supply the
Issue's `revision` as `expectedRevision`; read current state on a conflict.

For an Issue Session `issue_session_started` or `issue_session_update`, use the supplied `issue`
for the current goal, `goalRevision`, status, blockers and materials. Call
`get_issue` when the Issue view is absent or current state is in doubt.
Find material Messages in `messages` by `id`, and use `get_message` for
referenced Messages that need readback. Workspace is the Session's `Cwd`.

```json
{"events":[{"event_id":"evt_issue","kind":"issue_session_started","issue":{"id":"mass-t1-i1","goal":"Verify the report.","goalRevision":1,"status":"active","materials":[{"messageId":"mass-m1"}]},"messages":[{"id":"mass-m1","text":"Verify the report.","actor":"sender-1"}]}]}
```

| Event | Additional fields |
| --- | --- |
| `route_request` | `refs` with `trigger_message`; eligible initial requests also carry `content`, `threadKey`, and `routing`. A return carries the original `internal_event` reference. |
| `routed_message` | One `trigger_message` in `refs`, Markdown `content` when eligible, and `quotedReply` and `issue` for an eligible matched reply. |
| `report_submitted` | `refs` with `report`; eligible notifications include `report` and `issue`. |
| Prior goal or historical Report `context_supplement` | `refs` with `report`. |
| `issue_session_started` and `issue_session_update` | Eligible `issue` and short material `messages`. An update can include `content` and `body.materials`. |
| Issue Session `context_supplement` | Provided `content`, `body.materials`, and eligible short material `messages`. |
| `report_reminder`, `binding_recovered` | `event_id` and `kind`. |
| Topic dependency `context_supplement` | `body.type`, `body.dependencyIssueId`, `body.dependentIssueIds`, `body.previousStatus`, and `body.status`. |
| Terminal Topic recovery `context_supplement` | `body.recovery`. |
| `outbox_failed` | `refs` with `outbox`. |

Structured `content` uses the Outbox schemas, including Markdown prompts for
`options`. If an event's content or body exceeds the event budget, Core sends
`truncated: true`. Call `get_internal_event` with the event's `event_id` to read
its complete Agent view. Its stored record and Observer detail retain the
original fields.

Topic instructions include the configured Issue template choices. Use
`get_topic` with `include: ["executionTemplates"]` to read the current choices.
Session reuse requires matching saved Session instructions.

When a supported mutation responds to an event, pass its `event_id` as
`triggerEventId` so the operation continues the event's trace.

All-`context_supplement` batches use `session.inject`. Any other kind selects
`session.prompt` (StartOrSteer). Acceptance settles the rows as `sent`.
A restart can submit unconfirmed rows again using the same IDs. Issue Session
startup and updates, Report notifications, Report reminders and Issue
dependency changes use this path. The Observer exposes the acceptance state
and timestamp separately from work completion.

### Local operator reconciliation

Set `operatorTokenFile` to a distinct private credential with at
least 32 non-whitespace bytes and bind Core to loopback. Inspection uses
`GET /v1/operator/session-bindings/{sessionBindingId}` to read the saved binding, its stop
evidence and a live identity-checked massd snapshot. Recovery uses
`POST /v1/operator/input-recovery`. Both require the local operator credential.

```sh
superagent inspect-session-binding SESSION_BINDING_ID --endpoint http://127.0.0.1:24370 \
  --operator-token-file /private/operator.token
superagent recover-input SESSION_BINDING_ID --endpoint http://127.0.0.1:24370 \
  --operator-token-file /private/operator.token --operation-id incident-123 \
  --mode retry --reconciliation 'Compared original native trace and affected work' \
  --accept-possible-duplicate
```

`retry` targets a current blocked Router, Topic or Issue Session binding. The operator
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

`settle` releases a retired Issue Session or Topic binding. For an Issue Session, the next
explicit start creates the next SessionBinding. For a Topic, settlement prepares its
next owner so queued source Messages can reach it. Core prefers reusing an
identity-checked local Claude Session on macOS/Linux after its online native
process has closed and massd verifies the same resource offline. Before
resuming, Core atomically publishes the next generation credential at the
same file path. massd reads it when it opens the same native Session. Preparation retries ambiguous resume outcomes against
the original Session.

During normal Issue Session dispatch, Core resumes the same Session and retries
pending input. Sent events stay settled. If massd records
`session_recovery_missing` after verifying release of the original control
and settling the failed resume, Core retires that SessionBinding and starts a fresh Issue Session when the
Issue can continue. The new Issue Session receives
`issue_session_started` in the same workspace. It uses the Issue and materials in the
event when available and calls `get_issue` if it needs current Issue
facts. An opening or unreachable Session stays pending while
Core reconciles the original identity.

`replace` requests a fresh Session after verified release. A current Issue Session
replacement retires the old SessionBinding and schedules a fresh Issue Session.
A retired Issue Session replacement records the release policy for its next explicit
start. Router and Topic replacement retain valid unsubmitted facts with their
stable IDs, apply current event eligibility and enqueue a recovery notification.
A canceled Topic receives that recovery notification as context. Sent
internal events keep their original ID and timestamp.

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
MCP clients call these tools through `/v1/mcp`.

| Tool | Arguments | Result |
| --- | --- | --- |
| `schedule_followup` | `operationId,dueAt,content`, optional `materials` | A new `scheduled` Timer at revision 1. |
| `update_followup` | `operationId,followupId,expectedRevision`, optional `dueAt,content,materials` | The next revision. Supply at least one change. An empty materials array clears materials. |
| `cancel_followup` | `operationId,followupId,expectedRevision` | The next revision with status `canceled`. |

`dueAt` accepts RFC3339 with `Z` or a numeric offset and up to nine fractional
digits. The record stores the UTC instant. Content must be nonblank. Materials
use the Issue material shape and the same Topic scope for Message references.
Replaying the original `schedule_followup` request with the same binding and
operation ID returns the followup in its current state. Reusing that operation
ID with different parameters returns `conflict`. Updates and cancellations
compare the current revision. A stale revision returns `conflict`.
Changes to a current `fired`, `canceled` or `invalidated` Timer
fail with `failed_precondition`. Use a new `schedule_followup` for another followup.

Core scans on startup and every 250 ms while execution is enabled. A valid past
time is eligible on the next scan. The first valid transaction to commit
decides a race between firing, updating, canceling and Topic termination.
Firing keeps the selected revision and commits `firedAt`, the task Thread,
its Topic subscription, and one Trigger Message with its routing event in
the same transaction. The internal source is `core:timer`. The task ID is both
the native Thread ID and `sourceMessageId`. The Message text is the followup
reason. Its `meta` includes `type: timer.followup`, `taskId`, `topicId`,
`taskRevision`, `dueAt` and any task materials. The Message carries no
external reply address.

The usual single-subscription rule routes the Message to the Topic. Core submits the `routed_message` through StartOrSteer. Use `get_message` to read the Message reference and `get_topic` with
`include: ["followups"]` to check the current task.

Completing or canceling the Topic atomically invalidates scheduled Timers and
supersedes unconfirmed followup events. Accepted events and their
Message and Thread remain readable. Reopening preserves the task and event
history. On restart, due scheduled Timers can fire and valid unconfirmed
events use the same event ID on retry.

`get_topic` with `include: ["followups"]` lists followups in the bound Topic.
Observer and `observe timers --topic-id TOPIC_ID` expose the
revision, due and fired times, state, `triggerMessageId`, `threadKey` and
`followup` routing event.

## Pull request registration and subscriptions

Use `pullRequests.add` in `update_topic` for a Topic association or in
`update_issue` for an Issue association. The Issue Agent can register a PR for
its bound Issue before submitting its Report. Core checks current ownership
before writing or reusing the association.

Core saves the canonical URL in the Topic's `pullRequests` and, when scoped
to an Issue, in that Issue's `pullRequests`. Both writes, the Thread, the Topic
subscription share a transaction. A link returns
`id`, `canonicalUrl`, `threadKey`, `topicId`, `subscriptionId` and
`createdAt`. Issue links also carry `issueId` and the initiating
`sessionBindingId` if an Issue Session registered that link. Registration reuses the link for the same canonical URL and Topic or Issue,
including across Issue Session generations. Multiple Issues can share one parent Topic–Thread
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

`send_reply` atomically creates one pending Outbox item. Use the same
`operationId` and parameters to retrieve the same item with its current state.
Use `get_message` with `include: ["replies"]` to read authorized replies.
A successful queue receipt confirms local persistence.

The `send_reply` input schema advertises the supported content types and fields. Each reply has one object
with `type` and `data`. `markdown` carries `data.text`. `options` carries a
Markdown `data.prompt` and at least two ordered `data.options` with distinct
`id` values and a display `label`. Supply this object as `content` when
queueing the reply.

Router may give one explicit `messageId` or the distinct `messageIds` of its
current routing input. Topic may give an explicit associated Message or omit
the target to use its oldest associated Message. Core orders the candidates by
its own `receivedAt` then the stable SQLite acceptance sequence. It checks the
chosen Message's channel after selection. No match, missing native address or
an out-of-scope target returns an error. Issue Session has no reply capability.

An Outbox item carries `id`, `messageId`, `content` and `status`, with optional
`receipt` and `blockerRef`. The status is `pending`, `sent` or `failed`.
A Topic can set `blockerRef: {issueId, blockerId}` for one open human blocker
in its own Issue. Router replies cannot set this field.

### Quoted answers

For DWS, Core matches the inbound `quotedMessage.message_id` against the
native `receipt.messageId` of a sent question. Source, account and the inbound
native conversation must match. If `quotedMessage.conversation_id` is provided,
it must also match. Topic `get_message` on an associated Message returns
`quotedReply: {outboxId, blockerRef}` for a unique match within its Topic.
The lookup takes effect as soon as the send receipt is saved, including for
answers already admitted.

A match identifies the original blocker. The Topic reads the current Issue
and decides whether the answer is sufficient. With no match, the Topic
evaluates which questions the user has clearly answered and requests
clarification when needed. Unanswered blockers stay open. Pass the reply
Message ID as the basis in `update_issue.blockers.resolve`. Core rejects a
matched quoted Message as the basis for any other blocker, including a
replacement blocker for the same question. To explicitly continue the current
Issue Session after resolving the last blocker, set `blockers.unblockedStatus` to
`"active"` and `sessionInput.mode` to `"continue"`. The resolution and `issue_session_update` commit together.

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
| `permanent_failure` | Save `errorCode` and notify the author through Core event dispatch. |
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

Observer shows pending, failed and sent replies by `messageId` or `threadKey`.
The author can request reply history for one Message via `get_message`. Details show
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

Channel consumers can use the receipt's `echoStatus` to defer feedback while
it is `held` or `echo_unknown`. An omitted value, `cleared` or `released`
allows feedback on a routable Message. `outbound_echo` suppresses feedback.
An exact late match takes precedence in the receipt even if Observer retains
the earlier `cleared` or `released` routing history.

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
and `reason`, authenticated with `operatorTokenFile` on the
loopback listener. A replay of that release returns its recorded routing
event. A later match on an already routed Message is recorded for inspection
without another delivery.

## Harness relationship

The MASS harness defines the massd Runtime and Session contracts, including
input receipts. This API describes the Superagent Core, SQLite schema and role
interfaces.
