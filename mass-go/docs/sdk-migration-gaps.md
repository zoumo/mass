# SDK migration gaps and T05 acceptance

This page is the T05 record for the migration from MASS-owned provider event
shapes to the unified `agent-sdk-go` contract. It does not change the
[harness contracts](../../harness/index.md).

Current follow-up: [T06 SDK upgrade acceptance](sdk-latest-sync.md). The pins and results below remain the historical T05 record.

## Pins and base

- T05 branch: `sdk-source-of-truth-t05-legacy-wire-simulation`
- T05 base: MASS `e7a74846`
- Runtime SDK: `github.com/zoumo/agent-sdk-go` at
  `v0.0.0-20261109202203-1c8514ff8a18`, with no `replace`
- Recording/replay tool: `github.com/zoumo/agent-sdk-testify` at
  `v0.1.1-0.20261109183517-b7c1fd72fc17`
- Checked-in consumer exports: recorded with SDK
  `v0.0.0-20261105065348-5edf8b62e963` and testify
  `v0.0.0-20261105165039-e4799fe3484c`
- Latest follow-up target: SDK `efcb24ca`, testify `v0.1.2`; T06 owns that
  upgrade and it is not claimed here.

## Public breaking changes

| Area | T05 behavior |
| --- | --- |
| Event union | The wire has 16 SDK live branches plus MASS `session.history_replay`. `agent.plan` is absent from the OpenAPI and generated client. |
| Removed plan data | Persisted `agent.plan` events fail with `unsupported/legacy_event_schema` instead of being rewritten, deleted, or exposed as an unknown payload. HTTP history returns 422; `massctl` exits 1 with the same public code and reason. |
| Tool identity | `agent.tool_call` and `agent.tool_result` use SDK `toolName`, `provider`, and `kind`. The old MASS `name` field is not accepted. Missing, null, non-string, or empty `toolName`/`provider` is classified as the same legacy-schema failure. |
| Content blocks | Every event content array and `Input.content` references the SDK five-branch `ContentBlock` schema. The generated model no longer exposes untyped per-event content arrays. |
| MCP transport | The unified SDK has no `sse` transport. Claude accepts `stdio` and `http`; `sse` fails before native open. |
| Initial input | The SDK no longer supplies `InitialInputProvider`. MASS composes open/resume with `StartOrSteer`; the composition is not atomic and does not retry an uncertain submission. |
| Claude steering | A busy `StartOrSteer` result can be `applied_best_effort`; MASS does not promote that result into proof that the input joined the current turn or that work is idle. |
| History | The SDK has no unified history/replay API. MASS continues to orchestrate provider-native history queries, paging, cancellation, and retained-fact persistence. |

These changes follow the explicit unified-SDK direction even where SDK migration
documentation recommends retaining the older MASS JSON shape.

## Implementation gaps

- **Codex explicit MCP was unavailable at T05.** That snapshot rejected every
  non-nil Session declaration with `unsupported/mcp_isolation_unavailable`.
  The 2026-11-11 follow-up at SDK `4959190bc518` removes the MASS isolation gate
  and passes resolved stdio/HTTP servers through SDK open/resume/load. Codex
  ambient field-level merging remains SDK/native behavior; omitted and empty
  lists do not disable ambient servers. Focused synthetic tests verify this
  adapter boundary, without changing the historical T05 acceptance results or
  claiming real MCP connection/health acceptance.
- **The consumer replay fixtures are stale.** The four checked-in captures were
  made with SDK `5edf8b62`. Under `1c8514ff`, Codex initialization now requires
  `capabilities.experimentalApi`, and Claude `Open` waits for `system init`
  before returning while the old capture sends the first user frame before
  `system init`. Strict replay exposes both protocol differences. The captures
  must be re-recorded with the T05 pins and an authenticated native CLI; editing
  their order, frames, or version metadata would destroy their consumer
  provenance.
- **No real-model acceptance was executed.** No owned authenticated Claude or
  Codex recording session was available in this worktree. The installed Claude
  version also differed from the recording version. This is UNVERIFIED, not a
  pass.
