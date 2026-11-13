# T06 SDK upgrade acceptance

T06 upgrades MASS at base `df607217` to
`github.com/zoumo/agent-sdk-go v0.0.0-20261110170013-efcb24caac28` and
`github.com/zoumo/agent-sdk-testify v0.1.2`, without a replacement. Validation
uses Go 1.26.10, `GOWORK=off`, and `GOPROXY=https://goproxy.cn,direct` on
2026-11-11. The supplied SDK worktree path did not exist during this run;
interface inspection used the checksum-verified module cache at the exact pin.
The separate dirty SDK checkout was not read or modified.

## Changes

- `events.ContentBlock` and all five branches alias the SDK struct union.
  Text extraction reads `block.Text`; SDK validation and JSON encoding retain
  sole ownership of content parsing. Input copying snapshots all five pointer
  branches and nested annotations before asynchronous dispatch. Providers still
  reject non-text input and text annotations.
- `SessionSpec.SystemPrompt` maps to SDK `AdditionalSystemPrompt` for Codex and
  Claude open, every resume, and history-load resume. Its meaning is append to
  provider defaults. Synthetic native traces verify Codex
  `developerInstructions` and Claude `--append-system-prompt`, including two
  independent resumes and a load. Invalid UTF-8 and NUL are mapped to the SDK's
  `invalid_argument` reasons before native open/resume/load effects.
- Additional directories remain unsupported, including Codex load. Initial
  input remains a composition of open/resume and `StartOrSteer`; no removed
  `InitialInputProvider`, plan payload, or compatibility alias is restored.
- Driver close caches the SDK result. Successful close settles a drained
  binding even when old observation state records an error. The SDK owns
  natural-termination cleanup and lease release; MASS calls its idempotent
  close and waits for routing before forgetting a binding.
- Admitted provider Stop is terminal and caches release errors. The explicit
  SDK `provider_has_open_sessions` admission rejection retains ownership and
  permits a later Stop. MASS rejects Stop while open/resume/load is in flight.
  Codex Start checks terminal state before returning cached capabilities.
- Capability mapping uses SDK `Supports`, so unknown true keys cannot become
  supported MASS capabilities.
- Huma binds the SDK ContentBlock struct before installing the custom JSON
  union schema. This avoids a duplicate-name panic caused by the new struct
  reflection; generated OpenAPI and client output remain unchanged.

## Validation

Long commands used an outer `timeout`, Go test timeouts, and `/tmp/mass-t06-*`
logs. A selected test result covers only the named scenarios.

| Check | Result |
| --- | --- |
| `make fmt-go` | PASS |
| `GOWORK=off make lint-go` | PASS, zero issues |
| `go test -race ./pkg/events ./pkg/runtime/... ./pkg/service ./pkg/transport ./pkg/client -count=1 -timeout=240s` | Events, all runtime packages, transport, and client PASS. Service FAIL with exactly the six known baseline subtests below. |
| `go generate ./pkg/client/...` | PASS; generated OpenAPI and client have no diff |
| `go test -race ./examples/sdkprobe -count=1 -timeout=90s` | PASS for both provider probes |
| `make build-go` | PASS for massd and massctl |
| EventStore legacy and independent-history selection | PASS under race: `TestLegacyEventSchema*` and `TestIndependentHistoryRebuildAndIdentity` |
| `TestEntrypointSuite` | FAIL: all twelve T06 failing subtests reproduce at `df607217`; details below |
| Native lifecycle and prompt integration selection | PASS: `TestLatestSDK*`, `TestTicket02ActiveClose*`, all `TestTicket04*`, `TestTicket06CodexPagedLoadAndSameConnectionInput`, and `TestTicket07ClaudeLoadHTTPAndTwoRetainedProcessWrites` |
| New runtime regression tests | PASS under race: union branch snapshot, invalid/multiple branch rejection, text-only admission, unknown capability, repeated prompt propagation, Stop error caching/admission, concurrent Close error caching, and drained natural EOF ownership |
| `MASS_RECORD=0 go test -race -v ./tests/simulation -count=1 -timeout=150s` | FAIL on the same four stale consumer captures and the incomplete-execution test that depends on stale Codex replay. Artifact, provenance, selection and secret audits PASS. |

The six service failures match the T05 baseline reported in
[SDK migration gaps](sdk-migration-gaps.md):

