# MASS Go experiment

[harness](../harness/index.md) is the read-only project contract. This implementation
uses **CLI → HTTP/SSE → Service → Store / Runtime**. It has one SessionEvent model,
one Driver consumer, independent live observers, and file-authoritative history.
Executable assembly lives under `cmd/`; CLI and other library modules live under `pkg/`.

## One-command synthetic demonstration

From the repository root, with Go 1.26.9 and pinned dependencies already cached:

```sh
make prototype-go
```

This builds the pinned `agent-sdk-go` Codex **and** Claude mock CLIs plus `massd`
and `massctl`, then runs `TestEntrypointSuite` through actual CLI processes and
HTTP+SSE. Every scenario is synthetic. SDK mocks match protocol frames strictly;
they do not execute tools, use accounts, call models, or prove intelligence.
The command fails on a failed scenario or cleanup readback. Test-owned binaries,
workspaces, scenarios, data and loopback ports are temporary; process, listener
and directory cleanup is checked. No personal Agent configuration is used.

The suite demonstrates no-input native identity, separate text input, automatic
permission answers through original handles, live previews, complete persisted
history and pagination/filtering, explicit user questions for each Agent,
request-before-ACK handling, cancel receipt versus observed termination,
archive/stop and original-ID resume followed by fresh input. It tests CLI parse
errors, Runtime operations, disconnect isolation and failed-creation cleanup.
Answer and filtered queries are tested through HTTP because they are not CLI commands.
Each persistent event read from SSE is compared with a committed history query.
SDK probe passes are reported separately and do not stand in for this suite.

Claude creation can return `unknown` after its synthetic init: identity alone is
not an idle/work-state proof. Claude accepts **one input per running connection**;
a second input is rejected even after idle. Archive and resume the original ID
before the next input. The demo exercises that restriction. Synthetic resume
checks SDK startup/init and retained MASS identity/history; it does not establish
real CLI native persistence or consumption of resume IDs.

## Daemon and CLI

```sh
make build-go
# Supply a fresh, absolute, application-owned data directory.
dist/massd-go --data /absolute/owned/data --listen 127.0.0.1:24368 --local \
  --prototype-auto-approve-permissions
dist/massctl runtime list
```

The permission flag is explicit prototype assembly policy; it leaves form/user
questions pending. Without it permissions are manual too. `massd` prints its bound
URL as JSON, supports port 0, and settles Service on SIGINT/SIGTERM. Shutdown
failure exits nonzero. Select explicit `--local` trusted loopback mode or
`--token-file`; direct remote access requires TLS. `/readyz` measures HTTP
admission only. The CLI reads the Bearer token from `MASS_TOKEN`.

`massctl <resource> <operation>` uses the `/apis/v1` HTTP contract. Run
`massctl --help`, `massctl runtime --help`, or `massctl session --help` for the
complete command tree; `massctl version` reports the client build. The old `--op`
interface is removed. CLI implementation lives in `pkg/massctl`; `cmd/massctl`
only assembles process I/O and signals.

Address precedence is `--url`, then `MASS_URL`, then `http://127.0.0.1:24368`.
Authentication uses `MASS_TOKEN`. Output defaults to human summaries/tables;
`-o json` returns the complete business result, and watch emits SessionEvent
JSONL. Errors retain `code/reason/message` on stderr; watch readiness is also on
stderr. Exit codes are 0 (success), 1 (operation/I/O failure or unknown result),
2 (invalid arguments), and 130 (interrupted wait). Ordinary requests default to
30 seconds; `--timeout 0` disables the timeout. Watch applies that timeout only
through `subscription.ready`, then observes until explicitly closed or interrupted.
Neither timeout nor Ctrl-C cancels Agent work; requests are never retried.

For Codex, explicitly create and start a Runtime, then create a Session without
sending input (the working directory is interpreted on the Agent host):

```sh
dist/massctl runtime create codex-local --type codex --executable /absolute/path/to/codex
dist/massctl runtime start codex-local
dist/massctl session create review -n demo --runtime codex-local --cwd /absolute/owned/workspace
# Keep this running in one terminal; wait for subscription.ready on stderr.
dist/massctl -o json session watch review -n demo
# In another terminal:
dist/massctl session prompt review -n demo --text 'Describe this workspace'
dist/massctl session history review -n demo --limit 100
dist/massctl session archive review -n demo
dist/massctl session resume review -n demo
dist/massctl runtime stop codex-local
```

For Claude, use the installed Claude executable with the same resource commands:

