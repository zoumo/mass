# Experimental Go runtime

This isolated module adapts the public Go SDK's Codex and Claude process
transports to `pkg/runtime`. SDK commit
`6dbf3e9c8264e7d562ab7ffbf382490afb1aa8bc` is pinned by a normal module requirement,
without replace or go.work. It has no daemon or HTTP service.

## Use

Import `pkg/runtime/codex` or `pkg/runtime/claude`, then supply an explicitly
chosen executable and environment. Each connection supports one session:

```go
conn, err := codex.Open(ctx, codex.Options{
    Executable: "/absolute/path/to/authorized/executable",
    Env: []string{},
})
if err != nil { return err }
defer conn.Close()
session, err := conn.NewSession(ctx)
if err != nil { return err }
submission, err := session.Submit(ctx, "hello")
if err != nil { return err }
_ = submission // Inspect evidence and optional native turn ID.
for {
    event, err := session.Next(ctx)
    if err != nil { return err }
    // Handle messages, tools, failures, unknown diagnostics and interactions.
    if event.Kind == runtime.TurnCompleted { break }
}
```

This function-body sketch uses a `context.Context` and imports the `codex`
adapter and `runtime` package. Claude has the same options and interface.
Only explicitly selected SDK mocks are used for the offline tests below.
Build them with `go build -o /tmp/mock-codex github.com/zoumo/agent-sdk-go/codex/cmd/mock`
or `go build -o /tmp/mock-claude github.com/zoumo/agent-sdk-go/claude/cmd/mock`.
Pass that executable and `CODEX_MOCK_SCENARIO=/absolute/scenario.json` or
`CLAUDE_MOCK_SCENARIO=/absolute/scenario.json` in Env. The pinned SDK's
[mock guide](https://github.com/zoumo/agent-sdk-go/blob/6dbf3e9c8264e7d562ab7ffbf382490afb1aa8bc/docs/mock.md)
describes synthetic scenario files; adapter test fixtures are local examples.

| Contract | Codex | Claude |
| --- | --- | --- |
| Session identity | Native thread ID, not tree-shared SDK `Thread.SessionID` | Pending until root system/result session ID is observed |
| Submission evidence | `native_acknowledged`, native turn ID | `sdk_write_success`, no native turn ID or acknowledgement |
| Cancel | Requires native turn ID | Requires empty turn ID |
| Interactions | Command/file approval and user questions | Tool permission and AskUserQuestion |

Submission success is neither `native_recorded` nor completion. Cancel requests
interruption; Claude interrupt acknowledgement cannot establish an interrupted
result. Turn lifecycle statuses normalize to `in_progress`, `completed`, `failed`,
`interrupted`, or `unknown` for unrecognized Codex statuses. Claude success maps
to completed; other results map to failed without guessing interruption. Other
event statuses describe message/tool phases or errors.

Respond through the original interaction `Handle` once. Copies share its
authority; a canceled response context does not consume it. Close invalidates
pending handles. Tests also verify already-delivered Codex handles after a later
native resolution and Claude handles after native cancellation. A queued Codex
approval may remain writable if resolution arrives before dequeue. This is not a general
production guard for every queued native resolution or timeout race; the SDK's
approval semantics remain in effect. Claude question IDs are question text.
Child Claude events and explicitly child-owned permissions remain Unknown,
without granting root answer authority or implementing child scheduling.
Unknown/failure diagnostics retain a bounded native kind and SHA-256 without
raw payloads or native error text. Recognized message/tool text is exposed.

`Next` has one consumer; its context stops only the wait. Session creation's
context does not own the later subscription. Close terminates the owned
connection/process even without event consumption; it is not a production
per-thread archive. Codex Close uses SDK Abort to discard unread subscription
data. The pinned SDK uses independent per-stream ChannX delivery: SDK Close
drains accepted records and Abort discards unread records. Deprecated
EventBuffer/EventQueue settings are ignored. CallTimeout remains 30 seconds;
approval behavior is unchanged.

## Offline verification and limits

```sh
cd mass-go
go test ./...
go test -race ./...
go vet ./...
go build ./...
```

Suites build both public SDK mock executables and replay synthetic scenarios
through actual process transports. Coverage includes messages/tools, native
completion, interruption, one-shot response handles, errors/unknown diagnostics,
queued facts before disconnect, and owned mock PID reclamation. Tests never
invoke real CLIs, models or credential discovery.

No GitHub workflows are configured in the current repository; these are local
Go checks, not remote Go CI evidence. This experiment has no Rust-equivalent
resource budget, persistence, recovery, scheduling or production lifecycle.
