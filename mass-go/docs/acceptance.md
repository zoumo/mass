# SDK session adaptation: layered acceptance

This is the current Go implementation acceptance map, not a change to the
[harness contracts](../../harness/index.md). The SDK is pinned to
`github.com/zoumo/agent-sdk-go v0.0.0-20261101172223-619cf8ac7e14`, source
`619cf8ac7e1484f82c40d6fd96155fab9404e634`, with no local `replace`.
A recipe or an existing test is not itself an executed PASS.

## Evidence Classes

| Class | What it can establish | Current scope |
| --- | --- | --- |
| SDK protocol probe | Immutable SDK APIs against strict authored native JSONL | `examples/sdkprobe`; separate from product acceptance |
| Product synthetic integration | Actual adapters, Service, file Store, CLI/HTTP/SSE and owned protocol peers | Default offline checks and the combinations below; no models or tools run |
| Public-module fault injection | Deterministic append/publish/close/admission outcomes through public interfaces | Original facts, unsettled obligations, bounded isolation and cleanup, not native CLI guarantees |
| Recording-driven MASS simulation | Product actions consumed against admitted, unmodified official captures | Executed: 0/2 selected continuation captures; strict initialization mismatches prevent business acceptance |
| Native protocol/live Agent | Installed CLI, actual identity, account/model and persistence | Not required for this SDK adaptation; no real-model execution authorized |

SDK-only replay is not MASS recording acceptance. Authored peers are not official
recordings. Old logs under `examples/sdkprobe/evidence`, the prior six-ticket
experiment and [HTTP v1](http-v1-acceptance.md) /
[Store/Broadcast](store-broadcast-acceptance.md) reports retain their original
source/version attribution; they are not current-pin PASS evidence.

## Reproducible Offline Entry Points

From the repository root, with Go 1.26.9, golangci-lint v2.12.2, `ps`, and
immutable module dependencies already cached:

```sh
rtk proxy sh -c 'GOWORK=off GOPROXY=off GOSUMDB=off make check-go'
rtk proxy sh -c 'GOWORK=off GOPROXY=off GOSUMDB=off make prototype-go'
rtk proxy sh -c 'GOWORK=off GOPROXY=off GOSUMDB=off go -C mass-go test -race -count=1 -v -timeout=180s ./tests/vertical -run "^Test(File)?EntrypointSuite$"'
```

`check-go` runs lint, all offline race packages, builds both executables, and
checks formatting. The package timeout is 300 seconds; vertical TestMain allows
330 seconds including owned builds and cleanup. `prototype-go` selects the
existing product entrypoint suite; those tests also run in `check-go`.
A November 2 full `check-go` attempt hit the vertical package's aggregate
300-second timeout, not a failing assertion. Retain that failure; partition
the top-level vertical selectors into independent invocations when the aggregate
suite exceeds that budget, and report their results separately from `check-go`.
Do not describe split results as a successful single `check-go` run.

Missing tools/dependencies mean NOT EXECUTED;
these offline commands do not download a fallback SDK or launch real Agents.

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
| Loaded connection, current approval and subsequent input, then archive | `integrated_load_ticket08_test.go` | Both providers; replay never grants live authority; Claude completion requires explicit native linkage |
| Close with queued interactions, stop concurrent load | `integrated_lifecycle_ticket08_test.go` | Both providers; stale handles, retained facts, confirmed versus unknown cleanup remain distinct |
| Healthy peer progresses after slow peer saturation, before slow gate release | `TestTicket05NativeProvidersIsolateSaturatedSession`, `pkg/service/overload_test.go` | Both providers; original complete facts/order/UUID, storage and required-publish ownership; upstream total exhaustion explicitly has no progress guarantee |
| Permission, form, early request before ACK, correction after NotSent | `native_interactions_ticket02_test.go`, entrypoint suites | Original native capability only; no policy-created acceptance proof |
| Claude serial input, ambiguity, cancel and late result | `claude_continuity_test.go`, entrypoint suites | Unlinked/conflicting evidence fences work instead of inventing TurnIDs |
| Archive/stop drains admitted final facts; uncertain native close | `lifecycle_ticket04_test.go`, `native_lifecycle_ticket04_test.go` | No global shared-client kill to simulate Session isolation |
| Codex retained full paged history and same connection input | `history_load_ticket06_test.go`, `history_load_http_ticket06_test.go` | Native ID/cwd/source checks, five content kinds, missing/null/empty output, partial failure |
| Claude retained main chain and same process continuation | `history_load_ticket07_test.go` | Explicit ConfigDir/ProjectDir, drift/corruption refusal, offset100 crossing, cleanup/cancel; synthetic UUID echoes are not real CLI guarantees |
| Load cancel, stale cancel, native completion before delivery, failed append/required publish | `pkg/service/*ticket06_test.go`, `load_integrity_ticket06_test.go` | Accepted load persists independently of HTTP disconnect; unsettled responsibilities remain visible |
| Real daemon/CLI/SSE/history, original-ID resume, disconnect and explicit cancellation | `TestEntrypointSuite`, `TestFileEntrypointSuite`, `TestFailureSuite` | File-authoritative persistence, owned temporary listener/workspace and process cleanup |
| Explicit non-scenario runtime launch | `TestExplicitEnvironmentLaunchWithoutScenarioConfig` | Both providers; proves product configuration works with an owned executable, not real credentials |

