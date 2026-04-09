---
estimated_steps: 16
estimated_files: 1
skills_used: []
---

# T04: Write verification script and run smoke tests

Write scripts/verify-m005-s01-contract.sh modeled on scripts/verify-m002-s01-contract.sh. Run it to confirm zero contradictions. Run go test ./pkg/spec to confirm bundle specs remain valid.

The script must use the same require_heading / forbid_pattern helper pattern from the M002 script.

Positive checks (require_heading):
- contract-convergence.md: 'Agent Model Convergence'
- agentd.md: agent identity / Agent Manager headings
- ari-spec.md: agent/create, agent/prompt method headings
- shim-rpc-spec.md: turnId or turn ordering heading
- agent-shim.md: M005 stability heading or mention

Negative checks (forbid_pattern):
- ari-spec.md: no '"method": "session/new"', '"method": "session/prompt"', '"method": "session/remove"' etc. in normative examples
- agentd.md: no session/new or session/prompt as external API language (grep carefully — shim-internal refs are OK, so target normative sections)
- room-spec.md: no sessionId in room/status examples, no session/new in projection steps
- ari-spec.md: no paused:warm or paused:cold

Bundle spec smoke test:
- go test ./pkg/spec -run TestExampleBundlesAreValid -count=1

The script must exit 0 on success, non-zero on any failure, and print clear error messages for each failure.

## Inputs

- ``scripts/verify-m002-s01-contract.sh` — pattern to follow for verification script structure`
- ``docs/design/agentd/agentd.md` — T01 output to verify`
- ``docs/design/agentd/ari-spec.md` — T01 output to verify`
- ``docs/design/runtime/shim-rpc-spec.md` — T02 output to verify`
- ``docs/design/runtime/agent-shim.md` — T02 output to verify`
- ``docs/design/orchestrator/room-spec.md` — T03 output to verify`
- ``docs/design/contract-convergence.md` — T03 output to verify`
- ``docs/design/README.md` — T03 output to verify`

## Expected Output

- ``scripts/verify-m005-s01-contract.sh` — verification script with positive and negative checks, exits 0 on success`

## Verification

bash scripts/verify-m005-s01-contract.sh && go test ./pkg/spec -run TestExampleBundlesAreValid -count=1 && echo 'T04 verify pass'
