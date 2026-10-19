# MASS Go — Ticket02 vertical slice

The authoritative project design is [harness](../harness/index.md). This Go
implementation currently delivers Codex no-input creation, independent text input,
file-backed identity/history and live observation through Service. It replaces the
old managed/catalog runtime and HTTP projection. There is no HTTP server yet.

Package responsibilities:

- `pkg/resource`: shared persistent metadata/spec/status values.
- `pkg/runtime`: Registry/Driver contracts, shared execution values and the single
  sealed SessionEvent model; `pkg/runtime/codex` uses the pinned real SDK.
- `pkg/store`: metadata/history interfaces; `pkg/store/file` holds complete authority.
- `pkg/service`: creation, the exclusive Driver consumer, identity binding, ordered
  persistence and publication to independent bounded subscriptions.
- `cmd/massd`: executable SDK-mock workflow; `cmd/massctl`: local read-only queries.

All examples below are synthetic. The SDK mock never calls a model or executes a
real tool. No account or personal configuration is required. The factory currently
requires an explicit absolute mock executable and scenario; it declares only text
input support. `examples/sdkprobe` remains the independent Ticket01 protocol probe.

## Run

From this directory (Go 1.26.9 and the pinned module dependencies are required):

```sh
go build -o /tmp/mass-mock-codex github.com/zoumo/agent-sdk-go/codex/cmd/mock
go build -o /tmp/massd-go ./cmd/massd
go build -o /tmp/massctl ./cmd/massctl
scenario="$PWD/tests/vertical/testdata/codex.json"
data="$(mktemp -d /tmp/mass-data.XXXXXX)"
/tmp/massd-go -data "$data" -codex /tmp/mass-mock-codex -scenario "$scenario" -cwd /synthetic
/tmp/massctl -data "$data" -namespace default -name demo
/tmp/massctl -data "$data" -id codex-session -history -limit 2
```

The fixture strictly matches initialize, thread/start without any initial input,
then a separate `hello` turn/start. It provides a native idle identity, work-start,
user message, two preview deltas, synthetic command call/result, a complete answer
and work-end. `/synthetic` is a scenario value; no filesystem operations or tools
run there. Tests substitute a private temporary workspace into the same fixture.
A data directory is for one run: repeating this creation with existing names fails
rather than replacing identities. Query commands do not construct a Registry or
Driver. Status fields read after restart are persisted observations, not a live
health check. This app entrypoint is an experimental executable path, not the final
daemon/client protocol.

## Validate offline

```sh
GOWORK=off GOPROXY=off go test -count=1 -timeout=180s ./...
GOWORK=off GOPROXY=off go test -race -count=1 -timeout=180s ./...
GOWORK=off GOPROXY=off go vet ./...
GOWORK=off GOPROXY=off go build ./...
```

`tests/vertical` builds the SDK-owned mock and both application binaries, runs the
actual SDK/Service/Store path, gates identity commit, reads the file on every
persistent publication, tests UUID retries, and reopens read-only identity/history.
It verifies native millisecond timestamps and excludes preview chunks from history.
`pkg/store/file` tests writer ownership, concurrent uniqueness, optimistic versions,
generation, detached snapshots, encoding errors, null/absence, cursor filtering,
restart and corrupt-file rejection. TestMain reads back owned process cleanup.

Initial file layout, encoding and commit semantics are documented in
[docs/file-store.md](docs/file-store.md). These are implementation choices while
harness storage encoding and file protocol remain open.

Not delivered or validated in Ticket02: Claude in the new core; permissions/user
questions; production Agent/model/account behavior; full resource CRUD/listing and
activation recovery; cancellation, archive/resume; uncertain-operation recovery,
crash/power-loss injection, old-run/work isolation and overload/fault acceptance;
HTTP/SSE/final CLI; SQLite; Linux or Rust. Later tickets own those capabilities.
The old product/native tests were removed with their replaced API; their historical
passes do not validate this implementation.
