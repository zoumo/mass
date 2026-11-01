# Claude retained history loading

The asynchronous Session load API uses the shared Service load lifecycle and
one runtime stream. Claude uses the immutable SDK package-level
`GetSessionMessages` API and opens `ResumeID` exactly once. The verified native
process remains the process used by subsequent inputs; querying a transcript
alone is not evidence that a session can execute.

## Source and integrity

Both the runtime's explicit `env.CLAUDE_CONFIG_DIR` and the requested `cwd`
(SDK `ProjectDir`) must be absolute. There is no inherited HOME/config fallback
or search through other projects. The requested native ID, observed Session ID,
native init ID, and init cwd are checked separately. A record's cwd, when present,
must match; absent cwd is recorded as unavailable evidence, not invented proof.

Only retained main history is materialized. Each offset query scans the full
bounded file; offsets of 100 are result slices, not I/O pagination. Limits are
64 MiB, 100000 lines/records, 8 MiB per line, 10000 messages and 10000 directory
entries. SDK scan completeness and per-call integrity checks apply. Ordered raw
records and UUIDs are compared before/after resume, across page reads, and after
delivery. These checks detect observed drift; they do not provide a shared
cross-call snapshot or prove that a file never changed and changed back.

Malformed/incomplete chains, duplicate UUIDs, identity conflicts, unsupported
valid content, conflicting tool relationships and observed drift fail the load.
Already committed facts remain visible. A root null parent is legitimate; it
does not acquire an invented parent. Hidden bookkeeping parents remain raw
relationships rather than synthesized visible messages.

## Materialized facts and authority

The adapter preserves ordered user messages, agent messages, thinking, tool
calls and tool results. Exact native record JSON and parsed raw data accompany
the converted facts. Absent, null, empty and explicitly truncated outputs remain
distinct. A missing result stays missing; truncation does not invent omitted
bytes. Native timestamps are used when supplied and valid; otherwise time is
explicitly the import observation time. Unsupported blocks fail rather than
silently disappearing. Historical tools do not restore approval authority or
current state.

Successful load requires verified resume, native completion, and local durable
delivery. Current state remains unknown without live evidence; historical idle
never authorizes input. The shared load completion gate permits safe input once
load settles. Every postload input, including the first, needs explicit native
linkage for work attribution. A caller UUID is an optional correlation aid, not
a native TurnID. Ordinary assistant/result observations without recognized
linkage remain unknown and do not complete current work. The positive synthetic
peer echoes caller UUIDs; that test is NOT a guarantee that real Claude echoes them. No real-model acceptance is
claimed here.

## Bounded failure and cleanup

The existing bounded Service delivery lane remains in force. A 105-record test
proves the offset boundary and first/last ordering only. A 205-record producer
can exceed the established 128 normal queued-event limit and explicitly fail
with `session_delivery_overloaded`; this is not successful large-history load.
The original failed run is retained in ticket07 validation evidence.

Cancellation uses the original load ID. It does not wait for an externally
blocked durable append before acknowledging the request. Cleanup remains false,
input stays fenced, and the original overload remains visible until the owned
native handle is released and admitted local facts drain. Tests release the
public EventStore gate, observe eventual cleanup, and verify a healthy sibling.
Durable drain can take longer than the old five-second shutdown observation;
that deadline alone is not proof of deadlock.

If SDK Open fails without returning a Session handle, the adapter cannot prove
native release. Even when an independent synthetic process log shows exit,
product cleanup remains honestly unconfirmed, with original responsibility
retained. Shared cancel/stale-ID/stop/restart behavior is reused, not replaced
with a second orchestration mechanism.
