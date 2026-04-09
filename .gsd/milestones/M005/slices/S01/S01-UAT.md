# S01: Design Contract — Agent Model Convergence — UAT

**Milestone:** M005
**Written:** 2026-04-08T15:50:58.139Z

## Preconditions

- Working directory: `/Users/jim/code/zoumo/open-agent-runtime`
- All 4 task summaries show `verification_result: passed`
- `bash scripts/verify-m005-s01-contract.sh` exits 0
- `go test ./pkg/spec -run TestExampleBundlesAreValid -count=1` exits 0

---

## Test Cases

### TC01 — Verification script exits 0 (primary gate)

**What it proves:** All 7 authority documents are contradiction-free.

```bash
bash scripts/verify-m005-s01-contract.sh
echo "Exit: $?"
```

**Expected:** Output `M005/S01 contract verification passed`, exit code 0.

---

### TC02 — ari-spec.md uses agent/* exclusively in normative JSON examples

**What it proves:** The external ARI surface is fully renamed; no stale session/* method strings remain in normative examples.

```bash
# Must find at least 6 agent/* methods
grep -c 'agent/create\|agent/prompt\|agent/stop\|agent/delete\|agent/status\|agent/list' docs/design/agentd/ari-spec.md
# Must find 0 session/* method strings in JSON blocks
grep -E '"method":\s*"session/(new|prompt|cancel|stop|remove|list|status)"' docs/design/agentd/ari-spec.md && echo "FAIL: stale session/* found" || echo "PASS: no stale session/* methods"
```

**Expected:** count ≥ 6; no output from second grep; exit 0 for both.

---

### TC03 — agentd.md documents Agent Manager and async agent/create

**What it proves:** The two subsystem split (Agent Manager external / Session Manager internal) and async create semantics are in the authoritative daemon design.

```bash
grep -q 'Agent Manager' docs/design/agentd/agentd.md && echo "PASS: Agent Manager present" || echo "FAIL"
grep -q 'agent/create' docs/design/agentd/agentd.md && echo "PASS: agent/create present" || echo "FAIL"
grep -q 'creating' docs/design/agentd/agentd.md && echo "PASS: creating state present" || echo "FAIL"
```

**Expected:** All three lines print PASS.

---

### TC04 — shim-rpc-spec.md documents turn-aware event ordering fields

**What it proves:** The shim event envelope spec contains the S05 implementation targets.

```bash
grep -q 'turnId' docs/design/runtime/shim-rpc-spec.md && echo "PASS: turnId" || echo "FAIL"
grep -q 'streamSeq' docs/design/runtime/shim-rpc-spec.md && echo "PASS: streamSeq" || echo "FAIL"
grep -q 'phase' docs/design/runtime/shim-rpc-spec.md && echo "PASS: phase" || echo "FAIL"
# Shim RPC methods must remain unchanged
grep -c '"method": "session/' docs/design/runtime/shim-rpc-spec.md
```

**Expected:** All three PASS; session/ count ≥ 1 (shim methods intact).

---

### TC05 — agent-shim.md carries the M005 stability statement

**What it proves:** The shim stability posture is explicitly documented, not implied.

```bash
grep -qi 'M005' docs/design/runtime/agent-shim.md && echo "PASS: M005 stability statement present" || echo "FAIL"
```

**Expected:** PASS.

---

### TC06 — room-spec.md uses agent/create flow; no sessionId in members

**What it proves:** Room projection lifecycle is updated; old sessionId member field is gone.

```bash
grep -q 'agent/create' docs/design/orchestrator/room-spec.md && echo "PASS: agent/create in projection" || echo "FAIL"
grep -q 'sessionId' docs/design/orchestrator/room-spec.md && echo "FAIL: stale sessionId found" || echo "PASS: no sessionId"
```

**Expected:** First PASS; second PASS (no sessionId).

---

### TC07 — contract-convergence.md has Agent Model Convergence section

**What it proves:** The M005 invariants are captured in the cross-doc authority map.

```bash
grep -q 'Agent Model Convergence' docs/design/contract-convergence.md && echo "PASS" || echo "FAIL"
```

**Expected:** PASS.

---

### TC08 — README.md distinguishes Agent (external) from Session (internal)

**What it proves:** Entry-point doc gives correct mental model to new readers.

```bash
grep -qi 'Agent.*external\|external.*Agent' docs/design/README.md && echo "PASS: agent/session distinction present" || echo "FAIL"
```

**Expected:** PASS.

---

### TC09 — No paused:warm or paused:cold in any authority document

**What it proves:** The retired checkpoint states are fully removed from the external model.

```bash
grep -r 'paused:warm\|paused:cold' docs/design/agentd/ docs/design/contract-convergence.md && echo "FAIL: paused states found" || echo "PASS: paused states absent"
```

**Expected:** PASS (no output from grep).

---

### TC10 — Bundle spec smoke test still passes

**What it proves:** Design-doc changes did not break any checked-in bundle examples.

```bash
go test ./pkg/spec -run TestExampleBundlesAreValid -count=1
```

**Expected:** `ok github.com/open-agent-d/open-agent-d/pkg/spec` with passing status.

---

## Edge Cases

**EC01 — Script tolerates future agents.** Running `bash scripts/verify-m005-s01-contract.sh` after adding a new document (e.g., agentd-v2.md) should still pass — the script only checks the 7 authority files listed in S01 scope.

**EC02 — Shim-internal prose references don't trigger forbidden patterns.** Text like "the shim continues to use session/prompt internally" in agentd.md does NOT appear as `"method": "session/prompt"` in a JSON block, so the forbidden-pattern check correctly ignores it.

**EC03 — Partial revert detection.** If someone reverts agentd.md to use 'Session Manager' as the external subsystem name, `grep -q 'Agent Manager'` fails and the verifier exits non-zero with a clear error message.

