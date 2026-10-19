# MASS Go — Ticket04 resource lifecycle

The authoritative project design is [harness](../harness/index.md). This Go
implementation currently delivers Codex/Claude no-input creation, independent text input,
file-backed identity/history, resource directories, desired configuration updates,
archive/stop and same-ID recovery through Service. It replaces the
old managed/catalog runtime and HTTP projection. There is no HTTP server yet.

Package responsibilities:

- `pkg/resource`: shared persistent metadata/spec/status values.
- `pkg/runtime`: Registry/Driver contracts, shared execution values and the single
  sealed SessionEvent model; `pkg/runtime/codex` and `pkg/runtime/claude` use the pinned real SDKs.
- `pkg/store`: metadata/history interfaces; `pkg/store/file` holds complete authority.
- `pkg/service`: creation, the exclusive Driver consumer, identity binding, ordered
  persistence and publication to independent bounded subscriptions.
- `cmd/massd`: executable SDK-mock workflow; `cmd/massctl`: local read-only queries.

All examples below are synthetic. The SDK mock never calls a model or executes a
real tool. No account or personal configuration is required. The factory currently
requires an explicit absolute mock executable and scenario; both adapters support text input and native permission/question handles. `examples/sdkprobe` remains the independent Ticket01 protocol probe.

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

Not delivered or validated: production Agent/model/account behavior;
cancellation; uncertain-operation recovery,
crash/power-loss injection, old-run/work isolation and overload/fault acceptance;
HTTP/SSE/final CLI; SQLite; Linux or Rust. Later tickets own those capabilities.
The old product/native tests were removed with their replaced API; their historical
passes do not validate this implementation.

## Claude and prototype permission policy

Build `github.com/zoumo/agent-sdk-go/claude/cmd/mock` and select it with
`massd -claude /absolute/mock-claude -scenario /absolute/claude-basic.json`.
Use `tests/vertical/testdata/claude-basic.json` with a private absolute `-cwd`.
The executable explicitly installs `prototype.AutoApprovePermissions` through
`service.NewWithPolicy`. `service.New` has no automatic approval policy.
AskUserQuestion and Codex requestUserInput are form interactions; this policy
leaves them unanswered. Applications answer the observed interaction reference
through `Service.AnswerInteraction`, using explicit native question answers in
`{action: accept, content: {answers: ...}}`. No arbitrary JSON Schema validation
is claimed. Responses always use the original SDK handle and are never replayed.

Claude creation requires a native system/init before input. The supplied scenario
provides it synthetically; missing init fails and cleans only the owned process.
This does not establish real Claude's no-input behavior. Claude SDK Options do
not support systemPrompt/additionalDirs; these are rejected. Claude input accepts
one text block. Text/thinking multi-block arrays retain order within each event
kind, native message identity and empty text. Tool input must be an object under
the pinned SDK; tool result content accepts string/array/null or absence. Dynamic
values are decoded with UseNumber, including nested null/empty values. Unsupported
complete content types fail the Session rather than silently becoming text.
Only message identity is retained for partials, never accumulated content.

Permission replies prove local writes (`sent=true`) only. They do not prove native
acceptance, tool execution or successful completion. These SDK paths lack an
explicit native acceptance acknowledgment for replies, so no interaction_closed
is fabricated from the write. Automatic cancellation/expiry notifications and
pending-interaction queries are not yet implemented. At most one answer is sent
per reference; invalid or uncertain attempts cannot be retried under that same
reference. Questions must be answered before normal shutdown of unsettled work.

Ticket03 `tests/vertical` covers both agents' automatic and manual permission
paths, explicit questions, native initialization, durable publication, Claude
multi-block thinking/text, delta-only exclusion, dynamic tool values, and failed
opens that leave a healthy session usable. Normal/failure cleanup reads back the
process table. These scenarios are synthetic; no real account/model is accessed.

## Configuration, directory and normal lifecycle

`Service.UpdateRuntime(name, spec)` replaces the desired type/config only. Store
allocates generation and resourceVersion atomically. Status writes retry a known
CAS conflict from a fresh resource and never reuse a stale spec. The active Driver
and appliedGeneration remain fixed; repeated start returns that original run.
Sessions opened during configuration changes bind its original effective version.
Only confirmed stop/removal followed by start applies the new desired version.

`ListRuntimes` returns all matching resources without pagination. `ListSessions`
queries the entire persistent directory, including stopped and archived records,
with optional namespace/runtime/state filters and an opaque exclusive cursor.
Ordering is namespace/name/uid lexical order. No cross-page snapshot is held;
limit may change but filter changes invalidate the cursor. Queries and history
never activate a Driver. Runtime deletion requires no persistent or in-flight
Session references and confirmed release; archiving a Session retains its reference.

Archive and resume serialize on persistent Session identity, including across
Runtime generations. Stop seals open/resume/input/answer admission, closes idle
Sessions, drains routed facts through their persistence workers, then stops and
removes the original Driver. Archive becomes archived only after confirmed native
release and history drain. Stop retains resources/history with unknown Session
state and clears current running versions/capabilities. Existing live observers
survive archive/resume and stop/start in the same Service; shutdown terminates them.
Active or uncertain work is rejected for closure rather than declared released.
Full cancellation, late settlement and fault/overload handling remain Ticket05.

Both adapter factories require an application-owned absolute `RecoveryDir`, set
by `massd` to `<data>/private`. The Registry passes the Runtime name when constructing
the Driver. Material is adapter-private; Service and Store do not receive it.
See [the Go recovery protocol](docs/recovery.md) for exact ownership checks and limits.

`tests/vertical/lifecycle_test.go` runs the pinned SDK-owned fake processes for
Codex configuration races, native release gating, cold same-ID recovery,
Runtime UID replacement, missing/mismatched material, original history prefix,
explicit new input, Claude recovery and preserved live observation. These tests
validate synthetic protocol behavior, not real native persistence or model behavior.
