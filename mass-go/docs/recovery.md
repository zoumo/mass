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
`CreateSession` and `ResumeSession` start the referenced Runtime on demand; an
explicit `StartRuntime` remains available. get/list/history and subscriptions
never start a Runtime. The adapter validates private material before sending
native resume.

Codex material preserves the native creation Thread metadata. It requires
non-ephemeral, matching thread/session IDs and cwd. Resume sends the original ID
through `SDK.ResumeThread` with `excludeTurns=true`, using no history/path
alternate selector and no input. The returned ID/session ID/cwd/createdAt/provider/
native path must match saved context. Returned old turns are not converted into
new public events. A new observation source binds the original persistent identity.
The fixed SDK verifies the returned thread ID itself as well.

Codex close calls the current SDK Session's `Close` operation. There is no
MASS-side "unsettled work" precondition before that call, and the SDK close
result is the native success criterion. MASS does not separately interrupt the
turn, wait for work completion, kill a PID or probe provider health. After a
successful SDK close, Service settles its own accepted delivery, history commit
and publication responsibilities before publishing `offline`.

Claude material preserves the observed init ID/cwd and original Session context.
Resume opens a fresh SDK client with literal `Options.ResumeID` and requires the
caller to provide this operation's explicit initial input. That real user frame
lets the CLI emit a native init; the driver then requires the same ID and cwd.
Without explicit input, resume fails closed before opening a native process: a
local transcript proves only that a record exists, not that the current process
restored the requested identity. No hidden or replayed input is sent.
Unsupported systemPrompt/additionalDirs fail before opening. Close likewise uses
the current SDK Session's `Close` result as the native success criterion, then
Service drains the owned stream and worker responsibilities. Both adapters leave
the Session identity, history and recovery material intact.

The SDK fake accepts Claude's `--resume=value` startup option and strictly checks
the subsequent scripted conversation. It does not restore real CLI state or
certify that a CLI actually consumed a particular resume ID. The test proves our
Options binding, startup compatibility, observed same identity and independent
new input. Codex's strict fake additionally matches the explicit thread/resume
request. Both paths preserve all existing public UUIDs and append order; old input,
tools, responses and business actions are never replayed.

Ticket05 failure settlement, bounded queues, original-operation ownership,
explicit reconciliation, stop retry and conservative Claude work admission are
specified in [failure-settlement.md](failure-settlement.md). Claude accepts one
input per run because the fixed stream cannot prove native work identity for all
late results/interrupts; close and same-ID resume are required before further input.

Limits: no account/data-ownership or native durable-storage acceptance; no kernel
crash/power-loss acceptance or crash-recoverable private per-fact outbox. After a
process restart, an explicit start/resume may establish a new local Driver for a
persisted unknown Runtime when this process has no run binding. This treats the
old local owner as unreachable; it does not prove provider work or backend
resources stopped and does not reconstruct per-fact obligations. Manual
cross-process settlement remains unimplemented. HTTP and CLI close/resume paths
are implemented and exercised by temporary file/SQLite-backed vertical tests.
SQLite provides the current metadata authority and a rebuildable event-history
query replica; recovery must preserve the former while the latter may be rebuilt.
Real-Agent persistence and durable-storage behavior remain unverified. In the
current five-second acceptance, Codex and Claude both passed idle auto-close,
offline rejection, resume, manual close and resume while Runtime stayed running.
Claude's passing path created the Session with explicit initial input: CLI
2.1.258 then emits native init identity after the real user frame.
Zero-input Claude creation remains unsupported and unverified; without user input
the CLI emits no native init identity, and creation does not send hidden input.
The same native identity requirement applies to resume. Because CLI 2.1.258 emits
no init without input, Claude resume requires explicit `initialInput`; zero-input
resume is rejected before opening a native process.
