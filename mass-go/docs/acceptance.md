# Go implementation acceptance: layered evidence

This is the current Go implementation acceptance map, not a change to the
[harness contracts](../../harness/index.md). The SDK is pinned to
`github.com/zoumo/agent-sdk-go v0.0.0-20261105065348-5edf8b62e963`, source
`5edf8b62e963e59efa20383d5373a9aee278f135`, with no local `replace`. The
external `agent-sdk-testify` tool is pinned to
`v0.0.0-20261105165039-e4799fe3484c`; it is a test-only recording and replay
dependency. Exact consumer case hashes and the current gate result are in
[consumer recording acceptance](recording-acceptance.md).

A recipe or an existing test is not itself an executed PASS.

## Followup Readiness

The source-bound measurements in this section predate the external-tool
adaptation. They are retained as historical evidence for the sources and old SDK
pin they name; they are not PASS evidence for the current module or external
`agent-sdk-testify` contract.

For the current SDK pin and external prepared-mock path, a November 3, 2026
rerun of the two admitted continuation captures remains **0/2 BLOCKED** with no
business acceptance or full-consumption receipt. Codex failed during runtime
initialization with `EOF`; its peer receipt reports `ordinal 1: client mismatch`.
Claude failed at `ordinal 1: client mismatch` with `fullConsumption=false` and
`clientEOF=false`. Neither provider consumed a complete protocol. The rerun used
external `agent-sdk-testify 0.1.0-dev` with SHA-256
`b181d8a3b9f2b9d4e7182598fb4c61142a157c054d07da7bdc3e1f0b43a2d7c1`.
Attempt-local evidence is at
`/tmp/mass-sdk-official-20261103.2C5ncS/codex/` and
`/tmp/mass-sdk-official-20261103-claude.OBzX95/claude/`. These are preserved
diagnostics, not durable repository fixtures.

**NOT-READY: official recording-driven acceptance remains BLOCKED (0/2).**
SDK-FU-05 independently executed source
`bb3f09cea0465631093543024c505eb3f7199edb`; its documentation delivery is
`94155e38cb6b9f5ef25b52ca7b3a8aea999689e4`; see the durable
[independent simulation results](sdk-simulation-results.md) for inputs, commands,
hashes, receipts and evidence locations. Both official cases failed at ordinal 1,
before business acceptance; both now report logical release=true. Earlier
cleanup=false attempts remain historical failures, not overwritten successes.

Keep these independent authored successes separate from the official denominator:
Codex passed actual testify simulate with all 18 frames, fullConsumption=true
and clientEOF=true; Claude passed a stateful driver/peer run with complete=true
and clientEOF=true, not static-recording consumption. Both passed their two-input
business checks and logical cleanup. Neither proves native Agent behavior.

Applicability to final gate-003 source `5589792cd54a523beff60daf20a5d3fdf0b9d30d`
was checked against SDK-FU-05's executed source: all 255 inventoried
product/driver/fixture/module/SDK/material/tool files are unchanged and present;
108 test/documentation files were excluded. This supports continued applicability
of those outputs across the test-only fixes, not a new simulation execution.
The source-rebinding audit is retained with integration evidence. Its first
validator attempt resolved relative paths from the wrong working directory and
is explicitly invalid; only the corrected root-bound comparison is used.

**Offline integration gate-003: PASS**, separately from the overall official
recording-driven NOT-READY status. The coordinator's earlier frozen gate-001 on
`bb3f09cea0465631093543024c505eb3f7199edb` failed: make exit 2,
372 seconds elapsed. `TestTicket17ClaudeProvenMessageLinkage/saturation`
hit its ten-second observer.Next context deadline at
`tests/vertical/claude_message_linkage_ticket17_test.go:71` (call site 149).
This was a scenario assertion failure, not the 600-second package timeout.
All 183 discovered top-level tests completed: 180 passed, one failed, two opt-in
scale tests skipped; none omitted. Source was unchanged. All three owned roots
and their processes were absent. Build and format stages after race did not
run because make stopped. Resource cleanup does not make the gate pass.
The coordinator's focused count=10 diagnostic passed (83.245 seconds total,
about 6.6-8.1 seconds per saturation iteration). It did not naturally reproduce
the failure and cannot prove a repair or its root cause.
Retained evidence: `followup-evidence/integration/final-gate-001/` outside Git.