### All 18 Current Event Examples

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
model/delivery tests, not a claim each native CLI emits all 18 branches.

## Supported Boundaries And Failures

- Both providers accept text, including multiple text blocks. Unsupported
  complete native content fails explicitly rather than silently disappearing.
  Five content kinds in output/history do not imply five input kinds are enabled.
- Codex uses a shared native Client; Claude uses an owned process per Session.
  There is one Receive consumer per Session, not a second request-reader loop.
- A receipt, local sent evidence, native acceptance, work completion, load native
  completion, durable delivery and native cleanup are separate claims. Claude
  write success is not native acceptance. Unknown work never becomes idle merely
  because an HTTP request or historical replay ended.
- Current interaction authority is consumed once and cannot be restored from
  replay, an archived Session, stale load cancellation or copied IDs. There is
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

## Recording Simulation

`tests/sdksimulation` implements the SDK tool's `--request` / `result.json`
convention through the public MASS Service, actual adapters and file stores.
It starts the runtime, creates with explicit input, waits for committed messages
and public idle state, submits the second input on the original native identity,
and attempts shutdown even on failure. It uses only the runner-supplied peer.

Build from `mass-go/`:

```sh
rtk proxy go build -o /tmp/mass-sdk-driver ./tests/sdksimulation
rtk proxy go build -o /tmp/agent-sdk-testify github.com/zoumo/agent-sdk-go/tools/agent-sdk-testify
```

Use `agent-sdk-testify simulate --cases-file <selection.json> --driver
/tmp/mass-sdk-driver --output <new-attempt> --jobs 2 --timeout 60s`.
The selection is a JSON array of objects with a `case` field containing the
absolute capture directory from the pinned module's
`tests/simulation/recordings/<provider>/SDK-US-001/SDK-CASE-001/`.
Only the two-input continuation case is implemented; other cases fail explicitly.

The November 2, 2026 attempt selected these unmodified admitted captures:

| Provider | Capture | Result |
| --- | --- | --- |
| Codex | `20261101T065559Z-6a7c186793d249cb8595d5a057c65941` | FAIL at ordinal 1: recorded official Python client identity differs from MASS initialization |
| Claude | `20261101T065847Z-7f3b2b919eef44a091d5ce4e539406d1` | FAIL at ordinal 1: recording omits `forwardSubagentText`; Go SDK defaults to true |

The runner reports 0/2 passed. Neither case reached business assertions or full
protocol consumption. No real Agent was launched. The failure does not establish
a MASS business regression or a passing adaptation. Do not change the product's
initialization identity or observation policy just to match these captures.
The strict matcher and original capture bytes remain unchanged.

Local attempt logs, driver results, peer stderr, cleanup results and source
hashes are retained under `.scratch/sdk-trim/simulation/`. Early attempts include
driver setup errors and are not acceptance evidence. The final runner summary
is authoritative for the recorded attempt. Claude reports cleanup=false because
MASS retains an unresolved opening obligation after unexpected peer EOF; that is
separate from whether operating-system child processes remain.

## Current Completion Boundary

This iteration requires SDK adaptation, necessary bug fixes and SDK-tool-driven
MASS simulation. Real-model E2E and full new-feature acceptance are not completion
conditions. No real CLI/model calls are authorized by these offline recipes.
