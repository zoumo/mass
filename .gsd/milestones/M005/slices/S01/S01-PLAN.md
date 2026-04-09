# S01: Design Contract — Agent Model Convergence

**Goal:** All design docs consistently describe agent as the external object and session as internal runtime realization. No contradictions across the 7 authority documents.
**Demo:** After this: TBD

## Tasks
- [x] **T01: Rewrote agentd.md and ari-spec.md to agent-first model: agent/* replaces session/* in external ARI surface, Agent Manager added, async create with state polling documented, stop/delete separation and restart defined** — 
  - Files: docs/design/agentd/agentd.md, docs/design/agentd/ari-spec.md
  - Verify: grep -c 'agent/create\|agent/prompt\|agent/stop\|agent/delete\|agent/status\|agent/list' docs/design/agentd/ari-spec.md | xargs test 6 -le && ! grep -E '"method":\s*"session/(new|prompt|cancel|stop|remove|list|status)"' docs/design/agentd/ari-spec.md && grep -q 'Agent Manager' docs/design/agentd/agentd.md && grep -q 'agent/create' docs/design/agentd/agentd.md && echo 'T01 verify pass'
- [x] **T02: Added Turn-Aware Event Ordering section to shim-rpc-spec.md (turnId/streamSeq/phase fields, replay semantics) and M005 stability statement to agent-shim.md** — 
  - Files: docs/design/runtime/shim-rpc-spec.md, docs/design/runtime/agent-shim.md
  - Verify: grep -q 'turnId' docs/design/runtime/shim-rpc-spec.md && grep -q 'streamSeq' docs/design/runtime/shim-rpc-spec.md && grep -q 'phase' docs/design/runtime/shim-rpc-spec.md && grep -qi 'M005' docs/design/runtime/agent-shim.md && echo 'T02 verify pass'
- [x] **T03: Updated room-spec, contract-convergence, and README to agent-first model completing design contract convergence across all 7 authority docs** — 
  - Files: docs/design/orchestrator/room-spec.md, docs/design/contract-convergence.md, docs/design/README.md
  - Verify: grep -q 'agent/create' docs/design/orchestrator/room-spec.md && ! grep -q 'sessionId' docs/design/orchestrator/room-spec.md && grep -q 'Agent Model Convergence' docs/design/contract-convergence.md && grep -qi 'Agent.*external\|external.*Agent' docs/design/README.md && echo 'T03 verify pass'
- [x] **T04: Wrote scripts/verify-m005-s01-contract.sh with 5 positive heading checks and 5 negative pattern checks; all pass and bundle spec smoke test exits 0** — 
  - Files: scripts/verify-m005-s01-contract.sh
  - Verify: bash scripts/verify-m005-s01-contract.sh && go test ./pkg/spec -run TestExampleBundlesAreValid -count=1 && echo 'T04 verify pass'
