# SDK session adaptation: layered acceptance

This is the current Go implementation acceptance map, not a change to the
[harness contracts](../../harness/index.md). The SDK is pinned to
`github.com/zoumo/agent-sdk-go v0.0.0-20261031154401-ed6c8216607c`, source
`ed6c8216607ceacb5077b558a8302a587c9e6d5b`, with no local `replace`.
The integration baseline for ticket08 is MASS
`ba4f6984b0d995b3354d9d435c9145d359bbd6a0`. Final source/commit, commands,
artifact hashes, failures and cleanup readbacks are recorded separately in
central `.scratch/sdk-session-adaptation/validation/ticket-08.md`.
A recipe or an existing test is not itself an executed PASS.

## Evidence Classes

| Class | What it can establish | Current scope |
| --- | --- | --- |
| SDK protocol probe | Immutable SDK APIs against strict authored native JSONL | `examples/sdkprobe`; separate from product acceptance |
| Product synthetic integration | Actual adapters, Service, file Store, CLI/HTTP/SSE and owned protocol peers | Default offline checks and the combinations below; no models or tools run |
| Public-module fault injection | Deterministic append/publish/close/admission outcomes through public interfaces | Original facts, unsettled obligations, bounded isolation and cleanup, not native CLI guarantees |
| Recording-driven MASS simulation | Product actions consumed against admitted, unmodified official captures | NOT EXECUTED: no MASS recording request/result driver or proven compatible complete case selection in this checkout |
| Native protocol/live Agent | Installed CLI, actual identity, account/model and persistence | NOT EXECUTED in ticket08; explicit, separately authorized procedure below |

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
rtk proxy sh -c 'GOWORK=off GOPROXY=off GOSUMDB=off make acceptance-sdk-go'
rtk proxy sh -c 'GOWORK=off GOPROXY=off GOSUMDB=off go -C mass-go test -race -count=1 -v -timeout=180s ./tests/vertical -run "^Test(File)?EntrypointSuite$"'
```

`check-go` runs lint, all offline race packages, builds both executables, and
checks formatting. The full package timeout is300 seconds; vertical TestMain
allows330 seconds including owned builds and final cleanup. Individual scenario
deadlines remain unchanged. The prior180-second aggregate timeout failed and is
retained in ticket08 evidence, not erased by the budget change.
`prototype-go` builds the immutable SDK strict protocol mock
for each provider plus real massd/massctl and exercises `TestEntrypointSuite`.
`acceptance-sdk-go` selects all ticket08 combinations under race; it is not a
replacement for the complete check. Missing tools/dependencies mean NOT EXECUTED;
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

## Recording Simulation: Explicit Non-Execution

Only use the immutable module cache, never a mutable SDK checkout. Before any
recording run, bind capture manifest/admission, raw hashes, original business
result, scenario/provider and complete fixture selection. Verify the MASS
request/result driver exists and that unmodified capture requests match actual
MASS initialization/resume/input order. A strict mismatch is a failure or an
unsupported recipe, not permission to weaken matchers or replace client bytes.

This checkout provides authored protocol peers/probes, not a MASS recording
`--request`/`result.json` driver. A compatible source selection has not been
verified. Recording simulation therefore remains NOT EXECUTED, regardless of
SDK-only replay results. No recording repair, new captures or real CLI invocation
is part of ticket08. A future admitted run must retain attempts, full-consumption
and EOF results, nativeLaunched=false, business assertions and cleanup separately
from the offline result. Do not treat an empty selected-case set as PASS.

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
rtk proxy bin/massctl session archive check -n acceptance
rtk proxy bin/massctl session resume check -n acceptance
# Load is a separate fresh MASS name; get until completed/failed, do not retry:
rtk proxy bin/massctl -o json session load imported -n acceptance --runtime agent --cwd /ABS/OWNED/project --session-id ORIGINAL_NATIVE_ID
rtk proxy bin/massctl -o json session get imported -n acceptance
# Cancellation targets the original loadId, not whichever work is current:
rtk proxy bin/massctl -o json session cancel-load imported -n acceptance --load-id ORIGINAL_LOAD_ID
rtk proxy bin/massctl runtime stop agent
```

Repeat with `--type claude` and its own explicit configuration/workspace. HTTP
answering is available through `pkg/client.Client.Answer` or POST
`/apis/v1/sessions/{sessionId}/interactions/{interaction64}/answer`; the interaction
path segment is base64url-encoded according to the HTTP contract. See the
[client implementation notes](../pkg/client/README.md),
[runtime configuration](../README.md#runtime-launch-configuration) and
[load boundaries](../README.md#retained-history-load). Never print Bearer tokens.
