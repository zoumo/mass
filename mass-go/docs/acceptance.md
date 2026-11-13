# Go implementation acceptance: layered evidence

This is the current Go implementation acceptance map, not a change to the
[harness contracts](../../harness/index.md). The runtime SDK is pinned to
`github.com/zoumo/agent-sdk-go v0.0.0-20261113030733-09d4ddf1ff20`, source
`09d4ddf1`, with no local `replace`. The
external `agent-sdk-testify` tool is pinned to
`v0.2.1`; it is a test-only recording and replay
dependency. Exact consumer case hashes and the current gate result are in
[consumer recording acceptance](recording-acceptance.md).

Current T06 validation is recorded in [SDK upgrade acceptance](sdk-latest-sync.md).
A recipe or an existing test is not itself an executed PASS.

## Test Layers

| Layer | Command | Scope and boundary |
| --- | --- | --- |
| Unit | `make test-unit-go` | Package-local `pkg/`, `cmd/` and `internal/` tests under race; no cross-package product suite and no real Agent |
| Integration | `make test-integration-go` | Constructed multi-module scenarios in `tests/integration` plus strict SDK protocol probes in `examples/sdkprobe`; no recorded material or real Agent |
| Simulation | `make test-simulation-go` | Recorded Codex and Claude material replayed through `agent-sdk-testify`; requires `nativeLaunched=false`; no live model |
| E2E | `make test-e2e-go` | Real installed Codex/Claude CLI and model behind the `e2e` build tag plus `MASS_RECORD=1`; never selected by `test-go` or `check-go` |

`test-go` is the three offline layers. `check-go` adds lint, both executable
builds and formatting verification. A skipped E2E target is not a PASS.

## Evidence Classes

| Class | What it can establish | Current scope |
| --- | --- | --- |
| SDK protocol probe | Immutable SDK APIs against strict authored native JSONL | `examples/sdkprobe`; separate from product acceptance |
| Product integration | Actual adapters, Service, file Store, CLI/HTTP/SSE and owned protocol peers | `tests/integration`; constructed data only; no models or tools run |
| Public-module fault injection | Deterministic append/publish/close/admission outcomes through public interfaces | Original facts, unsettled obligations, bounded isolation and cleanup, not native CLI guarantees |
| Consumer simulation | MASS public actions against exported, hash-bound Codex and Claude captures | `tests/simulation`; four active cases plus four historical audited cases; all four active Claude/Codex replays pass at the current pins; no live model under test |
| Native recording session | Installed CLI, authenticated account, selected model and exported native session | Current Codex and Claude active cases were recorded through real CLIs with `auto/deepseek-v4.1-flash-1017`; Claude used CLI `2.1.293`; historical 2026-11-05 evidence retains its original pins |
| Live MASS product E2E | Real MASS daemon/CLI/HTTP/SSE with a native runtime, model, history load and persistence | NOT EXECUTED in this checkout; explicit, separately authorized procedure below |

Consumer recordings have `provenance=consumer`; official verification does not
apply, and replaying them does not prove live Agent behavior. Authored peers are
not recordings. Old logs under `examples/sdkprobe/evidence`, the prior six-ticket
experiment and [HTTP v1](http-v1-acceptance.md) /
[Store/Broadcast](store-broadcast-acceptance.md) reports retain their original
source/version attribution; they are not current-pin PASS evidence.

## Reproducible Offline Entry Points

From the repository root, with Go 1.26.10, golangci-lint v2.12.2, `ps`, and
immutable module dependencies already cached:

```sh
rtk proxy sh -c 'GOWORK=off GOPROXY=off GOSUMDB=off make check-go'
rtk proxy sh -c 'GOWORK=off GOPROXY=off GOSUMDB=off make test-unit-go'
rtk proxy sh -c 'GOWORK=off GOPROXY=off GOSUMDB=off make test-integration-go'
rtk proxy sh -c 'GOWORK=off GOPROXY=off GOSUMDB=off make test-simulation-go'
rtk proxy sh -c 'GOWORK=off GOPROXY=off GOSUMDB=off make prototype-go'
rtk proxy sh -c 'GOWORK=off GOPROXY=off GOSUMDB=off make acceptance-sdk-go'
```

`check-go` runs lint, the three offline layers, builds both executables, and
checks formatting. Unit and simulation each get an independent 600-second
timeout; integration gets 900 seconds because it also runs the strict SDK
probe. Integration's TestMain allows 330 seconds including owned builds and
final cleanup. Individual scenario deadlines remain unchanged.
`prototype-go` builds MASS's `internal/testpeer` strict scenario peer for each
provider plus real massd/massctl and exercises `TestEntrypointSuite`.
`acceptance-sdk-go` selects all ticket08 combinations under race; it is not a
replacement for the complete check. Missing tools/dependencies mean NOT EXECUTED;
these offline commands do not download a fallback SDK or launch real Agents.
`recording-gate-go` is a compatibility alias for `test-simulation-go`.
`test-simulation-go` forces `MASS_RECORD=0` and runs the four replay cases plus
artifact, receipt and credential-audit checks. `test-e2e-go` is the only target
that can launch a real Agent. See
[consumer recording acceptance](recording-acceptance.md).

