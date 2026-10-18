# Go Runtime events

The experimental Go Runtime publishes schema version 1 through `Session.Next`.
Each event has one `type` and one compatible concrete `payload`. Go callers use
`Event.Message`, `Reasoning`, `Plan`, `Metadata`, `Tool`, `Work`, `Session`,
`Interaction`, `Association`, `Creation`, `AgentState`, `Task`, `TaskSnapshot`,
`Usage`, `Observation`, `Error` or `Diagnostic`. JSON uses a single `payload` member;
it never serializes SDK objects, methods or response handles. Invalid unions,
unsupported schema versions and invalid message/content discriminants fail JSON
decode. Unknown envelope fields are ignored; unknown payload fields are rejected
by this version of the common schema. Native unknown fields do not enter a raw
public escape hatch. Unrecognized native variants retain a bounded kind and
digest as diagnostics. Supported message facts are converted in the adapters.

## Envelope and identity

Every current event stream is session-scoped and carries the configured Runtime
name and original native session ID. Codex uses its native thread ID. A random
connection epoch marks this attachment, including observations buffered before
Claude identity was known. The common publication owner assigns a positive
`seq` once per delivered event, in the current session lifetime. Its maximum is
2^53-1; exhaustion fails instead of wrapping. There is no durable cursor or
ordering promise between sessions.

`observedAt` retains adapter observation time where available.
`sourcePosition` is an attachment-local adapter receive cursor, scoped by the
event's Runtime/session/epoch. Several converted facts can share one source
position. Separate SDK event/request channels and callbacks do not establish a
native wire total order. Codex permissions from the separate request channel have no sourcePosition.
Final `seq` is distinct from that source position.

`origin` records native, SDK-derived or Runtime provenance, attribution and
observation completeness. Claude's explicit `isReplay` and `isSynthetic` values
retain presence, including explicit false. Known origin kind is retained;
replay never implies a newly created entity. Child/unresolved observations retain their actual subject and conservative
provenance. A child observation cannot complete root work or change its identity.

A message reference is a native message/item ID only when observed. A frame UUID
is `frameRef`; it never becomes a message or block reference. Content block
indexes retain the source value. Codex reasoning indexes have separate
`content` and `summary` namespaces. No native block ID, missing message ID or
Claude turn ID is generated. Without a proven message reference or block index,
those fields stay absent. Repeated equal deltas are all delivered; text equality
is not a deduplication rule.

## Messages and observed reasoning

| Common types | Codex | Claude |
| --- | --- | --- |
| `message.started`, `message.delta`, `message.completed` | Native user/assistant items and assistant text deltas share their item ID | API message-start establishes message ID and role; indexed block starts/deltas and full messages retain that association |
| `reasoning.delta`, `reasoning.completed` | Observed text/summary updates and reasoning items | Thinking blocks/deltas and explicitly redacted thinking blocks |
| `plan.updated` | Native structured turn plan replacements and prose plan item previews/completions | No standalone plan source mapped; tools or assistant text do not imply a plan |
| `session.metadata_changed` | Native title and model-reroute notifications | Present fields from authoritative root init observations, including repeated init |

Message payloads retain role and stage. Full content replaces the corresponding
message snapshot; consumers must not append it as a second unrelated message.
The ordered content list keeps block boundaries. Reasoning has its own payload
and preserves the message/index association. Reasoning blocks in a full message
are also marked with the reasoning namespace; consumers can use that association
to avoid rendering the same observation twice. Explicit redaction emits no
hidden data or reconstructed thought. Signatures alone yield no reasoning text.

Claude block-stop events carry `blockIndex` and `blockStage=completed` with
missing content, since the source does not repeat the text. API message-delta
boundaries likewise carry no invented text. API message-stop marks the end of
that message with missing content and unknown content completeness; it retains
the original message reference. A subsequent full assistant message supplies its
own observed snapshot. The Runtime does not reconstruct one by accumulating
previews. A new message-start resets the stream association, and message-stop
clears it. A full message's own ID is authoritative for that full observation.

A tool-bearing user/assistant message retains its role and ordered block
positions. Tool blocks have explicit unsupported content placeholders in the
message plus separate typed tool observations, with distinct call, message and
block-index fields. Tool IDs are never block IDs. See the tool contract below.

