# MASS Go experiment

[harness](../harness/index.md) is the read-only project contract. This implementation
uses **CLI → HTTP/SSE → Service → Store / Runtime**. It has one SessionEvent model,
one Driver consumer, independent live observers, and file-authoritative history.
Executable assembly lives under `cmd/`; CLI and other library modules live under `pkg/`.

## Layered verification

From the repository root, with Go 1.26.10 and pinned dependencies already cached:

```sh
# Package-local unit tests
make test-unit-go
# Constructed multi-module scenarios and strict SDK protocol probes
make test-integration-go
# Recorded consumer material replayed through agent-sdk-testify
make test-simulation-go
# Explicit real Codex/Claude recording; not run by test-go or check-go
make test-e2e-go
```

`test-go` and `check-go` run the first three layers. The E2E package has an
`e2e` build constraint, requires `MASS_RECORD=1`, and is never selected by the
normal offline suite.

The integration layer builds MASS's test-only strict protocol peer for Codex
**and** Claude plus `massd` and `massctl`, then runs `TestEntrypointSuite`
through actual CLI processes and HTTP+SSE. Every scenario is constructed. The
scenario peer matches ordered protocol frames
strictly, rebinds native request IDs, and does not execute tools, use accounts, call models,
or prove intelligence.
The command fails on a failed scenario or cleanup readback. Test-owned binaries,
workspaces, scenarios, data and loopback ports are temporary; process, listener
and directory cleanup is checked. No personal Agent configuration is used.

The suite demonstrates no-input native identity, separate text input, automatic
permission answers through original handles, live previews, complete persisted
history and pagination/filtering, explicit user questions for each Agent,
request-before-ACK handling, cancel receipt versus observed termination,
close/stop and original-ID resume followed by fresh input. It tests CLI parse
errors, Runtime operations, disconnect isolation and failed-creation cleanup.
Answer and filtered queries are tested through HTTP because they are not CLI commands.
Each persistent event read from SSE is compared with a committed history query.
SDK probe passes are reported separately and do not stand in for this suite.
`prototype-go` remains the focused entrypoint demonstration, and
`acceptance-sdk-go` remains the cross-capability selector.