Gate-001 predates the SDK-FU-06 cleanup fix
`8182fc201fe0e2d18652ccbaa7e573404038cc92`, which fixes test failure-path
cleanup, not the saturation deadline. The separate test fix
`d9a67323049656c0d6ba5a1336840eb2dc33837c` addresses a proven cumulative
observation-lifetime defect, described below. Gate-002 on frozen integration
source `5589792cd54a523beff60daf20a5d3fdf0b9d30d` also **FAILED**: exit 2
after four seconds at lint, before race/build/format. Source was unchanged.
Its diagnostics reference removed sdk-fu-02/sdk-fu-03 absolute paths and old
trim generated files, including failures to read removed sources. A fresh
external `GOLANGCI_LINT_CACHE` on the same source passes lint with zero issues,
confirming the stale-path cache failure rather than requiring source fixes.
No shared/global cache was cleared and no config or suppression changed.
Gate-003 on that same frozen source **PASSED**: `make check-go` exit 0,
346 seconds elapsed, with zero lint issues, all offline race packages, both
executable builds and format checks complete. Vertical took 333.194 seconds;
Service took 120.210 seconds. Other package results were standard Go-cache
results, explicitly marked cached in the log, not fresh executions.
Build-selected discovery and results match exactly: 183 top-level results,
181 PASS, zero FAIL, two opt-in scale SKIP, no missing or extra tests. The skips
are `TestEventStoreScaleAcceptance` and `TestReplicaScaleAcceptance`, not scale
acceptance. Source-before/source-after hashes are identical at
`5589792cd54a523beff60daf20a5d3fdf0b9d30d`. All three owned roots are absent,
with no owned processes remaining; both executable hashes are recorded.
Authoritative evidence is retained outside Git in
`followup-evidence/integration/final-gate-003/validation.json`, alongside its
full gate log and source snapshots.

Gate-003 used the isolated external lint cache and unchanged full-scope
`make check-go`, without suppressions, sharding or retry logic. Gate-001 and
gate-002 remain failed historical runs. This source-bound offline PASS does not
establish gate-001's full cause, erase intermittent failures, rerun simulation,
or change the official 0/2 BLOCKED result. No live/Linux/opt-in-scale PASS is
inferred.

SDK-FU-06 review found one Standards P2 (failure-path cleanup), zero Spec
findings. The fix registers bounded Service shutdown immediately after
construction, before store cleanup, and verifies owned peer release even when
an assertion aborts the test. Existing public Service/Runtime seams and owned
shell peers are unchanged. Race opening-cleanup regressions pass; an external
Go overlay injects an assertion failure after readiness: the original test
leaves its peer alive, while the fixed test reaps it before final PID readback.
The injected test intentionally exits nonzero in both runs. Evidence:
`/Users/jim/code/zoumo/mass-sdk-worktrees/followup-evidence/06/`.

The HTTP/SSE linkage scenario opens one subscription before 16 sequential
inputs. Its original ten-second Observe request context expired across all
rounds, despite individually timely operations. A deterministic external
overlay adds 1.1 seconds between rounds: original Next fails at about 10.14
seconds with an expired stream context while fresh HTTP history remains
readable. With the fix and the same delays, saturation passes in 26.77 seconds
with all fact counts, attribution, terminal, SSE/history and PID assertions.
The stream has a finite 170-second ceiling (opening plus 16 inputs at ten
seconds each); opening and each send-to-idle operation still have their own
ten-second cancellation. A negative overlay uses the existing peer's `hold`
input to withhold terminal completion: Next fails at 10.0003 seconds and owned
process cleanup passes. All 15 unmodified linkage subcases then pass under race
(21.114-second package time), with scoped lint at zero issues. No global context,
product code, assertion, round count, retry or queue behavior changes. This
proves the cumulative-lifetime defect and preserves missing-terminal detection;
it does not prove original host contention or exclude every other lost-terminal
hypothesis. Gate-001 remains a retained failed run. Differential inputs, logs
and validation are in the SDK-FU-06 evidence directory's `ticket17/` subdirectory.

Retained intermittent caveats: SDK-FU-03's full Service rerun failed
`TestSessionWorkSuite/TestBoundedIndependentObservationDoesNotCancelWork`
(six-second context deadline); three isolated race repeats passed. SDK-FU-04's
intermediate full race repeat failed
`TestTicket16CodexColdDirectInputHTTPHistoryAndContinuedInput` while waiting for
the fourth history event; 20 focused repeats and its subsequent standard gate
passed. Neither cause is proven, and later successes do not erase failures.
On November 3, 2026, a serial all-package run on the current external-tool
worktree failed once in
`TestTicket06CLILoadHTTPHistoryAndFutureSSE`: its SSE `Next` call hit the
ten-second observer context after 12.01 seconds, while the other packages passed.
The isolated test passed five consecutive runs, and an exact rerun of the serial
command passed with vertical at 383.616 seconds. The observer-deadline cause is
not proven; the first failure remains retained rather than replaced by the rerun.
SDK-FU-04's final gate on `4e5ad09a4ff07f0fc71f3e6f3b8d3fd1bdc847ce`
discovered 177 top-level tests (64 vertical): 175 passed, two opt-in scale tests
skipped. That is earlier-source evidence, not the final gate-003 result.