Each Go invocation has its own test-owned setup/cleanup budget. Repeat product
faults in independent `-count=1` invocations, rather than exhausting one shared
TestMain deadline. Preserve the first failure, exact timeout and eventual cleanup
before changing a test-owned aggregate budget. Default opt-in scale-test skips
are not scale PASS results.

## Product Combination Map

All paths below are relative to `mass-go/`. The named tests are scenario
selectors, not a blanket claim that every event originates natively on both
providers. HTTP answers are deliberate: this CLI has no answer command.

| Behavior | Scenario/source | Evidence boundary |
| --- | --- | --- |
| Three inputs on one connection with approval in the middle | `TestTicket08ContinuousInputWithApproval` | Both providers; actual CLI prompt, HTTP invalid/correct/duplicate answer, SSE complete facts equal public history, original process reaped |
| Loaded connection, current approval and subsequent input, then close | `integrated_load_ticket08_test.go` | Both providers; replay never grants live authority; Claude completion requires explicit native linkage |
| Close with queued interactions, stop concurrent load | `integrated_lifecycle_ticket08_test.go` | Both providers; stale handles, retained facts, confirmed versus unknown cleanup remain distinct |
| Healthy peer progresses after slow peer saturation, before slow gate release | `TestTicket05NativeProvidersIsolateSaturatedSession`, `pkg/service/overload_test.go` | Both providers; original complete facts/order/UUID, storage and required-publish ownership; upstream total exhaustion explicitly has no progress guarantee |
| Permission, form, early request before ACK, correction after NotSent | `native_interactions_ticket02_test.go`, entrypoint suites | Original native capability only; no policy-created acceptance proof |
| Claude serial input, ambiguity, cancel and late result | `claude_continuity_test.go`, entrypoint suites | Unlinked/conflicting evidence fences work instead of inventing TurnIDs |
| Close/stop drains admitted final facts; uncertain native close | `lifecycle_ticket04_test.go`, `native_lifecycle_ticket04_test.go` | No global shared-client kill to simulate Session isolation |
| Codex retained full paged history and same connection input | `history_load_ticket06_test.go`, `history_load_http_ticket06_test.go` | Native ID/cwd/source checks, five content kinds, missing/null/empty output, partial failure |
| Claude retained main chain and same process continuation | `history_load_ticket07_test.go` | Explicit ConfigDir/ProjectDir, drift/corruption refusal, offset100 crossing, cleanup/cancel; synthetic UUID echoes are not real CLI guarantees |
| Load cancel, stale cancel, native completion before delivery, failed append/required publish | `pkg/service/*ticket06_test.go`, `load_integrity_ticket06_test.go` | Accepted load persists independently of HTTP disconnect; unsettled responsibilities remain visible |
| Real daemon/CLI/SSE/history, original-ID resume, disconnect and explicit cancellation | `TestEntrypointSuite`, `TestFileEntrypointSuite`, `TestFailureSuite` | File-authoritative persistence, owned temporary listener/workspace and process cleanup |
| Cold restart keeps Runtime/Session unknown, then explicit resume starts the Runtime and restores the original Session | `TestTicket03ColdRestartStartsUnknownRuntimeAndResumesSession` | File Store reopen and Service state machine; not an OS process crash, real CLI or provider-release proof |
| Explicit non-scenario runtime launch | `TestExplicitEnvironmentLaunchWithoutScenarioConfig` | Both providers; proves product configuration works with an owned executable, not real credentials |

### All 17 Current Event Examples

The source of truth is the current
`harness/modules/runtime/event-examples.json`, not an old frozen copy. Three
independent boundaries actually encode/decode its full examples:

```sh
rtk proxy sh -c 'GOWORK=off GOPROXY=off GOSUMDB=off go -C mass-go test -race -count=1 -v -timeout=180s ./pkg/events -run "^TestHarnessEventExamplesRoundTrip$"'
rtk proxy sh -c 'GOWORK=off GOPROXY=off GOSUMDB=off go -C mass-go test -race -count=1 -v -timeout=180s ./pkg/client -run "^TestGeneratedHTTPSuite$/TestAllEventBranchesStreamAndGeneratedModelRoundTrip$"'
rtk proxy sh -c 'GOWORK=off GOPROXY=off GOSUMDB=off go -C mass-go test -race -count=1 -v -timeout=180s ./pkg/service -run "^TestSessionWorkSuite$/TestAllEventBranchesUseSameLiveAndHistoryModel$"'
```

