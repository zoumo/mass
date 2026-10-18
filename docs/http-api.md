# Native HTTP API (Native Tickets 01–07)

Current product contract: Codex 0.160.1 text and commandExecution. The earlier ACP contracts are superseded by [native design](design/native-runtime-connectors.md). Codex is the only implemented provider. See stage acceptance for current evidence and limits.

`POST /api/v1/agentd/jsonrpc` accepts JSON-RPC 2.0 single calls. `/livez` and `/readyz` remain HTTP health checks. Notifications return 204 without a result; batches are unsupported. Codes: -32600 invalid envelope, -32601 unknown method, -32602 invalid parameters/unsupported capability/busy, -32603 native/storage error.

| Method | Implemented behavior |
|---|---|
| `agentd/runtime/types` | currently `[{type:"codex"},{type:"codex-remote"}]`; composition-owned catalog, including unconfigured types; no activation |
| `agentd/runtime/list` | configured instances: name/type/enabled/status; no raw configuration |
| `agentd/runtime/status` | strict `{name}` selector; same instance view as list |
| `agentd/status` | configured runtime names/count, connected **session attachment** count; reads never activate a runtime |
| `agentd/session/new` | `{sessionName,spec:{runtimeName,cwd}}`; unique nonempty name and existing absolute cwd; native handshake/thread open then atomic metadata commit; no hidden prompt |
| `agentd/session/list` | persisted bindings and current live values; no runtime activation |
| `agentd/session/status` | `{sessionName}`; same object as new/list |
| `agentd/session/prompt` | `{sessionName,prompt:[{type:"text",text:"..."}]}`; nonempty array, ordered text blocks only |
| `agentd/session/get_event` | `{sessionName,afterSeq?:0,limit?:100}`; exclusive afterSeq, limit 1..1000; `{events,nextSeq}`; offline file query |
| `agentd/session/watch_event` | `{sessionName}` plus request id; live-only `text/event-stream`, one public event per data line; no initial snapshot/replay |
| `agentd/session/cancel` | `{}` after actual native interrupt ACK, or local no-work success; see cancellation contract below |
| `agentd/session/archive` | `{sessionName}`; same session object with lifecycle=offline only after confirmed native thread release; close_unknown on deadline |
| `agentd/session/resume` | `{sessionName}`; strict local recovery then native thread/resume, stable public/native identity; no new fallback |
| `agentd/session/set_config_option` | -32602 with `data.kind=unsupported_capability` |

Runtime registration uses the strict file schema in the [startup guide](../README.md).
Discovery never resolves secrets, reads the binary or starts a process. Instance status has
`phase` (`pending/starting/ready/stopping/stopped/error`) and nullable `unavailableReason`
(`reason/message`); ready clears the reason. Disabled instances remain visible and reject
new/resumed sessions while existing attachments retain control. `/readyz` is 200 for zero
instances or any enabled pending/ready instance, otherwise 503 with status `unavailable`;
`/livez` remains independent. Internal registration identity/version are not returned.

Session objects include sessionName/sessionId/spec, `formatVersion:2`, `historyMode:"paginated"`, connected/lifecycle and actual MASS capabilities. sessionId is the exact opaque ID returned by the Agent; no second MASS ID or nativeSessionId is returned. Private instance/recovery bindings and native host/version/transport details are excluded. New, resume and archive results include a separate operationId; lifecycle errors after admission retain it for diagnostics. Creating accepts only sessionName and spec.runtimeName/spec.cwd; it rejects preset sessionId and unknown fields. The name is reserved before opening; successful metadata commit precedes Handler activation and success. Caller disconnect does not abandon the accepted operation. An open_unknown response has operationId without an invented sessionId, retains ownership of its eventual result and never retries it. Current state/sessionInfo/usage/availableCommands remain null until observed; connected configOptions is the known empty catalog, offline current values are null.

Prompt/text, getEvent, live watch, shell execution and automatic once-only command approvals are enabled. Online tools=[shell], toolApprovals=true and toolOutput=retained_or_unavailable. cancel is enabled while connected. archive is available, resume is available only offline, and both prompt/cancel are disabled while closing. concurrentInput/steer/setConfigOption/interactiveRequests/backgroundLifecycle remain false. Multiple sessions on one runtimeName share a single lazy app-server and can run concurrently; concurrentInput remains false **within each session**. Different runtime names own separate hosts, even when their configurations match. Claude is unsupported.