`ContentSet.availability` distinguishes `missing`, `null`, `provided_empty`,
`provided`, `redacted` and `unsupported`. An empty string has an explicit text
block containing `""`; an empty block array has no fabricated text block.
`completeness` separately records complete, partial, truncated or unknown content.
Unknown media stays an unsupported block with partial completeness; media bytes
are not exposed as text. Native JSON content is a JSON block with lossless JSON
bytes, including a provided JSON value `null`, which differs from a null content
field. Length alone never establishes truncation.

The pinned Claude SDK rejects null or absent content in a complete assistant/user
message before producing a usable native event. Those malformed shapes fail the
stream; the Runtime does not fabricate a complete empty message. The SDK's raw
API message-start observations can represent missing and explicit-null content,
and the Runtime preserves that distinction. Codex's optional reasoning content
can also be missing without inventing reasoning.

## Plans and metadata projection

Structured plans retain ordered text/status entries and replacement semantics,
including an empty replacement. Native `inProgress` maps to `in_progress`;
unknown statuses remain explicitly unknown. Prose plans retain the actual text
and stage without inventing checklist entries or execution evidence.

Metadata has explicit present-field replacement semantics. Nil pointers mean no
update; a present pointer to an empty collection clears it. Capabilities, commands,
tools, model, working directory and title remain separate fields. Native capability
labels are observed metadata, not a promise that MASS implements those features.
There is no independent session name. Null metadata values are invalid, rather
than an implicit empty replacement. `Session.Snapshot().Metadata` projects the
published patches; reading it does not consume events or start a process. Returned
snapshots own their field values and collections.

## Structured tools

`tool.announced`, `tool.updated` and `tool.completed` carry `ToolPayload`.
`callRef` is the original native item/tool-use ID, scoped by Runtime, session and
attachment epoch. Updates and results use that reference even when the result
has no name, message ID or input snapshot. `messageRef` and `blockIndex` retain
their separate native coordinates. A missing field stays unknown; consumers can
associate observations by callRef without a Runtime-owned history cache.

`name` is the semantic tool name, `category` is execute/read/write/search/network/
other/unknown, and `displayLabel` is optional presentation text. Codex command
execution uses name `commandExecution`, category execute and the actual command
as its display label. Its input JSON keeps command, cwd and parsed actions;
locations carry the working directory. File changes use category write and keep
ordered changes/diffs in input JSON plus their paths in locations. MCP and
dynamic calls keep tool name, server/namespace and structured arguments. Their
category is unknown because a name does not establish the operation's semantics.
Claude tool names likewise do not establish a semantic category.

| Observation | Common state and evidence |
| --- | --- |
| Codex item-start or Claude tool-use | announced / call_observed |
| Codex command/file output delta or MCP progress | running / native_progress |
| Claude indexed input JSON delta or block-stop | announced / call_observed; input generation does not prove execution |
| Native tool result or completed item | completed, failed, cancelled or unknown, according to observed outcome |
| Permission acceptance or parent work completion | No tool state/result synthesized |

Claude API input fragments use `inputJsonDelta`, an explicit JSON fragment string
with missing/partial input. A fragment can be invalid standalone JSON; it never
replaces a structured argument snapshot. The full tool-use message supplies the
observed JSON input. Block-stop only ends input generation and has no invented
result. The mapper retains one active API block's immutable identity, resets it
at block/message boundaries, and never accumulates argument fragments. Stream
previews and full observations may announce the same callRef again; they are
observations of that call rather than two calls.

`input`, `output` and optional `content` are independent ContentSets. Codex MCP
result content keeps its ordered blocks in output; `structuredContent` is a JSON
value in content, including an explicit JSON value null. Dynamic result content
keeps text blocks and explicit unsupported placeholders. Claude tool-result
strings keep their exact text, arrays keep ordered blocks, and explicit JSON
blocks retain JSON values and lossless numbers. Unsupported media/unknown blocks
make completeness partial; media data is not flattened or exported as text.
The pinned Claude SDK rejects bare object tool-result content before producing a
usable event. It accepts missing/null tool-result content inside an otherwise
valid complete message; these remain distinct from an empty string or array.
This does not change the complete message's stricter content boundary above.

Missing/null output has no invented block. Empty strings have an actual empty
text block; empty block arrays have no text block. MCP result absence/null is
retained independently of a provided empty content array. No output length is
interpreted as truncation. The common schema supports completeness truncated
only with explicit `truncationEvidence`; round-trip tests verify that rule.
The pinned SDK's supported tool schemas expose no recognized native truncation
signal, so a positive native tool-truncation mapping remains unverified. Unknown
extension fields and text that merely says truncated do not establish evidence.

