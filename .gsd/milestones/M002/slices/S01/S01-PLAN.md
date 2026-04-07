# S01: Design contract convergence

**Goal:** Eliminate the core design-contract conflicts before more runtime work lands on top of them.
**Demo:** After this: the design docs and runtime contract can be read as one coherent story for Room, Session, Runtime, Workspace, bootstrap, security boundaries, and state mapping.

## Must-Haves

- One authoritative bootstrap contract across `agentRoot.path`, resolved `cwd`, `session/new`, `systemPrompt`, and later `session/prompt` work entry.
- One non-conflicting desired-vs-realized Room ownership story across orchestrator, agentd, ARI, and shared workspace semantics.
- One explicit authority map for OAR session identity, ACP `sessionId`, runtime/session/process state mapping, and the persistence gaps that remain for S03.
- One explicit boundary contract for local workspace attachment, hook execution, env injection precedence, shared workspace access, and the intended ACP capability posture.
- Mechanical proof that checked-in bundle examples parse and validate, including the fixed `bin/bundles/claude-code/config.json` fixture.

## Threat Surface

- **Abuse**: contradictory docs can normalize unsafe assumptions about host command execution, local-path attachment, attach/recovery authority, or ACP capability exposure.
- **Data exposure**: local workspaces, injected environment variables, and shared-room workspaces can expose secrets or project data if the contract blurs ownership and trust boundaries.
- **Input trust**: workspace specs, room/session metadata, bundle config, env entries, hook commands, and prompt input all cross trust boundaries and must be described as untrusted until validated.

## Requirement Impact

- **Requirements touched**: R032, R033, R036, R038, R044
- **Re-verify**: final `docs/design/*` coherence, bundle example config validity, and the contract assumptions S02/S03 will implement against.
- **Decisions revisited**: D008, D015, D016

## Proof Level

- This slice proves: contract
- Real runtime required: no
- Human/UAT required: no

## Verification

- `bash scripts/verify-m002-s01-contract.sh`
- `go test ./pkg/spec -run TestExampleBundlesAreValid -count=1`

## Observability / Diagnostics

- Runtime signals: `scripts/verify-m002-s01-contract.sh` exits non-zero on contradictory phrases or missing sections; `TestExampleBundlesAreValid` fails on broken checked-in bundle configs.
- Inspection surfaces: repo-root shell script output, `go test` output from `pkg/spec/example_bundles_test.go`, and `docs/design/contract-convergence.md` as the cross-doc authority map.
- Failure visibility: exact file/pattern drift, missing contract section, or invalid bundle path/config field is surfaced directly in the failing command output.
- Redaction constraints: verification must not print secret env values; it should assert on schema/keys and documented boundaries only.

## Integration Closure

- Upstream surfaces consumed: `docs/design/README.md`, `docs/design/orchestrator/room-spec.md`, `docs/design/agentd/agentd.md`, `docs/design/agentd/ari-spec.md`, `docs/design/runtime/runtime-spec.md`, `docs/design/runtime/config-spec.md`, `docs/design/runtime/design.md`, `docs/design/runtime/shim-rpc-spec.md`, `docs/design/runtime/agent-shim.md`, `docs/design/workspace/workspace-spec.md`, `pkg/spec/config.go`, `pkg/spec/config_test.go`, `bin/bundles/claude-code/config.json`
- New wiring introduced in this slice: `docs/design/contract-convergence.md` becomes the authority map, `scripts/verify-m002-s01-contract.sh` becomes the mechanical coherence gate, and `pkg/spec/example_bundles_test.go` validates checked-in bundle configs.
- What remains before the milestone is truly usable end-to-end: S02 must implement the clean-break shim/runtime protocol, S03 must make persistence/recovery/security truth match the contract, and S04 must prove the result with real `gsd-pi` and `claude-code` flows.

## Tasks

- [ ] **T01: Add the contract verifier and bundle example proof** `est:45m`
  - Why: This slice needs an objective proof surface, and the checked-in `claude-code` bundle typo should stop blocking later real-client work now.
  - Files: `docs/design/contract-convergence.md`, `scripts/verify-m002-s01-contract.sh`, `pkg/spec/example_bundles_test.go`, `bin/bundles/claude-code/config.json`
  - Do: Add a concise contract authority map with invariant sections for bootstrap/state/security/shim targets; add a repo-root verifier script that checks required sections and flags legacy contradictory phrases; add a Go test that loads each checked-in bundle example through `spec.ParseConfig` and `spec.ValidateConfig`; fix the `oaiVersion` typo in the `claude-code` bundle fixture.
  - Verify: `test -x scripts/verify-m002-s01-contract.sh && bash -n scripts/verify-m002-s01-contract.sh && go test ./pkg/spec -run TestExampleBundlesAreValid -count=1`
  - Done when: the authority map exists, the verifier is executable, the bundle example test passes, and the `claude-code` bundle validates.