Claude creation can return `unknown` after init: identity alone does not prove
idle state. The Claude Driver uses the SDK agent Provider and Session, including
its message, block, tool, interaction and state conversion. Input receipts retain
the SDK `applied_best_effort` result without upgrading native acceptance.
See [layered acceptance](docs/acceptance.md).

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
owned directory), `tokenFile`, `tlsCert`, `tlsKey`,
`prototypeAutoApprovePermissions`, and `sessionIdleTimeout`. Automatic permission
approval is off by default.
`sessionIdleTimeout` accepts Go duration syntax such as `5s` or `10m`; its
default is `10m`, and `0` disables automatic Session close. The matching
`--session-idle-timeout` flag overrides the file. Negative or invalid durations
are rejected before the daemon opens its stores. Automatic close releases
idle Session connections without stopping their Runtime.
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
`-o json` pretty-prints the complete business result, and watch emits
SessionEvent JSONL. Errors retain `code/reason/message` on stderr; watch
readiness is also on stderr. Exit codes are 0 (success), 1 (operation/I/O
failure or unknown result), 2 (invalid arguments), and 130 (interrupted wait).
Ordinary requests default to 30 seconds; `--timeout 0` disables the timeout.
Watch applies that timeout only through `subscription.ready`, then observes
until explicitly closed or interrupted.
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
bin/massctl session close review -n demo
bin/massctl session resume review -n demo
bin/massctl runtime stop codex-local
```

For Claude, use the installed Claude executable with the same resource commands:

```sh
bin/massctl runtime create claude-local --type claude --executable /absolute/path/to/claude
bin/massctl runtime start claude-local
bin/massctl session create review -n demo --runtime claude-local --cwd /absolute/owned/workspace
bin/massctl session prompt review -n demo --text 'Describe this workspace'
# Unlinked completion stays unknown; do not automatically retry/resume to
# bypass that fence. Explicit close/resume retains the original identity.
bin/massctl session close review -n demo
bin/massctl session resume review -n demo --text 'continue after close'
```

Both drivers currently accept text input; Claude requires exactly one text block.
Claude rejects `systemPrompt` and `additionalDirs` before dispatch; Codex maps
`systemPrompt` to native base instructions and rejects `additionalDirs`.
Claude resume supports zero input and requires an explicit absolute
`env.CLAUDE_CONFIG_DIR`. The SDK verifies CLI version, retained transcript and
live identity. Optional initial input is submitted after that verification.
These command examples describe supported interfaces, not proof of real CLI/model
execution. Authentication, real native recovery, accounts and models must be
validated separately in owned test directories. Permission auto-approval remains
an explicit daemon prototype policy; pending form/question interactions require
the HTTP answer API. This CLI version deliberately has no answer command.

### Consumer recording and replay

From the repository root, run `make test-simulation-go` (or its compatibility
alias `make recording-gate-go`) to replay the four exported Codex and Claude
basic and permission cases through a running MASS daemon and its public
HTTP/SSE API. The target forces `MASS_RECORD=0` and runs the artifact, receipt
and credential audits with the replays. The mock route must be
`executor=external-sdk`, `target=replay`, `provenance=consumer`, and the replay
receipt must show `nativeLaunched=false`.

The checked-in captures were recorded with SDK `5edf8b62`. They no longer pass
strict replay at the current T06 pin `efcb24ca`: Codex now sends an added initialize
capability, and Claude waits for `system init` before `Open` while the capture
sends the first user frame earlier. The artifact and audit tests pass, but all
four replay cases fail until those captures are re-recorded with the current SDK,
testify, and native CLI versions. They are retained as historical consumer
evidence and are not current-pin PASS evidence. Exact failures, hashes and
recording commands are in
[consumer recording acceptance](docs/recording-acceptance.md).

Real recording requires an authenticated Codex CLI and explicit opt-in.
`MASS_RECORD_MODEL` selects one model slug for both providers; the checked-in
cases use `auto/deepseek-v4.1-flash-1017`. Real recording lives behind both the
`e2e` build tag and `MASS_RECORD=1`.

```sh
MASS_RECORD=1 MASS_RECORD_MODEL=auto/deepseek-v4.1-flash-1017 \
  MASS_RECORD_CODEX=/absolute/path/to/native/codex \
  MASS_RECORD_OUTPUT=/var/tmp/new-mass-codex-basic \
  go test -tags=e2e -count=1 -v ./tests/e2e -run '^TestRecordCodexBasic$'
```

The permission case uses an explicit native `on-request` approval policy and a
read-only sandbox. Its prompt requests a harmless project-local write, and the
test answers the allow option actually offered by Codex; it never auto-approves
or invents an option.

```sh
MASS_RECORD=1 MASS_RECORD_MODEL=auto/deepseek-v4.1-flash-1017 \
  MASS_RECORD_CODEX=/absolute/path/to/native/codex \
  MASS_RECORD_OUTPUT=/var/tmp/new-mass-codex-permission \
  go test -tags=e2e -count=1 -v ./tests/e2e -run '^TestRecordCodexPermission$'
```

Use the native CLI executable to avoid package-launcher environment injection.
The output directory must not exist. Recording uses an isolated session under
`/var/tmp`, stable protocol cwd, and disabled project instruction discovery.
It publishes the export only after two strict replays and a personal-directory
scan succeed. Original sessions remain private under `/var/tmp` for local audit;
the index's `sourceSession` is relative to that directory. Failed sessions are
retained for diagnosis and do not create a passing export. Treat these sessions
as private because testify stages authentication there. The output contains
native session metadata; inspect it before sharing outside the repository.

Claude recording uses the same lifecycle and exports to
`tests/testdata/simulation/recordings/claude/<case>/`:

```sh
MASS_RECORD=1 MASS_RECORD_MODEL=auto/deepseek-v4.1-flash-1017 \
  MASS_RECORD_CLAUDE=/absolute/path/to/native/claude \
  go test -tags=e2e -count=1 -v ./tests/e2e -run '^TestRecordClaudeBasic$'

