---
id: S02
parent: M002
milestone: M002
provides:
  - Clean-break shim RPC surface: session/* + runtime/* methods replace all legacy PascalCase / $/event names
  - events.Envelope type with monotonic seq as the single shape for live notifications and history replay
  - events.Translator assigns seq in-order so history replay and live subscribe share identical envelope format
  - ShimClient.NotificationHandler(method, rawParams) interface for typed protocol dispatch
  - RuntimeStatus() method for recovery.lastSeq access pattern
  - pkg/events, pkg/rpc, pkg/agentd, pkg/ari, cmd/agent-shim-cli all compile and test-pass on new surface
requires:
  []
affects:
  - S03
  - S04
key_files:
  - pkg/events/envelope.go
  - pkg/events/log.go
  - pkg/events/translator.go
  - pkg/events/log_test.go
  - pkg/events/translator_test.go
  - pkg/rpc/server.go
  - pkg/rpc/server_test.go
  - pkg/runtime/runtime.go
  - cmd/agent-shim/main.go
  - pkg/agentd/shim_client.go
  - pkg/agentd/shim_client_test.go
  - pkg/agentd/process.go
  - pkg/agentd/process_test.go
  - cmd/agent-shim-cli/main.go
  - pkg/ari/server.go
key_decisions:
  - events.Envelope{method, seq, params} is the canonical notification shape for both live and replay paths (D026)
  - NotificationHandler takes (method, rawParams []byte) — typed dispatch avoids deserializing in the transport layer
  - RuntimeStatus() returns ShimStateInfo including recovery.lastSeq — recovery state is inspectable without restart
  - shim-rpc-spec.md is sole normative owner of shim method and notification names; agent-shim.md is descriptive only (K029)
patterns_established:
  - events.Envelope with monotonic seq as the single live+replay notification shape
  - ShimClient.Subscribe(afterSeq) → chan Envelope pattern for replayable history subscription
  - Post-Create hook pattern: runtime state-change notifications attach after mgr.Create() to exclude bootstrap noise
  - No-legacy-name grep gate: !rg '"Prompt"|"Cancel"|"Subscribe"|"GetState"|...' in non-test source as slice-close proof
observability_surfaces:
  - ShimClient.RuntimeStatus() exposes recovery.lastSeq for reconnect gap detection
  - runtime/status RPC returns last known state with seq anchor for reply continuity
  - pkg/events log preserves full envelope history for replay on reconnect
drill_down_paths:
  - .gsd/milestones/M002/slices/S02/tasks/T01-SUMMARY.md
  - .gsd/milestones/M002/slices/S02/tasks/T02-SUMMARY.md
  - .gsd/milestones/M002/slices/S02/tasks/T03-SUMMARY.md
duration: ""
verification_result: passed
completed_at: 2026-04-07T17:10:00.000Z
blocker_discovered: false
---

# S02: shim-rpc clean break

**All legacy PascalCase shim methods and `$/event` notifications replaced with the clean-break `session/*` + `runtime/*` surface; all affected packages compile and pass tests; no legacy names remain in non-test source.**

## What Happened

S02 was a three-task implementation sprint to land the clean-break shim RPC contract that S01 designed.

**T01 — Notification envelope and shim server surface:**
Introduced `pkg/events/envelope.go` defining `events.Envelope{Method, Seq, Params}` as the single canonical shape for both live subscribe notifications and history replay. `events.Translator` was rewritten to assign monotonic `seq` values, ensuring `runtime/history` replay produces byte-for-byte identical envelopes to what was delivered live. `pkg/rpc/server.go` was rewritten from the legacy `Prompt`/`Cancel`/`Subscribe`/`GetState`/`GetHistory`/`Shutdown` method surface to the clean-break `session/prompt`, `session/cancel`, `session/subscribe`, `runtime/status`, `runtime/history`, `runtime/shutdown` surface. A post-`mgr.Create()` hook gates runtime state-change notifications so bootstrap traffic stays internal. `cmd/agent-shim/main.go` wired to the new server surface. Tests in `pkg/events` and `pkg/rpc` all pass.

**T02 — Downstream consumers: shim_client, process, CLI:**
`pkg/agentd/shim_client.go` rewrote the in-memory shim connection to call `session/prompt`, `session/cancel`, `session/subscribe`, `runtime/status`, `runtime/history`, `runtime/shutdown` and expose `NotificationHandler(method string, rawParams []byte)` for typed dispatch. `RuntimeStatus()` returns `ShimStateInfo` with `recovery.lastSeq` for reconnect gap detection. `pkg/agentd/process.go` updated to call renamed methods. `cmd/agent-shim-cli/main.go` migrated from legacy `shutdown` to `runtime/shutdown`, from legacy PascalCase to new surface. All `pkg/agentd` tests pass.

**T03 — ARI stability check and slice gate:**
Verified `pkg/ari/server.go` already uses the clean-break surface via Go method names routed through shim client — no code changes required. The four slice-gate checks all passed without modification: contract verifier script, `pkg/ari` tests, `pkg/runtime` tests, and the no-legacy-name grep gate (`! rg '"Prompt"|"Cancel"|"Subscribe"|"GetState"|"GetHistory"|"Shutdown"|"$/event"'` in non-test sources returned zero matches).

## Verification

All slice-gate checks passed:

1. `bash scripts/verify-m002-s01-contract.sh` — exit 0 (contract verification passed)
2. `go test ./pkg/events ./pkg/rpc -count=1` — exit 0
3. `go test ./pkg/agentd -count=1` — exit 0
4. `go test ./pkg/ari -count=1` — exit 0
5. `go test ./pkg/runtime -run TestRuntimeSuite -count=1` — exit 0
6. `! rg --glob '!**/*_test.go' '"Prompt"|"Cancel"|"Subscribe"|"GetState"|"GetHistory"|"Shutdown"|"$/event"' pkg/rpc pkg/agentd pkg/ari cmd/agent-shim-cli` — exit 0 (zero matches)

