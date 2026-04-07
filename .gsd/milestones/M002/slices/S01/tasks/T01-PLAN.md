---
estimated_steps: 4
estimated_files: 4
skills_used:
  - test
---

# T01: Add the contract verifier and bundle example proof

**Slice:** S01 — Design contract convergence
**Milestone:** M002

## Description

Create the proof surface for this slice before the larger doc rewrite starts. The task should leave two durable assets behind: a concise authority-map document that the rest of the slice can refine, and executable checks that catch both contract drift and broken checked-in bundle examples.

## Steps

1. Create `docs/design/contract-convergence.md` with explicit sections for authority map, bootstrap contract, state mapping, security boundaries, and shim target contract.
2. Add `scripts/verify-m002-s01-contract.sh` so it checks for required sections and flags the known stale phrases this slice is supposed to remove from normative docs.
3. Add `pkg/spec/example_bundles_test.go` that walks the checked-in bundle examples, parses each `config.json`, and validates it through `spec.ParseConfig` and `spec.ValidateConfig`.
4. Fix `bin/bundles/claude-code/config.json` so the example bundle passes the new validation test.

## Must-Haves

- [ ] `docs/design/contract-convergence.md` exists with the section structure later tasks will fill in.
- [ ] `scripts/verify-m002-s01-contract.sh` is executable and fails on missing sections or legacy contradictory phrases.
- [ ] `pkg/spec/example_bundles_test.go` proves the checked-in bundle examples are parseable and spec-valid.
- [ ] `bin/bundles/claude-code/config.json` uses `oarVersion` and passes validation.

## Verification

- `test -x scripts/verify-m002-s01-contract.sh`
- `bash -n scripts/verify-m002-s01-contract.sh`
- `go test ./pkg/spec -run TestExampleBundlesAreValid -count=1`

## Observability Impact

- Signals added/changed: `scripts/verify-m002-s01-contract.sh` exit status and `TestExampleBundlesAreValid` test failures become the slice’s durable drift signals.
- How a future agent inspects this: run the verifier script from repo root, then run the targeted `go test` command for bundle examples.
- Failure state exposed: the failing file path, missing contract section, or invalid config field is surfaced directly by the command output.

## Inputs

- `pkg/spec/config.go` — bundle parsing and validation entrypoints the new test should exercise
- `pkg/spec/config_test.go` — existing test style and suite conventions to follow
- `bin/bundles/gsd-pi/config.json` — known-good example bundle to keep valid
- `bin/bundles/claude-code/config.json` — broken example bundle that needs the `oarVersion` fix

## Expected Output

- `docs/design/contract-convergence.md` — initial authority map and invariant headings for the slice
- `scripts/verify-m002-s01-contract.sh` — executable contract-drift verifier
- `pkg/spec/example_bundles_test.go` — targeted bundle example validation test
- `bin/bundles/claude-code/config.json` — fixed example bundle config
