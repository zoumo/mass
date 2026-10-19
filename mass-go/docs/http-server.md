# Go HTTP + SSE server

Build both Go programs from the repository root with `make build-go`:
`dist/massd-go` and `dist/massctl`. No Rust build is involved.

```sh
./dist/massd-go --listen 127.0.0.1:24369 \
  --codex-bin /absolute/codex --claude-bin /absolute/claude \
  --cwd /absolute/workspace
./dist/massctl --endpoint http://127.0.0.1:24369 runtime list
./dist/massctl --endpoint http://127.0.0.1:24369 session new codex-demo \
  --runtime-name codex --cwd /absolute/workspace
./dist/massctl --endpoint http://127.0.0.1:24369 session new claude-demo \
  --runtime-name claude --cwd /absolute/workspace --prompt 'Say hello'
./dist/massctl --endpoint http://127.0.0.1:24369 session status claude-demo
./dist/massctl --endpoint http://127.0.0.1:24369 session events claude-demo
./dist/massctl --endpoint http://127.0.0.1:24369 session watch claude-demo
```

Only HTTP JSON-RPC (`POST /api/v1/agentd/jsonrpc`) and live SSE are implemented.
`/livez` and `/readyz` identify `implementation=go`; readiness means the HTTP
service accepts calls, not that an Agent/model has been probed. Runtimes are
registered as `codex` and `claude`, activated lazily on session creation. Their
explicit child environment is the server's startup environment; values are not
returned. Omitting one binary flag omits that runtime. The current SDK requires
Codex with the `projectId` Thread field (verified 0.163.0, not 0.144.4).

The Go Runtime owns cwd at runtime scope. Session `--cwd` must equal the server's
`--cwd`; arbitrary per-session cwd is rejected. Claude requires explicit initial
input: `new --prompt TEXT` opens with that input. Without it, creation remains
`awaiting_input`; `session prompt NAME TEXT` continues the same creation.
Codex can open without a prompt. `new` returns an asynchronous creation operation;
poll `status` for its actual native sessionId and operation outcome. The server
never invents a session ID or retries an unknown creation. Caller disconnect
does not cancel the accepted runtime operation.

The command names match the CLI, but Go and Rust have distinct contracts.
Go session objects expose the actual Go creation operation and session snapshot.
Events currently use the legacy schemaVersion 2 envelope, including
the session name and original native identity. Only complete facts enter history;
SSE also carries previews and observed state/error without seq. Public seq counts
complete facts independently of the internal protocol observation cursor. Runtime
lifecycle and interaction authority records are drained internally, not serialized
as public session events. See [projection coverage and limits](events.md#public-session-events).
The development target is [SessionEvent](../../harness/modules/runtime/events.md); the current seq/projector model does not yet satisfy it.
`prompt` returns the Go submission receipt, not a Rust native-recorded receipt.
`archive`/`delete` requests local detach and returns its close receipt, preserving
native history; it does not claim native release. `cancel` targets the current
foreground operation. HTTP `resume` and `set_config_option` currently return
unsupported, even where the underlying library can resume with explicit bindings.
No WebSocket, Unix socket, remote runtime or authentication layer is added.

Names, operations and event history are in memory and are lost on server exit;
native Agent history is separate. Limits: 128 retained session names, 1024 complete events
and 4 MiB per session, 16 watchers per session, 16 queued events per watcher,
2 MiB HTTP request bodies. History exhaustion marks `historyIncomplete=true`,
closes local observation and detaches the session; it never silently evicts facts.
Check `session status` or `events --page` for this flag. Slow SSE watchers disconnect;
watch has no initial replay or reconnect. Reaching event EOF is not turn completion.
Ctrl-C/SIGTERM stops the HTTP service and invokes Runtime.Stop for owned resources.

Offline validation: `cd mass-go && go test -race ./internal/httpserver ./cmd/massd`.
The HTTP fixture uses the SDK's synthetic Claude CLI, never credentials/models.
