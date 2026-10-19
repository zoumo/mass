# Protocol Runtime contract

Future development follows [harness](../../harness/index.md). This page records
the existing Go boundary; differences from the shared design are implementation gaps.

This contract describes the available protocol Runtime boundary. It is separate
from the native service's API and storage contract. A host registers named
instances of system-provided protocol types. Each instance manages several
native sessions, owns their transport attachments and response authorities, and
converts supported observations into common data. Durable storage and model
execution policy belong to the host.

## Resources and ownership

A registration consists of name, protocol type and configuration. Registration,
type discovery and managed queries have no process or model side effects.
Activation is explicit; concurrent activation of one name joins one attempt.
Different names have independent ownership. Configuration and active revision
are distinct. Execution configuration cannot change while active resources,
retained sessions or unresolved operations refer to it. Retired handles cannot
activate a replacement registration.

Session identity is exactly the configured Runtime name and original native
session ID. Codex uses the independently managed native thread ID; Claude uses
the authoritative root session ID. Tree groups, message/frame IDs, agent/run/task
IDs and operation references are separate namespaces. No session name, pending
session ID or native work ID is invented.

Before identity exists, creation is an operation. It may await explicit initial
input or native identity. Only caller-supplied input is sent, including explicit
empty input. Pending operations do not appear as sessions. Ending a caller wait
does not abandon ownership. Cancellation reserves cleanup of the same operation,
including late success. Unknown creation is never retried automatically. Bounded
provisional overflow or expiry reports incomplete or unknown outcome and local
cleanup evidence; it never reports a complete open after discarding early facts.

Managed queries retain closed/offline records with observation time and freshness.
They do not discover or adopt account sessions. Inventory is in memory. Recovery
requires host-retained opaque binding context, original configuration scope and
verified native identity. A request flag alone is insufficient. Mismatched or
unverifiable recovery publishes no replacement session and sends no hidden input.
A failed provisional recovery with unconfirmed cleanup retains its uncertainty
and fences another attachment of that key. PID disappearance does not replace
the cleanup receipt. A successful reattachment preserves identity and establishes
a fresh attachment epoch. The host protects bindings and serializes ownership; they are not a
credential, durable inventory or distributed lock.

## Lifecycle and evidence

Ready states its observation scope. A shared local process can establish
protocol responsiveness during activation; a process-per-session implementation
may only establish readiness to create processes. Ready does not prove model
authentication, provider availability or current health of every session.

Attachment, foreground work, interactions and background observations are
independent. Foreground completion neither finishes unresolved tools nor clears
background activity. Snapshots may be ahead of public event dequeue. One
unresolved foreground input is admitted per attachment; unrelated sessions remain
independent. Only one explicit text block is accepted outbound. Unsupported
content and options fail before transport handoff.

Submission evidence independently reports handoff, completed write, native
acknowledgement, native recording and completion. A write or acknowledgement is
not completion, host durability or permission to retry. Work identity appears only
when native evidence provides it. Cancellation targets the original operation;
acknowledgement and target termination settle independently. Admission remains
fenced while a session-wide interrupt could reach later work. Unknown outcomes
retain responsibility and are never automatically resent.

Closing one session affects that attachment, never a healthy shared service or
another session. Receipts distinguish local detachment, native release, owned
process reaping and incomplete facts. Both current local implementations lack
native-release proof: requesting native release yields `close_unknown`, even
after acknowledgement, EOF or process exit. The unresolved release fence survives
in retained recovery bindings. Explicit local detach can succeed with narrower
evidence; it does not delete native history or roll back effects.

Stop closes admission, invalidates authorities and releases owned resources.
Drain preserves accepted delivery while a reader progresses within the total
wait. Abort explicitly discards unread delivery. Drain deadline expiry reports
unknown stop/incomplete delivery; cleanup ownership continues beyond caller wait,
and reactivation stays fenced until cleanup settles. Owned process reaping does
not prove every remote or background effect ended. Stop does not unregister
configuration or delete workspace/account data.

## Common events and authorities

The following describes the internal protocol observation API. The public
session event boundary currently uses legacy schemaVersion 2 `PublicEvent` through
`EventProjector`. Future development follows the [SessionEvent contract](../../harness/modules/runtime/events.md);
this legacy representation is an implementation gap.
HTTP never exposes protocol lifecycle or authority records as session history.

Events are versioned discriminated data with concrete payloads, observation time
and provenance. Session content carries original native identity, attachment epoch,
per-session sequence and separately scoped source position when available.
Runtime/operation lifecycle envelopes follow the independent scope rules below. The [event contract](events.md)
defines message, reasoning, plan, metadata, tool, work, interaction, agent, task,
usage, observation and diagnostic semantics. Consumers do not parse native RPC
names, native tool encodings or SDK handles. Unknown native variants produce safe
diagnostics; malformed known observations fail at their proven scope. Native raw
errors and arbitrary payloads are not exposed through diagnostics.