Host cwd `/` and configured environment are runtime-scoped; each native thread receives its own spec.cwd. Shared transport routes by thread ID to its immutable attachment; unknown thread notifications never enter an arbitrary session. MASS admission rotates ready attachment lanes with a total queued-command capacity of 32 and verifies attachment ownership before upstream API handoff. The native client owns wire publication and response correlation separately from server-request replies. Request IDs are host-local; hostGeneration is retained only in private adapter evidence.

Creation reserves its name, stable instance and operationId without holding the global session map during Agent open. Runtime activation remains owned independently of its observer; an activation deadline reports runtime_unavailable without starting an Agent session or inventing an ID. A 30-second Agent open deadline reports open_unknown and keeps the same name reserved until the original result settles or releases. Metadata failure releases only the newly opened Attachment; thread release, notLoaded or confirmed owned-host reclamation supplies release evidence. Unknown release keeps its route, cleanup owner and reservation. Healthy peer sessions and the shared host remain available. Lifecycle and command failures expose stable MASS reasons and sanitized messages. Runtime commands return typed MASS results; the session protocol facade encodes JSON-RPC errors.

The bound command handle allocates operationId before admission. Prompt admission allocates a separate submissionId, sends clientUserMessageId to native turn/start and requires both its successful reply and a matching complete native userMessage/clientId, in either order. Result: `{operationId,submissionId,messageId,acceptance:"native_recorded"}`. This is not a universal model-consumed or Journal receipt. User history comes exclusively from the native echo. Ordinary prompt content is never substituted into history. Busy/unsettled input returns -32602 with `data.kind=busy` before another native submission. Input is not queued, merged or steered. An empty array, all-empty text, unsupported blocks, extra block fields or encoded content exceeding 1 MiB fails before attachment admission.

Acceptance has a 30-second deadline. Explicit upstream rejection uses `data.kind=rejected`; timeout/disconnect uses `acceptance_unknown` plus operationId, submissionId and observed sent/nativeAccepted/nativeRecorded booleans. `sent` records observed native receipt (an actual native RPC response or native turn/user fact), not local admission or direct socket-write observation. Unknown acceptance keeps admission fenced; there is no automatic retry. Dropping an HTTP caller does not cancel the independently owned submit task or late genuine full facts.

History validates all committed JSONL records, including beyond the requested page. nextSeq is the last returned seq, or the supplied cursor when empty. Watch rejects afterSeq/lastSeq/replayFrom. SSE has no Last-Event-ID continuation. A lagged subscriber disconnects; the bounded broadcaster never blocks core commits. Reading history/status after restart never starts an Agent or invokes handlers.

Service stop drains accepted core deliveries, closes the official runtime transport and reaps its owned child. Native events not yet admitted to the core at Stop are not claimed as drained; Stop does not imply protocol interruption or background-process termination. The exact bounds and shutdown ordering are specified below.

## Native command facts (Ticket 03)

Thread creation uses session-level `sandbox=workspace-write`, `approvalPolicy=untrusted` and `approvalsReviewer=user`. These are native thread options; MASS does not write global config or permission rules. The internal policy answers a supported command approval with exactly `decision=accept`, never an amendment or session-wide grant. Unsupported control requests receive a native protocol error, including fileChange approvals, questions, dynamic tools and configuration requests. Only commandExecution is advertised. Ticket 04 enables cancellation and cancellation-generation fences; archive/resume use the Ticket 05 lifecycle contract below.

`agent.tool_call` is a complete MASS execution projection: name=shell,
kind=execute, rawInput command/cwd, inputRepresentation=execution and
phase=parameters_ready. It can precede approval and does not prove execution.

`agent.tool_result` uses the same public itemId and a separate seq. It contains
public status, available exitCode/durationMs, execution input and public
callContext (or missing). Missing/null output is unavailable/unknown with no
rawOutput/content; provided strings, including empty strings, are unchanged with
completeness=retained. Proven truncation uses completeness=truncated and generic
output.truncation omittedBytes/retainedBytes. Native fields and the fixed-version
truncation proof remain private. No output deltas supply history.

Each schemaVersion=2 JSONL row contains event and versioned evidence. History,
SSE and Handlers see event only, after append and flush. Adapter-owned validation
checks original native objects against the exact public projection before resume.
See [events](events.md) and [storage](storage.md).

## Native cancellation (Ticket 04)