- **The latest SDK is intentionally deferred.** `efcb24ca` and testify `v0.1.2`
  are not tested by T05.

## T05 validation

Validation was run on 2026-11-11 with Go 1.26.10, `GOWORK=off`,
`GOPROXY=off`, and `GOSUMDB=off`.

| Command or boundary | Result | Detail |
| --- | --- | --- |
| `golangci-lint fmt` | PASS | No formatting diff remained. |
| `make lint-go` | PASS | Zero issues. |
| Targeted event, store, transport, client, and CLI tests | PASS | Includes legacy classification, SQL/file read paths, OpenAPI, generated models, HTTP 422, SSE frame identity, and CLI failure mapping. |
| `examples/sdkprobe` | PASS | Both providers and cleanup obligations pass at the T05 pins. |
| `make test-unit-go` | FAIL | Only `pkg/service` fails. The six failing subtests reproduce identically at base `e7a74846`; no T05 package failed. |
| `make test-integration-go` | FAIL | `TestTicket17ClaudeProvenMessageLinkage/proof` panics at `claude_message_linkage_ticket17_test.go:97`; the same panic reproduces at base `e7a74846`. The SDK probe passes independently. |
| `make test-simulation-go` | FAIL | The artifact, receipt, and credential-audit tests pass. All four consumer replays fail, and the incomplete-execution injection cannot establish its prerequisite replay. |
| `make check-go` | FAIL | Stops at the baseline `pkg/service` unit failure; it does not reach integration, simulation, or build. `make build-go` passes separately. |
| Real Agent E2E | UNVERIFIED | Not executed; no owned credentials or required recording versions were available. |

### Baseline unit failures

The following `pkg/service` tests fail identically at the T05 base and branch:

- `TestOpeningCleanupClaudeInitializeEOF`
- `TestPendingNativeCreateOwnerStop/shutdown=false/explicit=true`
- `TestPendingNativeCreateOwnerStop/shutdown=true/explicit=true`
- `TestPendingNativeRestoreOwnerStop/stop`
- `TestPendingNativeRestoreOwnerStop/shutdown`
- `TestPendingNativeRestoreOwnerStop/caller-abandon-only`

### Simulation failures

| Case | Error | Cause |
| --- | --- | --- |
| `TestCodexBasicReplay` | `unavailable: provider_start_failed` | Recorded initialize omitted `capabilities.experimentalApi`; the pinned SDK now sends it, so strict replay rejects the first client frame as a mismatch. |
| `TestCodexPermissionReplay` | `unavailable: provider_start_failed` | Same Codex initialize-protocol drift. |
| `TestClaudeBasicReplay` | `unavailable: initialize_wait_failed` | Recorded user frame precedes `system init`; the pinned provider waits for init during `Open`, creating a replay-order deadlock. |
| `TestClaudePermissionReplay` | `unavailable: initialize_wait_failed` | Same Claude ordering drift. |
| `TestRecordingRejectsIncompleteExecution` | `unavailable: provider_start_failed` | Its setup uses the stale Codex basic replay before mutating execution evidence. |

The checked-in hashes remain unchanged and still validate as artifacts. They do
not validate as compatible current-pin replay evidence.

## Reproduction

From the repository root:

```sh
rtk proxy sh -c 'GOWORK=off GOPROXY=off GOSUMDB=off make fmt-go'
rtk proxy sh -c 'GOWORK=off GOPROXY=off GOSUMDB=off make lint-go'
rtk proxy sh -c 'GOWORK=off GOPROXY=off GOSUMDB=off go -C mass-go test -race -count=1 -timeout=600s ./examples/sdkprobe'
rtk proxy sh -c 'GOWORK=off GOPROXY=off GOSUMDB=off make test-unit-go'
rtk proxy sh -c 'GOWORK=off GOPROXY=off GOSUMDB=off make test-integration-go'
rtk proxy sh -c 'GOWORK=off GOPROXY=off GOSUMDB=off make test-simulation-go'
rtk proxy sh -c 'GOWORK=off GOPROXY=off GOSUMDB=off make check-go'
```

Recording commands and the hash-bound fixture layout are in
[consumer recording acceptance](recording-acceptance.md).