Remaining external decisions are MASS-compatible admitted Codex material and
Claude native linkage/UUID-binding contract evidence, or explicit approval to
reduce the acceptance goal to authored evidence. No such approval, new seam,
native run, SDK/matcher/capture change, harness write or publication is implied.

## Evidence Classes

| Class | What it can establish | Current scope |
| --- | --- | --- |
| SDK protocol probe | Immutable SDK APIs against strict authored native JSONL | `examples/sdkprobe`; separate from product acceptance |
| Product synthetic integration | Actual adapters, Service, file Store, CLI/HTTP/SSE and owned protocol peers | Default offline checks and the combinations below; no models or tools run |
| Public-module fault injection | Deterministic append/publish/close/admission outcomes through public interfaces | Original facts, unsettled obligations, bounded isolation and cleanup, not native CLI guarantees |
| Consumer recording replay | MASS public actions against exported, hash-bound Codex and Claude captures | Four registered cases; offline PASS with `nativeLaunched=false`; no live model under test |
| Native recording session | Installed CLI, authenticated account, selected model and exported native session | Four consumer-session recordings executed on 2026-11-05 with `auto/deepseek-v4.1-flash-1017`; source sessions retained outside the repository |
| Live MASS product E2E | Real MASS daemon/CLI/HTTP/SSE with a native runtime, model, history load and persistence | NOT EXECUTED in this checkout; explicit, separately authorized procedure below |

Consumer recordings have `provenance=consumer`; official verification does not
apply, and replaying them does not prove live Agent behavior. Authored peers are
not recordings. Old logs under `examples/sdkprobe/evidence`, the prior six-ticket
experiment and [HTTP v1](http-v1-acceptance.md) /
[Store/Broadcast](store-broadcast-acceptance.md) reports retain their original
source/version attribution; they are not current-pin PASS evidence.

## Reproducible Offline Entry Points

From the repository root, with Go 1.26.9, golangci-lint v2.12.2, `ps`, and
immutable module dependencies already cached:

```sh
export AGENT_SDK_TESTIFY_BIN=/absolute/prebuilt/agent-sdk-testify
rtk proxy sh -c 'cache=$(mktemp -d /tmp/mass-offline-lint.XXXXXX) && printf "owned lint cache: %s\n" "$cache" && GOLANGCI_LINT_CACHE="$cache" GOWORK=off GOPROXY=off GOSUMDB=off GOFLAGS=-v MASS_SDK_RACE=1 make check-go'
rtk proxy sh -c 'GOWORK=off GOPROXY=off GOSUMDB=off make prototype-go'
rtk proxy sh -c 'GOWORK=off GOPROXY=off GOSUMDB=off make acceptance-sdk-go'
rtk proxy sh -c 'GOWORK=off GOPROXY=off GOSUMDB=off make recording-gate-go'
rtk proxy sh -c 'GOWORK=off GOPROXY=off GOSUMDB=off go -C mass-go test -race -count=1 -v -timeout=180s ./tests/vertical -run "^Test(File)?EntrypointSuite$"'
```

`check-go` runs lint, all offline race packages, builds both executables, and
checks formatting. The full package timeout is 300 seconds; vertical TestMain
allows 330 seconds including owned builds and final cleanup. Individual scenario
deadlines remain unchanged. The prior 180-second aggregate timeout failed and is
retained in ticket08 evidence, not erased by the budget change.
`prototype-go` builds MASS's `internal/testpeer` strict scenario peer for each
provider plus real massd/massctl and exercises `TestEntrypointSuite`.
`acceptance-sdk-go` selects all ticket08 combinations under race; it is not a
replacement for the complete check. Missing tools/dependencies mean NOT EXECUTED;
these offline commands do not download a fallback SDK or launch real Agents.
`recording-gate-go` forces `MASS_RECORD=0`, so the opt-in real recording tests
skip; it runs the four replay cases plus artifact, receipt and credential-audit
checks. See [consumer recording acceptance](recording-acceptance.md).

