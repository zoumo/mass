---
verdict: pass
remediation_round: 0
---

# Milestone Validation: M003

## Success Criteria Checklist
## Success Criteria (derived from M003-CONTEXT.md "Final Integrated Acceptance")

- [x] **agentd can restart, rediscover live shim sockets, reconnect to still-running shims, and surface truthful session state**
  - S01: RecoveryPhase lifecycle tracks recovering→complete; per-session RecoveryInfo surfaces recovered/outcome through session/status. 12 tests prove phase gating and metadata exposure.
  - S02: recoverSession reconciles shim-reported status against DB state (stopped→fail-close, created→running transition, generic mismatch→log-and-proceed). 3 tests + existing recovery tests.
  - S03: Atomic SubscribeFromSeq eliminates History→Subscribe event gap structurally. Recovery simplified to Status→Subscribe(fromSeq=0). Damaged-tail tolerance in ReadEventLog. ~12 new tests.
  - S04: Registry.RebuildFromDB and WorkspaceManager.InitRefCounts reconstruct workspace truth after restart. 7 tests prove ref_count tracking through session lifecycle and rebuild.

- [x] **A session in uncertain recovery state remains inspectable through status but blocks operational actions until truth is re-established**
  - S01: recoveryGuard blocks session/prompt and session/cancel with CodeRecoveryBlocked (-32001). session/status, session/list, session/stop remain unguarded. 6 ARI guard tests prove this.
  - S04: workspace/cleanup joins the blocked-during-recovery set. TestARIWorkspaceCleanupBlockedDuringRecovery proves this.

- [ ] **Codex can complete one real prompt round-trip on the hardened runtime path**
  - R040 (Codex compatibility) was **explicitly deferred by user** during milestone planning. The roadmap contains no Codex-related slices. This is an accepted scope reduction, not a delivery failure. The user selected "One real path (Recommended)" during discussion but subsequently deferred R040 before slices were planned.

- [x] **Restart-proof recovery bar** (from discussion: "Restart-proof (Recommended)")
  - All 4 slices collectively prove agentd restart safety: phase gating (S01), shim reconnect + reconciliation (S02), gap-free event resume (S03), workspace truth rebuild (S04).

- [x] **Fail-closed safety posture** (from discussion: "Fail closed (Recommended)")
  - S01: operational actions blocked during recovery; S02: stopped shims fail-closed (session marked stopped, not recovered); S04: destructive cleanup blocked during recovery.

- [x] **Full test suite regression-clean**
  - `go test ./pkg/agentd/... ./pkg/ari/... ./pkg/events/... ./pkg/rpc/... ./pkg/workspace/... ./pkg/meta/... -count=1` — all 6 packages pass (18.2s total, 0 failures).

## Slice Delivery Audit
| Slice | Claimed Deliverable | Delivered Evidence | Verdict |
|-------|--------------------|--------------------|---------|
| S01 — Fail-Closed Recovery Posture | RecoveryPhase type, recoveryGuard on prompt/cancel, SessionRecoveryInfo in status | `recovery_posture.go`, `recovery_posture_test.go` (6 tests), `server.go` guards + `server_test.go` (6 tests). Build/vet/test all pass. | ✅ Delivered |
| S02 — Live Shim Reconnect and Truthful Session Rebuild | Shim-vs-DB state reconciliation, TOCTOU-free socket startup | 3-branch reconciliation switch in `recovery.go`, unconditional `os.Remove` in `main.go`, 3 new tests (ShimReportsStopped, ReconcileCreatedToRunning, ShimMismatchLogsWarning). | ✅ Delivered |
| S03 — Atomic Event Resume and Damaged-Tail Tolerance | Damaged-tail ReadEventLog, atomic SubscribeFromSeq, simplified recovery to Status→Subscribe(fromSeq=0) | `log.go` rewritten with bufio.Scanner + tail classification (5 tests), `translator.go` SubscribeFromSeq under mutex (2 tests), `shim_client.go` extended with fromSeq (1 test), `recovery.go` simplified to atomic subscribe, `rpc/server.go` fromSeq param (2 tests). | ✅ Delivered |
| S04 — Reconciled Workspace Ref Truth and Safe Cleanup | DB-backed ref_count tracking, Registry+WorkspaceManager rebuild from DB, cleanup gated on DB ref_count + recovery phase | `server.go` AcquireWorkspace wiring + DB-first cleanup gate + recovery guard, `registry.go` RebuildFromDB, `manager.go` InitRefCounts, `meta/workspace.go` ListWorkspaceRefs, `main.go` startup wiring. 7 new tests. | ✅ Delivered |

