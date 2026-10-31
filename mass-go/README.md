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

This builds the pinned `agent-sdk-go` protocol simulation driver for Codex **and** Claude plus `massd`
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

The default listen address is `127.0.0.1:24368`. The daemon uses [Viper](https://github.com/spf13/viper) to read
`~/.mass/config.yaml` when present and defaults its storage root to `~/.mass` (without a `data/` subdirectory).
Use `--config /absolute/config.yaml` to select another file; an explicitly selected
file must exist. Command-line flags override file values, including explicit
boolean `false`. Unknown fields, invalid field types and malformed YAML are rejected.
The old default `~/.mass/config.json` is no longer discovered; convert its contents
to YAML and save them as `~/.mass/config.yaml`.

For local single-user use, create `~/.mass/config.yaml` with:

```yaml
listen: 127.0.0.1:24368
local: true
```

Then run `bin/massd` without flags. Optional fields are `data` (an absolute
owned directory), `tokenFile`, `tlsCert`, `tlsKey`, and
`prototypeAutoApprovePermissions`. Automatic permission approval is off by default.
Existing data is not moved into the default directory. Legacy `state.json` snapshots
are explicitly rejected; there is no automatic migration.

Runtime metadata lives in `~/.mass/runtime.jsonl`. Each Session uses
`~/.mass/sessions/<namespace>/<name>/state.json` for its resource and
`events.jsonl` for its append-only events, one complete event per line. Event
bodies are read on demand; Store keeps only event identity/offset/digest indexes
in memory. Recovery records contain only metadata or the current new event batch,
never full history. The original names are directory
components, never hashes; unsafe path components and symlink entries are rejected.
Driver-private recovery remains under `~/.mass/private/`, separate from public
Session metadata/history. `--data` or the YAML `data` field overrides the root,
placing `runtime.jsonl`, `session_index.jsonl`, `sessions/` and `private/` directly inside it.
`session_index.jsonl` contains only namespace, name, sessionId and runtimeRef;
Session status changes and history appends do not rewrite it.
it is rebuilt from Session resources on writable open. Read-only open never repairs
an index. `.store.json` keeps the format marker and cursor signing key, separate
from Runtime records.

```sh
make build-go
# Supply a fresh, absolute, application-owned data directory.
bin/massd --data /absolute/owned/data --listen 127.0.0.1:24368 --local \
  --prototype-auto-approve-permissions
bin/massctl runtime list
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
interface is removed. CLI implementation lives in `cmd/massctl/subcommand`, organized by resource
subcommands; `cmd/massctl/main.go` only assembles process I/O and signals.

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
bin/massctl runtime create codex-local --type codex --executable /absolute/path/to/codex
bin/massctl runtime start codex-local
bin/massctl session create review -n demo --runtime codex-local --cwd /absolute/owned/workspace
# Keep this running in one terminal; wait for subscription.ready on stderr.
bin/massctl -o json session watch review -n demo
# In another terminal:
bin/massctl session prompt review -n demo --text 'Describe this workspace'
bin/massctl session history review -n demo --limit 100
bin/massctl session archive review -n demo
bin/massctl session resume review -n demo
bin/massctl runtime stop codex-local
```

For Claude, use the installed Claude executable with the same resource commands:

```sh
bin/massctl runtime create claude-local --type claude --executable /absolute/path/to/claude
bin/massctl runtime start claude-local
bin/massctl session create review -n demo --runtime claude-local --cwd /absolute/owned/workspace
bin/massctl session prompt review -n demo --text 'Describe this workspace'
# Claude currently accepts one input per running connection; release and resume
# the original identity before another input.
bin/massctl session archive review -n demo
bin/massctl session resume review -n demo
```

Both drivers currently accept text input; Claude requires exactly one text block.
Claude rejects `systemPrompt` and `additionalDirs` before dispatch; Codex maps
`systemPrompt` to native base instructions and rejects `additionalDirs`.
These command examples describe supported interfaces, not proof of real CLI/model
execution. Authentication, real native recovery, accounts and models must be
validated separately in owned test directories. Permission auto-approval remains
an explicit daemon prototype policy; pending form/question interactions require
the HTTP answer API. This CLI version deliberately has no answer command.

### Runtime Launch Configuration

The SDK is pinned to `v0.0.0-20261031154401-ed6c8216607c`, source commit
`ed6c8216607ceacb5077b558a8302a587c9e6d5b`, without a local replacement.
Codex shares one native Client across its Sessions; Claude owns one native
process per Session. Each Session has one Receive consumer for native events
and original request capabilities. SDK observations do not establish Agent state.
Codex input receipts retain the native turn ID and acceptance; Claude receipts
prove only a complete local write, not native acceptance or recording.

`--config-json` / `--config-file` accept these launch fields:

| Field | Behavior |
| --- | --- |
| `executable` | Required absolute executable path; never discovered from personal configuration. |
| `args` | Codex process arguments, default `["app-server"]`; explicit `[]` means no arguments. Claude rejects nonempty arguments; use an explicit launcher if needed. |
| `envPolicy` | `inherit` (default) merges parent environment with `env`; `replace` passes only configured entries, including an explicitly empty environment. |
| `env` | String-to-string overrides; callers supply any required PATH/HOME/config-directory settings explicitly when replacing. |
| `scenario` | Optional absolute authored simulation file, injected as provider `_MOCK_SCENARIO`; not required for real runtimes. |

Validation does not open credentials or history. Offline tests launch only the
pinned SDK `tools/agent-sdk-testify protocol mock` via strict temporary launchers,
with an explicit scenario and no real-agent fallback. They do not prove real
CLI startup, authentication, model behavior or native history availability.
Native history load and same-connection Claude continuous input remain follow-up
work; this baseline retains the explicit one-input Claude boundary.

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

### Delivery overload boundaries

The Runtime EventPipe keeps at most 128 queued events in total and at most 64
for one native Session. Nonempty Session queues are read round-robin, preserving
FIFO within each Session (there is no cross-Session chronology guarantee).
`Send` waits for capacity with its caller context or until `End`; there is no
hidden timeout, producer goroutine, or additional forwarding queue. A waiting
caller owns one cloned event; the adapters have one Receive producer per native
Session. The sole consumer can hold one returned event outside the pipe budget.
`Flush` is a routing watermark: it includes the consumer's last returned event
until its next `Next` call, not a storage/native-release acknowledgement.

Service normal delivery admits 128 queued events per Session, plus one active
settlement. At saturation, that Session rejects further input/answers with HTTP
429 (`resource_exhausted`, reason `session_delivery_overloaded`), enters
`closing`, and starts an original-source-fenced owned close outside the shared
Runtime reader. One `session.error` diagnostic follows the already queued
prefix. The **same** channel has 256 slots total, including the diagnostic and
lifecycle barriers; the remaining slots retain the closing source while peers
continue. This is a finite closing reserve, not a second queue. Native close
reuses normal cancellation/release/drain coordination, never kills a shared
Codex Client, and remembers confirmed release across storage/publication retries.
The automatic attempt has a five-second context budget where lifecycle APIs
honor cancellation. Synchronous Store/Handler calls cannot be forcibly timed out.

If the reserve also fills (or an already-closing/retired/opening owner exhausts
its existing boundary), the Runtime retains one original overflow event and
seals shared admission instead of silently dropping facts. Healthy-peer progress
is **not** promised under that total exhaustion. `ReconcileDelivery` explicitly
resumes the original reader; it does not implicitly reopen failed admission.
Append failure retains the original full fact/UUID and serial settlement for
`ReconcileHistory`. Unknown native release or unavailable persistence remains
`closing`/unsettled; only confirmed native release plus settlement permits
`archived`. A later explicit Archive/Stop can settle the same owner. Closing
state and HTTP rejection are visible while persistence is blocked; the ordered
realtime diagnostic can itself wait behind that blocked prefix.

Counts apply to all Session events, not payload bytes. SDK native FIFOs grow
dynamically, and downstream handlers/subscriptions have independent boundaries.
These local limits do not prove whole-chain bounded memory, OOM safety, or that
all upstream native facts reached durable storage. A fault in a shared backend
or its global writer lock can still stall all callers; the isolation guarantee
covers a Session-local blocked Append/Handler while peer dependencies remain
healthy. Ordinary slow subscriptions
still fail independently with `slow_subscriber`; required handler failure still
stops that event's remaining fan-out without replay or loss of committed history.
No HTTP request-body cap is introduced.

The HTTP input codec preserves all five ContentBlock shapes; existing native
adapters execute text input only. Claude accepts exactly one text block.
Complete text/thinking, tool calls/results, permission/form interactions, state
and sanitized Session errors are mapped; deltas are previews and never fabricated
into complete history. Unsupported complete content fails explicitly. Images,
audio, files, arbitrary native extension/subagent attribution, full event-handler
registration APIs, configuration-option APIs, pending-interaction lookup and
automatic expiry notifications are not implemented.

File persistence and adapter-private recovery are experimental choices, documented
in [file-store](docs/file-store.md), [recovery](docs/recovery.md) and
[failure settlement](docs/failure-settlement.md). `metadata.sqlite` and
`history.sqlite` are complete, disposable disk replicas rebuilt from authoritative
files on every Open. SQL serves resource/history queries and deduplication to
optimize server operation; files remain the sole authority. Resource status
updates replicate only changed resources and never scan unrelated history.
Metadata uses atomic replacement; event logs use append/fsync with an incremental
recovery intent. Fault and process-exit tests do not establish power-loss guarantees. Uncertain original
operations/pending UUIDs are retained within the original process; cross-process
unknown-operation settlement/outbox recovery is not implemented. Real Agent CLIs,
accounts/models/tools, native durable recovery, Linux and Rust validation are
outside these synthetic results. The HTTP v1 binding is implemented; these synthetic results do not establish
production readiness or real Agent execution.

The six-ticket scenario matrix, commit map and evidence guide are in
[acceptance](docs/acceptance.md).
