# Go protocol Runtime

This isolated module adapts the public Go SDK's Codex and Claude process
transports to the common `pkg/runtime.Runtime` interface. SDK commit
`6dbf3e9c8264e7d562ab7ffbf382490afb1aa8bc` is pinned by a normal module requirement,
without replace or go.work. It has no daemon or HTTP service.

The [language-independent Runtime contract](docs/runtime-contract.md) describes
available behavior and remaining gaps. Run the [offline public consumer](examples/offline/README.md)
with temporary synthetic SDK mock CLIs; it never selects an installed Agent.
Complete Runtime/operation lifecycle events and dynamic capability availability
records remain unfinished. Session-scoped integration does not establish full
contract completion or production readiness.

## Named Runtime instances

`pkg/runtime/catalog` assembles the two system-provided protocol types. `Types`
and `Describe` return their configuration fields and process topology. A caller
registers a case-sensitive name, protocol type and configuration:

```go
registry := catalog.New()
_, err := registry.Register(runtime.Registration{
    Name: "local-codex",
    Type: "codex",
    Config: runtime.Config{
        Executable: "/absolute/path/to/authorized/executable",
        Env: []string{},
    },
})
if err != nil { return err }
worker, err := registry.Get("local-codex")
if err != nil { return err }
if err := worker.Start(ctx); err != nil { return err }
defer worker.Stop()
input := "hello" // The only initial input authorized by this caller.
op, err := worker.CreateSession(ctx, runtime.SessionOptions{InitialInput: &input})
if err != nil { return err }
ticker := time.NewTicker(10 * time.Millisecond)
defer ticker.Stop()
for op.Phase != "opened" {
    if op.Phase == "outcome_unknown" || op.Phase == "closed" {
        return errors.New("creation did not open; inspect its operation")
    }
    select {
    case <-ctx.Done(): return ctx.Err()
    case <-ticker.C:
    }
    op, err = worker.GetOperation(op.OperationRef)
    if err != nil { return err }
}
session, err := worker.GetSession(*op.Key)
if err != nil { return err }
for {
    event, err := session.Next(ctx)
    if err != nil { return err }
    if event.Kind == runtime.TurnCompleted { break }
}
```

This function-body sketch uses a `context.Context` and imports `errors`,
`time`, `pkg/runtime` and `pkg/runtime/catalog`.
Claude uses the same interface with type `claude`.
The old single-use Connection interface has been removed.

Registration validates the name (1–128 bytes without surrounding whitespace),
known type, absolute executable path, explicit non-nil child environment and
optional absolute working directory. Environment entries must be `KEY=value`.
An empty environment replaces inherited variables. Get/List, type discovery and
registration do not launch processes or probe credentials. Configuration is
copied at registration; snapshots do not expose the environment.

Concurrent Start calls share one activation attempt for a name. Separate names
own independent resources. Runtime snapshots distinguish configuration revision
from the revision actually activated, and retain timestamped health evidence:

| Protocol | Start and health | Session resources |
| --- | --- | --- |
| Codex | Starts one owned app-server and observes protocol initialization | Multiple native threads share the process; each has an independent SDK subscription and routed interaction queue |
| Claude | Checks executable availability and prepares process creation; connection and protocol health remain unknown | Each creation with explicit input starts a separate owned process and initializes its protocol |

Ready describes this activation scope. It does not prove model authentication,
provider reachability, or current health of every session. Health is an
observation at its recorded time; Status performs no live model probe.

UpdateConfig requires the expected revision and rejects active instances or
retained session ownership. Unregister has the same ownership restriction.
Closed session records retain their original configuration scope. Updating or
unregistering an unused instance retires old handles atomically; they cannot
later activate against a replacement registration.

## Native sessions and transport behavior

A SessionKey has exactly `runtimeName` and `sessionId`. Codex uses the native
thread ID, never the SDK tree-shared SessionID. Claude learns its ID from an
authoritative root system/result observation, binding it once. Child observations,
message UUIDs and later conflicting root IDs cannot replace that identity.
Conflicting root IDs produce a safe diagnostic. No native ID, session name,
native turn ID or initial input is generated.

