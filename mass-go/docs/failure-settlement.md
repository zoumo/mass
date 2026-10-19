# Go experimental failure settlement (Ticket05)

These choices implement the Go experiment against the read-only harness. Evidence
uses the fixed agent-sdk-go SDK and its own strict synthetic Codex/Claude CLI
subprocesses. No real account, model, tool execution or native durable recovery is
verified. These choices do not amend harness.

## Admission and original ownership

Controls capture their original lane before waiting and verify it has not been
replaced. Pending interactions retain original run/work association and native
closures; completion/close invalidates old handles. An ACK/write receipt remains
separate from termination. Cancel captures the original Codex turn ID or original
Claude client, never a later work item. EOF preserves unknown work and separately
proven acceptance. Unknown input and cancellation are not resent.

Codex records at most 128 retired native turns per run, then refuses further
inputs until explicit close/resume. Old starts/completions cannot replace a new
turn; unknown old turn content/requests cannot acquire a new run association.

The fixed Claude stream lacks a verifiable native turn ID for all results,
requests and interruption. Each run therefore admits **one input**. Further input
explicitly requires close and same-ID resume. The original client and locally
fixed work token own observations for that input. This avoids rebinding ambiguous
late results or interrupts to a newer work. It does not prove arbitrary repeated
or overlapping native Claude work isolation. No artificial native ID is exported.
Old observation ends and drains before a fresh run is installed.

## Creation and identity uncertainty

Creation reserves its name before native side effects and does not hold a shared
Runtime mutex during native open. Caller cancellation abandons only the wait; the
bounded original operation continues with its original Driver/address. Its owner
releases a late ID, or archives a late identity commit after staged facts settle.
Stop waits original creation settlement. Late cleanup does not wait for the
runtime mutex while Stop holds it and waits the original operation.

Claude stages at most 128 SDK events before init and validates native ID/cwd
before delivering complete events. Service stages another bounded 128 early
events until authoritative identity commit. Capacity failure is explicit. Known
released open failures can release reservations; unknown open/cleanup or identity
write outcomes retain them. A same-name call cannot replace the operation.

`Service.ReconcileOpening` explicitly waits the original operation, confirms
release, reads authoritative identity/spec, and either confirms no resource or
drains original staged facts before archiving the matching resource. It does not
open a process, invent an ID or transfer ownership to a conflicting record. A
still-unknown native ID remains unresolved and blocks stopping/reopening.

## History settlement

Each Session has one FIFO worker. Complete facts append with their original
UUID/time before publication. Any append failure pauses that writer with the
current event and queued tail retained; input stays sealed. Repairing a path
does not restart it. Finished subscriptions stay finished. Failed records are
not published as committed facts.

`file.Store.Reconcile` explicitly reloads/validates the authoritative snapshot
under the original flock, verifies the known immutable history prefix, and
fsyncs the file/directory. Failed validation leaves writes sealed even if the
path is repaired later. It does not edit corrupt data. Separately,
`Service.ReconcileHistory` validates all committed pages and original event
identity/content, then retries the same UUID. Already written prefixes are not
assumed absent, and retries do not move progress backward or reopen input.

Fault injection covers successful real file append followed by unknown return,
partial batch prefix, successful identity commit followed by unknown return, and
blocked Session append. It does not simulate a kernel power loss or replace SDK
observations with a fake Driver. Store does not receive private Runtime evidence.

## Bounded queues, subscribers and overload

The experiment uses capacity 128 for Driver delivery, Session queues, early
staging, interactions and per-Session subscriptions. Active Runtime owners are
capped at 128; active/pending Sessions per Runtime and concurrent resume owners
are capped at 128. Observation ownership is capped at 128 squared. Finished
subscriber references and unused keyed operation locks are removed. These are
experimental capacities, not harness defaults or bounds on growing file history.

Slow append runs in its own worker and does not block shared reads or another
Session before capacity exhaustion. Subscriber overflow ends only that
subscription and cannot change history. Previews use the same FIFO and explicit
overflow policy; they are never accumulated into complete facts.

When a Session/creation queue fills, the shared consumer explicitly fails,
retains one overflow event, and keeps the original stream/upstream accepted
queues owned. It stops admission instead of discarding complete facts or
spawning workers. Shared capacity exhaustion can halt shared delivery; permanent
fairness after exhaustion is not promised. `Service.ReconcileDelivery` explicitly
queues the retained event and continues the **same** acquired stream with its
original sources/UUIDs. It does not reacquire Events, reconnect or reopen input.
Repeated saturation requires explicit settlement. The suite accounts for all
160 complete facts through saturation and recovery.

## Stop, unknown results and restart

Stop seals activation/open/resume/input/answers first. Accepted controls finish
against their original lane and opens continue settling. Stop keeps consuming,
cancels original unsettled work once, waits observed termination, confirms
Session release, drains accepted facts, then stops the original Driver and
drains again before recording stopped. Concurrent stops share original
responsibility. Independent Runtime shutdown drains run concurrently.

Timeout or release/commit uncertainty keeps stopping/closing and ownership.
Late running/idle cannot reopen admission or overwrite closing/archived.
Explicit stop retry can complete after late termination/release. A Session error
does not stop a healthy shared Driver. Persisted unconfirmed Runtime/Session
ownership cannot be cold-opened as a new owner merely because the Store becomes
writable or a process restarts.

Pending UUIDs and operation closures are retained in memory in this process;
there is no crash-recoverable private per-fact outbox. After process loss,
unconfirmed ownership is refused rather than reconstructed, resent or declared
clean. Manual forensic recovery/cross-process settlement, kernel crash/power
loss, SQLite, real Agent/model/account behavior and Linux remain unverified.
HTTP/final CLI exposure is Ticket06 and is not implemented here. User questions
remain separately tested by the existing actual-SDK synthetic suites.