Each Go invocation has its own test-owned setup/cleanup budget. Repeat product
faults in independent `-count=1` invocations, rather than exhausting one shared
TestMain deadline. Preserve the first failure, exact timeout and eventual cleanup
before changing a test-owned aggregate budget. Default opt-in scale-test skips
are not scale PASS results.

## Retained Behavior Review

The targeted independent Spec review compared broad baseline
`b29c57c4566cf1cf07bbd505836a794b1a00d3b8`, trim
`8a48a628c71259aa5a2ac688334a8d5d8cf6a261` and followup tip
`bb3f09cea0465631093543024c505eb3f7199edb`. It found no confirmed retention
regression. This is source/contract-to-regression mapping, not a fresh exhaustive
review of every changed file or a declaration that each test passed. The later
SDK-FU-06 deltas change only the failure cleanup and observation timing in tests;
the implementation anchors below are unchanged.

Paths in this table are relative to `mass-go/`. The detailed product combination
map follows; neither table extends official/native acceptance.

| Retained contract | Implementation anchor | Regression anchor / assertion |
| --- | --- | --- |
| Async load, verified native source, ordered import, same resumed connection, separate native/local completion | [Service load](../pkg/service/load.go), [Codex load](../pkg/runtime/codex/load.go), [Claude load](../pkg/runtime/claude/load.go) | [Codex history](../tests/vertical/history_load_ticket06_test.go), [Claude history](../tests/vertical/history_load_ticket07_test.go), [load lifecycle](../pkg/service/load_lifecycle_ticket06_test.go): ordering, pagination, continuation and unresolved restart fences |
| Explicit create input, identity/commit gate, original opening owner and receipt; ordinary create never replays input | [Service](../pkg/service/service.go), [Codex driver](../pkg/runtime/codex/driver.go), [Claude driver](../pkg/runtime/claude/driver.go) | [Explicit create](../tests/vertical/explicit_create_ticket14_test.go), [durable recovery](../tests/vertical/explicit_create_recovery_ticket14_test.go): early facts, late receipt and exact input list across recovery |
| Optional resume input on original ID/cwd; dispatched uncertainty remains unknown, ordinary resume input-free | [Service lifecycle](../pkg/service/lifecycle.go), both provider drivers | [Explicit resume](../tests/vertical/explicit_resume_ticket20_test.go): early terminal/receipt ordering, mismatch, commit fence and no hidden replay |
| Original native interaction handle, live run/work binding, single-use dispatch; retry only if confirmed unsent | [Interaction authority](../pkg/runtime/interactions.go), both provider drivers | [Native interactions](../tests/vertical/native_interactions_ticket02_test.go), [integrated lifecycle](../tests/vertical/integrated_lifecycle_ticket08_test.go): correction, consumed handle, archive revocation |
| Input/work ownership, conservative foreign/stale/conflicting linkage and first-resume input | [Codex driver](../pkg/runtime/codex/driver.go), [Claude linkage](../pkg/runtime/claude/message_linkage.go), [driver observation](../tests/sdksimulation/observe.go) | [Claude continuity](../tests/vertical/claude_continuity_test.go), [message linkage](../tests/vertical/claude_message_linkage_ticket17_test.go): no fabricated completion/authority, retained saturation facts |
| Sole steady-state Receive, startup/load handoff, Service-owned public Runtime stream | Both provider drivers, [Claude load](../pkg/runtime/claude/load.go), [Service](../pkg/service/service.go); simulation observer forwards Next inline | Native interaction, Claude history and continuity regressions plus structural ownership review; not a proof that arbitrary concurrent Receive is impossible |
| Drain before release, original cleanup errors, shared-client isolation, caller cancellation does not abandon opening ownership | Both provider drivers, [event stream](../pkg/events/stream.go), Service lifecycle | [Native lifecycle](../tests/vertical/native_lifecycle_ticket04_test.go), [pending create](../pkg/service/pending_native_create_test.go), [pending restore](../pkg/service/pending_native_restore_test.go), [opening cleanup](../pkg/service/opening_cleanup_test.go) |
| Bounded fair admission, overload isolation, commit-before-publish, original UUID settlement and no failed-load overtaking | Event stream and Service | [Stream tests](../pkg/events/stream_test.go), [Service overload](../pkg/service/overload_test.go), [vertical overload](../tests/vertical/overload_ticket05_test.go), [load integrity](../pkg/service/load_integrity_ticket06_test.go): retained admitted facts and explicit unsettled responsibilities |

### Simplification And Followup Changes