`agentd/session/cancel` and Handler cancellation use the same attachment-bound control. Ordinary input and control ingress each have an independent capacity of 32 across ingress/scheduled commands. A session has at most one cancellation round and shared result; repeated calls admitted during that round share one native interrupt. The round captures its target turn and never retargets later work. Host generation, attachment connectionId and cancelGeneration are distinct. Cancellation closes admission and increments only that attachment's cancelGeneration. Cancellation and upstream API handoff have one publication order: every command or grant rechecks ownership and generation before handoff. Requests already handed off retain their actual settlement; the native client owns subsequent wire publication. Approval publication retains its place in that order until the upstream reply settles. It cannot be retroactively withdrawn.

An actual `turn/interrupt` successful response returns `{}`. It does not mean foreground termination or background-process cleanup. After cancellation admission, rejection, deadline or lost observation returns -32603, data.kind=cancel_unknown. An already offline attachment rejects before cancellation admission with -32602/data.kind=offline. Unknown results retain the fence even if a late ACK or terminal arrives. The independently owned native cancellation task still waits for a late turn identity and interrupts that turn after the HTTP deadline; it does not reopen admission or rewrite the already returned unknown result. Notifications remain HTTP 204 without result guarantees. An unused attachment or fully settled terminal with no old callback/policy is a local no-work success, without fabricating idle. A never-written input invalidated by cancellation returns -32603/data.kind=cancelled with sent=false; no native input or interrupt is needed for that local no-work case.

Cancellation during a pending turn/start keeps reading the real response and native turn/echo identities, then targets that exact turn. A 30-second HTTP acceptance timeout does not abandon settlement. If interruption ends the turn before a complete user echo exists, the prompt reports acceptance_unknown with actual observed booleans; MASS never inserts a prompt copy. Every failed bound prompt/cancel attempt has its own operationId, including calls sharing one cancellation round; no native cancellation generation is exposed. Admission remains `cancelling` until interrupt ACK, matching actual terminal, prior start/acceptance observer, server callbacks, delayed observations, accepted fact delivery and delayed policy decisions settle, in either ACK/terminal order. Old commands still in MASS admission reject before official API handoff. A request already handed to the upstream client, including one still waiting in that client, cannot be classified as locally suppressed: its settlement remains owned, and cancellation waits for its actual turn identity, interrupt, terminal and request settlement. A cancelled, fully settled turn may admit a new input even when no user echo was recorded.

The ordinary once-only policy rechecks host, attachment, cancelGeneration and turn/item at publication. Native resolved invalidates the callback. A delayed policy result is still settled locally after its gate releases; no stale accept is written, and its held context budget is then released. Already handed-off approval decisions cannot be withdrawn: subsequent stopping requires a real interrupt. A matching turn/completed may clear unfinished tool context without emitting an agent.tool_result. Foreground termination does not erase ownership of same-work native facts. Repeated native tool startup metadata with unchanged input does not publish another call or grant approval; input conflicts still fail. Complete native item facts remain authoritative; delta-only text/thinking/tool output is never promoted to history on interruption.

Evidence, failures, version and cleanup: Ticket 04 acceptance. Archive/resume and EOF/Stop/backpressure are covered below; final current-product evidence is in stage acceptance.


<a id="official-rust-client-and-transport"></a>

## Native transport boundary

The adapter owns native handshake, framing, request routing and callback resolution. MASS owns admission, generations, policy and durable facts. Valid unknown notifications are ignored; damaged known payloads fail the attributable attachment or, if unattributable, the host. Shutdown reclaims owned resources separately from protocol interrupt. See [client implementation and provenance](develop/codex-client.md) and [adapter behavior](design/native-runtime-connectors.md).

## Archive and explicit resume (Ticket 05)

Archive fences target admission and new permission decisions, then interrupts foreground work while still subscribed. It settles the target submission, native interrupt, callbacks, delayed policy decisions and already accepted complete facts before unsubscribe. The independent control lane prioritizes unsubscribe. Fixed child-only `-c thread_unload_delay_secs=0` is unchanged. A matching real `thread/closed` or unsubscribe response `status=notLoaded` proves thread release; successful wait/reap of a failed owned host separately proves that host is gone; ACK, `notSubscribed` and idle alone do not. Archive never invokes native archive/delete or stops the shared host. Runtime-independent Handler instances and committed files remain.