## Cross-Slice Integration
## Cross-Slice Integration — All Boundaries Aligned

**S01 → S02:** S01 established the RecoveryPhase lifecycle on ProcessManager and wired it into RecoverSessions. S02 builds on this — the reconciliation code runs within the recovering→complete phase window managed by S01. ✅ No boundary mismatch.

**S01 → S04:** S01 established the `recoveryGuard` pattern (check IsRecovering, return -32001). S04 reused this exact pattern in `handleWorkspaceCleanup`, extending the fail-closed posture to destructive workspace operations. ✅ Pattern reused correctly.

**S02 → S03/S04:** S02 ensures DB state is reconciled to match shim truth before recovery proceeds. S03's atomic subscribe and S04's workspace rebuild both depend on DB state being truthful — this dependency is satisfied. ✅ Data flow consistent.

**S03 → Recovery Flow:** S03 replaced the three-step Status→History→Subscribe with two-step Status→Subscribe(fromSeq=0). S02's reconciliation block sits between Status and Subscribe in recoverSession, meaning the reconciliation still runs. ✅ Recovery ordering preserved.

**S04 startup wiring:** Registry.RebuildFromDB and WorkspaceManager.InitRefCounts are called in `main.go` after RecoverSessions but before ARI server starts. This means recovery phase is Complete by the time workspace state is rebuilt — consistent with S01's lifecycle. ✅ Startup ordering correct.

No cross-slice boundary mismatches detected.

## Requirement Coverage
## Requirement Coverage

| Requirement | Status Before M003 | M003 Impact | Status After M003 |
|-------------|-------------------|-------------|-------------------|
| R035 (event recovery single resume path) | validated (M002 baseline) | **S03 upgraded:** SubscribeFromSeq eliminates History→Subscribe gap structurally. Proven by TestSubscribeFromSeq_BackfillAndLive. | validated (strengthened) |
| R036 (session config/identity durable for restart) | validated (M002 baseline) | **S01+S02 strengthened:** RecoveryInfo metadata, shim-vs-DB reconciliation | validated (strengthened) |
| R037 (workspace identity, cleanup boundaries) | validated (M002 baseline) | **S04 hardened:** DB-backed ref_count as cleanup gate, rebuild from DB after restart, recovery guard on cleanup. 7 integration tests. | validated (hardened) |
| R038 (security boundaries) | validated (M002 baseline) | **S01 applied:** fail-closed runtime behavior with explicit -32001 error code and guard pattern | validated (applied) |
| R040 (Codex compatibility) | deferred | **Not addressed** — R040 was explicitly deferred by user before milestone planning. No Codex slices in roadmap. | deferred (unchanged) |
| R044 (recovery/safety hardening backlog) | active | **Advanced by all 4 slices:** S01 (recovery posture), S02 (shim reconnect hardening), S03 (event resume hardening), S04 (workspace cleanup hardening). Partially consumed — umbrella requirement may have remaining items. | active (advanced) |

### Unaddressed Active Requirements
- R040: Codex round-trip — deferred by user, documented as accepted scope reduction
- R044: Umbrella requirement — substantially advanced but intentionally remains active for future hardening work


## Verdict Rationale
**Verdict: PASS.** All four planned slices delivered their claimed output with comprehensive test evidence. The full test suite (6 packages, 18.2s) passes with zero regressions. Cross-slice integration boundaries are aligned — S01's recovery posture is reused by S04, S02's reconciliation feeds into S03/S04's trust assumptions, and the startup ordering in main.go respects the phase lifecycle.

The only gap against the original M003-CONTEXT.md aspirational acceptance criteria is R040 (Codex prompt round-trip), which was **explicitly deferred by the user** before milestone planning — the roadmap was planned and executed with 4 slices, none involving Codex. This is a documented scope reduction, not a delivery failure.

R044 (umbrella hardening requirement) was substantially advanced by all 4 slices but remains active by design — it's a backlog-tracking requirement that spans milestones.

Three requirements were meaningfully strengthened (R035, R036, R037) and one was applied (R038). The "Restart-proof" and "Fail-closed" discussion bars are both met with test evidence.