Codex nonzero exit codes, failed/declined statuses, MCP errors and dynamic
`success:false` retain failure evidence, including terminal evidence carried by
an item-start notification. Explicit outcome evidence takes precedence over
that notification label. Exit code zero and duration zero retain
presence via pointers. Claude `is_error:true` yields failed/native_is_error.
Parent success cannot override a tool failure or complete an unresolved call.
Unknown Codex item variants retain a safe diagnostic, digest and proven call
reference; unknown Claude content remains explicitly unsupported. Malformed
supported tool data yields a session decode failure when attributable data is
available; shapes rejected earlier by the SDK terminate its stream with a safe
error. Native error text is not copied into a public error/diagnostic.

## Verification and remaining scope

Public-interface suites build the pinned SDK's mock CLI commands in temporary
directories and replace child environments with synthetic scenarios. They cover
both protocols' message and reasoning conversion, plans, metadata, identity and
serialization. No agent/model or credentials are used. Passing these tests does
not verify live CLI compatibility.

Work data retain native status in this envelope. Interaction authority follows
the gate below; bounded delivery follows the policy below. Runtime-local publication does
not establish whole-process memory bounds,
complete agent/task attribution, durable replay or native causality.

## Submission association and live state

Events carry operationRef only when the submission association is proven. Codex
work references remain original native turn IDs and are matched to the SDK
StartTurn reply, including notifications received before that reply. Claude
root results use the exclusive admitted operation without fabricating a native
work ID. Child and unresolved observations cannot settle root foreground work.
A user echo alone does not prove native recording; Codex requires the matched
work reference. Native-recorded evidence does not mean a durable host commit.

A per-session receive worker assigns sequence and projects the latest snapshot.
Next cancels only its consumer wait, and a snapshot may be ahead of delivered
events. Metadata omission/explicit clearing still uses replacement patches.
Foreground completion, attachment release and background completeness remain
independent. Background inventories retain per-source completeness; absence of an
inventory stays unknown. Cancellation ACK and terminal status remain separate observations.
Only a native explicit interrupted status becomes interrupted; a generic error
or failed result is never converted to cancellation by local intent.

## Questions and permissions

`interaction.requested` carries only data: an attachment-scoped random
`interactionRef`, permission/question kind, requested state, tool/call context,
one-time decisions, or ordered questions. Codex decisions are restricted to the
observed native offer when present; policy amendments and session-wide grants
are never converted into one-time approval. `Question` retains labels and
descriptions, selection multiplicity and nullable required/freeText/secret
constraints. An absent constraint is unknown. Native request IDs, SDK handles,
callbacks and native response objects are private.

`Response` contains either a one-time permission `decision` or `answers` keyed
by the observed question reference. An answer uses exactly one scalar `text` or
`selections` array. Validation uses the retained original specification, so edits
to a dequeued event do not change authority. Native Claude question/input unknown
fields survive SDK answer conversion, without becoming a public RPC escape hatch.

`interaction.resolved` carries the original reference and observed resolved or
cancelled state when correlation is available. SDK response-write success alone
never creates this event. A requested event can be read after its authority has
been invalidated. Native resolution observed before local reference registration
remains diagnostic, and SDK liveness is still checked at response admission.

`Runtime.Respond` validates the exact SessionKey; the attachment owns the final
single-attempt gate. It rejects cross-session/old-epoch references, spent or
invalidated requests and unsupported permanent decisions before SDK handoff.
`ResponseReceipt` separates handoff, completed write and native-resolution
evidence. Unknown writes consume the attempt and never permit automatic retry.
No socket-write revocation or guarantee about unobserved native resolution is
provided. See [Runtime limits](../README.md) for pinned SDK ID and Claude
historical-target observability limits.

## Agents, runs, tasks and accounting

`subject` separates the observed agent, run, task, parent tool and native source
session from the event envelope's owning SessionKey. Missing identities stay
absent. SDK association references have `associationScope: sdk_attachment` and
are meaningful only within Runtime name, native session and connection epoch.
Neither an association reference nor a task/tool ID becomes a native agent ID.

