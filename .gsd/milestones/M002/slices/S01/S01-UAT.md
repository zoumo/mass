# S01: Design contract convergence — UAT

**Milestone:** M002
**Written:** 2026-04-07T11:40:08.620Z

## UAT Type

- UAT mode: artifact-driven
- Why this mode is sufficient: S01 ships a converged documentation contract plus mechanical proof surfaces, not a new runtime binary path. The right acceptance test is to inspect the authoritative docs and rerun the verifier/test surfaces that guard against drift.

## Preconditions

- Repository is at `/Users/jim/code/zoumo/open-agent-runtime`.
- Go toolchain is installed.
- The tester is using the checked-in docs and bundle fixtures from this slice, not an older worktree copy.
- No local edits are pending in the design docs under `docs/design/*`, `scripts/verify-m002-s01-contract.sh`, `pkg/spec/example_bundles_test.go`, or `bin/bundles/claude-code/config.json`.

## Smoke Test

Run the two slice proof commands:

1. `bash scripts/verify-m002-s01-contract.sh`
2. `go test ./pkg/spec -run TestExampleBundlesAreValid -count=1`

Expected: both commands exit 0. If either fails, the slice is not accepted.

## Test Cases

### 1. Bootstrap and identity contract is singular

1. Run:
   `rg -n "resolved cwd|ACP sessionId|session/new|systemPrompt|State Mapping" docs/design/runtime/runtime-spec.md docs/design/runtime/config-spec.md docs/design/runtime/design.md docs/design/contract-convergence.md`
2. Inspect the matched sections.
3. **Expected:** The docs consistently show `session/new` as configuration-only bootstrap, `session/prompt` as later work entry, `agentRoot.path` as the bundle input that resolves the runtime `cwd`, and OAR `sessionId` as distinct from ACP `sessionId`.

### 2. Room ownership and workspace host-impact boundaries are explicit

1. Run:
   `rg -n "Desired vs Realized|session/new|session/prompt|local workspace|hook|env|shared workspace|capability" docs/design/orchestrator/room-spec.md docs/design/agentd/agentd.md docs/design/agentd/ari-spec.md docs/design/workspace/workspace-spec.md docs/design/contract-convergence.md`
2. Inspect the matched sections.
3. **Expected:** Room Spec describes orchestrator-owned desired state, ARI/agentd describe realized runtime state, and the docs explicitly call out local workspace attachment rules, hook execution as host impact, env precedence, shared workspace reuse risk, and capability posture.

### 3. Shim contract points to the clean-break target only

1. Open `docs/design/runtime/shim-rpc-spec.md`, `docs/design/runtime/agent-shim.md`, `docs/design/README.md`, and `docs/design/contract-convergence.md`.
2. Verify the shim surface is described as `session/*` + `runtime/*`, with `session/update` and `runtime/stateChange` as the notification surface.
3. Verify any mention of legacy PascalCase or `$/event` is clearly labeled as implementation lag or historical context, not the current normative contract.
4. **Expected:** `shim-rpc-spec.md` is the only normative owner of shim method and notification names; `agent-shim.md` is descriptive; README points readers to the authority map and the final spec owners.

### 4. Mechanical contract verifier stays green

1. Run: `bash scripts/verify-m002-s01-contract.sh`
2. Confirm output contains `contract verification passed`.
3. **Expected:** Exit code 0. Any failure means cross-doc authority drift has been reintroduced.

### 5. Example bundles still satisfy the spec after doc convergence

1. Run: `go test ./pkg/spec -run TestExampleBundlesAreValid -count=1`
2. Confirm the test package reports `ok`.
3. **Expected:** Exit code 0. This proves the checked-in bundle examples, including `bin/bundles/claude-code/config.json`, still validate after the convergence work.

## Edge Cases

### Legacy implementation references remain descriptive only

1. Search:
   `rg -n "PascalCase|\$/event|implementation lag|legacy" docs/design/runtime/shim-rpc-spec.md docs/design/runtime/agent-shim.md docs/design/contract-convergence.md`
2. Review any matches.
3. **Expected:** Legacy naming appears only as non-normative implementation-lag or historical context, never as a parallel current contract.

### Design index does not point readers at the wrong authority

1. Open `docs/design/README.md`.
2. Check the runtime/shim entries in the index and matrix.
3. **Expected:** The README points readers first to `docs/design/contract-convergence.md` for authority mapping and to `runtime-spec.md` + `shim-rpc-spec.md` for the final bootstrap/shim contract, rather than implying `agent-shim.md` owns protocol truth.

## Failure Signals

- `bash scripts/verify-m002-s01-contract.sh` exits non-zero or stops printing `contract verification passed`.
- `go test ./pkg/spec -run TestExampleBundlesAreValid -count=1` exits non-zero.
- `session/new` is described as a work entry point in one doc and bootstrap-only in another.
- Room ownership is described as agentd-owned orchestration intent anywhere in the authoritative docs.
- Legacy PascalCase / `$/event` shim naming is presented as current normative API instead of implementation lag.
- Workspace boundary rules for local path attachment, hooks, env precedence, or shared reuse are absent or contradictory.

## Requirements Proved By This UAT

- R032 — The authoritative design docs now define one non-conflicting contract for Room, Session, Runtime, Workspace, and shim recovery semantics.
- R033 — `agentRoot.path`, resolved `cwd`, `session/new`, `systemPrompt`, and bootstrap behavior now have one authoritative meaning.
- R038 — Local workspace, hook, env, and shared-workspace host-impact boundaries are now explicit in the design contract.

## Not Proven By This UAT

- This UAT does not prove the runtime implementation has already adopted the new clean-break shim RPC surface.
- This UAT does not prove durable restart/reconnect truth, replay reconstruction, or fail-closed recovery behavior; those remain follow-on work for S03 and later slices.

## Notes for Tester

- This is a design-contract slice, so artifact inspection plus verifier/test reruns are the correct acceptance path.
- Implementation-lag notes are allowed in `docs/design/runtime/agent-shim.md`; they only become a failure if they start redefining the normative contract.
- If the verifier fails after an innocent wording edit, inspect `docs/design/contract-convergence.md` first, then decide whether the docs or the verifier script needs to be updated to preserve a single authority map.
