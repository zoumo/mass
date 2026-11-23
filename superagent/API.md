# Core v1 API

This reference covers the Core interfaces for channel adapters, Observer readers,
and role MCP clients. See the [deployment guide](README.md) for startup and
configuration. MASS Runtime and Session operations follow the
[MASS JSON-RPC contract](../harness/bindings/jsonrpc.md).

## Start and ownership

One Core process owns `dataDir` at a time. A successful start returns
`status: ready`, `endpoint`, and `observerEndpoint`. Accepted records survive
process exit. For a lost intake response, use the Message's original identity.
See [data directory upgrades](README.md#reuse-an-existing-core-data-directory).

## Submit Message

`POST /v1/channel/messages` with `Content-Type: application/json` and
`Authorization: Bearer TOKEN`. The token identifies exactly one configured
source/account; body fields cannot select another identity.

```json
{
  "operationId": "stable-adapter-operation-17",
  "message": {
    "sourceMessageId": "native-message-id",
    "threadKey": "https://github.example/org/repo/pull/42",
    "content": [
      {"type": "text", "text": "Review requested. https://github.example/org/repo/pull/42#comment"},
      {"type": "resource_link", "uri": "file:///logs/checks.log", "name": "checks.log"}
    ],
    "_meta": {"observationId": "obs-17"}
  }
}
```

`operationId` and `sourceMessageId` are required. The adapter supplies the
final `threadKey`. Core stores it and matches subscriptions by exact string.
A key is at most 2048 UTF-8 bytes and has no control characters or leading
or trailing whitespace. A `sources` entry authorizes writes with `threadKeys`
for exact matches and `threadKeyPrefixes` for literal prefixes. List the same
range in multiple entries to let their accounts write to the same Threads.
A structured request needs a match in at least one of the configured lists.

`content` is a nonempty ordered array using the same content union as Prompt.
The supported types are `text`, `image`, `audio`, `resource`, and `resource_link`.
Use text for source facts and webpage links. Use `resource_link` for attachment
references. Optional `_meta` is an opaque JSON object. Core saves it for
Observer diagnostics and omits it from Agent tools and Session input.
Core generates `receivedAt`. Unknown fields and trailing JSON values fail.

The request can include `replyTarget` and `inReplyToOutboxId` beside `message`.
`replyTarget` is an opaque JSON object of at most 16 KiB. It enables replies
and is visible to the account's configured Outbox consumer. To link an answer
to a prior reply, supply the `inReplyToOutboxId` of a sent item whose original
Message has the same source, account and Thread.

The legacy `threadRef` request remains accepted during the adapter cutover.
When a credential configures write scopes, they also restrict legacy intake.
Legacy source fields, addressing, and echo behavior apply to that request shape.

Success HTTP 200:

```json
{"status":"accepted","duplicate":false,"messageId":"mass-m1","threadKey":"https://github.example/org/repo/pull/42","acceptedAt":"2026-11-18T01:02:03.000000000Z","eventId":"evt_..."}
```

Message, Thread update and the initial internal event commit together.
The receipt identifies the accepted Message and Thread. Its optional
`eventId` is the initial event ID for routable input and can be read
through `/v1/observer/routing-events/{event_id}`. Echo handling may admit a
Message without an event ID.

Zero or multiple Thread subscriptions enqueue a `route_request` to Router.
Exactly one queues a `routed_message` to that Topic. Both reference the
stored original Message. Initial event status is `pending`. The event
becomes `sent` after massd accepts the Session input.

Admission deduplicates by the authenticated source, account, and
`sourceMessageId`. Duplicate intake returns the original Message ID, Thread
key, acceptance time, and initial event ID with `duplicate=true`. Changing
the Thread key, ordered content, reply target or Outbox reference under that
identity returns `conflict`.
Object field order is immaterial. Changing `_meta` alone returns the original
receipt and preserves the first metadata. `operationId` correlates a caller's
request and does not participate in Message deduplication.

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
not_found=404, conflict/failed_precondition/unsupported/stale_binding=409,
and unavailable=503.
All error bodies omit credentials and raw storage/upstream errors.

Missing native identity fails invalid_argument. Wrong authenticated source or
account fails forbidden. Storage errors never return accepted. After an
unavailable storage result or lost connection, retry with the same `operationId`
and native identity. The retry can recover an acceptance after restart.

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

Run `superagent observe --help` for CLI collections, and request the chosen
collection's `--help` for filters. The CLI accepts a numeric HTTP origin and
uses the same read API without loading the channel configuration or tokens.

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

See the [Core configuration example](config.example.yaml) for Session profiles
and Issue templates, the [embedded deployment guide](README.md#running-mass-inside-core)
for data ownership, and the [WebSocket binding](../harness/bindings/websocket.md)
for the optional host mount and token boundary. Core validates Runtime availability
when it prepares a Session. Issue templates identify an allowed Runtime and model
pair. Core sends the literal configured values and the optional effort to MASS.

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

Core prepares a binding before submitting business input. It queries MASS by
fixed namespace and name, then creates the Session without initial business
input if needed. If a create response is lost, Core resolves the same name again.
It compares the saved Session spec, resource UID, and native Session identity
before reuse. Conflicts block the existing binding. Offline matches resume
without business input. On restart, Core reconciles existing bindings before
resuming dispatch.

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
appends `/v1/mcp`. If a reverse proxy terminates TLS, forward `/v1/mcp` to Core and set the
upstream `Host` to the local address that receives the request.
Core writes a private file containing the scoped bearer under `dataDir/mcp-files`
and passes its absolute path in `headers[].valueFrom.fileKeyRef`. Run Core and
massd on the same host under the same operating-system account. For separate
processes, set `massd.endpoint` to the RPC address reachable from Core. Without
an explicit `externalURL`, use a fixed Core listen port. A wildcard Core listener
maps to `127.0.0.1` or `::1` for native Session MCP connections. Core persists the
MCP URL and credential path in the Session spec. Keep both stable across restarts.

Call `tools/list` with the same bearer used to call tools. The server advertises
role-specific input schemas from [`roleTools`](pkg/core/mcp_tools.go). Core
assembles the role instructions from [`bindingSystemPrompt`](pkg/core/execution.go).

Router can read source Messages and routing rounds. Topic reads follow its
routing associations even when several Topics subscribe to the Thread. Issue
Sessions can read Messages listed in their Issue materials. In-scope callers can
read Internal Events addressed to their binding. Cursor scope includes the role
and the filters. An unavailable scoped record returns `not_found`.

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

Core resource IDs stay fixed after creation, including when an Issue or Topic
reopens. Treat SessionBinding IDs as opaque. A Report has its own ID and references
its Topic notification with `eventId`.

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

For tool arguments, use [role MCP discovery](#role-mcp-and-source-scope). A
new Topic uses `create.goal` or falls back to the description or title.
An Issue inherits the Topic workspace unless the request supplies an existing
absolute path. Message materials must belong to the Topic's reception history.
A stale `expectedRevision` returns `conflict` with `resource` and `currentRevision`.

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
transaction. `start_issue` also resolves any supplied blockers in that
transaction. Another start with a current SessionBinding returns `conflict`.
Each SessionBinding retains its starting goal revision, Runtime, model and
effort. Its Observer detail includes the `startup` event state.

Topic verifies the result and records acceptance with a reason and evidence.
Core saves the author and timestamp. Topic completion also requires all Issues
to be terminal. Topic cancellation requests stops for its Sessions and cancels
its nonterminal Issues. A reopened Issue requires an explicit start.

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

Call `tools/list` for the Issue Session tool schemas. `get_issue` resolves the
bound Issue. `get_message` requires a Message in its materials. Use
`submit_report` with the `goalRevision` used during work.

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

Core sends the ordered `events` JSON as the first text block of Session
`Input.Content`. Read `kind` to select the action. Deduplicate by `event_id`.
An event's `messageContent` lists the `messageId` and `blockCount` for each
attached Message. The blocks after the header follow event order and then
`messageContent` order. Each block retains its Prompt content type. A Message
that exceeds the budget keeps its reference. `get_message` returns its full
`content` array. Legacy records can still use inline `content.data.text`.

For a Router `route_request`, use the Message ID from `refs`. Pass the current
round in `route_messages.rounds`. After a stale round error or when the event
contains only a reference, use `get_message` with `include: ["routing"]`.

```json
{"events":[{"event_id":"evt_router","kind":"route_request","refs":[{"kind":"trigger_message","id":"mass-m1"}],"messageContent":[{"messageId":"mass-m1","blockCount":2}],"threadKey":"opaque/42","routing":{"round":1,"state":"pending"}}]}
```

For a Topic `routed_message`, Core may inline short content, matched quoted
answers, and associated Issue context. Use `get_message` to read a referenced
Message, its source facts, and any available quote association. When handling
`report_submitted`, check whether the Report is current and verify its evidence
against the current Issue goal. Use `get_report` and `get_issue` for readback.

Issue Session events can inline the Issue goal, `goalRevision`, blockers, and
short material Messages. Use `get_issue` for current Issue state and
`get_message` for referenced material. If `truncated` is true, use
`get_internal_event` with `event_id` to read the complete Agent view. See the
[role instructions](pkg/core/execution.go) for the actions associated with
individual event kinds. Pass `event_id` as `triggerEventId` on supported
mutations that respond to an event.

Batches of `context_supplement` use `session.inject`. A batch containing any
other kind uses `session.prompt`. Core records `sent` after it confirms input
acceptance, and may redeliver an unsettled event with the original ID. The
Observer records input acceptance separately from work completion. See
[ADR 0002](docs/adr/0002-settle-session-input-with-evidence.md).

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

Use `schedule_followup`, `update_followup`, or `cancel_followup`. Call
`tools/list` for the current tool schemas.

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
the same transaction. The internal source is `core:timer`. The task provides
its final Thread key and uses its task ID as `sourceMessageId`. The Message
contains the followup reason in a text block. The `followup` field in the
`routed_message` and Topic `get_message` results gives the task ID, revision,
due time, and material references from the scheduled task.

The single-subscription rule routes the Message to its Topic via StartOrSteer.
Use `get_topic` with `include: ["followups"]` to check the current task.

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

Call `tools/list` for the `send_reply` content schema. The content type can be
`markdown` or `options`.

Router may give one explicit `messageId` or the distinct `messageIds` of its
current routing input. Topic may give an explicit associated Message or omit
the target to use its oldest associated Message. Core orders the candidates by
its own `receivedAt` then the stable SQLite acceptance sequence. It checks the
chosen Message's reply target after selection. No match, missing reply target or
an out-of-scope target returns an error. Issue Session has no reply capability.

An Outbox item carries `id`, `messageId`, `content` and `status`, with optional
`receipt` and `blockerRef`. The status is `pending`, `sent` or `failed`.
A Topic can set `blockerRef: {issueId, blockerId}` for one open human blocker
in its own Issue. Router replies cannot set this field.

### Quoted answers

For structured intake, `inReplyToOutboxId` records the adapter's match at
admission. Topic `get_message` on an associated Message can return
`quotedReply: {outboxId, blockerRef?}`. The Topic Session receives the related
blocker context while the blocker remains open and belongs to that Topic.

For legacy DWS intake, Core matches the inbound `quotedMessage.message_id` against the
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

Core resolves the account and reply target from the original Message. For each
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

Channel reads include `source`, `accountId` and the original Message's
`replyTarget`. During the transition, reads for legacy items carry `channel`.
Query results include pending replies with and without known tasks.
Failed replies with an elapsed `receipt.retryAt` are eligible for retry.

Result writes accept `Content-Type: application/json` with `outboxId`,
`expected: {status, receipt?}` from the read, and `outcome`:

| Outcome | Additional fields and behavior |
| --- | --- |
| `pending` | Save `taskId` before querying. Subsequent pulls can query the same native task. |
| `sent` | Preserve the current task ID when present. Adapters preserve native delivery identifiers in their own state. |
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

Results for legacy items can include the native `conversationId` and `messageId`
on `sent`. When present, `messageId` requires `conversationId`.

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
