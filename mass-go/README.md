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
a, err := worker.CreateSession(ctx)
if err != nil { return err }
b, err := worker.CreateSession(ctx)
if err != nil { return err }
if err := a.Close(); err != nil { return err }
submission, err := b.Submit(ctx, "hello")
if err != nil { return err }
_ = submission // Inspect actual submission evidence.
for {
    event, err := b.Next(ctx)
    if err != nil { return err }
    if event.Kind == runtime.TurnCompleted { break }
}
```

This function-body sketch uses a `context.Context` and imports `pkg/runtime`
and `pkg/runtime/catalog`. Claude uses the same interface with type `claude`.
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
| Claude | Checks executable availability and prepares process creation; connection and protocol health remain unknown | Each CreateSession starts a separate owned process and initializes its protocol |

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
authoritative root system/result observation. Before that observation, Key has
an empty sessionId, and the attachment is absent from managed session queries.
No native ID is generated and no input is injected. Callers supply the first
input explicitly. GetSession rejects empty IDs and foreign Runtime names.
GetSession/ListSessions observe only managed attachments, including closed
records after Stop; they do not discover or adopt native account sessions.
Inventory is in memory.

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
requests interruption; an ACK alone does not establish termination. Existing
message/tool conversion, normalized completion statuses, bounded diagnostic kind
and SHA-256, and connection-bound one-shot interaction handles remain available.
Native stderr and unknown raw payloads are excluded from public events. Known
message/tool content remains visible. Events include their owning Runtime name;
session-scoped routing uses the native managed session identity. Child Claude
observations remain diagnostic and cannot acquire root permission authority.

Respond through the original interaction Handle once. A canceled response
context does not consume it. Runtime Stop invalidates pending SDK handles;
native resolution/cancellation follows the pinned SDK's semantics. This slice
does not provide a data-only interaction event schema, operation-based pending
creation, identity-verified resume, foreground admission control, or durable
history. It does not claim complete typed event coverage or production lifecycle
and delivery guarantees. In particular, SDK queued-approval limitations remain,
and local Codex detach does not prove native permission resolution.

Next has one consumer; its context stops only that wait. Session creation's
context does not own the later attachment. Codex uses Abort on local detach to
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
The existing message/tool, cancellation, interaction and diagnostic cases also
run through the named catalog and common Runtime interface.

For mock usage, build `github.com/zoumo/agent-sdk-go/codex/cmd/mock` or
`github.com/zoumo/agent-sdk-go/claude/cmd/mock` and pass its absolute path. Set
CODEX_MOCK_SCENARIO or CLAUDE_MOCK_SCENARIO in Config.Env. The public SDK's
[mock guide](https://github.com/zoumo/agent-sdk-go/blob/6dbf3e9c8264e7d562ab7ffbf382490afb1aa8bc/docs/mock.md)
and the integration fixtures describe the synthetic scenario format.

Tests never invoke real agents, models or credential discovery. Real-CLI
compatibility remains unverified. No GitHub workflows are configured in this
repository; local Go checks are not remote CI evidence. This experiment does
not change the production Rust runtime.