CreateSession returns a JSON-serializable CreationOperation. Its operationRef
identifies an operation and is rejected as a session address. Before native
identity, Key is absent and no Session appears in GetSession/ListSessions.
Claude creation without InitialInput returns `awaiting_input` without launching
a process. ContinueCreation supplies exactly one explicit string, including an
explicit empty string if desired; the same input can instead be supplied in
SessionOptions at creation. A repeated continuation fails with conflict. Codex
can obtain identity without any input, or submit the explicit initial input
on the newly created native thread.

GetOperation reports `awaiting_input`, `awaiting_native_identity`, `opened`,
`cleaning_up`, `closed` or `outcome_unknown`, input requirements, optional real
SessionKey, observed identity/input-write evidence and cleanup state. Unknown
input-write evidence does not prove that input was never sent. Ending the
CreateSession/ContinueCreation caller context after admission does not end the
operation. The Runtime owns one completion/cleanup worker; it never retries an
unknown native open. CancelOperation cancels unused preparation or marks an
in-flight creation for cleanup when it settles. A late success then closes its
original attachment instead of exposing a replacement session. Cancellation is
not native rollback. A cancellation after an operation opened returns conflict;
close that actual session explicitly.

Config.CreationLimits and the type catalog expose finite provisional limits:

| Field | Default | Accepted nonzero range |
| --- | --- | --- |
| Facts | 64 observations before Claude root identity | 1–4096 |
| Bytes | 1 MiB cumulative provisional source bytes | 1 byte–16 MiB |
| Timeout | 30 seconds for one native open, including explicit initial input | 1 millisecond–60 seconds |

Zero selects the default. Timeout is a Go time.Duration (JSON nanoseconds).
Native frames (including original permission requests) and serialized SDK-derived
observations share the provisional budget before root identity. The asynchronous
permission callback does not count the same original request a second time. The Claude delivery channel
holds at most Facts+1 converted events; an early
frame that expands beyond that capacity cannot block the open beyond Timeout.
Overflow or expiry produces an explicit incomplete/unknown operation and owned
process cleanup. Buffered observations retain their adapter receive timestamp
and attachment-local source position when bound to the root. These positions
do not promise native wire order. SDK queues and post-open delivery have their
own limits; this is not a whole-process memory bound.

Local cleanup is reported separately from native release. Claude reaps its
owned process. Codex aborts the local subscription on a late canceled open;
this does not delete or archive the native thread. If a native create reply
arrives after the Runtime/SDK call deadline, the pinned SDK cannot recover that
reply's identity. The operation remains outcome_unknown with native cleanup
unconfirmed, and prevents configuration replacement/unregistration even after
Stop. No duplicate create or substitute identity is exposed.

GetSession rejects empty IDs, operation references and foreign Runtime names.
GetSession/ListSessions observe only managed attachments, including closed
records after Stop; they do not discover or adopt native account sessions.
Inventory and operation records are in memory.

Closing a Codex session aborts only its local SDK subscription. It does not
archive/delete the native thread or stop the shared app-server. Closing a
Claude session closes and reaps that session's process. Other sessions remain
usable. Stop closes all remaining owned attachments and processes without
unregistering configuration or deleting data. A fully stopped Runtime can be
activated again. Neither session close nor process exit proves rollback of
remote effects.

| Behavior | Codex | Claude |
| --- | --- | --- |
| Text submission evidence | Native turn ACK and original work ID | Complete local Send write; no native turn ID or submission ACK |
| Cancel | Resolves the original operation to its native work ID | Guards a session-wide interrupt against later work |
| Interactions | Command/file approval and user questions | Tool permission and AskUserQuestion |

Submit accepts `runtime.TextInput(text)` or an InputCommand containing exactly
one text block, including explicit empty text. Text is limited to 1 MiB; rich
blocks, extra fields and options fail before SDK handoff. Each attachment retains
up to 256 submission records (and at most one cancellation per submission),
then returns backpressure without evicting facts or sending another input.

Submission returns serializable operationRef, key, admitted/rejected/unknown
outcome, optional original workRef, and independent handedToSDK, writeCompleted,
nativeAcknowledged, nativeRecorded and completed observations. Evidence uses
known values, unknown or not_supported, never an inferred false default. Codex
ACK does not claim local write instrumentation or completion. Claude write
success does not claim a native ACK, recording or invented work ID. A Codex user
record must match the acknowledged work ID before it contributes recording
evidence; uncorrelated echoes do not. Native recording is not host durability.
Use GetSubmission(operationRef) to inspect later evidence. Initial input follows
the same admission gate and its events carry the submission operationRef.