MASS_RECORD=1 MASS_RECORD_MODEL=auto/deepseek-v4.1-flash-1017 \
  MASS_RECORD_CLAUDE=/absolute/path/to/native/claude \
  go test -tags=e2e -count=1 -v ./tests/e2e -run '^TestRecordClaudePermission$'
```

Set `MASS_RECORD_CLAUDE_BASIC_OUTPUT` or
`MASS_RECORD_CLAUDE_PERMISSION_OUTPUT` to publish to another absolute path.
The permission wrapper supplies only the inline `permissions.ask: ["Bash"]`
setting and answers the offered `allow-once` option through MASS. Testify
isolates `HOME` during `prepare`; if Claude authentication is stored in
`~/.claude/settings.json` environment values, expose the configured
`ANTHROPIC_BASE_URL` and token only in the recording process environment. Do not
print or copy those values into recorded material. If the native CLI does not
emit the requested interaction, the case remains unverified and no passing
export is published.

### Runtime Configuration

The runtime SDK is pinned to `v0.0.0-20261111031457-4959190bc518`,
source commit `4959190b`, without a local replacement. The external
`agent-sdk-testify` tool is pinned to
`v0.1.3-0.20261111025559-1f8db14a50e1` for consumer recording and replay and
is not a production dependency.
The checked-in consumer fixtures remain bound to their recording SDK
`v0.0.0-20261105065348-5edf8b62e963`; that provenance is intentionally not
rewritten to the current runtime pin, and that incompatibility is reported
rather than hidden.
Codex shares one native Client across its Sessions; Claude owns one native
process per Session. Claude has one SDK Events consumer through MASS agentrelay;
live event identity, payload and metadata come from the SDK.
An unexpected relay EOF fails only its original Session binding; it does not
end a shared Runtime stream or synthesize an agent state event.
The SDK exposes `OpenOptions.Config` and optional `ConfigController.SetConfig`;
MASS does not yet expose model/effort configuration through its public API.
Codex input receipts retain the native turn ID and acceptance; Claude receipts
prove only a complete local write, not native acceptance or recording.

`--config-json` / `--config-file` accept these fields:

| Field | Behavior |
| --- | --- |
| `executable` | Required absolute executable path; never discovered from personal configuration. |
| `args` | Codex process arguments, default `["app-server"]`; explicit `[]` means no arguments. Claude rejects nonempty arguments; use an explicit launcher if needed. |
| `envPolicy` | `inherit` (default) merges parent environment with `env`; `replace` passes only configured entries, including an explicitly empty environment. |
| `env` | String-to-string overrides; callers supply any required PATH/HOME/config-directory settings explicitly when replacing. |
| `scenario` | Optional absolute authored simulation file, injected as provider `_MOCK_SCENARIO`; not required for real runtimes. |

Runtime `spec` separately accepts Session defaults:

| Field | Behavior |
| --- | --- |
| `features` | Session feature defaults; currently accepts `agentQuestions`. |
| `mcpServers` | Session MCP defaults. Each item requires `name` and `type` plus the transport fields. The runtime SDK supports `stdio` and `http`; `sse` is rejected. |

`features` merges by field and `mcpServers` merges by server name: a Session
value overrides the Runtime value with the same name, and a Session-only name is
added. An empty Session `mcpServers` list adds no overrides, so it does not
clear Runtime defaults. Each Session operation reads the current Runtime spec
before calling the Driver, so `runtime update` affects the next create, resume
or history load without restarting the Runtime. An already open Session is
unchanged.
`--features-json` and `--mcp-servers-json` set these fields from `massctl`;
`null` clears Runtime defaults. A Session omission or `null` inherits them.

For example, a Runtime may be created with:

```json
{
  "metadata": {"name": "agent"},
  "spec": {
    "type": "claude",
    "config": {"executable": "/absolute/path/to/agent"},
    "features": {"agentQuestions": false},
    "mcpServers": [{
      "name": "remote",
      "type": "http",
      "url": "https://example.invalid/mcp",
      "headers": {"Authorization": "Bearer ${TOKEN}"}
    }]
  }
}
```

Session `spec` accepts the same `features` and `mcpServers` keys; history-load
input accepts them as top-level request fields. `stdio` servers use `command`,
`args`, `env` and optional `cwd`; `http` and `sse` servers use `url` and
`headers`; `envHeaders` and `bearerTokenEnvVar` are HTTP-only. Remote URLs must
use HTTPS, except HTTP on loopback, and must not contain userinfo credentials.
Sensitive header/env entries use `${ENV_VAR}` references rather than literal
secrets. The unified SDK supports only `stdio` and `http`; `sse` is rejected.
Claude supports `stdio` and `http`, and rejects `sse`, `cwd`, `envHeaders` and
`bearerTokenEnvVar`. Claude follows the SDK target CLI merge behavior; an empty
list does not disable ambient servers.

Codex currently rejects every explicit Session `mcpServers` declaration before
native open with `unsupported/mcp_isolation_unavailable`. The pinned SDK cannot
enforce per-Session isolation from Codex ambient and project MCP configuration.
A nil list leaves provider ambient MCP behavior unchanged. This is an explicit
implementation gap against the harness stdio/HTTP requirement, not a supported
Codex capability. MCP configuration does not connect or health-check a server at
Session open.

The internal Codex mapping tests still cover `${ENV_VAR}` resolution and header
translation, but Session open rejects before that path. Those unit tests do not
establish a currently usable Codex MCP feature.

Validation does not open credentials or history. Offline tests launch only MASS's
`internal/testpeer` strict scenario binary via temporary launchers, with an explicit
scenario and no real-agent fallback. They do not prove real CLI startup,
authentication, model behavior or native history availability.
Both providers support explicit asynchronous retained-main-history load followed
by input on the verified resumed connection. Ordinary resume does not import
history. Claude continuation state follows SDK events; a result frame alone does
not establish idle state. See [history loading](#retained-history-load)
and [Claude source/cleanup limits](docs/claude-history-load.md).

Existing Session commands use `NAME -n NAMESPACE` or a raw global `SESSION_ID`
without namespace, with no default namespace, `--id`, guessing or lookup fallback.
IDs are encoded as one URL path segment. `session list` and `session history`
accept only `--limit` (default 100) and `--after`; one page is returned, including
its next cursor. History and watch are independent: watch shows only future
events, never fills history or reconnects, and unexpected EOF is a failure.
`session close` accepts any lifecycle state, preserves identity and history,
does not stop the Runtime, and can be retried safely when it leaves the Session
in `closing`.

Runtime create requires NAME and `--type`; absent configuration sends `{}`.
`--executable`, `--config-file` and `--config-json` are mutually exclusive input
modes. Files contain RuntimeConfig itself; `-` reads stdin. Runtime update first
GETs the resource, preserves its type, and PUTs a complete spec at the resource
path. `--executable` preserves other config fields; a complete JSON config replaces
them. `--features-json` and `--mcp-servers-json` independently set Session
defaults; omission preserves the current value and `null` clears it. An update
may contain only Session defaults. Empty updates fail locally. There is no
conditional-update protection: concurrent edits can be overwritten. Updates
never restart running instances.
Runtime delete never stops or cascades: Service rejects running/unreleased
resources and persistent Session references. Stop explicitly first; offline
Sessions retain their references and history. Successful delete has empty JSON
stdout. Test-only driver configuration stays in complete JSON, not public flags.

The client owns no local Store, Session directory, projection or retry policy.
See [the Go HTTP v1 implementation](docs/http-experiment.md) for server migration
status and protocol details. Server implementation and client contract can migrate
in separate commits; integration checks must use a daemon providing these routes.

## Verification and boundaries

```sh
make test-unit-go
make test-integration-go
make test-simulation-go
make build-go
```

The layers are package-local tests, constructed integration scenarios, recorded
simulation replay, and separately authorized real-agent E2E. `test-go` runs the
three offline layers; `test-e2e-go` is the only target that may launch a real
Codex or Claude process. Both executables parse flags with Cobra; use `--help`
to list options.

Lint uses golangci-lint v2.12.2 and the unchanged `.golangci.yaml` copied from
agentrun. Install the pinned tool with:

```sh
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
```

From the repository root, `make fmt-go`, `make lint-go`, and `make check-go`
format, lint, and verify the Go implementation respectively.

The public module tests, SDK protocol probes and `tests/integration` additionally
cover Store concurrency/cursors, lifecycle, uncertain operations, original work
isolation, blocked writes, partial/unknown commits and bounded overload. An SSE
write gate proves that 140 committed facts survive observer overflow, which emits
an explicit error. Client framing tests distinguish end, abnormal error and
unexpected disconnection, preserve terminal errors and release blocked reads.

### Retained history load

Use a started Runtime, a new MASS name and the original native ID. All paths are
absolute paths on the Agent host, not paths inferred from the HTTP client's HOME.
Codex history comes from native metadata/full ascending turn/item queries; set
its explicit `env.CODEX_HOME` when source identity requires it. Claude requires
explicit `env.CLAUDE_CONFIG_DIR` and the requested `cwd` as ProjectDir. Runtime
configuration can be supplied as complete JSON with `runtime create --config-json` (see
`massctl runtime create --help` for the exact input shape).

```sh
bin/massctl -o json session load imported -n demo --runtime agent \
  --cwd /absolute/owned/project --session-id ORIGINAL_NATIVE_ID
