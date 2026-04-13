---
estimated_steps: 1
estimated_files: 2
skills_used: []
---

# T02: Create api/shim/service.go + api/shim/client.go

Define ShimService interface + RegisterShimService function. Subscribe uses Peer abstraction. Create typed ShimClient using pkg/jsonrpc.Client.

## Inputs

- `api/shim/types.go`
- `api/methods.go`
- `pkg/jsonrpc/peer.go`
- `pkg/jsonrpc/server.go`
- `pkg/jsonrpc/client.go`
- `docs/plan/codebase-refactor-20260413.md Phase 3`

## Expected Output

- `api/shim/service.go`
- `api/shim/client.go`

## Verification

go build ./api/shim/...