Only one unresolved foreground submission is admitted per session. Concurrent
calls receive busy; other sessions progress independently. Completion-before-ACK
is retained and reconciled against the exact returned native work ID. There is
no current-work assignment of unproven native IDs. SDK local timeout, caller
wait cancellation after handoff or disconnect can leave the original operation
unknown. The SDK can discard late call replies; MASS neither repairs those
replies nor automatically retries input. Rejected native requests and unknown
delivery have distinct safe errors with operation scope and acceptance evidence.

Cancel takes the original submission operationRef, never caller-supplied native
turn parameters. It reserves one attempt, waits for a required late work ID and
keeps its target fixed. Its receipt separates requested, nativeAcknowledged and
targetFinished. GetCancellation observes later target termination. An interrupt
ACK alone does not reopen admission. Terminal-first also retains the fence until
the admitted interrupt call settles, so a late session-wide interrupt cannot
reach a new input. If a Claude session-wide interrupt has an unknown ACK,
its admission fence remains after the old result; a discarded late ACK cannot
safely settle that responsibility. Close the attachment explicitly. Codex's fixed
native target cannot redirect to later work. An unavailable native ID after an
expired Codex call returns unknown promptly instead of waiting indefinitely.
Reusing an existing cancellation returns conflict. Canceling
an already finished submission sends no interrupt. Cancellation is not rollback
or proof that a failed Claude result was interrupted.

Session snapshots keep foreground, attachment and background observations
separate. A receive worker projects facts without requiring Next; snapshots show
the latest observed state and can be ahead of dequeued events. Foreground
completion does not close the attachment, finish tools or clear background
activity. Background inventories project observed task/run state and scoped
replacements; missing inventory remains explicitly unknown.
Native idle alone never settles an unresolved input. One common owner assigns
publication sequence and preserves adapter observation timestamps. Each public stream has one publication owner and a golib ChannX queue, guarded
by Runtime count and serialized-byte admission. Config.DeliveryLimits defaults
to 256 retained facts and 4 MiB; nonzero ranges are 1–4096 facts and 1 byte–16 MiB.
Zero selects the finite default. Dequeue releases the corresponding quota.
Previews are retained under the same policy as complete facts; none are silently
coalesced or dropped. On overflow, the accepted prefix remains readable, then
Next returns ErrIncomplete joined with ErrBackpressure. The attachment stops
observing and invalidates authorities without closing a healthy shared transport.
Snapshots may precede public dequeue, and quota failure is explicit incompleteness.
An upstream SDK queue can still accumulate; these are Runtime-owned bounds,
not a whole-process memory guarantee.

The event stream uses a versioned, data-only envelope and concrete payloads.
See the [event contract](docs/events.md) for supported mappings, content presence,
tool lifecycle, stream association and decode rules. Tools retain structured
input/output, stable call references and explicit execution/failure evidence.
Interactions use `interaction.requested` data and Runtime-owned random references.
Call `runtime.Respond(ctx, session.Key(), event.Interaction.Ref, response)` or
`session.Respond(ctx, ref, response)` on the original live attachment. No public
SDK handle or native request correlation is exposed. Permission decisions are
`allow_once` and `deny_once`; permanent grants and native response objects are
unsupported. Codex narrows these to its observed `availableDecisions` when
present; an offer of only broader policy choices grants no one-time response.
Questions retain option descriptions, single/multiple selection,
free-text and secret constraints where observed. Nil constraint fields mean the
native schema did not establish them. Claude's pinned question schema defines
neither required nor secret fields; unknown native question/input fields stay
private and survive answer conversion. Codex has no advertised multi-select
question flag in this pinned schema.

```go
receipt, err := worker.Respond(ctx, session.Key(), event.Interaction.Ref,
    runtime.Response{Decision: "allow_once"})
// Question alternative: Answers: map[string]runtime.Answer{
//     questionRef: {Selections: []string{"A"}},
// } (or Text: &text for a scalar/free-text answer).
```

The internal gate checks the original specification, attachment and native
request state. Invalid answers and already-canceled contexts consume no authority.
One concurrent caller reserves the response; every attempt handed to the SDK is
single-use even after an unknown result. The receipt separates SDK handoff,
completed write and native resolution. A completed write does not prove native
acceptance. Observed native resolution/cancellation emits `interaction.resolved`
when the original common reference is already known; otherwise it remains a safe
diagnostic. The requested fact may still be dequeued later, with no usable authority.

