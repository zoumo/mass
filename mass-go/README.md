# MASS Go experiment

[harness](../harness/index.md) is the read-only project contract. This implementation
uses **CLI → HTTP/SSE → Service → Store / Runtime**. It has one SessionEvent model,
one Driver consumer, independent live observers, and file-authoritative history.
Executable assembly and CLI commands live under `cmd/`; library packages live under `pkg/`.

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
dist/massd-go --data /absolute/owned/data --listen 127.0.0.1:8787 --local \
  --prototype-auto-approve-permissions
printf '%s\n' '{}' | dist/massctl --url http://127.0.0.1:8787 --op runtime.list
```

The permission flag is explicit prototype assembly policy; it leaves form/user
questions pending. Without it permissions are manual too. `massd` prints its bound
URL as JSON, supports port 0, and settles Service on SIGINT/SIGTERM. Shutdown
failure exits nonzero. Select explicit `--local` trusted loopback mode or
`--token-file`; direct remote access requires TLS. `/readyz` measures HTTP
admission only. The CLI reads the Bearer token from `MASS_TOKEN`.

`massctl --op OPERATION` consumes one JSON object on stdin and prints the raw
Service result as JSON; errors go to stderr with nonzero exit. `--op observe`
consumes a SessionRef and prints `{"ready":true}` followed by SessionEvent JSON
lines. SIGINT/SIGTERM disconnects observation; it does not cancel Agent work.
The CLI owns no local Store, Session directory, projection or retry policy.
See [the Go HTTP v1 implementation](docs/http-experiment.md) for all operations,
request shapes, errors and SSE termination semantics.

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
