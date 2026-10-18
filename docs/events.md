# MASS event contract (schemaVersion 2)

Only authoritative complete facts enter history. Previews never accumulate into
complete content, including on idle, cancellation, EOF, archive or Stop.

Every public envelope contains schemaVersion=2, sessionName/sessionId, type, time
and payload. Item events carry opaque itemId. Consumers never decode item IDs or
use them as native coordinates. The real Agent-returned sessionId remains the
public session identity. Preview and full facts use the same stable item ID;
tool call and result share their item ID but each receive a separate seq.

| Durable type | Public payload |
|---|---|
| user.message | messageId and complete text content |
| agent.message | messageId, complete text content and optional phase |
| agent.thinking | messageId and text blocks with summary/content channels and indexes |
| agent.tool_call | id, name=shell, kind=execute, title, phase=parameters_ready, inputRepresentation=execution and rawInput command/cwd |
| agent.tool_result | id, name=shell, kind=execute, status, available exitCode/durationMs, execution input, output availability/completeness and public callContext (or missing) |

A call can precede approval and does not prove execution began. Result status is
completed/failed/declined only when proved; otherwise unknown. Missing/null output
has availability=unavailable and completeness=unknown, without rawOutput/content.
An explicitly empty string is provided output. Provided strings appear unchanged
in rawOutput and text content, with completeness=retained or, when proved,
truncated. Truncation includes generic omittedBytes/retainedBytes under
output.truncation. Retained does not promise unlimited terminal archival.

Public events have no native objects, coordinates, version or transport fields.
The adapter preserves these in versioned private evidence and validates the exact
public projection. Reasoning never concatenates channels or invents nonempty text.

One session committer allocates consecutive seq and time, appends one JSONL record
containing both event and evidence, flushes, then shares the same immutable public
event with SSE and Handlers. History returns that public event unchanged. Flush
is not fsync or a cross-file transaction. Write failure seals the writer; the
uncommitted fact reaches neither SSE nor Handlers. Storage errors publish only a
sanitized transient session.error.

Transient categories remain agent.message_chunk, agent.thinking_chunk,
agent.tool_call_chunk, agent.plan, session.state_update, session.error,
session.info_update, session.config_option_update,
session.available_commands_update and session.usage_update. Current code emits
message/thinking previews and observed state/error; other names remain reserved,
not advertised capabilities. Transients have no seq and never enter history.
State updates carry observed state, without native source. Unknown/offline state
is null. Disconnect does not manufacture idle or successful completion.

SSE is live-only. Lagged subscribers disconnect. Complete facts wait in bounded
queues; previews may drop. Handler lanes are isolated. Stop drains accepted facts;
it cannot guarantee progress with permanently blocked storage. Queue and context
budgets include both public projections and private evidence, not total process
RSS, SDK frames or temporary serialization allocations. See [bounds](design/native-runtime-connectors.md#native-ticket-06-bounded-core-delivery-and-shared-transport-pressure),
[storage](storage.md) and [Handlers](handlers.md).
