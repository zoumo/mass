# Go prototype recovery protocol

This is an implementation choice for Ticket04; it does not amend harness.
The prototype runs SDK-owned synthetic peers only, with no model, account or
personal data. The fixed SDK is `6dbf3e9c8264`.

Factories receive an absolute private recovery root from application assembly,
and the Runtime name from Registry creation. Paths use provider plus SHA256 of
Runtime name and native session ID, so names/IDs are not arbitrary paths. Each
mode-0600 JSON material stores its format, provider, original Runtime name,
native ID, executable path, Session address/spec and adapter-specific native
context. It is written with temporary-file fsync, rename and directory fsync.
Existing conflicting material is refused. It is never a resource DTO, event,
history extension or Store field. Close retains it. The single application Store
writer remains the assembly owner; this is not a distributed ownership protocol.

Service verifies every page of committed public history before invoking native
resume. The complete file is validated on Store open, including a corrupt tail
beyond the first page. A Session's persistent UID, creation time, spec, ID and
Runtime name remain unchanged. Runtime UID is deliberately not an ownership key.
The Runtime must be explicitly started before resume; get/list/history cannot
start it. The adapter validates private material before sending native resume.

Codex material preserves the native creation Thread metadata. It requires
non-ephemeral, matching thread/session IDs and cwd. Resume sends the original ID
through `SDK.ResumeThread` with `excludeTurns=true`, using no history/path
alternate selector and no input. The returned ID/session ID/cwd/createdAt/provider/
native path must match saved context. Returned old turns are not converted into
new public events. A new observation source binds the original persistent identity.
The fixed SDK verifies the returned thread ID itself as well.

Codex close first rejects unsettled work, then calls `thread/unsubscribe` using
SDK.Call and generated request/response values. `unsubscribed` additionally waits
for the corresponding native `thread/closed`; `notLoaded` confirms no loaded
resource. `notSubscribed` alone is insufficient. Local Subscription.Close is
only observation cleanup and is never release evidence. After the observer ends,
an internal stream barrier ensures prior events have been routed before Service
drains its Session worker. Neither barrier is a public event.

Claude material preserves the observed init ID/cwd and original Session context.
Resume opens a fresh SDK client with literal `Options.ResumeID`; it waits for a
native init with the same ID and cwd and sends no hidden input. Unsupported
systemPrompt/additionalDirs fail before opening. SDK.Close plus observer completion
provides owned process/pipe release, followed by stream and worker drain.

The SDK fake accepts Claude's `--resume=value` startup option and strictly checks
the subsequent scripted conversation. It does not restore real CLI state or
certify that a CLI actually consumed a particular resume ID. The test proves our
Options binding, startup compatibility, observed same identity and independent
new input. Codex's strict fake additionally matches the explicit thread/resume
request. Both paths preserve all existing public UUIDs and append order; old input,
tools, responses and business actions are never replayed.

Limits: no real Agent/account/data ownership or native durable-storage acceptance;
no crash/power-loss or private-file fault injection; no private uncommitted-tail
reconciliation or complete private per-fact evidence journal; no automatic pending
interaction restoration. Native resume mismatch/timeout and storage failures still
need Ticket05's complete unknown-operation ownership/late cleanup protocol. Normal
stop refuses active work instead of cancelling it. It does not claim the later
fault, concurrency-isolation and overload acceptance matrix has passed. HTTP and
final CLI exposure remain Ticket06. SQLite is not implemented or needed for reads.
