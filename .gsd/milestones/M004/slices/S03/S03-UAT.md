# S03: End-to-End Multi-Agent Integration Proof — UAT

**Milestone:** M004
**Written:** 2026-04-08T06:30:12.527Z

## UAT: End-to-End Multi-Agent Integration Proof

### Preconditions
- `mockagent` binary built at `./bin/mockagent`
- `agent-shim` binary built at `./bin/agent-shim`
- Go test environment functional

---

### Test Case 1: Full 3-Agent Round-Trip Lifecycle

**Steps:**
1. Run `go test ./pkg/ari/ -count=1 -v -run TestARIMultiAgentRoundTrip -timeout 120s`
2. Observe Room creation with mode "mesh"
3. Observe 3 sessions created (agent-a, agent-b, agent-c), all in "created" state per roomStatus
4. Observe A→B message delivery: `Delivered==true`, agent-b auto-starts to "running"
5. Observe B→A reply delivery: `Delivered==true`, agent-a auto-starts to "running" (bidirectional proof)
6. Observe A→C delivery: `Delivered==true`, agent-c auto-starts, all 3 agents now "running"
7. Observe all 3 sessions stopped, room deleted, post-delete roomStatus returns error

**Expected:** Test PASS. All message deliveries succeed. State transitions verified at each step. Clean teardown with no orphaned resources.

---

### Test Case 2: Teardown Ordering Guards

**Steps:**
1. Run `go test ./pkg/ari/ -count=1 -v -run TestARIRoomTeardownGuards -timeout 120s`
2. Observe Room creation and 2-agent bootstrap
3. Observe A→B delivery auto-starts agent-b to "running"
4. Attempt `room/delete` while agent-b is running — expect CodeInvalidParams error containing "active member"
5. Attempt `session/remove` on running agent-b — expect CodeInvalidParams error (ErrDeleteProtected)
6. Stop all sessions, then `room/delete` — expect success

**Expected:** Test PASS. Error paths return correct error codes and messages. Deletion succeeds only after proper stop sequence.

---

### Test Case 3: Full Suite Regression Check

**Steps:**
1. Run `go test ./pkg/ari/ -count=1 -short -timeout 120s`

**Expected:** All 47 tests PASS. No regressions in existing room lifecycle (S01), routing (S02), session management, or workspace operations.

---

### Edge Cases Verified
- **Bidirectional routing:** A→B followed by B→A (not just unidirectional)
- **3-agent participation:** Not just pairwise — a third independent agent receives messages
- **Auto-start on delivery:** Sessions in "created" state auto-start when targeted by roomSend
- **Delete-with-active-members guard:** room/delete blocked while any session is non-stopped
- **Remove-running-session guard:** session/remove blocked while session is running
- **Created-to-stopped transition:** session/stop on a "created" (never-started) session transitions cleanly to "stopped"
- **Post-delete verification:** room/status after room/delete returns error (room not found)