```sh
dist/massctl runtime create claude-local --type claude --executable /absolute/path/to/claude
dist/massctl runtime start claude-local
dist/massctl session create review -n demo --runtime claude-local --cwd /absolute/owned/workspace \
  --system-prompt-file /absolute/system.txt --additional-dir /absolute/owned/context
dist/massctl session prompt review -n demo --text 'Describe this workspace'
# Claude currently accepts one input per running connection; release and resume
# the original identity before another input.
dist/massctl session archive review -n demo
dist/massctl session resume review -n demo
```

Both drivers currently accept text input; Claude requires exactly one text block.
These command examples describe supported interfaces, not proof of real CLI/model
execution. Authentication, real native recovery, accounts and models must be
validated separately in owned test directories. Permission auto-approval remains
an explicit daemon prototype policy; pending form/question interactions require
the HTTP answer API. This CLI version deliberately has no answer command.

Existing Session commands use `NAME -n NAMESPACE` or a raw global `SESSION_ID`
without namespace, with no default namespace, `--id`, guessing or lookup fallback.
IDs are encoded as one URL path segment. `session list` and `session history`
accept only `--limit` (default 100) and `--after`; one page is returned, including
its next cursor. History and watch are independent: watch shows only future
events, never fills history or reconnects, and unexpected EOF is a failure.

Runtime create requires NAME and `--type`; absent configuration sends `{}`.
`--executable`, `--config-file` and `--config-json` are mutually exclusive input
modes. Files contain RuntimeConfig itself; `-` reads stdin. Runtime update first
GETs the resource, preserves its type, and PUTs a complete spec at the resource
path. `--executable` preserves other config fields; a complete JSON config replaces
them. Empty updates fail locally. There is no conditional-update protection:
concurrent edits can be overwritten. Updates never restart running instances.
Runtime delete never stops or cascades: Service rejects running/unreleased
resources and persistent Session references. Stop explicitly first; archived
Sessions retain their references and history. Successful delete has empty JSON
stdout. Test-only driver configuration stays in complete JSON, not public flags.

The client owns no local Store, Session directory, projection or retry policy.
See [the Go HTTP v1 implementation](docs/http-experiment.md) for server migration
status and protocol details. Server implementation and client contract can migrate
in separate commits; integration checks must use a daemon providing these routes.

## Verification and boundaries

```sh
cd mass-go
GOWORK=off GOPROXY=off go test -count=1 -v -timeout=180s ./...
GOWORK=off GOPROXY=off go test -race -count=1 -v -timeout=240s ./...
golangci-lint run ./...
golangci-lint fmt --diff
GOWORK=off GOPROXY=off go build ./...
```

Tests use `testify/suite`, including SDK probes and vertical scenarios.
Both executables parse flags with Cobra; use `--help` to list options.

Lint uses golangci-lint v2.12.2 and the unchanged `.golangci.yaml` copied from
agentrun. Install the pinned tool with:

```sh
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
```

From the repository root, `make fmt-go`, `make lint-go`, and `make check-go`
format, lint, and verify the Go implementation respectively.

The public module tests, SDK protocol probes and `tests/vertical` additionally
cover Store concurrency/cursors, lifecycle, uncertain operations, original work
isolation, blocked writes, partial/unknown commits and bounded overload. An SSE
write gate proves that 140 committed facts survive observer overflow, which emits
an explicit error. Client framing tests distinguish end, abnormal error and
unexpected disconnection, preserve terminal errors and release blocked reads.

The HTTP input codec preserves all five ContentBlock shapes; existing native
adapters execute text input only. Claude accepts exactly one text block.
Complete text/thinking, tool calls/results, permission/form interactions, state
and sanitized Session errors are mapped; deltas are previews and never fabricated
into complete history. Unsupported complete content fails explicitly. Images,
audio, files, arbitrary native extension/subagent attribution, full event-handler
registration APIs, configuration-option APIs, pending-interaction lookup and
automatic expiry notifications are not implemented.

File snapshots and adapter-private recovery are experimental choices, documented
in [file-store](docs/file-store.md), [recovery](docs/recovery.md) and
[failure settlement](docs/failure-settlement.md). There is **no SQLite index**.
Whole-file writes do not establish crash/power-loss guarantees. Uncertain original
operations/pending UUIDs are retained within the original process; cross-process
unknown-operation settlement/outbox recovery is not implemented. Real Agent CLIs,
accounts/models/tools, native durable recovery, Linux and Rust validation are
outside these synthetic results. The HTTP v1 binding is implemented; these synthetic results do not establish
production readiness or real Agent execution.

The six-ticket scenario matrix, commit map and evidence guide are in
[acceptance](docs/acceptance.md).