# The receipt is acceptance only; inspect historyLoad until completed or failed.
bin/massctl -o json session get imported -n demo
bin/massctl -o json session history imported -n demo --limit 100
# Subscribe before new input; wait for subscription.ready on stderr.
bin/massctl -o json session watch imported -n demo
# In another terminal, only after load deliveryCompleted:
bin/massctl -o json session prompt imported -n demo --text 'Continue this session'
# Cancel only an unfinished load, using its original receipt loadId:
bin/massctl -o json session cancel-load imported -n demo --load-id ORIGINAL_LOAD_ID
```

A receipt, native_completed marker, or ended observer is not durable completion.
Input opens only after verified resume and successful append/required publication.
Successful load retains the native connection; it does not need another resume.
`cleanupCompleted:false` on success is not a release failure: the connection is
retained. Cancel after completed cannot close later live work. Request timeout or
observer disconnect does not cancel an accepted background load.

Replay marks retained content and native identities; it does not restore old
state, permissions or answer authority. SSE is future-only; use public paginated
history for a late observer. Unsupported content, identity/source mismatch,
corruption, observed source drift, uncertain cleanup or partial publication never
become successful complete history. Partial committed facts and the reservation
remain queryable. Append failure needs explicit original-UUID reconciliation;
required publication is not automatically replayed. Stop/cancel retain original
ownership until admitted facts and native release settle.

The two history queries do not provide a cross-call atomic snapshot. Claude
105-record synthetic success crosses offset100; offsets are full-file scan result
slices, not I/O pagination. A 205-record replay can exceed the existing 128-event
normal lane boundary and fail explicitly with `session_delivery_overloaded`.
It is NOT evidence of successful large-history load. Limits and unknown native
linkage remain in force after a successful import.

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
`offline`. A later explicit Close/Stop can settle the same owner. Closing
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

T06 upgrade details, validation results, and remaining harness differences are in
[SDK upgrade acceptance](docs/sdk-latest-sync.md). Session `systemPrompt` appends
caller instructions to provider defaults via SDK `AdditionalSystemPrompt`;
MASS repeats the complete value on every open, resume, and load resume.