The trim from `078a5136e` to `8a48a628` changed no `mass-go/pkg` product code.
It shared the identical SDK protocol-tool build and ticket14/ticket20 recovery
peer, consolidated ticket12/15/16 rejection assertions, and removed ticket16's
duplicate cold positive still exercised by ticket12. The common rejection
helper retains native call counts, no hidden start/retry, partial-history
counts, unsubscribe/release and PID checks. Unique direct-input, metadata-drift
and explicit-resume cases remain. Generated client/schema remain because they
carry the public surface. Reduced duplication, not line count, is the benefit.

SDK-FU-01 documents the strict compatibility limits in the
[simulation route](sdk-simulation-route.md). SDK-FU-02's
[driver](../tests/sdksimulation/main.go) and inline
[observer](../tests/sdksimulation/observe.go) require exact Ready/Done history,
distinct owned receipts, work/run completion and post-stop history, rather than
counting response frames alone; no second consumer is introduced. SDK-FU-03
classifies confirmed Claude SDK startup release, preserving native diagnostics
and shutdown-timeout uncertainty. SDK-FU-04 only changes aggregate budgets and
retains race/full package selection. SDK-FU-05 supplies separately classified
[execution results](sdk-simulation-results.md). SDK-FU-06 fixes failure-path
test cleanup and per-input observation budgets without deleting assertions or
changing supported behavior. Their scoped evidence and the final integrated
offline gate are distinguished in Followup Readiness.

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

## Consumer Recording Replay

This checkout contains four consumer-provenance recordings under
`tests/testdata/simulation/recordings/{codex,claude}/{basic,permission}/`.
Each case binds an adjacent `index.json` to a `material/` export. The offline
gate replays them through a real MASS daemon and public HTTP/SSE, checks that the
mock route is `executor=external-sdk`, `target=replay`,
`provenance=consumer`, and requires the replay receipt to prove complete
consumption, client EOF, cleanup, matching plan/raw hashes and
`nativeLaunched=false`. It also treats missing, extra, altered, incompletely
consumed or secret-bearing material as failure.

Current offline status is four consumer cases passed, zero failed, and zero
unverified. The four opt-in recording tests passed separately under
`MASS_RECORD=1` with real Codex and Claude CLIs and
`auto/deepseek-v4.1-flash-1017`; `MASS_RECORD=0` replays those exports without
launching a native process. Live MASS product E2E and native persistence or
recovery behavior remain unverified. Official verification does not apply to
consumer recordings. A successful replay is not a live Codex/Claude or model
acceptance result. Full commands, artifact hashes, failure injection coverage
and recording instructions are in
[consumer recording acceptance](recording-acceptance.md).

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
   versus observed work termination; archive/resume original identity. Then use
   that run's owned native history ID in explicit load, inspect separate native/
   delivery/cleanup fields, observe future-only events, input/approval after load
   and archive. Missing Claude linkage is unknown/fenced, not success. Record an
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
export AGENT_SDK_TESTIFY_BIN=/absolute/prebuilt/agent-sdk-testify
rtk proxy go build -o /tmp/mass-sdk-driver ./tests/sdksimulation
rtk proxy go build -o /tmp/sdk-simulate github.com/zoumo/agent-sdk-go/tests/simulation/simulate
```

Run one selection per fresh output:

```sh
/tmp/sdk-simulate --recording /absolute/capture --driver /tmp/mass-sdk-driver \
  --output /absolute/new-attempt --timeout 60s
```

The SDK-owned runner prepares the external tool, passes its workspace and
environment to the host, writes `runtime.json` with `protocolCwd`, and invokes
the MASS driver. Only the two-input continuation case is implemented; other
cases fail explicitly.

The historical November 2, 2026 attempt selected these unmodified admitted
captures with the SDK-owned runner interface available at that time:

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
is authoritative for that historical attempt. Its Claude cleanup=false reflected
an unresolved opening obligation after unexpected peer EOF, independently of OS
child absence. SDK-FU-03 classified confirmed SDK startup release without hiding
native diagnostics or releasing shutdown-timeout uncertainty. SDK-FU-05's newer
independent strict run reports shutdownAttempted=true, storesClosed=true and
released=true for both providers, but still 0/2 official business passes and no
full-consumption receipts. This does not release other post-open cleanup=false
obligations or retroactively pass the original attempt.

## Current Completion Boundary

This iteration requires SDK adaptation, necessary bug fixes and SDK-tool-driven
MASS simulation. Real-model E2E and full new-feature acceptance are not completion
conditions. No real CLI/model calls are authorized by these offline recipes.