- [ ] **T02: Converge runtime bootstrap, identity, and state mapping docs** `est:1h`
  - Why: R033 stays unsafe until the runtime docs describe one startup sequence, one `cwd` story, and one identity/state mapping.
  - Files: `docs/design/runtime/runtime-spec.md`, `docs/design/runtime/config-spec.md`, `docs/design/runtime/design.md`, `docs/design/contract-convergence.md`
  - Do: Rewrite the runtime-facing docs so `session/new` is configuration-only, `systemPrompt` is session configuration rather than task input, `agentRoot.path` resolves to the canonical `cwd`, and OAR session IDs are explicitly separated from ACP `sessionId`; add the runtime/session/process mapping table and the durable-field gap note S03 will need.
  - Verify: `rg -n "resolved cwd|ACP sessionId|session/new|systemPrompt|State Mapping" docs/design/runtime/runtime-spec.md docs/design/runtime/config-spec.md docs/design/runtime/design.md docs/design/contract-convergence.md`
  - Done when: the runtime docs read as one bootstrap/identity/state story and the authority map names the remaining persistence gap instead of hiding it.
- [ ] **T03: Align Room ownership and security-boundary docs** `est:1h`
  - Why: R032 and R038 both depend on getting the desired-vs-realized Room split and the host-impact boundaries into one non-conflicting story.
  - Files: `docs/design/orchestrator/room-spec.md`, `docs/design/agentd/agentd.md`, `docs/design/agentd/ari-spec.md`, `docs/design/workspace/workspace-spec.md`, `docs/design/contract-convergence.md`
  - Do: Rewrite the orchestrator, agentd, ARI, and workspace docs so Room intent stays orchestrator-owned while realized runtime room state is agentd-owned; make `session/new` configuration-only and `session/prompt` the work-entry path; and spell out local path canonicalization, hook execution as host commands, env precedence, shared workspace implications, and the intended ACP capability/security posture.
  - Verify: `rg -n "Desired vs Realized|session/new|session/prompt|local workspace|hook|env|shared workspace|capability" docs/design/orchestrator/room-spec.md docs/design/agentd/agentd.md docs/design/agentd/ari-spec.md docs/design/workspace/workspace-spec.md docs/design/contract-convergence.md`
  - Done when: Room ownership, shared workspace semantics, and security boundaries read consistently across the design set and the authority map captures the follow-on gaps for S03.
- [ ] **T04: Rewrite shim protocol docs to the clean-break target and close the loop** `est:45m`
  - Why: S02 needs one normative shim story, and S01 is not done until the final coherence gate passes against that story.
  - Files: `docs/design/runtime/shim-rpc-spec.md`, `docs/design/runtime/agent-shim.md`, `docs/design/README.md`, `docs/design/contract-convergence.md`
  - Do: Rewrite the shim docs around the clean-break `session/*` + `runtime/*` surface, remove legacy PascalCase/`$/event` as normative behavior, document the intended recovery/discovery/socket story and any explicit implementation lag notes, then refresh the design index and authority map so the whole doc set points at the same target contract.
  - Verify: `bash scripts/verify-m002-s01-contract.sh && go test ./pkg/spec -run TestExampleBundlesAreValid -count=1`
  - Done when: the verifier passes, the bundle example test still passes, and the normative docs no longer preserve the legacy shim surface as if it were current contract.

## Files Likely Touched

- `docs/design/README.md`
- `docs/design/contract-convergence.md`
- `docs/design/orchestrator/room-spec.md`
- `docs/design/agentd/agentd.md`
- `docs/design/agentd/ari-spec.md`
- `docs/design/runtime/runtime-spec.md`
- `docs/design/runtime/config-spec.md`
- `docs/design/runtime/design.md`
- `docs/design/runtime/shim-rpc-spec.md`
- `docs/design/runtime/agent-shim.md`
- `docs/design/workspace/workspace-spec.md`
- `scripts/verify-m002-s01-contract.sh`
- `pkg/spec/example_bundles_test.go`
- `bin/bundles/claude-code/config.json`
