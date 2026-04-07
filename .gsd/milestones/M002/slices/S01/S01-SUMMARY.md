---
id: S01
parent: M002
milestone: M002
provides:
  - One authority map for Room, Session, Runtime, Workspace, and shim recovery semantics across docs/design/*
  - A clean-break shim target contract using `session/*` + `runtime/*` instead of legacy PascalCase/`$/event` as normative API
  - Explicit host-impact boundary rules for local workspaces, hooks, env precedence, and shared workspace reuse
  - A mechanical proof surface for future design edits via the contract verifier script and example bundle validation test
requires: []
affects:
  - S02
  - S03
  - S04
key_files:
  - docs/design/contract-convergence.md
  - scripts/verify-m002-s01-contract.sh
  - docs/design/runtime/runtime-spec.md
  - docs/design/runtime/config-spec.md
  - docs/design/runtime/design.md
  - docs/design/orchestrator/room-spec.md
  - docs/design/agentd/agentd.md
  - docs/design/agentd/ari-spec.md
  - docs/design/workspace/workspace-spec.md
  - docs/design/runtime/shim-rpc-spec.md
  - docs/design/runtime/agent-shim.md
  - docs/design/README.md
  - pkg/spec/example_bundles_test.go
  - bin/bundles/claude-code/config.json
key_decisions:
  - Use a repo-root verifier script and checked-in example bundle validation as the proof surface for documentation convergence work.
  - Treat `session/new` as configuration-only bootstrap and route work entry through later `session/prompt`.
  - Treat `agentRoot.path` as the bundle input, resolved `cwd` as runtime-derived, and OAR `sessionId` as distinct from ACP `sessionId`.
  - Treat Room Spec as orchestrator-owned desired state and ARI `room/*` as agentd-owned realized runtime state.
  - Use a clean-break shim surface with `session/*` for turn control and `runtime/*` for process/replay control; keep socket/state-dir layout in runtime-spec and replay/reconnect semantics in shim-rpc-spec.
patterns_established:
  - Normative cross-doc ownership now lives in `docs/design/contract-convergence.md`; downstream docs are expected to defer rather than restate competing authority.
  - Normative shim method and notification names live only in `docs/design/runtime/shim-rpc-spec.md`; `agent-shim.md` is descriptive and may mention implementation lag without redefining the contract.
  - Documentation changes in this area must be closed with both the shell verifier and example bundle validation so prose drift and checked-in proof drift fail together.
observability_surfaces:
  - `bash scripts/verify-m002-s01-contract.sh`
  - `go test ./pkg/spec -run TestExampleBundlesAreValid -count=1`
drill_down_paths:
  - .gsd/milestones/M002/slices/S01/tasks/T01-SUMMARY.md
  - .gsd/milestones/M002/slices/S01/tasks/T02-SUMMARY.md
  - .gsd/milestones/M002/slices/S01/tasks/T03-SUMMARY.md
  - .gsd/milestones/M002/slices/S01/tasks/T04-SUMMARY.md
duration: ""
verification_result: passed
completed_at: 2026-04-07T11:31:19Z
---

# S01: Design contract convergence

**Converged the runtime design set onto one bootstrap-first, clean-break contract and left the slice with mechanical proof instead of prose-only trust.**

## What Happened

S01 closed the design contradictions that were making further runtime work unsafe.

T01 established the proof surface for the slice: a repo-root verifier script plus checked-in example bundle validation. That turned design convergence from a subjective doc review into something future edits can re-run mechanically.

T02 rewrote the runtime-facing docs around one bootstrap-first story. `session/new` is now configuration-only, `session/prompt` is the work-entry path, `agentRoot.path` is the bundle input that resolves to the runtime `cwd`, and OAR session identity is explicitly distinct from ACP `sessionId`. The remaining durable-state truth gaps were named directly as S03 follow-on work instead of being left as implied ambiguity.

T03 converged Room and workspace ownership semantics. The Room Spec now describes orchestrator-owned desired state while ARI `room/*` is the agentd-owned realized runtime projection. Workspace docs now state host-impact boundaries directly: local path attachment is unmanaged but validated, hooks are host command execution, env precedence is inherited host env → runtime-class env → `session/new` overrides, and shared workspace reuse means shared visibility and shared write risk.

T04 finished the clean-break shim story. `docs/design/runtime/shim-rpc-spec.md` now owns the normative `session/*` + `runtime/*` shim surface, `agent-shim.md` describes the component boundary without redefining protocol details, and `docs/design/README.md` points readers at the authority map and final spec owners. Legacy PascalCase / `$/event` references are now implementation-lag notes rather than part of the normative target contract.

Taken together, the slice moved the project from contradictory design prose to a single authority map that downstream implementation slices can build against without guessing which document wins.

## Verification

Slice-level verification passed at close:

- `bash scripts/verify-m002-s01-contract.sh` → exit 0 (`contract verification passed`)
- `go test ./pkg/spec -run TestExampleBundlesAreValid -count=1` → exit 0

Task-level evidence also covered the intermediate convergence steps:

- T02 grep proof showed the bootstrap/identity language landed in runtime-spec, config-spec, design.md, and contract-convergence.md.
- T03 grep proof showed the desired-vs-realized Room split and host-impact boundary rules landed across room-spec, agentd, ARI, workspace, and convergence docs.
- T04 reran the final contract verifier and bundle test after the shim-doc rewrite.

## Requirements Advanced

- R036 — S01 named the durable bootstrap, replay, reconnect, and restart-truth gaps precisely enough for S03 to implement them without reopening design authority debates.
- R044 — S01 explicitly separated convergence work from later restart, replay, cleanup, and cross-client hardening so the remaining hardening backlog stays intentional rather than accidental drift.

## Requirements Validated

- R032 — `docs/design/*` now define one non-conflicting contract for Room, Session, Runtime, Workspace, and shim recovery semantics; the final verifier and bundle proof both passed.
- R033 — `agentRoot.path`, resolved `cwd`, `session/new`, `systemPrompt`, and bootstrap behavior now have one authoritative meaning across the runtime docs.
- R038 — Local workspace attachment, hook execution, environment injection, and shared workspace access now have explicit host-impact boundary rules in the authoritative design set.

## New Requirements Surfaced

- none

## Requirements Invalidated or Re-scoped

- none

## Operational Readiness

- **Health signal**: `bash scripts/verify-m002-s01-contract.sh` passes and `go test ./pkg/spec -run TestExampleBundlesAreValid -count=1` passes after any design edit in the converged surfaces.
- **Failure signal**: The verifier fails when a doc reintroduces conflicting authority, missing headings, or legacy shim surface wording; the example bundle test fails when checked-in proof fixtures drift from spec reality.
- **Recovery**: Restore a single authority owner in the affected docs, keep implementation-lag notes descriptive only, then rerun both verification commands until green.
- **Monitoring gaps**: The proof surface only checks declared contract invariants and example bundle validity. It does not prove the runtime implementation already conforms to the new clean-break shim contract; S02 and S03 still have to land that truth in code.

## Deviations

Minor only. T03 had to restore exact section headings expected by the verifier after an initial wording change, but that stayed within the planned convergence work. No scope expansion beyond the written slice plan.

## Known Limitations

The runtime implementation still lags the newly converged shim contract. Legacy PascalCase / `$/event` behavior can still exist in code as implementation debt even though it is no longer normative in the docs.

Durable restart truth is still incomplete. S01 named the persistence and replay boundaries, but S03 still owns the actual storage, rebuild, reconnect, and fail-closed behavior.

The verifier is intentionally narrow. It proves cross-doc convergence and example-bundle validity, not end-to-end runtime compliance.

## Follow-ups

- S02 should implement the clean-break shim RPC surface (`session/*`, `runtime/*`, typed notifications) against the now-fixed authority split.
- S03 should persist and reconcile enough session/bootstrap identity to make restart and reconnect state truthful instead of descriptive.
- S04 should verify the converged contract against real CLI integrations so doc truth and runtime truth do not diverge again.

## Files Created/Modified

- `docs/design/contract-convergence.md` — cross-doc authority map and final convergence notes for the slice.
- `scripts/verify-m002-s01-contract.sh` — repo-root verifier for the converged design invariants.
- `pkg/spec/example_bundles_test.go` — checked-in example bundle proof surface.
- `bin/bundles/claude-code/config.json` — corrected example bundle fixture so proof uses truthful checked-in inputs.
- `docs/design/runtime/runtime-spec.md` — bootstrap-first runtime contract and identity/cwd semantics.
- `docs/design/runtime/config-spec.md` — aligned bundle configuration semantics for bootstrap and `systemPrompt`.
- `docs/design/runtime/design.md` — cross-layer state mapping and durable-state follow-on gaps.
- `docs/design/orchestrator/room-spec.md` — desired-state Room ownership model.
- `docs/design/agentd/agentd.md` — realized Room/runtime projection and bootstrap alignment.
- `docs/design/agentd/ari-spec.md` — aligned `room/*`, `session/new`, and `session/prompt` semantics.
- `docs/design/workspace/workspace-spec.md` — explicit local workspace, hook, env, and shared-workspace boundary rules.
- `docs/design/runtime/shim-rpc-spec.md` — authoritative clean-break shim protocol contract.
- `docs/design/runtime/agent-shim.md` — descriptive component boundary aligned to the authoritative shim spec.
- `docs/design/README.md` — design index updated to point readers to the final authorities.

## Forward Intelligence

### What the next slice should know
- The design debate is over for this surface. Treat `docs/design/contract-convergence.md` as the authority map, then implement to `runtime-spec.md` + `shim-rpc-spec.md` instead of reopening naming or ownership questions.
- `session/new` is configuration/bootstrap only. Any code path that treats it as hidden work entry is now contradicting the contract and should be changed rather than justified.
- Room ownership is intentionally split: orchestrator owns desired state, agentd owns realized runtime projection. That distinction should shape both API behavior and persistence design.

### What's fragile
- The convergence verifier depends on specific authority sections and wording boundaries — if a future edit moves those concepts without updating the script, the failure may be in the verifier or the docs, not necessarily the underlying design.
- The implementation-lag note in `docs/design/runtime/agent-shim.md` — it is allowed to describe current code drift, but it must never become a second normative protocol source.

### Authoritative diagnostics
- `scripts/verify-m002-s01-contract.sh` — fastest signal that cross-doc authority drift has reappeared.
- `go test ./pkg/spec -run TestExampleBundlesAreValid -count=1` — strongest checked-in proof that bundle examples still match the spec after doc or config edits.
- `docs/design/contract-convergence.md` — first place to inspect when two design docs appear to disagree.

### What assumptions changed
- The earlier assumption that shim docs could preserve legacy PascalCase / `$/event` naming as part of the current contract changed — the authoritative target is now a clean break to `session/*` + `runtime/*`.
- The earlier mixed Room ownership story changed — desired state belongs to the orchestrator docs, realized runtime state belongs to agentd/ARI.
- The earlier ambiguity around bootstrap changed — `systemPrompt` is configuration, not an implicit turn, and `session/prompt` is the work-entry path.
