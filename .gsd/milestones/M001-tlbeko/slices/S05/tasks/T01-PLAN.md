---
estimated_steps: 8
estimated_files: 1
skills_used: []
---

# T01: Define ARI workspace request/response types

Create pkg/ari/types.go with request/response structs for workspace/prepare, workspace/list, workspace/cleanup methods. Follow ARI spec exactly for field names and types. Reuse WorkspaceSpec from pkg/workspace/spec.go for prepare params.

## Steps

1. Create pkg/ari/types.go file with package declaration `package ari`
2. Import workspace package: `"github.com/open-agent-d/open-agent-d/pkg/workspace"`
3. Define WorkspacePrepareParams struct with single field `Spec workspace.WorkspaceSpec` (JSON tag: "spec")
4. Define WorkspacePrepareResult struct with fields: `WorkspaceId string` (JSON tag: "workspaceId"), `Path string` (JSON tag: "path"), `Status string` (JSON tag: "status" — always "ready" on success)
5. Define WorkspaceListParams struct (empty or with optional filter fields — keep empty for this slice)
6. Define WorkspaceListResult struct with single field `Workspaces []WorkspaceInfo` (JSON tag: "workspaces")
7. Define WorkspaceInfo struct with fields: `WorkspaceId string` ("workspaceId"), `Name string` ("name"), `Path string` ("path"), `Status string` ("status"), `Refs []string` ("refs" — session IDs, empty for now)
8. Define WorkspaceCleanupParams struct with single field `WorkspaceId string` (JSON tag: "workspaceId")
9. Run `go build ./pkg/ari/...` to verify compilation

## Must-Haves

- [ ] WorkspacePrepareParams defined with Spec field (workspace.WorkspaceSpec, JSON tag "spec")
- [ ] WorkspacePrepareResult defined with WorkspaceId, Path, Status fields (JSON tags match ARI spec)
- [ ] WorkspaceListParams defined (empty struct for this slice)
- [ ] WorkspaceListResult defined with Workspaces []WorkspaceInfo field
- [ ] WorkspaceInfo defined with WorkspaceId, Name, Path, Status, Refs fields
- [ ] WorkspaceCleanupParams defined with WorkspaceId field
- [ ] All structs have correct JSON tags matching ARI spec (camelCase field names)
- [ ] Package compiles without error

## Verification

go build ./pkg/ari/... compiles without error

## Observability Impact

None — pure type definitions, no runtime behavior

## Inputs

- `docs/design/agentd/ari-spec.md` — ARI method definitions showing request/response field names
- `pkg/workspace/spec.go` — WorkspaceSpec type to reuse for prepare params

## Expected Output

- `pkg/ari/types.go` — Request/response types for workspace methods