- `TestOpeningCleanupClaudeInitializeEOF`
- `TestPendingNativeCreateOwnerStop/shutdown=false/explicit=true`
- `TestPendingNativeCreateOwnerStop/shutdown=true/explicit=true`
- `TestPendingNativeRestoreOwnerStop/stop`
- `TestPendingNativeRestoreOwnerStop/shutdown`
- `TestPendingNativeRestoreOwnerStop/caller-abandon-only`

A broader history selection exceeded its 150-second group timeout. It also
reported Claude unlinked-result, multiple-offset, and overload timing failures.
A narrowed race rerun passed the multiple-offset and overload tests. The
unlinked-result timeout reproduced at both `df607217` in an independent worktree
and T06 (`history_load_ticket07_test.go:493`). Full integration acceptance is
therefore not claimed. A broader EventStore run also exceeded the deliberately
short 90-second timeout in the large `TestInterruptedEventProjectionStartup`
case; the targeted legacy classification checks are reported separately. The pre-existing Ticket17 linkage panic was not included
in the focused rerun.

The full product entrypoint suite completed in 107.961 seconds on T06 and
107.443 seconds at baseline. The following twelve T06 failures all reproduced
at `df607217`:

- `TestClaudeBasicRecovery`
- `TestClaudeCancellation`
- `TestClaudeCloseDrainsFinalFact`
- `TestClaudeConfirmedChildProvenance`
- `TestClaudeConflictingResultBlocksReadmission`
- `TestClaudeContinuousInputSameProcess`
- `TestClaudeLateResultsAndCancelReadmission`
- `TestClaudeResumedFirstInputRequiresLinkage`
- `TestClaudeStopDrainsFinalFact`
- `TestClaudeUnlinkedResultBlocksReadmission`
- `TestFailedCreateCleanup`
- `TestTicket02ClaudeQuestionAuthority`

These cover old synthetic-peer state/linkage assumptions, old snake-case
metadata expectations, explicit Claude resume-config requirements, unresolved
create cleanup, and pre-existing error-code expectations. The baseline run
also failed `TestTicket02CodexQuestionAuthority`; that failure did not occur
in T06's full run. No full product-suite PASS is claimed. Both completed runs
reported no owned test-peer/application processes remaining.

## Recording and real-agent evidence

Current recording and simulation version probes use `efcb24ca` / `v0.1.2`.
All four checked-in captures retain SDK `5edf8b62` and their original testify
provenance, raw frames, hashes, and indexes. Codex initialization's added
`experimentalApi` and Claude's init-before-Open-return ordering still make
these captures incompatible. They require fresh recording rather than frame
editing.

Real-agent execution and new recording are **UNVERIFIED**. The installed Codex
CLI reports `0.166.0` and its login status reports a ChatGPT login; this is not
model-execution evidence. The installed Claude executable was killed by signal
9 even for `--version`. No real model request or replacement consumer capture
was produced. The recording acceptance page distinguishes its T05 no-owned-auth state from
this current Codex login state.

## Harness and specification follow-up

No `harness/**` file was changed. The following exact references need parent
review before any authorized harness update:

- `harness/modules/runtime/models.tsp:42` and
  `harness/modules/runtime/design.md:57` declare `systemPrompt` without the SDK's
  append-to-defaults semantics or the requirement to repeat the complete value
  on resume. The public MASS field name and JSON wire remain unchanged.
- `harness/modules/service/design.md:228` and
  `harness/modules/service/session.md:140` describe configuration-only prompt
  behavior but do not state the additive provider mapping.
- `harness/implementation-gaps.md:41` records the older Claude observation-error
  veto on close. T06 now follows the current runtime contract at
  `harness/modules/runtime/design.md:77`: successful SDK close is the release
  result, while event routing must still drain. This is stale implementation
  evidence, not a request to rewrite historical acceptance as a new pass.
- At the T06 pin recorded above, explicit Codex MCP still rejected non-nil
  declarations. The 2026-11-11 follow-up at SDK `4959190bc518` removes that MASS
  gate and forwards resolved stdio/HTTP configuration for open/resume/load.
  Ambient field-level merge remains SDK/native-owned; omitted and empty lists
  do not disable ambient servers. Focused synthetic checks cover the adapter
  configuration boundary, not real MCP startup or health, and do not revise the
  historical T06 acceptance results above.

The shared `.scratch/sdk-source-of-truth/spec.md:162` and `:218` already state
that `InitialInputProvider` was removed and MASS composes open/resume with
`StartOrSteer`. No stale requirement needed changing; the integration worktree
and its specification files were left untouched.