Each runtime config accepts `closeTimeoutMs` (1..300000, default 30000). One total deadline covers closing, settlement and release. On deadline the RPC returns -32603/data.kind=close_unknown, status is lifecycle=closing, connected=false, resume=false. The original route and independent close task remain; repeated archive returns the same unknown error until actual late release completes offline. A missing terminal/closed event can keep that tombstone indefinitely while its host remains alive; confirmed owned-host reap allows later explicit recovery; no single-thread timeout kills the shared host. Previously subscribed SSE readers may continue receiving accepted facts from their original attachment; they do not follow a resumed attachment. New watch and commands are rejected while closing. After confirmed release, archive is idempotent offline.

Explicit resume serializes only this session lifecycle, checks unchanged persisted metadata and instance/format binding, then every public committed record, then adapter-owned recovery evidence and private committed records (schema, session identity, consecutive seq, public identity, opaque versioned private evidence checked by the adapter, native payload types/required fields, exact public message/tool/output projections and nested callContext consistency). Only after all checks pass may it truncate an unterminated tail and start/attach the runtime. Failed or unsupported recovery never deletes old data or calls thread/start. An open timeout retains its session reservation until actual late result cleanup. Returned old turns/items are not imported; `excludeTurns=true` is sent. No old input, tool, callback or Handler action is replayed. Metadata is not rewritten. New publications continue the recovered seq through fresh attachment-bound commands; old Handler commands retain their old attachment and fail. Resume can reuse the same host or lazily activate a new host after a full MASS restart. Offline queries start neither. Local ownership checks require the original file-backed ChatGPT account/user, data directories and global configuration; missing or changed ownership evidence returns `data.kind=recovery_unavailable` before activation. See [verified recovery scope](storage.md).

Single daemon writer per data directory remains required. Tail truncation/Journal flush do not provide fsync or power-loss guarantees. Native context retention determines recovery; thread/closed and protocol interrupt do not guarantee every external background descendant is gone. See real archive/recovery evidence.


## Native Ticket 06 fault and shutdown boundary

A native-recorded receipt can precede its bounded asynchronous core delivery;
only the Journal-backed `get_event` and committed SSE/Handler object prove a
local durable event. Append failure seals that attachment's writer, reports a
live session error and prevents uncommitted durable publication. Fixing the path
does not reactivate it. Ordinary command saturation rejects explicitly. The
prompt limit is 1 MiB of serialized text blocks. Runtime configuration accepts
`factLaneCapacity` from 1 to 64 (default 64); zero/out-of-range fails startup.

Shared host failure makes all its attachments disconnected/unknown; other runtime
names continue. Explicit new/resume starts at most one new host after owned old
host cleanup, without reopening other sessions or replaying input. An unresolved
failed attachment remains closing until native release and accepted core drain
are confirmed. SSE is live-only: after resume, subscribe to the new attachment.
Old subscriptions and Handler command contexts cannot attach to it implicitly.

A saturated core lane waits for full-fact capacity and rejects new ordinary
admission with `backpressure`; previews may drop. Individually oversized facts
or settlement timeout report history incomplete and close the attachment. The
fixed official client is now patched with a bounded 32-event consumer shared with
initialization/in-flight events. `sdkEventBytes` configures 1024..33554432 bytes
(default 32 MiB). WebSocket frame/message protection remains 128 MiB; RPC result/
error limit is 2 MiB. These are separate from the 128 MiB MASS payload pool and
are not an RSS ceiling. See [the exact limits and shared transport boundary](design/native-runtime-connectors.md#native-ticket-06-bounded-core-delivery-and-shared-transport-pressure).

On service shutdown, admission closes first, accepted core deliveries drain,
runtimes stop/reap, and Handler consumers drain within their configured total
deadline. SSE consumes already queued publications then reaches EOF. HTTP/SSE
caller disappearance does not automatically cancel a native turn. No idle,
complete message or successful tool result is manufactured from Stop/EOF/chunks.
Blocking file I/O remains outside any finite shutdown promise.

`codex-remote` instances use the same Session API. Session `spec.cwd` names an
absolute directory in the remote filesystem; local existence is not required.
Their `capabilities.resume` remains false online, archived and after daemon restart;
`agentd/session/resume` returns `unsupported_capability` without reconnecting.
A stopped Remote instance means its own connection/local worker ended. A Session
with no native release evidence remains closing with `close_unknown`; remote work
may continue. Other clients and the shared service are not stopped.
