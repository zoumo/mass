---
estimated_steps: 1
estimated_files: 1
skills_used: []
---

# T01: Update ari-spec.md with new domain wire shapes

Update docs/design/agentd/ari-spec.md to replace AgentInfo/AgentRunInfo/WorkspaceInfo with Agent/AgentRun/Workspace domain shapes per the final plan table.

## Inputs

- `docs/plan/codebase-refactor-20260413.md Phase 2b`
- `docs/design/agentd/ari-spec.md`

## Expected Output

- `docs/design/agentd/ari-spec.md (updated)`

## Verification

grep -L 'AgentInfo\|AgentRunInfo\|WorkspaceInfo' docs/design/agentd/ari-spec.md