## Requirements Advanced

- R034 — Legacy shim surface fully replaced; clean-break surface verified in all consumers

## Requirements Validated

- R034 — All legacy PascalCase / `$/event` names removed from non-test source across pkg/rpc, pkg/agentd, pkg/ari, cmd/agent-shim-cli. Zero-match grep gate passed. Full test suite passes.

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

T03 required zero ARI source changes. The shim client migration in T02 was already complete when T03 started, meaning ARI's Go-level routing was already correct. This was a positive deviation — the slice closed cleaner than planned.

## Known Limitations

- `runtime/status` recovery.lastSeq is available but restart reconciliation (using it to detect event gaps after daemon restart) is S03 scope. The field is observable but not yet acted upon during recovery.
- No integration tests exercise the full shim→agentd→ARI path with the new protocol. That level of proof is S04 scope.

## Follow-ups

- S03: Use `RuntimeStatus().recovery.lastSeq` to detect and close event gaps during daemon restart/reconnect
- S03: Persist session identity and config durably so recovery can rebuild truthful state
- S04: End-to-end CLI integration verification against real ACP clients (gsd-pi, claude-code)

## Files Created/Modified

- `pkg/events/envelope.go` — new: canonical Envelope type with Method, Seq, Params
- `pkg/events/log.go` — rewritten to store/retrieve Envelope slices
- `pkg/events/translator.go` — rewritten to assign monotonic seq; translates runtime events to Envelopes
- `pkg/events/log_test.go` — updated tests for new Envelope API
- `pkg/events/translator_test.go` — updated tests for seq assignment and envelope shape
- `pkg/rpc/server.go` — rewritten: session/* + runtime/* method surface replacing legacy PascalCase
- `pkg/rpc/server_test.go` — updated tests for new method names
- `pkg/runtime/runtime.go` — wired post-Create state-change hook for notification delivery
- `cmd/agent-shim/main.go` — updated to new server surface
- `pkg/agentd/shim_client.go` — rewritten: NotificationHandler interface, RuntimeStatus(), new method calls
- `pkg/agentd/shim_client_test.go` — updated for new ShimClient API
- `pkg/agentd/process.go` — updated calls to renamed shim methods
- `pkg/agentd/process_test.go` — updated for process.go API changes
- `cmd/agent-shim-cli/main.go` — migrated from legacy surface to session/* + runtime/*