The model test compares JSON field fidelity; the client test crosses HTTP/SSE
and generated models; the Service test crosses persistence/broadcast/history
including an actually accepted load for replay lifecycle events. These are
model/delivery tests, not a claim each native CLI emits all 17 branches.

## Supported Boundaries And Failures

- Both providers accept text. Claude accepts exactly one text block. Unsupported
  complete native content fails explicitly rather than silently disappearing.
  Five content kinds in output/history do not imply five input kinds are enabled.
- Codex uses a shared native Client; Claude uses an owned process per Session.
  There is one Receive consumer per Session, not a second request-reader loop.
- A receipt, local sent evidence, native acceptance, work completion, load native
  completion, durable delivery and native cleanup are separate claims. Claude
  write success is not native acceptance. Unknown work never becomes idle merely
  because an HTTP request or historical replay ended.
- Current interaction authority is consumed once and cannot be restored from
  replay, an offline Session, stale load cancellation or copied IDs. There is
  no pending-interaction recovery/list API or cross-connection authority replay.
- Claude postload work needs recognized native linkage on every input. Caller
  UUIDs are correlation aids, not native TurnIDs. The positive synthetic peer
  echoes UUIDs; real Claude linkage remains unverified.
- A 105-record Claude case verifies first/last order across offset100 only.
  A 205-record producer can explicitly fail `session_delivery_overloaded` at the
  existing128 normal lane bound. Partial retained facts remain visible;205 is
  NOT successful large-history acceptance. Per-call source checks are not an
  immutable cross-call snapshot. See [Claude loading](claude-history-load.md).
- Bounded retention/fair admission isolates a saturated Session where capacity
  remains. There is no hard whole-chain/OOM or unconditional upstream progress
  guarantee. Persistent append, required-handler and native-close faults remain
  visibly unsettled, including cleanup=false and fenced new input as appropriate.
- History is file-authoritative with indexed reads; no SQLite migration or
  crash-recoverable outbox is claimed by these tests. Fresh-Service unresolved
  reservation checks are not process-crash/disk-reopen acceptance. The HTTP body
  has an independent read deadline, not a newly introduced size cap.
- Generic business-handler/configuration APIs, subagent attribution, every native
  extension, Linux and cross-language parity are not certified by this macOS Go
  acceptance. No deployment, push, publication or production-readiness claim.

## Consumer Simulation

Active consumer-provenance recordings live in
`tests/testdata/simulation/recordings-4959190bc518/codex/` and
`tests/testdata/simulation/recordings-09d4ddf1ff20/claude/`. The historical
`recordings/` suite remains immutable for audit provenance. Each case binds an
adjacent `index.json` to a `material/` export. The offline gate replays the
active four through a real MASS daemon and public HTTP/SSE, checks that the mock
route is `executor=external-sdk`, `target=replay`, `provenance=consumer`, and
requires the replay receipt to prove complete consumption, client EOF, cleanup,
matching plan/raw hashes and `nativeLaunched=false`. It also treats missing,
extra, altered, incompletely consumed or secret-bearing material as failure.

All four active consumer replays pass at SDK `09d4ddf1ff20` and testify
`v0.2.1`. The Claude cases were freshly recorded with CLI `2.1.293`; MASS opens
with `--await-claim`, never assigns `--session-id`, and adopts the ID returned by
`claim_session`. The historical captures were recorded with SDK `5edf8b62` and
are no longer replayed. Live MASS product E2E and native persistence or
recovery behavior remain unverified. Official verification does not apply to
consumer recordings. Hashes and recording commands are in
[current-pin recording acceptance](codex-current-pin-recording.md); the older
snapshot is in [historical consumer recording acceptance](recording-acceptance.md).

## Current T05 Gate

`make check-go` passes at the current pins: lint, unit, integration, the SDK
probe, all four active simulation replays, both builds and the format check are
green. This is offline evidence only. It does not establish live-product E2E,
native persistence/recovery, or arbitrary model behavior. The earlier T05
failure list is retained in
[SDK migration gaps and T05 acceptance](sdk-migration-gaps.md) as history.

## Separately Authorized Native / Real-Model Procedure

This is an explicit manual product entry point, NOT an automated pass or an
instruction to run models during default tests. The user's final gate requires
an independent simulation chat first, then a separate independently authorized
real E2E chat, both providers with the **theta / glm-5.3** target. Ticket08 does
not execute that gate. Preconditions missing or unsupported by an installed CLI
must be reported NOT EXECUTED, not substituted with a different model.

