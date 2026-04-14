---
estimated_steps: 16
estimated_files: 2
skills_used: []
---

# T02: Update design docs to reflect M014 enriched state schema

## Description

M014 added `session`, `eventCounts`, and `updatedAt` to state.json and `sessionChanged` to state_change events. The design docs still show the pre-M014 schema. This task updates the normative examples to match the current implementation.

## Steps

1. **Update `docs/design/runtime/shim-rpc-spec.md`:**
   a. Find the `runtime/status` Response JSON example (around line 183). Add `updatedAt`, `session` (with a minimal example showing `agentInfo` and `capabilities`), and `eventCounts` to the `state` object. Add a prose note after the example explaining that `eventCounts` in the `runtime/status` response is overlaid from Translator memory for real-time accuracy, and may differ from the value persisted in state.json.
   b. Find the `state_change` content JSON example (around line 397). Add `"sessionChanged": ["configOptions"]` to demonstrate a metadata-only state_change. Add a prose note that `sessionChanged` is present on metadata-only changes where `previousStatus == status`, listing the possible values: agentInfo, capabilities, availableCommands, configOptions, sessionInfo, currentMode.

2. **Update `docs/design/runtime/runtime-spec.md`:**
   a. Find the State Example JSON (around line 57). Add `updatedAt`, `session` (with a minimal example showing `agentInfo`), and `eventCounts` fields. Keep the example simple — show that these fields exist; full type definitions are in the Go source.
   b. After the `The state MAY include additional properties.` line, add a brief description of the new fields: `updatedAt` (RFC3339Nano timestamp of last state write), `session` (ACP session metadata populated progressively), `eventCounts` (cumulative per-type event counts, derived field).

3. **Do NOT update `docs/design/runtime/agent-shim.md`** — per K029, it is descriptive only and defers to shim-rpc-spec.md and runtime-spec.md for protocol details.

4. Verify: `grep -q 'eventCounts' docs/design/runtime/shim-rpc-spec.md && grep -q 'eventCounts' docs/design/runtime/runtime-spec.md && grep -q 'sessionChanged' docs/design/runtime/shim-rpc-spec.md`

## Key constraints
- Per K029, shim-rpc-spec.md is the authority for method/notification semantics. runtime-spec.md is the authority for state dir layout and state shape. agent-shim.md is descriptive only — do NOT add protocol details there.
- Per K059, when docs explain removed concepts, use affirmative phrasing to avoid tripping grep gates.
- Keep examples minimal — show that the fields exist, don't reproduce the full Go type hierarchy in JSON.
- Write prose in the same language as the surrounding text (Chinese for shim-rpc-spec.md, English for runtime-spec.md).

## Inputs

- `docs/design/runtime/shim-rpc-spec.md`
- `docs/design/runtime/runtime-spec.md`
- `pkg/runtime-spec/api/state.go`
- `pkg/shim/api/event_types.go`

## Expected Output

- `docs/design/runtime/shim-rpc-spec.md`
- `docs/design/runtime/runtime-spec.md`

## Verification

grep -q 'eventCounts' docs/design/runtime/shim-rpc-spec.md && grep -q 'eventCounts' docs/design/runtime/runtime-spec.md && grep -q 'sessionChanged' docs/design/runtime/shim-rpc-spec.md && grep -q 'updatedAt' docs/design/runtime/runtime-spec.md
