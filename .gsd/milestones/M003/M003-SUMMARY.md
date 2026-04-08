---
id: M003
title: "Recovery and Safety Hardening"
status: complete
completed_at: 2026-04-08T04:08:26.115Z
key_decisions:
  - D039: Two-level recovery model — atomic RecoveryPhase for daemon-wide gating + per-session RecoveryInfo for inspection
  - D040: Block session/prompt and session/cancel during recovery; allow status/list/stop/attach/detach
  - D041: RecoverSessions always transitions to Complete on all exit paths to prevent permanent recovery-blocked state
  - D042: ErrInvalidTransition from Transition() logged at Warn — transition edge cases don't block reconnecting to a live shim
  - D043: Unconditional os.Remove for TOCTOU-free socket cleanup
  - D044: Atomic subscribe-from-seq mechanism extends session/subscribe with optional fromSeq parameter
  - D045: Damaged-tail tolerance via bufio.Scanner + per-line json.Unmarshal replacing json.Decoder
  - D046: Two-pass classification for damaged-tail detection (tail-only-corrupt vs mid-file corruption)
  - D047: SubscribeFromSeq replaces separate History+Subscribe in recovery flow
  - D048: Workspace cleanup gates on DB ref_count instead of volatile registry RefCount
  - D049: DB ref_count is authoritative for cleanup safety; in-memory RefCount is fallback only
  - D050: Registry and WorkspaceManager refcounts rebuilt from DB after restart (non-fatal)
key_files:
  - pkg/agentd/recovery_posture.go — RecoveryPhase type, RecoveryInfo struct, phase constants
  - pkg/agentd/recovery_posture_test.go — 6 unit tests for phase tracking and IsRecovering semantics
  - pkg/agentd/recovery.go — recoverSession with shim-vs-DB reconciliation and atomic Subscribe(fromSeq=0)
  - pkg/agentd/recovery_test.go — 9 tests including reconciliation paths (stopped, created→running, mismatch)
  - pkg/agentd/shim_client.go — Extended Subscribe with fromSeq parameter
  - pkg/agentd/shim_client_test.go — TestShimClientSubscribeFromSeq
  - pkg/agentd/process.go — recoveryPhase atomic field, SetRecoveryPhase/GetRecoveryPhase/IsRecovering methods
  - pkg/ari/server.go — recoveryGuard helper, guards on prompt/cancel/cleanup, RecoveryInfo in session/status
  - pkg/ari/server_test.go — 36 tests including recovery guard and workspace ref_count tests
  - pkg/ari/registry.go — RebuildFromDB method
  - pkg/ari/registry_test.go — TestRegistryRebuildFromDB
  - pkg/events/log.go — Damaged-tail tolerant ReadEventLog with two-pass classification
  - pkg/events/log_test.go — 10 tests including damaged-tail scenarios
  - pkg/events/translator.go — SubscribeFromSeq atomic method
  - pkg/events/translator_test.go — TestSubscribeFromSeq_BackfillAndLive
  - pkg/rpc/server.go — session/subscribe fromSeq parameter and backfill support
  - pkg/rpc/server_test.go — fromSeq backfill integration test
  - pkg/workspace/manager.go — InitRefCounts method
  - pkg/workspace/manager_test.go — TestWorkspaceManagerInitRefCounts
  - pkg/meta/workspace.go — ListWorkspaceRefs helper
  - cmd/agentd/main.go — TOCTOU-free socket cleanup, Registry rebuild + InitRefCounts wiring after recovery
lessons_learned:
  - Two-level state gating (daemon-wide atomic phase + per-entity metadata) is the right pattern when you need both fast guards and detailed inspection — the guard is a single atomic read, the metadata is only read on demand
  - Always transition out of a blocking state on EVERY exit path including systemic failures — the fail-closed posture must be time-bounded, not a permanent trap (D041)
  - Shim truth wins over DB truth during recovery — when the two disagree, update DB to match shim (the running process is ground truth), don't fail recovery for a stale DB state
  - TOCTOU races in socket cleanup are real — unconditional os.Remove ignoring ErrNotExist is simpler and correct vs Stat→Remove→Serve
  - Holding a mutex during file I/O is acceptable in recovery/startup paths where the resource is idle — but this constraint MUST be documented in godoc to prevent hot-path misuse
  - Two-pass damaged-tail classification (classify lines, then decide) is simpler and more correct than single-pass with lookahead — the file is already fully read by the scanner
  - DB-as-truth for cleanup gating is essential when in-memory state doesn't survive restarts — volatile RefCount of 0 after restart would incorrectly allow cleanup of workspaces with active sessions
  - Non-fatal startup rebuild from DB is the right default — the daemon should start and serve reduced functionality rather than fail to start because workspace metadata couldn't be loaded