`agent.associated` and `agent.created` directly convert the pinned SDK's distinct
records. MASS adds no relationship graph or creation inference. Association
payloads preserve evidence, provenance, parent and enrichment/redirect references.
A first observation, repeated init, resumed task, replay, or history association
cannot by itself establish creation. Claude's pinned SDK creation criteria require
its exact supported CLI version, builtin tool evidence and corroborating native
local-agent observations; these synthetic tests do not verify an installed CLI.
Codex successful spawn may produce Created without an additional Associated.
Native thread snapshots returned during Codex creation seed the SDK subscription;
historical relationships remain associated, never newly created. Resume keeps its
pre-RPC subscription and excludes native turn history; no history-import API or
Claude history-seed input is exposed by this Runtime.

`agent.state_changed` describes observed source/run state. A terminal run does
not delete the reusable agent. Claude's SDK-derived records preserve original run
ID and stale qualifier; a missing run never inherits the newest run. Native frames
preserve only their own observed identities; native stale classification remains
unknown when the public SDK provides none. Codex retains child turn IDs separately
from root WorkRef. Child and unresolved facts never acquire the current foreground
operation association. Child message streaming contexts are isolated by exact
observed source/run/tool identities; enrichment does not guess a stream merge.
Child metadata cannot overwrite root metadata.

`task.changed` retains original task identity, run, background evidence, observed
status and optional progress/result. `stateObserved` distinguishes an absent
status patch from an explicitly observed unknown native status: omission keeps
the prior projection, while an unknown observed status replaces it with unknown.
`task.snapshot` is an explicit replacement inventory for a source scope, including
an empty inventory. Omitted tasks mean no update; null or malformed known inventory
is a decode error. Root task notifications use the native session's inventory
scope; explicitly sourced child inventories remain separate. Source-scope values
are opaque. `Session.Snapshot().Background` includes task observations and per-source
inventories/completeness independently of foreground and attachment state. Unknown
inventory differs from an observed empty replacement. Global completeness stays
partial even for a complete source inventory, since unseen child sources may exist.
Terminal task observations are retained until their source replaces them.

Background projection retains at most 256 task/run entries, 256 source scopes and
1 MiB serialized state per attachment; overflow ends publication with explicit
`ErrBackpressure`, preserving the prior snapshot. Claude child message contexts
retain at most 256 distinct source/run contexts and 1 MiB of identity data; excess
observations emit a scoped backpressure failure without guessing another context.
These fixed local limits do not bound SDK queues or whole-process memory.

`usage.updated` preserves native accounting counters as numeric JSON without
float64 conversion, its accounting scope and cumulative/delta/unknown mode.
Codex total and last counters are separate cumulative and delta observations;
Claude message, task and result counters keep distinct accounting scopes and
unknown mode. Native child inclusion is currently unknown in both adapters. The
Runtime never sums accounting observations; root totals and child measurements
must not be blindly added. Consumers must retain envelope/subject scope and source
position when deciding whether a measurement is additive. Common serialization
also represents explicit yes/no child inclusion when evidence is available.

`observation.changed` preserves SDK forwarding/lineage/run observation information
and completeness separately from native work termination. SDK startup uncertainty,
unknown ancestry and local observation gaps cannot manufacture root authority.


## Delivery settlement

One per-session publication owner feeds the common ChannX queue. Configurable
count/serialized-byte quotas include accepted unread output, and apply equally
to previews, complete facts, interactions and lifecycle data. Capacity failure
retains the accepted prefix and finishes Next with explicit incomplete/backpressure;
it does not silently evict complete events. Unknown valid variants keep a
classified diagnostic and digest, including valid non-object payloads. Known
malformed facts publish decode_error at the proven session scope and then seal
that attachment; unknown supported fields retain their documented availability
and extension behavior. Safe error/diagnostic payloads exclude arbitrary native
error text and raw unknown payloads. Recognized user/tool content remains content.

StopDrain and StopAbort have distinct public delivery settlement and a total
bounded wait; see the [Runtime stop contract](../README.md#ordered-delivery-failure-and-bounded-stop).
A terminal SDK request channel is not evidence of event-stream EOF. Source
channels and observation timestamps never establish a native global order.
SDK queues and native truncation/timeout limits remain outside stronger Runtime
guarantees. No local drain, EOF or ACK proves native release.

The available stream is session-scoped. Runtime/operation lifecycle event streams,
distinct opened/resumed/attachment-state event families and dynamic capability
availability records remain unavailable. Status/operation polling and close/stop
receipts provide current lifecycle observations. See the
[protocol Runtime contract](runtime-contract.md) for these remaining gaps.
