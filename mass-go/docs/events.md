# Go message and metadata events

The experimental Go Runtime publishes schema version 1 through `Session.Next`.
Each event has one `type` and one compatible concrete `payload`. Go callers use
`Event.Message`, `Reasoning`, `Plan`, `Metadata`, `Tool`, `Work`, `Session`,
`Interaction`, `Error` or `Diagnostic`. JSON uses a single `payload` member;
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
native wire total order. Final `seq` is distinct from that source position.

`origin` records native, SDK-derived or Runtime provenance, attribution and
observation completeness. Claude's explicit `isReplay` and `isSynthetic` values
retain presence, including explicit false. Known origin kind is retained;
replay never implies a newly created entity. Child/unresolved observations keep
the existing diagnostic behavior pending the separate agent-attribution work.

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
message plus separate typed baseline tool observations, with distinct call,
message and block-index fields. Tool IDs are never block IDs. Structured tool
input/output and execution evidence remain outside this message slice.

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

## Verification and remaining scope

Public-interface suites build the pinned SDK's mock CLI commands in temporary
directories and replace child environments with synthetic scenarios. They cover
both protocols' message and reasoning conversion, plans, metadata, identity and
serialization. No agent/model or credentials are used. Passing these tests does
not verify live CLI compatibility.

Tool/work/interaction data use concrete baseline payloads in this envelope.
Their richer execution, control, authority and failure guarantees remain separate
work. Runtime-local publication does not establish whole-process memory bounds,
complete agent/task attribution, durable replay or native causality.
