---
estimated_steps: 1
estimated_files: 2
skills_used: []
---

# T01: Create pkg/shim/server/service.go and pkg/shim/client/client.go

Extract shim business logic from pkg/rpc/server.go into Service struct implementing api/shim.ShimService. Create pkg/shim/client/client.go Dial helper.

## Inputs

- `pkg/rpc/server.go`
- `api/shim/service.go`
- `api/shim/client.go`
- `api/shim/types.go`
- `pkg/jsonrpc/server.go`

## Expected Output

- `pkg/shim/server/service.go`
- `pkg/shim/client/client.go`

## Verification

go build ./pkg/shim/...
