# Experimental Go runtime

This isolated module adapts the public Go SDK's Codex and Claude process
transports to the common `pkg/runtime.Runtime` interface. SDK commit
`6dbf3e9c8264e7d562ab7ffbf382490afb1aa8bc` is pinned by a normal module requirement,
without replace or go.work. It has no daemon or HTTP service.

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
Native frames, permission callbacks and serialized SDK-derived observations
share the provisional budget before root identity. The Claude delivery channel
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
| Text submission evidence | `native_acknowledged`, actual native turn ID | `sdk_write_success`, no fabricated native turn ID |
| Cancel | Requires observed native turn ID | Requires empty turn ID |
| Interactions | Command/file approval and user questions | Tool permission and AskUserQuestion |

Submission success is neither native-recorded evidence nor completion. Cancel
requests interruption; an ACK alone does not establish termination. The event
stream uses a versioned, data-only envelope and concrete payloads. See the
[event contract](docs/events.md) for supported mappings, content
presence, tool lifecycle, stream association and decode rules. Tools retain
structured input/output, stable call references and explicit execution/failure
evidence. Foreground admission control and stronger interaction authority remain
separate work.

Interaction events contain a data reference, prompt and questions. The live
attachment resolves its existing response authority through
`session.ResponseHandle(event.Interaction.Ref)`; the event contains no handle.
Respond once through that original authority. A canceled response context does
not consume it. Runtime Stop invalidates pending SDK handles; native
resolution/cancellation follows the pinned SDK's semantics. Serialization and
replay create no authority. The current lookup is attachment-local and retains
SDK queued-approval limitations; it does not claim the stronger lifecycle gate
or typed response validation still required by the planned interaction work.
Identity-verified resume, durable history and production lifecycle/delivery
guarantees are also not implemented. Local Codex detach does not prove native
permission resolution.

Next has one consumer; its context stops only that wait. Session creation's
context controls admission, not the native open or later attachment lifetime.
Codex uses Abort on local detach to
discard unread subscription data. Runtime Stop does not require callers to
consume events. The per-session Codex interaction queue holds 64 requests;
backpressure on an unread session can delay other interaction delivery until
that session is read or closed. The SDK uses independent per-stream ChannX
queues; deprecated EventBuffer/EventQueue options are ignored. SDK CallTimeout
remains 30 seconds. These facts do not establish a whole-process memory bound
or a total shutdown deadline.

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
Message tests additionally cover native item/stream/full correlation, distinct
frame and block coordinates, equal no-ID previews, replay provenance, user and
tool-bearing messages, observed/redacted reasoning, native plans and collection
clearing. Tool tests cover announcements, indexed JSON input fragments, output
updates, command/file/MCP/dynamic conversion, missing/null/empty content, mixed
unsupported blocks, explicit failure evidence and parent completion without a
tool result. Common serialization cases reject incompatible payloads and invalid
schema/stage/content and preserve lossless JSON numbers. Cancellation,
interaction and diagnostic cases also run through the named catalog and common
Runtime interface.

For mock usage, build `github.com/zoumo/agent-sdk-go/codex/cmd/mock` or
`github.com/zoumo/agent-sdk-go/claude/cmd/mock` and pass its absolute path. Set
CODEX_MOCK_SCENARIO or CLAUDE_MOCK_SCENARIO in Config.Env. The public SDK's
[mock guide](https://github.com/zoumo/agent-sdk-go/blob/6dbf3e9c8264e7d562ab7ffbf382490afb1aa8bc/docs/mock.md)
and the integration fixtures describe the synthetic scenario format.

Tests never invoke real agents, models or credential discovery. Real-CLI
compatibility remains unverified. No GitHub workflows are configured in this
repository; local Go checks are not remote CI evidence. This experiment does
not change the production Rust runtime.
