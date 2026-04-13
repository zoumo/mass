# S03: Phase 2b: ARI Clean-Break Contract Convergence

**Goal:** ARI clean-break contract convergence: update ari-spec.md, create api/ari/domain.go with sensitive fields hidden, update api/ari/types.go Result types to use domain types, update all consumers, delete api/meta/.
**Demo:** make build + go test ./... pass; ARI JSON shape matches updated ari-spec.md

## Must-Haves

- Not provided.

## Proof Level

- This slice proves: Not provided.

## Integration Closure

Not provided.

## Verification

- Not provided.

## Tasks

- [x] **T01: Update ari-spec.md with new domain wire shapes** `est:30m`
  Update docs/design/agentd/ari-spec.md to replace AgentInfo/AgentRunInfo/WorkspaceInfo with Agent/AgentRun/Workspace domain shapes per the final plan table.
  - Files: `docs/design/agentd/ari-spec.md`
  - Verify: grep -L 'AgentInfo\|AgentRunInfo\|WorkspaceInfo' docs/design/agentd/ari-spec.md

- [x] **T02: Create api/ari/domain.go and update api/ari/types.go** `est:2h`
  1. Create api/ari/domain.go by moving types from api/meta/types.go, adding json:"-" to sensitive fields
2. Update api/ari/types.go: delete AgentInfo/AgentRunInfo/WorkspaceInfo, update Result types to use domain types
3. Update all ~20 import files from api/meta to api/ari
4. Update pkg/ari/server.go: remove agentRunToInfo/agentToInfo, return domain types directly
5. Delete api/meta/
  - Files: `api/ari/domain.go`, `api/ari/types.go`, `pkg/ari/server.go`, `pkg/store/agent.go`, `pkg/store/agentrun.go`, `pkg/store/workspace.go`
  - Verify: make build && go test ./...

## Files Likely Touched

- docs/design/agentd/ari-spec.md
- api/ari/domain.go
- api/ari/types.go
- pkg/ari/server.go
- pkg/store/agent.go
- pkg/store/agentrun.go
- pkg/store/workspace.go