---

# M003: Recovery and Safety Hardening

**Hardened daemon recovery with fail-closed posture, shim-vs-DB reconciliation, atomic event resume, damaged-tail tolerance, and DB-backed workspace cleanup safety across restarts.**

## What Happened

M003 delivered four slices that close the remaining safety gaps in agentd's recovery and cleanup paths, making the daemon's restart behavior truthful, observable, and safe.

**S01 — Fail-Closed Recovery Posture and Discovery Contract.** Introduced the `RecoveryPhase` type (idle/recovering/complete) as an atomic field on ProcessManager, plus per-session `RecoveryInfo` for detailed inspection through ARI `session/status`. The `recoveryGuard` helper blocks `session/prompt` and `session/cancel` with JSON-RPC error -32001 (CodeRecoveryBlocked) during active recovery, while keeping read-only inspection (status, list, stop) always available. `RecoverSessions` manages the phase lifecycle, always transitioning to Complete on every exit path — including systemic failures — to avoid a permanent recovery-blocked state. 12 tests cover phase tracking, ARI guard behavior, and recovery metadata.

**S02 — Live Shim Reconnect and Truthful Session Rebuild.** Added shim-vs-DB state reconciliation in `recoverSession`: stopped shims trigger fail-closed (session marked stopped in DB), created→running mismatches are reconciled via `Transition()`, and other mismatches are logged but recovery proceeds. Also eliminated a TOCTOU race in ARI socket startup by replacing `Stat→Remove→Serve` with unconditional `os.Remove`. 3 unit tests cover all reconciliation paths.

**S03 — Atomic Event Resume and Damaged-Tail Tolerance.** Rewrote `ReadEventLog` from `json.Decoder` to `bufio.Scanner` per-line scanning with two-pass damaged-tail classification — corrupt lines at file tail (from crash-induced partial writes) are skipped, while mid-file corruption still errors. Added `Translator.SubscribeFromSeq` that reads the JSONL log and registers a live subscription under a single mutex hold, structurally eliminating the History→Subscribe event gap. Recovery was simplified from three steps (Status→History→Subscribe) to two steps (Status→Subscribe(fromSeq=0)). The RPC `session/subscribe` was extended with an optional `fromSeq` parameter, backward compatible with existing `afterSeq`. 9+ new tests cover damaged-tail scenarios, atomic subscribe, and the simplified recovery flow.

**S04 — Reconciled Workspace Ref Truth and Safe Cleanup.** Wired session lifecycle to DB `ref_count` tracking — `handleSessionNew` calls `store.AcquireWorkspace`, `handleWorkspacePrepare` persists the full Source spec (not `{}`). Added `Registry.RebuildFromDB` and `WorkspaceManager.InitRefCounts` to repopulate in-memory state from DB after restart (non-fatal failures). Replaced the volatile in-memory `RefCount` check in `handleWorkspaceCleanup` with a DB-based `ref_count` gate that survives daemon restarts, and added recovery-phase guard to block cleanup during active recovery. 7 new tests prove ref_count tracking, cleanup safety, registry rebuild, and manager initialization.

Cross-slice integration is clean: S01's recovery posture is consumed by S04 for workspace cleanup gating, S03's atomic subscribe replaced the recovery flow that S02 reconciles, and all slices share the fail-closed philosophy established in S01.

## Success Criteria Results

The M003 ROADMAP.md had Vision: TBD and no explicit success criteria enumerated. However, the milestone's implied success criteria — based on its title "Recovery and Safety Hardening" and the four slice goals — were all met:

- **Fail-closed recovery posture** ✅ — RecoveryPhase atomic tracking blocks prompt/cancel during recovery; session/stop always allowed; 12 tests prove guard behavior
- **Shim-vs-DB truthful reconciliation** ✅ — Three reconciliation paths (stopped, created→running, generic mismatch) implemented and tested; TOCTOU socket race eliminated
- **Atomic event resume (no gap)** ✅ — SubscribeFromSeq holds mutex during log read + subscription registration; proven by TestSubscribeFromSeq_BackfillAndLive
- **Damaged-tail tolerance** ✅ — Two-pass line classification skips corrupt tail lines while erroring on mid-file corruption; 5 tests cover the classification matrix
- **DB-backed workspace cleanup safety** ✅ — Cleanup gates on persisted DB ref_count, not volatile in-memory state; recovery guard blocks cleanup during recovery window; 7 integration tests prove safety across restarts