1. Record MASS commit and binary hashes, immutable module/version/no-replace,
   installed CLI absolute path/version/help, OS and model/provider configuration.
   Inspect only dedicated test configuration; do not read personal HOME/history.
   No secrets or access-bearing URLs may enter logs.
2. Prepare fresh absolute owned data, HOME, CODEX_HOME, CLAUDE_CONFIG_DIR and
   workspace directories, a loopback ephemeral listener and externally supplied
   test credentials. Never copy personal credentials or reuse personal history.
   Use `envPolicy: replace`; explicitly supply PATH and required config/env keys.
   The MASS runtime config has no `model` field: select theta/glm-5.3 using the
   installed CLI's verified configuration or an explicit owned launcher that
   preserves SDK arguments and execs the real CLI. Claude rejects nonempty MASS
   `args`; Codex defaults to `["app-server"]`. Do not pass invented flags or add
   `scenario` to real runtime configuration.
3. Run the product commands below against the owned daemon. Capture observe-ready
   before producing work. Use the HTTP client Answer method on the original live
   permission/form handle, after inspecting its actual options/schema; never use
   prototype autoapproval to claim manual-answer coverage. A selected permission
   answer is `{"outcome":"selected","optionId":"allow-once"}` only when that
   option was actually offered. Do not answer old replayed requests.
4. For each provider: no-input identity; first text input; current approval or
   question and answer; completion; second same-connection input; cancel receipt
   versus observed work termination; close/resume original identity. Then use
   that run's owned native history ID in explicit load, inspect separate native/
   delivery/cleanup fields, observe future-only events, input/approval after load
   and close. Missing Claude linkage is unknown/fenced, not success. Record an
   untriggered model interaction as untriggered, not PASS. Synthetic deterministic
   overload remains a separate class, not a reason to overload a real account.
5. On success AND failure: stop original Runtime, settle admitted local facts,
   record original unknown responsibilities, stop daemon and Wait, verify owned
   PIDs absent and listener closed, remove only owned directories and read back
   absence. A timeout or Close request alone is not release proof; unknown product
   cleanup remains unknown even if external ps observes process exit.

Build and manual invocation template (replace placeholders explicitly; these
commands have NOT been executed against a real Agent in this acceptance):

```sh
rtk proxy make build-go
rtk proxy bin/massd --data /ABS/OWNED/data --listen 127.0.0.1:0 --local
# Use the printed URL in MASS_URL; automatic permission approval stays OFF.
# /ABS/OWNED/provider.json contains a RuntimeConfig, not a Runtime resource:
# {"executable":"/ABS/OWNED/verified-launcher","envPolicy":"replace",
#  "env":{"HOME":"/ABS/OWNED/home","PATH":"/EXPLICIT/PATH",
#         "CODEX_HOME":"/ABS/OWNED/codex","CLAUDE_CONFIG_DIR":"/ABS/OWNED/claude"}}
# Supply only the selected provider's required entries and external auth settings.
rtk proxy bin/massctl runtime create agent --type codex --config-file /ABS/OWNED/provider.json
rtk proxy bin/massctl runtime start agent
rtk proxy bin/massctl -o json session create check -n acceptance --runtime agent --cwd /ABS/OWNED/project
rtk proxy bin/massctl -o json session watch check -n acceptance
# In a separate terminal after subscription.ready:
rtk proxy bin/massctl -o json session prompt check -n acceptance --text 'Reply with a short text'
rtk proxy bin/massctl -o json session get check -n acceptance
rtk proxy bin/massctl -o json session history check -n acceptance --limit 100
rtk proxy bin/massctl session close check -n acceptance
rtk proxy bin/massctl session resume check -n acceptance --text 'continue after close'
# Load is a separate fresh MASS name; get until completed/failed, do not retry:
rtk proxy bin/massctl -o json session load imported -n acceptance --runtime agent --cwd /ABS/OWNED/project --session-id ORIGINAL_NATIVE_ID
rtk proxy bin/massctl -o json session get imported -n acceptance
# Cancellation targets the original loadId, not whichever work is current:
rtk proxy bin/massctl -o json session cancel-load imported -n acceptance --load-id ORIGINAL_LOAD_ID
rtk proxy bin/massctl runtime stop agent
```

Repeat with `--type claude` and its own explicit configuration/workspace. Claude
requires the explicit resume input above; zero-input Claude resume fails before
native open because it cannot observe identity. HTTP
answering is available through `pkg/client.Client.Answer` or POST
`/apis/v1/sessions/{sessionId}/interactions/{interaction64}/answer`; the interaction
path segment is base64url-encoded according to the HTTP contract. See the
[client implementation notes](../pkg/client/README.md),
[runtime configuration](../README.md#runtime-launch-configuration) and
[load boundaries](../README.md#retained-history-load). Never print Bearer tokens.
