---
estimated_steps: 5
estimated_files: 6
skills_used: []
---

# T02: Create api/ari/domain.go and update api/ari/types.go

1. Create api/ari/domain.go by moving types from api/meta/types.go, adding json:"-" to sensitive fields
2. Update api/ari/types.go: delete AgentInfo/AgentRunInfo/WorkspaceInfo, update Result types to use domain types
3. Update all ~20 import files from api/meta to api/ari
4. Update pkg/ari/server.go: remove agentRunToInfo/agentToInfo, return domain types directly
5. Delete api/meta/

## Inputs

- `api/meta/types.go`
- `api/ari/types.go`
- `pkg/ari/server.go`
- `docs/plan/codebase-refactor-20260413.md Phase 2b`

## Expected Output

- `api/ari/domain.go`

## Verification

make build && go test ./...
