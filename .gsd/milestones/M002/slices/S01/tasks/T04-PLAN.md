---
estimated_steps: 4
estimated_files: 4
skills_used: []
---

# T04: Rewrite shim protocol docs to the clean-break target and close the loop

**Slice:** S01 — Design contract convergence
**Milestone:** M002

## Description

Finish the slice by making the shim boundary tell the same story as the rest of the design set. This task should replace the legacy PascalCase/`$/event` surface in the normative docs with the clean-break `session/*` + `runtime/*` target, reconcile the recovery/discovery story, and leave the final verifier green.

## Steps

1. Rewrite `docs/design/runtime/shim-rpc-spec.md` to describe the clean-break shim surface, event model, and recovery expectations that S02 will implement.
2. Update `docs/design/runtime/agent-shim.md` so the component description matches the rewritten protocol surface, the intended authority boundary, and the recovery/discovery story.
3. Refresh `docs/design/README.md` and `docs/design/contract-convergence.md` so the design index points readers at the final authoritative docs rather than preserving the legacy shim story.
4. Run the slice verifier and bundle example test, then tighten any remaining wording until both pass.

## Must-Haves

- [ ] The normative shim docs describe the clean-break `session/*` + `runtime/*` target rather than the legacy PascalCase/`$/event` surface.
- [ ] Recovery/discovery/socket semantics are described once, with any remaining implementation lag called out explicitly instead of mixed into the normative contract.
- [ ] The final slice verification commands both pass.

## Verification

- `bash scripts/verify-m002-s01-contract.sh`
- `go test ./pkg/spec -run TestExampleBundlesAreValid -count=1`

## Inputs

- `docs/design/contract-convergence.md` — accumulated authority notes from T01–T03
- `docs/design/runtime/shim-rpc-spec.md` — legacy shim contract to replace
- `docs/design/runtime/agent-shim.md` — shim component narrative to align with the clean-break contract
- `docs/design/README.md` — design index that must point to the final authority structure

## Expected Output

- `docs/design/runtime/shim-rpc-spec.md` — converged clean-break shim protocol spec
- `docs/design/runtime/agent-shim.md` — component doc aligned with the clean-break protocol and recovery story
- `docs/design/README.md` — updated design index reflecting the final authority map
- `docs/design/contract-convergence.md` — finalized cross-doc authority notes and invariants
