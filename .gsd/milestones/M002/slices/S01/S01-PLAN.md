# S01: Design contract convergence

**Goal:** Eliminate the core design-contract conflicts before more runtime work lands on top of them.
**Demo:** After this: TBD

## Tasks
- [x] **T01: Add the contract verifier and bundle example proof** — Build the slice proof surface first: add the cross-doc authority map, add the repo-root verifier script, add checked-in bundle validation tests, and fix the broken `claude-code` bundle fixture so later slices start from truthful examples.
  - Estimate: 45m
  - Files: docs/design/contract-convergence.md, scripts/verify-m002-s01-contract.sh, pkg/spec/example_bundles_test.go, bin/bundles/claude-code/config.json
  - Verify: test -x scripts/verify-m002-s01-contract.sh && bash -n scripts/verify-m002-s01-contract.sh && go test ./pkg/spec -run TestExampleBundlesAreValid -count=1
- [x] **T02: Converged the runtime design docs on one bootstrap-first story and named the remaining durable-state gaps for S03.** — Rewrite the runtime-facing docs so `session/new` is configuration-only, `session/prompt` is the work-entry path, `agentRoot.path` resolves the canonical `cwd`, and OAR session identity is clearly separated from ACP `sessionId`.
  - Estimate: 1h
  - Files: docs/design/runtime/runtime-spec.md, docs/design/runtime/config-spec.md, docs/design/runtime/design.md, docs/design/contract-convergence.md
  - Verify: rg -n "resolved cwd|ACP sessionId|session/new|systemPrompt|State Mapping" docs/design/runtime/runtime-spec.md docs/design/runtime/config-spec.md docs/design/runtime/design.md docs/design/contract-convergence.md
- [x] **T03: Aligned Room ownership docs around a desired-vs-realized split and made workspace host-impact boundaries explicit.** — Resolve the desired-vs-realized Room split and make the host-impact boundaries explicit across orchestrator, agentd, ARI, and workspace docs, including local path attachment, hooks, env precedence, shared workspace reuse, and the intended ACP capability posture.
  - Estimate: 1h
  - Files: docs/design/orchestrator/room-spec.md, docs/design/agentd/agentd.md, docs/design/agentd/ari-spec.md, docs/design/workspace/workspace-spec.md, docs/design/contract-convergence.md
  - Verify: rg -n "Desired vs Realized|session/new|session/prompt|local workspace|hook|env|shared workspace|capability" docs/design/orchestrator/room-spec.md docs/design/agentd/agentd.md docs/design/agentd/ari-spec.md docs/design/workspace/workspace-spec.md docs/design/contract-convergence.md
- [x] **T04: Rewrite shim protocol docs to the clean-break target and close the loop** — Finish the slice by replacing the legacy PascalCase/`$/event` shim story with the clean-break `session/*` + `runtime/*` target, reconciling recovery/discovery wording, and leaving the final contract verifier green.
  - Estimate: 45m
  - Files: docs/design/runtime/shim-rpc-spec.md, docs/design/runtime/agent-shim.md, docs/design/README.md, docs/design/contract-convergence.md
  - Verify: bash scripts/verify-m002-s01-contract.sh && go test ./pkg/spec -run TestExampleBundlesAreValid -count=1