Session close, Runtime stop, target cancellation and observed root completion
invalidate the original authority. Copied events and references cannot cross
session or attachment epochs. Explicit child requests without proven ownership
are diagnostic and receive no root authority. Invalidation is a gate before
handoff; it cannot revoke an already handed-off SDK/socket write or an unobserved
native decision. Claude provides no native work ID on a permission request:
target invalidation covers requests observed on that attachment; a delayed SDK
callback with no original work correlation cannot prove which historical work
created it. No stronger historical attribution guarantee is advertised.
Codex's pinned SDK rejects a duplicate active exact request ID on a shared
connection; integer `7` and string `"7"` remain distinct. Claude's separate
session processes may concurrently reuse the same request ID.

Each attachment retains at most 256 authority records and 1 MiB of admitted native
request bytes; it reports backpressure rather than evicting authority or
silently allowing a decision. SDK queues are separate from these bounds.
Identity-verified local resume requires the host binding port described below.
Durable history and production guarantees are not implemented. Local Codex
detach does not prove native permission resolution.

Next has one consumer; its context stops only that wait. Session creation's
context controls admission, not the native open or later attachment lifetime.
Codex uses Abort on local detach to
discard unread subscription data. Runtime Stop does not require callers to
consume events. The per-session Codex interaction queue holds 64 requests;
backpressure on an unread session can delay other interaction delivery until
that session is read or closed. The SDK uses independent per-stream ChannX
queues; deprecated EventBuffer/EventQueue options are ignored. SDK CallTimeout
remains 30 seconds; local expiration cannot recover a discarded late reply. These facts do not establish a whole-process memory bound or a bound on every
upstream SDK activity. StopWith separately bounds the caller wait as described below.

## Verified recovery and explicit close goals

Construct a catalog with `catalog.NewWithBindings(hostStore)` to retain opaque
`runtime.RecoveryBinding` values through the common `BindingStore` port. The
host implements cancellation-aware `Save`/`Load`, copies bytes, keeps only the
latest value per SessionKey, and serializes ownership of a Runtime name. Ordinary
`catalog.New()` still supports creation and managed queries; recovery without a
binding port returns `ErrUnverifiable`. No private session directory is scanned.

Successful creation saves the binding before publishing the opened session.
The versioned context binds the original native key to the configured protocol,
executable path, effective working directory and exact explicit environment.
An omitted Dir is pinned to the construction-time working directory. Environment
values are not copied to binding bytes, snapshots or events; the context carries
an execution-scope digest. This verifies configured scope, not external changes
to account files, executable contents or native persistence. The host must retain
its original absolute data/account configuration and protect the binding store.
Bindings are not transferable credentials or a distributed ownership lock.

After explicit local detach or Stop, call `worker.ResumeSession(ctx, savedKey)`.
The Runtime must already be started. Missing, malformed, wrong-version or
wrong-key bindings return `ErrUnverifiable`; changed configuration scope returns
`ErrConflict`. Neither path sends native recovery or creation requests. Codex
uses `thread/resume` with the original thread ID and excludes history. It reserves
the private subscription/request route before the RPC so immediate post-reply
facts cannot fall into a retired attachment. The adapter decodes the response
through the public SDK typed Call to distinguish `ErrIdentityMismatch` from an
unverifiable reply. No raw RPC is exposed to callers. Claude starts a fresh owned
process with literal `--resume=<native ID>` and waits for an authoritative root
system/result observation without sending input. If the CLI requires a prompt
before producing identity, recovery times out as unverifiable; MASS sends no
hidden prompt. A different root ID fails and the provisional process is reaped.
Neither protocol falls back to create or replays user input.

Successful recovery preserves SessionKey, creates a new ConnectionEpoch and
replaces the managed attachment once. Old attachment objects and queued/replayed
events retain their original epoch; their interaction references cannot answer
through the new attachment. Resumed Codex requests for work not acknowledged
from a submission on this attachment remain diagnostics carrying the observed
work ID, without control authority. They are not attributed to a new work merely
because the thread ID matches. Claude callback authority stays in its original
process/connection; the existing limitation for callbacks without native work
correlation still applies. SDK writes already handed off cannot be revoked.