One publication owner sequences each session. Independent SDK sources do not
provide native global order; there is no cross-session total order or durable
cursor. Missing, null, empty, unsupported, redacted and truncated content remain
distinct where source evidence reaches conversion. Truncation requires explicit
evidence. Equal no-ID deltas are not deduplicated by text. Association is not
creation, child/run identity is not root work identity, and usage scopes are
preserved without automatic aggregation.

Interaction events contain specifications and opaque references, never response
authority. A response is checked against the retained original specification and
session/epoch authority. Invalid answers and pre-cancelled calls consume nothing.
One attempt is admitted; any handoff consumes it even if the outcome is unknown.
Observed native resolution, cancellation and local lifecycle changes invalidate
subsequent handoffs. Copied/modified events grant no authority. A handoff already
in progress cannot be revoked at the socket, and unobserved native decisions
cannot be predicted. Unresolved child requests gain no root authority.

## Bounds and failure scope

All Runtime-owned admission and retention limits are finite. Zero selects a finite
default, never unlimited storage or time. Output count/serialized-byte exhaustion
preserves the accepted prefix and then reports explicit incomplete/backpressure;
complete accepted facts are not silently evicted or coalesced. Explicit abort
and failed drain report loss. Upstream SDK queues and whole-process memory are
outside these local bounds. A slow reader cannot indefinitely prevent owned
process cleanup.

Session failures seal the affected attachment. Shared transport failures affect
its bound sessions. Other Runtime names remain usable. Failure does not fabricate
successful tool results, interrupted work, native release or new session identity.
EOF does not erase observed submission acceptance. A successful native ACK or
complete local input write remains known even if the stream ends before Submit
returns; foreground execution and completion can independently remain unknown.
Late SDK replies may be discarded after local timeout; no reply repair, automatic
replay or stronger timeout guarantee is provided.

## Available scope and remaining gaps

Both local protocols provide named activation, multiple native sessions, managed
queries, explicit text input, cancellation, interaction authority, local verified
resume and isolated close. Recovery can still be unverifiable if a native peer
requires input before revealing identity. Remote transport, native discovery,
rich outbound content and native configuration mutation are unavailable. External
server connection closure is unverified; local process tests provide no evidence
for that topology. These are separate optional capabilities, not required local
behavior emulated with prompts.

Codex creation requests a non-ephemeral native thread, enabling native history
recovery after app-server exit. The host must separately retain the opaque
binding and original execution scope. Local detach/Stop never deletes native
history or proves native release. Codex child content requires explicit SDK
relationship evidence to this root or a proven descendant; unrelated or
unresolved thread notifications retain only safe diagnostics.

Lifecycle and content observation use the same pure-data event schema. The
Runtime stream publishes runtime status, operation state/failure and distinct
session opened/resumed/state observations. Runtime-scoped session observations
contain the actual SessionKey and epoch in their snapshot payload. Unresolved
operation observations do not fabricate a session header. Creation, resume,
submission, cancellation and response receipts remain distinct typed data.
Pre-admission rejection creates no operation identity.

The lifecycle stream has one Runtime cursor, independent of each session's
content cursor and native ordering. Runtime/operation scope describes the subject
of a lifecycle observation; its publication sequence belongs to that Runtime
stream. A single publication owner still sequences each session content stream.
After settled Stop, accepted lifecycle observations remain readable, followed by
EOF. Explicit reactivation continues the Runtime cursor. Ending a wait never
ends ownership or authorizes replay.

Lifecycle retention is independently bounded by count and encoded bytes. Overflow
preserves accepted facts, then explicitly fails incomplete/backpressure and
fences new work. Fixed-target cancellation and cleanup continue. Stop retains
lifecycle evidence without waiting for its reader; drain/abort controls session
content delivery. Retained lifecycle data are neither silently evicted nor a
whole-process memory guarantee. A terminal lifecycle gap is not repaired by
restarting the same handle.

System types declare per-feature support and conditions. Runtime and session
snapshots project current availability and a safe unavailable reason. These
records reflect local readiness, ownership prerequisites, attachment/foreground
state, interaction authority, retention and control capacity. They are conditional
admission observations, not proof of native success, account authentication or
future identity verification. Copying records does not grant control authority.
Unsupported capabilities carry explicit reasons.

Complete Claude messages with missing/null content and bare-object tool results
are rejected by the pinned SDK before conversion. Stream-boundary missing/null
and tool-result missing/null are covered, but do not prove complete-message
presence parity. Positive native truncation conversion is unverified because no
recognized signal reaches the supported source schemas. Claude callback facts
without original work identity cannot prove historical work attribution. No real
Agent/model, installed CLI compatibility or production readiness is established
by offline mock transport evidence.
