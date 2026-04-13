---
estimated_steps: 1
estimated_files: 2
skills_used: []
---

# T01: Create api/ari/service.go + api/ari/client.go

Define WorkspaceService, AgentRunService, AgentService interfaces + Register functions using pkg/jsonrpc.Server. Create typed ARI clients using pkg/jsonrpc.Client.

## Inputs

- `api/ari/types.go`
- `api/ari/domain.go`
- `pkg/jsonrpc/server.go`
- `pkg/jsonrpc/client.go`
- `api/methods.go`
- `docs/plan/codebase-refactor-20260413.md Phase 3`

## Expected Output

- `api/ari/service.go`
- `api/ari/client.go`

## Verification

go build ./api/ari/...
