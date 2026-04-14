---
estimated_steps: 73
estimated_files: 7
skills_used: []
---

# T01: Create new pkg/ari/api/, pkg/ari/server/, pkg/ari/client/ target files

Create all destination files for the ARI package restructure. Do NOT delete anything or update consumers yet — this task is additive only. All 7 new files must compile cleanly before T02 begins.

## Steps

1. Create `pkg/ari/api/types.go`:
   - Copy content from `api/ari/types.go`
   - Change `package ari` → `package api`
   - All imports and type definitions are unchanged (already uses `apiruntime "github.com/zoumo/oar/pkg/runtime-spec/api"` from S01)

2. Create `pkg/ari/api/domain.go`:
   - Copy content from `api/ari/domain.go`
   - Change `package ari` → `package api`
   - All imports and types unchanged

3. Create `pkg/ari/api/methods.go`:
   - New file, `package api`
   - Extract ONLY ARI method constants from `api/methods.go` (the 3 const blocks for Workspace*, AgentRun*, Agent* — do NOT include shim methods MethodSession*, MethodRuntime*, MethodShimEvent*)
   - Content:
     ```go
     package api

     // ARI workspace methods (orchestrator ↔ agentd).
     const (
         MethodWorkspaceCreate = "workspace/create"
         MethodWorkspaceStatus = "workspace/status"
         MethodWorkspaceList   = "workspace/list"
         MethodWorkspaceDelete = "workspace/delete"
         MethodWorkspaceSend   = "workspace/send"
     )

     // ARI agentrun methods.
     const (
         MethodAgentRunCreate  = "agentrun/create"
         MethodAgentRunPrompt  = "agentrun/prompt"
         MethodAgentRunCancel  = "agentrun/cancel"
         MethodAgentRunStop    = "agentrun/stop"
         MethodAgentRunDelete  = "agentrun/delete"
         MethodAgentRunRestart = "agentrun/restart"
         MethodAgentRunList    = "agentrun/list"
         MethodAgentRunStatus  = "agentrun/status"
         MethodAgentRunAttach  = "agentrun/attach"
     )

     // ARI agent definition methods.
     const (
         MethodAgentSet    = "agent/set"
         MethodAgentGet    = "agent/get"
         MethodAgentList   = "agent/list"
         MethodAgentDelete = "agent/delete"
     )
     ```

4. Create `pkg/ari/server/service.go`:
   - Copy content from `api/ari/service.go`
   - Change `package ari` → `package server`
   - Add import `pkgariapi "github.com/zoumo/oar/pkg/ari/api"` alongside existing `"github.com/zoumo/oar/pkg/jsonrpc"`
   - Qualify ALL bare type references with `pkgariapi.`: e.g. `WorkspaceCreateParams` → `pkgariapi.WorkspaceCreateParams`, `WorkspaceCreateResult` → `pkgariapi.WorkspaceCreateResult`, `AgentRun` → `pkgariapi.AgentRun`, etc. This applies to both interface method signatures AND function bodies (`var req WorkspaceCreateParams` → `var req pkgariapi.WorkspaceCreateParams`)
   - Remove old `package ari` comment header if it mentions the old package

5. Create `pkg/ari/server/registry.go`:
   - Copy content from `pkg/ari/registry.go`
   - Change `package ari` → `package server`
   - Change `apiari "github.com/zoumo/oar/api/ari"` → `pkgariapi "github.com/zoumo/oar/pkg/ari/api"`
   - Replace all `apiari.` references with `pkgariapi.`

6. Create `pkg/ari/client/typed.go`:
   - Copy content from `api/ari/client.go`
   - Change `package ari` → `package client`
   - Change `"github.com/zoumo/oar/api"` → `pkgariapi "github.com/zoumo/oar/pkg/ari/api"` (the file uses api.MethodX constants)
   - Add `"github.com/zoumo/oar/pkg/jsonrpc"` import (already present)
   - Qualify all bare type references with `pkgariapi.` (e.g. `WorkspaceCreateParams` → `pkgariapi.WorkspaceCreateParams`)
   - Replace `api.MethodWorkspaceCreate` → `pkgariapi.MethodWorkspaceCreate`, etc. for all method constants

7. Create `pkg/ari/client/simple.go`:
   - Copy content from `pkg/ari/client.go`
   - Change `package ari` → `package client`
   - No import changes (file uses only stdlib: encoding/json, fmt, net, sync)

8. Verify new packages compile:
   ```
   go build ./pkg/ari/api/...
   go build ./pkg/ari/server/...
   go build ./pkg/ari/client/...
   ```
   Fix any compile errors before finishing.

## Inputs

- `api/ari/types.go`
- `api/ari/domain.go`
- `api/ari/service.go`
- `api/ari/client.go`
- `api/methods.go`
- `pkg/ari/registry.go`
- `pkg/ari/client.go`

## Expected Output

- `pkg/ari/api/types.go`
- `pkg/ari/api/domain.go`
- `pkg/ari/api/methods.go`
- `pkg/ari/server/service.go`
- `pkg/ari/server/registry.go`
- `pkg/ari/client/typed.go`
- `pkg/ari/client/simple.go`

## Verification

go build ./pkg/ari/api/... && go build ./pkg/ari/server/... && go build ./pkg/ari/client/...
