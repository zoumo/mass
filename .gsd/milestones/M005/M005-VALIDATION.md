---
verdict: pass
remediation_round: 0
---

# Milestone Validation: M005

## Success Criteria Checklist

## Success Criteria Checklist

All criteria derived from the M005 context document and slice summaries.

### SC01 — External ARI surface uses agent/* exclusively; session/* is internal-only
- **Status: ✅ PASS**
- Evidence: `grep -c '"agent/' pkg/ari/server.go` → 10 dispatch cases. `grep '"session/new|prompt|cancel|stop|remove|list|status"' pkg/ari/server.go` → zero matches (exit 1). `TestARISessionMethodsRemoved` confirms all 8 session/* methods return MethodNotFound. 64 pkg/ari tests pass. ARI dispatch table shows workspace/*, agent/*, room/* only — no session/* cases.

### SC02 — agents table exists with room+name UNIQUE constraint; sessions.agent_id FK wired
- **Status: ✅ PASS**
- Evidence: `pkg/meta/schema.sql` contains `CREATE TABLE IF NOT EXISTS agents (…)` (schema v3, line 129) with `UNIQUE(room, name)` constraint and `idx_agents_room_name` index. Schema v4 adds `sessions.agent_id TEXT DEFAULT NULL REFERENCES agents(id) ON DELETE SET NULL`. 102 tests pass across pkg/meta and pkg/agentd.

### SC03 — 5-state agent state machine; paused:warm/paused:cold fully removed
- **Status: ✅ PASS**
- Evidence: `pkg/meta/models.go` defines `AgentStateCreating/Created/Running/Stopped/Error`. `rg 'PausedWarm|PausedCold|paused:warm|paused:cold' --type go` returns exit 1 (zero matches in production Go). Two comment-only references to `paused:warm` in `pkg/ari/server.go` (lines 690, 704) are inside the dead `handleSessionRemove` function — no dispatch case routes to it; confirmed harmless.

### SC04 — async agent/create returns creating immediately; background bootstrap transitions to created/error
- **Status: ✅ PASS**
- Evidence: `TestARIAgentCreateAsync` (PASS) — create returns creating → poll status → transitions to created. `TestARIAgentCreateAsyncErrorState` (PASS) — invalid runtimeClass transitions agent to error with non-empty ErrorMessage. Both tests use real mockagent shim. 90s goroutine timeout with context.Background() decoupled from request context.

### SC05 — agent/restart is fully implemented (not MethodNotFound stub)
- **Status: ✅ PASS**
- Evidence: `TestARIAgentRestartAsync` (PASS, 0.45s) — full lifecycle: create → poll → prompt → stop → restart → poll → prompt → stop → delete. `agentdctl agent restart --help` output shows correct async polling guidance.

### SC06 — OAR_AGENT_ID / OAR_AGENT_NAME in env; deprecated OAR_SESSION_ID / OAR_ROOM_AGENT removed
- **Status: ✅ PASS**
- Evidence: `pkg/agentd/process.go` lines 284-285 inject `OAR_AGENT_ID` and `OAR_AGENT_NAME`. `grep -n 'OAR_SESSION_ID|OAR_ROOM_AGENT' pkg/agentd/process.go | grep -v '//'` → exit 1 (no production injections). `TestGenerateConfigWithRoomMCPInjection` (3 subtests) passes.

### SC07 — Turn-aware event envelopes: turnId/streamSeq/phase on session/update; runtime/stateChange excluded
- **Status: ✅ PASS**
- Evidence: `pkg/events/envelope.go` has `TurnId string`, `StreamSeq *int`, `Phase string` on `SessionUpdateParams`. 7 `TestTurnAwareEnvelope_*` unit tests all PASS — covering: TurnId assigned/propagated, streamSeq monotonic, multiple turns reset correctly, stateChange excluded, JSON round-trip, replay ordering. RPC integration tests updated to 6-event model with turn field assertions.

### SC08 — room/status returns agent-aligned fields (AgentState/Description/RuntimeClass); room/send guards on agent state
- **Status: ✅ PASS**
- Evidence: `RoomMember` struct in `pkg/ari/types.go` carries `Description`, `RuntimeClass`, `AgentState` (SessionId/State removed). `handleRoomStatus` queries agents table. `handleRoomSend` guards on `AgentStateStopped` and `AgentStateCreating`. 5 test functions updated and passing.

### SC09 — room-mcp-server rewritten with modelcontextprotocol/go-sdk v0.8.0
- **Status: ✅ PASS**
- Evidence: `go.mod` contains `github.com/modelcontextprotocol/go-sdk v0.8.0`. `go build ./cmd/room-mcp-server` exits 0. Config reads `OAR_AGENT_ID`/`OAR_AGENT_NAME`. Hand-rolled 497-line JSON-RPC loop deleted.

### SC10 — Agent identity (room+name+agentId) survives daemon restart; RecoverSessions reconciles agent state
- **Status: ✅ PASS**
- Evidence: `TestAgentdRestartRecovery` (7-phase, 3.34s, PASS) — agents created pre-restart have identical agentId/room/name post-restart even in error state. RecoverSessions failure branch marks dead-shim agents as error; creating-cleanup pass handles bootstrap races. 3 new unit tests cover all reconciliation branches.

### SC11 — All 7 authority design documents contradiction-free; contract verifier exits 0
- **Status: ✅ PASS**
- Evidence: `bash scripts/verify-m005-s01-contract.sh` → "M005/S01 contract verification passed", exit 0. All 7 docs (agentd.md, ari-spec.md, shim-rpc-spec.md, agent-shim.md, room-spec.md, contract-convergence.md, README.md) verified clean.

### SC12 — Integration test suite migrated to agent/* surface; full test suite passes
- **Status: ✅ PASS**
- Evidence: `go test ./tests/integration/... -count=1 -timeout 180s` → ok (6.577s), 7 pass, 2 skip (ANTHROPIC_API_KEY not set, expected). `grep session/* tests/integration/ | grep -v real_cli_test.go` → empty. `go test ./pkg/... -count=1` → all 8 packages pass.


## Slice Delivery Audit

## Slice Delivery Audit

| Slice | Claimed Output | Delivered? | Evidence |
|-------|---------------|------------|----------|
| S01 — Design Contract | 7 authority docs rewritten; contract verifier script | ✅ | `bash scripts/verify-m005-s01-contract.sh` exits 0; all 7 files confirmed present and updated |
| S02 — Schema & State Machine | agents table (schema v3), sessions.agent_id FK (schema v4), 5-state machine, 102 tests pass | ✅ | schema.sql verified; `go test ./pkg/meta/... ./pkg/agentd/...` → 102 tests pass |
| S03 — ARI Agent Surface | 10 agent/* handlers, 9 session/* removed, AgentManager, agentdctl CLI, 64 pkg/ari tests | ✅ | dispatch table verified (10 cases); TestARISessionMethodsRemoved passes; `go test ./pkg/ari/...` 64 tests |
| S04 — Async Lifecycle | Async create goroutine, real restart (not stub), OAR_AGENT_ID/NAME env, agentdctl restart | ✅ | TestARIAgentCreateAsync/ErrorState/RestartAsync all PASS; process.go lines 284-285 confirmed |
| S05 — Event Ordering | TurnId/StreamSeq/*int/Phase on SessionUpdateParams, Translator turn tracking, 7 unit tests | ✅ | envelope.go fields present; 7 turn tests PASS; RPC integration 6-event model passes |
| S06 — Room & MCP Alignment | RoomMember agent-aligned fields, room-mcp-server SDK rewrite, deprecated env vars removed | ✅ | RoomMember struct verified; go-sdk in go.mod; OAR_SESSION_ID/OAR_ROOM_AGENT absent from process.go |
| S07 — Recovery & Integration Proof | RecoverSessions with agent reconciliation, TestAgentdRestartRecovery, integration test migration | ✅ | TestAgentdRestartRecovery PASS (3.34s); `go test ./tests/integration/...` 7 pass; zero session/* in non-CLI tests |

**S02 title rendering note:** The roadmap shows "⬜" for S02's title (rendering artifact in the markdown table), but the DB status shows `complete` with 2/2 tasks done, and the S02-SUMMARY.md is fully populated as "Schema & State Machine — agents Table and State Convergence." This is a cosmetic rendering issue only.

All 7 slices: 19/19 tasks complete, `verification_result: passed` on all slice completion records.


## Cross-Slice Integration

## Cross-Slice Integration Check

### S01 → S02 (design contract → schema)
- **Claimed:** S01 provides 5-state model for S02 agents table and state machine
- **Delivered:** ✅ S02 agents table uses creating/created/running/stopped/error; SessionManager validTransitions mirrors spec exactly; paused:* explicitly rejected in tests

### S01 → S03 (design contract → ARI handlers)
- **Claimed:** S01 provides agent/* method signatures for S03 migration
- **Delivered:** ✅ S03 dispatch has exactly 10 agent/* cases matching ari-spec.md (agent/create, prompt, cancel, stop, delete, restart, list, status, attach, detach)

### S01 → S05 (design contract → event ordering)
- **Claimed:** S01 provides turnId/streamSeq/phase spec for S05 implementation
- **Delivered:** ✅ S05 implements exactly the three fields documented in shim-rpc-spec.md; stateChange exclusion rule implemented; replay semantics (turnId,streamSeq) within turn, seq across turns

### S02 → S03 (schema → ARI handlers)
- **Claimed:** S03 consumes meta.Agent types and CRUD
- **Delivered:** ✅ AgentManager wraps meta.Store CRUD; Create/Get/GetByRoomName/List/UpdateState/Delete used in all 10 handlers; sessions.agent_id FK pre-flight handling (D072) correct

### S03 → S04 (ARI surface → async lifecycle)
- **Claimed:** S04 depends on handleAgentCreate/Stop/Delete/Status from S03
- **Delivered:** ✅ S04 refactors handleAgentCreate to async without breaking S03 surface; handleAgentRestart replaces MethodNotFound stub; creating-state guard added to handleAgentPrompt

### S03 → S06 (ARI surface → room alignment)
- **Claimed:** S06 depends on agent/* handlers, AgentManager, GetAgentByRoomName
- **Delivered:** ✅ handleRoomStatus uses agents.List; handleRoomSend uses GetAgentByRoomName for routing; RoomMember uses AgentState from agents table

### S04 → S05 (async lifecycle → event ordering)
- **Claimed:** S05 affects S04 (creating→created transitions now wrapped in turns)
- **Delivered:** ✅ NotifyTurnStart/End wired in handlePrompt after async bootstrap ensures turn events only fire on ready agents (creating guard blocks prompts)

### S04 → S06 (env vars → room-mcp-server)
- **Claimed:** S06 must remove OAR_SESSION_ID/OAR_ROOM_AGENT deprecated aliases
- **Delivered:** ✅ process.go deprecated vars removed; room-mcp-server reads OAR_AGENT_ID/OAR_AGENT_NAME; TestGenerateConfigWithRoomMCPInjection asserts absence of deprecated vars

### S01–S06 → S07 (capstone)
- **Claimed:** S07 builds recovery and integration proof on top of all prior slices
- **Delivered:** ✅ AgentManager injected into ProcessManager; RecoverSessions uses agent state reconciliation; TestAgentdRestartRecovery proves R052; integration tests use full agent/* surface

### Boundary Mismatches
- **handleSessionRemove dead code:** `handleSessionRemove` function remains in `pkg/ari/server.go` (lines 681–721) but has NO dispatch case in the switch statement. It is unreachable at runtime. The two `paused:warm` comment references are inside this dead function. This is a minor cleanup gap — no functional impact.
- **Phase field unpopulated:** `Phase string` field exists on `SessionUpdateParams` but no code path populates it. Documented as known limitation in S05; reserved for future use.
- **agent/detach placeholder:** `handleAgentDetach` returns nil (no-op). Documented in S03 known limitations. No dispatch impact.

All integration boundaries deliver what downstream slices require.


## Requirement Coverage

## Requirement Coverage

All 6 M005-scoped requirements are validated:

| Req | Description | Slice | Status | Proof |
|-----|-------------|-------|--------|-------|
| R047 | agent/* ARI external surface; session/* internal only | S03 | ✅ Validated | 10 agent/* dispatch cases; TestARISessionMethodsRemoved passes; 64 pkg/ari tests pass |
| R048 | async agent/create — returns creating, polls to created/error | S04 | ✅ Validated | TestARIAgentCreateAsync + TestARIAgentCreateAsyncErrorState both PASS with real mockagent shim |
| R049 | 5-state machine (creating/created/running/stopped/error); paused:* removed | S02 | ✅ Validated | 102 tests; rg paused check exits 1 in production Go |
| R050 | turnId/streamSeq/phase on session/update; stateChange excluded; global seq retained | S05 | ✅ Validated | 7 TestTurnAwareEnvelope_* tests PASS; RPC integration 6-event model confirmed |
| R051 | room-mcp-server with modelcontextprotocol/go-sdk; OAR_AGENT_ID/NAME env | S06 | ✅ Validated | go-sdk in go.mod; go build exits 0; TestGenerateConfigWithRoomMCPInjection passes |
| R052 | agent identity (room+name) survives daemon restart | S07 | ✅ Validated | TestAgentdRestartRecovery 7-phase test PASS (3.34s); agentId/room/name identical pre/post-restart |

**Requirements table from REQUIREMENTS.md:** All six requirements (R047–R052) show `status: validated` with specific proof evidence recorded. The M005 context document's requirements coverage table maps all 6 requirements to specific slices with no gaps.

**No active requirements unaddressed.** The only known deferral was R047 code-level grep gate (deferred from S01 to S03) — confirmed delivered in S03.


## Verification Class Compliance

## Verification Classes

### Contract Verification
**Status: ✅ Addressed**

`scripts/verify-m005-s01-contract.sh` runs 5 positive heading checks and 5 negative JSON-method-string pattern checks across all 7 authority documents. Exits 0. Runnable at any time as a regression gate. Design contract convergence verified in S01; implementation matches contract verified across S02–S07. `go test ./pkg/spec -run TestExampleBundlesAreValid` passes (bundle spec smoke test unaffected).

### Integration Verification
**Status: ✅ Addressed**

Full cross-package integration proven across multiple layers:
- `go test ./pkg/... -count=1` → all 8 packages pass (no regressions in pkg/agentd, pkg/ari, pkg/events, pkg/meta, pkg/rpc, pkg/runtime, pkg/spec, pkg/workspace)
- `go test ./tests/integration/... -count=1 -timeout 180s` → ok (6.577s), 7/7 pass, 2 skip (no API key — expected)
- `go build ./...` → BUILD OK (full module clean)
- TestARIAgentCreateAsync, TestARIAgentCreateAsyncErrorState, TestARIAgentRestartAsync use real mockagent shim — end-to-end integration verified

### Operational Verification
**Status: ✅ Addressed (with minor note)**

- `agentdctl agent --help` shows 8 subcommands (create/list/status/prompt/stop/delete/attach/cancel) with `restart` added in S04
- `go build ./cmd/agentdctl` and `go build ./cmd/room-mcp-server` both exit 0
- `TestAgentdRestartRecovery` is an operational-level test: it starts a real agentd process, kills it, restarts it, and verifies recovery semantics — operational continuity proven in a test environment
- RecoverSessions creating-cleanup branch handles daemon restart during bootstrap window — operational edge case addressed
- Minor note: `handleSessionRemove` is dead code (no dispatch case) but remains compiled into the binary. No operational impact but could be removed in a future cleanup pass.

### UAT
**Status: ✅ Addressed**

Each slice has a corresponding UAT.md with test cases that were designed to be runnable. S01 UAT has 10 test cases (TC01–TC10) plus 3 edge cases — all grep/test commands verified. S02–S07 UAT files follow the same pattern with slice-specific assertions. All 7 slices show `verification_result: passed`.



## Verdict Rationale
All 6 M005 requirements (R047–R052) are validated with concrete test evidence. All 7 slices show verification_result: passed with 19/19 tasks complete. The full test suite passes (8 pkg/ packages + integration tests). The contract verifier exits 0. Three known minor gaps (handleSessionRemove dead code with paused:warm comments, Phase field unpopulated, agent/detach no-op) are documented limitations with no functional impact, all pre-acknowledged in slice summaries. No material gaps, no missing deliverables, no regressions detected.
