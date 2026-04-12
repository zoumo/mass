---
estimated_steps: 3
estimated_files: 2
skills_used: []
---

# T05: docs — runtime-spec + shim-rpc-spec updates

Update design docs:
1. runtime-spec.md: add 5 rows to event type table + payload preservation policy note
2. shim-rpc-spec.md: add 5 rows to Typed Event table + update tool_call and tool_result payload field descriptions

## Inputs

- `docs/design/runtime/runtime-spec.md`
- `docs/design/runtime/shim-rpc-spec.md`

## Expected Output

- `runtime-spec.md with 5 new event rows + payload policy`
- `shim-rpc-spec.md with 5 new rows + updated tool_call/tool_result payloads`

## Verification

grep available_commands docs/design/runtime/runtime-spec.md && grep available_commands docs/design/runtime/shim-rpc-spec.md