All test suites pass: pkg/agentd (7.8s), pkg/ari (6.5s), pkg/events (2.0s), pkg/rpc (14.1s), pkg/workspace (16.5s), pkg/meta (3.3s). Build and vet are clean.

## Definition of Done Results

- **All slices complete** ✅ — S01 (3/3 tasks), S02 (2/2 tasks), S03 (3/3 tasks), S04 (3/3 tasks) — all marked complete in DB
- **All slice summaries exist** ✅ — S01-SUMMARY.md, S02-SUMMARY.md, S03-SUMMARY.md, S04-SUMMARY.md all present on disk
- **Cross-slice integration verified** ✅ — S01 recovery posture used by S04 cleanup guard; S03 atomic subscribe replaces S02's recovery flow; full test suite passes with zero regressions
- **Code changes verified** ✅ — All key files exist with M003-specific code (RecoveryPhase, recoveryGuard, SubscribeFromSeq, RebuildFromDB, InitRefCounts)
- **Full build clean** ✅ — `go build ./cmd/agentd/... ./cmd/agent-shim/... ./pkg/...` exits 0
- **Full vet clean** ✅ — `go vet` across all affected packages exits 0
- **Full test suite passes** ✅ — All 6 packages pass with zero failures

## Requirement Outcomes

### R035 (continuity) — Runtime event recovery single resume path
**Status: validated → validated (evidence strengthened)**
M003/S03 upgraded the resume path from Status→History→Subscribe to atomic Status→Subscribe(fromSeq=0). Translator.SubscribeFromSeq holds the broadcast mutex during both log read and subscription registration, structurally eliminating the event gap. Proven by TestSubscribeFromSeq_BackfillAndLive, TestShimClientSubscribeFromSeq, and the full recovery test suite. Previous M002 validation was based on the three-step approach; M003 made it structurally gap-free.

### R037 (core-capability) — Workspace identity, reuse, cleanup boundaries
**Status: validated → validated (evidence strengthened)**
M003/S04 delivered DB-backed ref_count as the cleanup gate (store.GetWorkspace check in handleWorkspaceCleanup), recovery-phase guard blocking cleanup during recovery, Registry.RebuildFromDB for workspace identity persistence across restarts, and WorkspaceManager.InitRefCounts for refcount consistency. 7 integration tests prove workspace identity persists, cleanup is blocked by DB refs, and cleanup is blocked during recovery.

### R044 (quality-attribute) — Additional restart/replay/cleanup hardening
**Status: active → active (advanced)**
M003 addressed the recovery posture portion (S01: fail-closed phase tracking), shim reconnect hardening (S02: shim-vs-DB reconciliation), event resume hardening (S03: atomic subscribe, damaged-tail tolerance), and workspace cleanup hardening (S04: DB-backed ref_count, restart rebuild). Remaining follow-on work: real CLI restart recovery tests, cross-client hardening.

## Deviations

- M003 ROADMAP.md had Vision: TBD and no formal success criteria or definition of done — verification was based on slice-level goals and deliverables
- S01: TestARIRecoveryInfo_InSessionStatus was covered at agentd level instead; 3 extra ARI guard tests added beyond plan
- S03/T01: Append-after-damaged-tail correctly reclassifies corruption as mid-file (not tail) — test adapted accordingly
- S03/T03: Also updated pkg/agentd/process.go (not in plan) for the new Subscribe signature
- S04: Updated existing TestARIWorkspaceCleanupWithRefs to set DB ref_count (required by the new DB-first gate)

## Follow-ups

- Recovery only proven with mockagent — real CLI restart recovery tests needed
- `runtime/history` RPC and `ShimClient.History` are no longer used by recovery — consider deprecating or removing
- Registry rebuild does not verify on-disk workspace path existence — stale workspace detection could be added
- Cross-client hardening (multiple ARI clients interacting with same sessions) remains untested
- SubscribeFromSeq performs file I/O under Translator mutex — if production logs grow large enough that recovery reads take >10ms, consider a lock-free approach with pending-writes buffer
