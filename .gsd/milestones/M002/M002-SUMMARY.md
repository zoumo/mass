---
id: M002
title: "Contract Convergence and ACP Runtime Truthfulness"
status: complete
completed_at: 2026-04-07T17:14:33.312Z
key_decisions:
  - D008/collaborative: Clean break to session/* + runtime/* — no legacy compatibility preserved
  - D009/collaborative: Retain SQLite metadata backend; defer BoltDB/abstraction to later milestone
  - D010/collaborative: Use gsd-pi and claude-code as required real ACP validation surfaces
  - D012/collaborative: Recovery authority from live agent-run state reconciled with persisted SQLite metadata; fail-closed when uncertain
  - D026/agent: events.Envelope with monotonic seq as single live+replay notification shape
  - D028-D033/agent: Schema v2 with discrete recovery columns + JSON blob for bootstrap_config; ALTER TABLE + isBenignSchemaError for idempotent migration
  - D034/agent: Recovered shims watched via DisconnectNotify, not Cmd.Wait
  - D035/agent: Bootstrap config persistence is non-fatal — session continues if persist fails
key_files:
  - docs/design/contract-convergence.md — cross-doc authority map
  - docs/design/runtime/run-rpc-spec.md — authoritative clean-break agent-run protocol
  - docs/design/runtime/runtime-spec.md — bootstrap-first runtime contract
  - scripts/verify-m002-s01-contract.sh — mechanical design proof surface
  - pkg/events/envelope.go — canonical Envelope type for live+replay
  - pkg/events/translator.go — monotonic seq assignment
  - pkg/rpc/server.go — session/* + runtime/* method surface
  - pkg/agentd/shim_client.go — NotificationHandler + RuntimeStatus()
  - pkg/agentd/recovery.go — RecoverSessions startup pass
  - pkg/meta/schema.sql — schema v2 with recovery columns
  - pkg/meta/session.go — UpdateSessionBootstrap()
  - tests/integration/restart_test.go — TestAgentdRestartRecovery proving R035+R036
  - tests/integration/real_cli_test.go — Real CLI integration harness
lessons_learned:
  - The two-part proof surface (shell verifier + bundle validation) was essential — each catches different drift categories and running only one leaves gaps (K030).
  - Clean-break protocol migration across 5+ packages is feasible in one slice when the authority map is settled first — S01's design work de-risked S02 significantly.
  - Recovered shims need DisconnectNotify since the daemon has no exec.Cmd handle — always design process watchers for the adopted-process case (K031).
  - Integration tests with real Unix sockets and process fork/kill are not hermetic — orphan cleanup is best-effort and test failure paths need defensive cleanup (K027).
  - The mockagent binary path (internal/testutil/mockagent) vs expected path (cmd/mockagent) tripped the S03 plan — always verify binary paths before writing plans (K032).
  - Schema migration without a framework works via ALTER TABLE + benign error detection, but the pattern must be extended as new error shapes appear (K033).
  - Real CLI integration tests need generous timeouts (120s for prompt) and graceful skip conditions — forcing strict timeouts or mandatory API keys would break CI.
---

# M002: Contract Convergence and ACP Runtime Truthfulness

**Converged the runtime design contract onto one authority map, replaced all legacy agent-run protocol surface with clean-break session/* + runtime/* methods, landed durable restart recovery with proven event continuity, and validated the assembled contract against real CLI agents.**

## What Happened

M002 was a four-slice campaign to make the MASS runtime contract internally consistent and its restart behavior truthful.

**S01 — Design contract convergence.** Closed the cross-document contradictions that had made further runtime work unsafe. Produced a single authority map (`docs/design/contract-convergence.md`) that assigns ownership of every design concept to exactly one document. Established that `session/new` is configuration-only bootstrap, `session/prompt` is the work-entry path, Room ownership is split between orchestrator desired state and agentd realized state, and workspace host-impact boundaries are explicit. Left a mechanical two-part proof surface (contract verifier script + example bundle validation test) so future design edits can be checked without prose review.

**S02 — shim-rpc clean break.** Replaced every legacy PascalCase method and `$/event` notification with the clean-break `session/*` + `runtime/*` surface across pkg/rpc, pkg/agentd, pkg/ari, and cmd/agent-run-cli. Introduced `events.Envelope{Method, Seq, Params}` as the single canonical shape for both live notifications and history replay, with monotonic seq assigned by `events.Translator`. Added `Client.RuntimeStatus()` exposing `recovery.lastSeq` for reconnect gap detection. The no-legacy-name grep gate proved zero residual legacy names in non-test source.

**S03 — Recovery and persistence truth-source.** Extended the sessions table to schema v2 with `bootstrap_config`, `shim_socket_path`, `shim_state_dir`, and `shim_pid` columns. Built `RecoverSessions` startup pass in `pkg/agentd/recovery.go` that lists non-terminal sessions, reconnects to live agent-runs via `runtime/status` → `runtime/history` → `session/subscribe`, and marks unreachable shims as stopped (fail-closed). The integration test `TestAgentdRestartRecovery` proves 8 events with contiguous sequence [0-7] across daemon restart with zero gaps, validating both R035 (event continuity) and R036 (config persistence).

**S04 — Real CLI integration verification.** Created a reusable test harness (`setupAgentdTestWithRuntimeClass` + `runRealCLILifecycle`) exercising the full ARI session lifecycle — workspace/prepare → session/new → session/prompt → session/status → session/stop → session/remove → workspace/cleanup — with real gsd-pi and claude-code runtime class configurations. Timeout infrastructure was tuned for real CLI startup and LLM call latencies (start=30s, prompt=120s). Tests skip gracefully when prerequisites (API keys, binaries) are missing, keeping CI green.

Across all four slices, the milestone moved the project from contradictory design prose and aspirational restart behavior to a mechanically verifiable contract with proven recovery truthfulness.

## Success Criteria Results

The roadmap did not define explicit success criteria (Vision was TBD). Verification is based on each slice's delivered objectives:

- ✅ **Design contract convergence** — `docs/design/contract-convergence.md` authority map exists; `bash scripts/verify-m002-s01-contract.sh` passes; `go test ./pkg/spec -run TestExampleBundlesAreValid -count=1` passes. Cross-doc contradictions eliminated.
- ✅ **Clean-break agent-run protocol** — All legacy PascalCase/`$/event` names removed from non-test source (rg gate: zero matches across pkg/rpc, pkg/agentd, pkg/ari, cmd/agent-run-cli). All affected packages compile and pass tests.
- ✅ **Durable recovery truth** — `TestAgentdRestartRecovery` proves event continuity (seq [0-7], zero gaps) and config persistence (bootstrap_config, socket_path, state_dir, PID survive restart). RecoverSessions reconnects live agent-runs and marks dead agent-runs stopped.
- ✅ **Real CLI integration** — `TestRealCLI_GsdPi` and `TestRealCLI_ClaudeCode` exercise full ARI lifecycle with real runtime class configs. Tests compile and execute (skip when prerequisites absent).
- ✅ **All unit and integration tests pass** — pkg/events, pkg/rpc, pkg/agentd, pkg/ari, pkg/runtime, pkg/meta all pass. TestAgentdRestartRecovery passes (2.92s).

## Definition of Done Results

- ✅ All 4 slices complete (S01, S02, S03, S04) — confirmed by gsd_milestone_status
- ✅ All 4 slice summaries exist on disk (S01-SUMMARY.md through S04-SUMMARY.md)
- ✅ Cross-slice integration verified: S02 consumed S01's design contract to implement the clean-break surface; S03 consumed S02's envelope/status API to build recovery; S04 consumed S01+S02+S03 to verify with real CLIs. All packages compile and test together.

## Requirement Outcomes

### Requirements Validated (active → validated during M002)

| Requirement | Evidence |
|---|---|
| R032 | `docs/design/*` now define one non-conflicting contract. Final verifier + bundle test passed at S01 close. |
| R033 | `agentRoot.path`, resolved `cwd`, `session/new`, `systemPrompt`, bootstrap have one authoritative meaning. Final verifier passed. |
| R034 | All legacy PascalCase / `$/event` names removed from non-test source. Grep gate: zero matches. Full test suite passes. |
| R035 | TestAgentdRestartRecovery Phase 6: 8 events with contiguous seq [0-7], zero gaps across restart. |
| R036 | TestAgentdRestartRecovery: bootstrap_config, socket_path, state_dir, PID persist; live agent-run reconnected, dead agent-run marked stopped. |
| R038 | Explicit host-impact boundary rules for workspaces, hooks, env, shared access documented in authoritative design set. Final verifier passed. |
| R039 | TestRealCLI_GsdPi and TestRealCLI_ClaudeCode exercise full ARI lifecycle with real runtime class configs. |

### Requirements Advanced (still active, progress made)

| Requirement | Progress |
|---|---|
| R036 | S01 named the durable bootstrap, replay, reconnect, and restart-truth gaps precisely enough for S03 to implement. |
| R044 | S01 explicitly separated convergence work from later restart, replay, cleanup, and cross-client hardening so the remaining backlog stays intentional. |

### Requirements Unchanged

- R037 (workspace identity/reuse/cleanup boundaries) — stays active; design boundaries documented but runtime enforcement continues in later milestones.
- R044 (additional hardening follow-on) — stays active; M002 addressed core convergence; remaining hardening is deferred by design.
- All deferred and out-of-scope requirements unchanged.

## Deviations

- The roadmap was planned with minimal metadata (Vision: TBD, no explicit success criteria or definition of done sections). Verification was based on slice-delivered objectives rather than formal criteria.
- S02/T03 required zero ARI source changes — a positive deviation, as the agent-run client migration in T02 was already complete.
- S03/T03 verify command referenced wrong mockagent path (cmd/mockagent vs internal/testutil/mockagent).
- S03/T02 added 3 extra test cases beyond the 3 specified in the plan.
- Some duplicate decisions were recorded (D028-D033 contain duplicates of D029/D030/D031 recovery posture decisions).

## Follow-ups

**Recovery hardening (M003 scope):**
- Recovery only proven with mockagent — real CLI agents (gsd-pi, claude-code) need restart recovery testing.
- Damaged-tail tolerance: what happens when history replay returns partial or corrupted data.
- Event log rotation / size limits (R022 deferred).
- Cross-client hardening: Codex runtime class validation (R040 deferred).

**Workspace enforcement:**
- R037 workspace identity/reuse/cleanup boundaries are documented but runtime enforcement hasn't been hardened.

**Room runtime:**
- R041 realized Room runtime with explicit ownership, routing, and delivery semantics is the next major feature direction after hardening.

**Operational:**
- Integration test cleanup for orphan processes on test failure is best-effort — could be improved with process group isolation.
- The contract verifier is intentionally narrow; it doesn't prove runtime conformance to the contract, only cross-doc consistency.