`GetSession`/`ListSessions` only query managed in-memory records. Snapshots report
`freshness: cached`, the last observation time and `availability` separately
from attachment state. Queries do not launch a process, check executable
availability or load the binding store. Closed records remain queryable after
Stop even if the executable has disappeared. A fresh Runtime has no inventory
until an explicit verified recovery succeeds; a host binding store alone is not
a durable inventory implementation.

Use `worker.CloseSession(ctx, key, runtime.CloseDetach)` for an explicit local
attachment close, or `runtime.CloseRelease` when the goal requires native
release. `Session.Close()` is the local-detach convenience without a receipt.
A CloseReceipt separates localAttachmentClosed, nativeRelease,
ownedProcessReaped, incompleteFacts and outcome. The local close aborts delivery,
so incompleteFacts is conservatively true; unfinished submission receipts retain
unknown completion/outcome instead of a fabricated terminal result. Admission
and response authority close immediately once local cleanup starts. Concurrent
Session.Close calls join cleanup; concurrent CloseSession operations return a
conflict. Closing remains distinct from closed until owned cleanup finishes.

Both adapters currently declare `NativeRelease: false`. A shared Codex local
subscription abort and a reaped Claude process do not prove native thread
release, deletion or remote-effect rollback. `CloseRelease` therefore performs
local cleanup but returns `close_unknown` / `ErrOutcomeUnknown`, even when local
closure and process reaping are proven. The recovery binding is conservatively
fenced before cleanup and stays fenced for unknown release, including across
fresh Runtime construction. A subsequent narrower detach cannot erase that
native-release uncertainty. A caller deadline or cleanup/store failure retains
uncertainty rather than authorizing a replacement. A binding-store admission
failure rejects close before local cleanup; the caller can inspect/retry the
host failure or explicitly Stop its Runtime. Store implementations must honor
the context; this module cannot bound arbitrary host code.

A single-session close never closes the healthy shared Codex client. Claude
closes only its own session process. Other managed sessions remain usable.
Only local transports are registered in this experiment: external-server
connections remain unsupported and external-server close behavior is unverified.
No implementation here signals, restarts or deletes an external service.

## Offline verification

```sh
cd mass-go
mise exec -- go test ./...
mise exec -- go test -race ./...
mise exec -- go vet ./...
mise exec -- go build ./...
```

Suites build both public SDK mock executables and replace the child environment
with synthetic scenario configuration. Tests cover two sessions in each of two
named instances, concurrent activation with process-count checks, unchanged
native identity, shared-ID isolation between names, close-A/send-B, retained
queries, ownership conflicts, stop/reactivation and owned PID reclamation.
Pending tests additionally cover reversed concurrent Claude opens, explicit
continuation, empty/operation session keys, late success after caller cancellation,
canceled opens, provisional count/byte/deadline exhaustion with PID checks,
child identity interference and ambiguous Codex opens without retry.
Control tests cover independent acknowledgement/write/record/completion evidence,
completion before acknowledgement, concurrent admission, both cancellation orders,
late identity, safe native rejection/disconnection, caller wait cancellation and
the actual 30-second SDK local timeout followed by a late reply.
Message tests additionally cover native item/stream/full correlation, distinct
frame and block coordinates, equal no-ID previews, replay provenance, user and
tool-bearing messages, observed/redacted reasoning, native plans and collection
clearing. Tool tests cover announcements, indexed JSON input fragments, output
updates, command/file/MCP/dynamic conversion, missing/null/empty content, mixed
unsupported blocks, explicit failure evidence and parent completion without a
tool result. Common serialization cases reject incompatible payloads and invalid
schema/stage/content and preserve lossless JSON numbers. Cancellation,
interaction and diagnostic cases also run through the named catalog and common
Runtime interface. Recovery tests cover both local cold resumes with host-retained
bindings, same-Runtime epoch replacement, historical request quarantine, wrong
identity/missing binding/configuration conflicts, offline inventory, explicit
close goals, persisted unknown-release fences, pending open/submit/interaction
cleanup and unread event delivery with owned PID checks. External-server closure
is not claimed as covered.

For mock usage, build `github.com/zoumo/agent-sdk-go/codex/cmd/mock` or
`github.com/zoumo/agent-sdk-go/claude/cmd/mock` and pass its absolute path. Set
CODEX_MOCK_SCENARIO or CLAUDE_MOCK_SCENARIO in Config.Env. The public SDK's
[mock guide](https://github.com/zoumo/agent-sdk-go/blob/6dbf3e9c8264e7d562ab7ffbf382490afb1aa8bc/docs/mock.md)
and the integration fixtures describe the synthetic scenario format.

Tests never invoke real agents, models or credential discovery. Real-CLI
compatibility remains unverified. No GitHub workflows are configured in this
repository; local Go checks are not remote CI evidence. This experiment does
not change the production Rust runtime.


Subagent and background observations use the [common event contract](docs/events.md#agents-runs-tasks-and-accounting).
SDK association and creation remain distinct; task/run/tool/agent identities and
late run facts retain their original scope. Foreground completion leaves background
inventories intact. Scoped empty snapshots replace only that source's inventory,
and usage counters are never aggregated by the Runtime. Public mock scenarios
cover live/resumed/replayed creation evidence, Codex history-seeded association,
Claude enrichment and stale/no-ID runs, child message/metadata isolation, background
replacement and lossless scoped accounting. Native stale classification is retained
only where the public SDK exposes it; no history-import or universal child-observation
coverage is claimed.


## Ordered delivery, failure and bounded stop

`StopWith(ctx, StopOptions{Policy: StopDrain})` closes admission immediately and
joins one cleanup owner. Local processes and attachments close independently of
the public reader. Accepted public events remain available to a draining reader.
If the reader does not drain before the total caller deadline, the receipt is
`stop_unknown`, returns the deadline error and explicitly reports incomplete
facts; unread delivery is then aborted. `StopAbort` explicitly discards unread
public delivery and reports incomplete facts. It does not manufacture completion
of native work. `Stop()` is the abort convenience with a five-second total wait.
StopWith uses five seconds when its context has no deadline and caps a supplied
wait at sixty seconds. Cleanup ownership continues after an expired wait;
reactivation stays fenced until cleanup settles. Receipt ownedResources is known
true only after successful local cleanup, otherwise unknown. Pending unknown
creation outcomes remain visible through GetOperation and remainingOperations.
No native release or remote-effect rollback is implied.

Session.Close preserves its accepted public prefix; Runtime abort frees unread
queues, including closed epochs retained across resume. Unpublished failed/canceled
attachments release their queues because they can never acquire a public reader. A drain can report stopped with incompleteFacts=true: accepted delivery
settled, but shutting a live native attachment cannot prove that every upstream
fact was observed. A drain timeout never claims successful complete delivery.
Other Runtime names remain independent. Session decode_error publishes the
failure at the proven session scope, seals that attachment and preserves its
prefix; shared Codex transport termination reaches each bound session with
transport scope. Submitted work without a terminal result retains unknown
completion/outcome even if its native ACK or local write was observed.

The Codex SDK request channel and subscription stream settle independently.
Closing requests does not terminate the subscription; event EOF does not hide
already routed requests. Both sources settle before the terminal stream result.
Permissions have observation timestamps but no sourcePosition that purports to
order them against the separate event channel. Publication seq is the single
per-session merge order; there is no native or cross-session total-order claim.

Fixed admission limits also cover 256 registrations, 256 retained creation
operations/attachment attempts per Runtime (including recovery), 256 environment entries and 1 MiB of executable/directory/
environment text per config. Input and continuation strings are at most 1 MiB.
Submission/authority/background/context bounds described above still apply;
root Claude normalization identity data is bounded to 1 MiB. Exhaustion rejects
admission or explicitly fails the scoped observation; retained complete facts
are not evicted to make room. The pinned SDK's per-stream ChannX ignores its
deprecated EventBuffer/EventQueue knobs. Its local CallTimeout can discard late
replies; there is no timeout repair, automatic resend or SDK queue overflow claim.
Native positive truncation remains unverified without reliable source evidence.
External-server transport and external connection closure remain unsupported and unverified.

## Reproduce offline validation

Use Go 1.26.9 through mise from this module directory:

```sh
mise exec go@1.26.9 -- go test -timeout=180s -count=1 ./...
mise exec go@1.26.9 -- go test -race -p 1 -timeout=240s -count=1 ./...
mise exec go@1.26.9 -- go vet ./...
mise exec go@1.26.9 -- go build ./...
mise exec go@1.26.9 -- go run ./examples/offline
```

Run ordinary and race suites sequentially. Tests build only the public SDK mock
commands and use strict synthetic scenarios with explicit child environments.
These checks have been exercised on macOS arm64. Other platforms, live native
CLIs, real models and external server connections remain unverified. Root Make
targets are for the separate Rust workspace.
