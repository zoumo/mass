---
estimated_steps: 1
estimated_files: 1
skills_used: []
---

# T02: Create pkg/ari/server/ split implementations

Split pkg/ari/server.go into pkg/ari/server/server.go (combined service implementing all 3 interfaces via one struct with shared deps). Register with jsonrpc.Server using api/ari Register functions.

## Inputs

- `pkg/ari/server.go`
- `api/ari/service.go`
- `api/ari/types.go`
- `api/ari/domain.go`

## Expected Output

- `pkg/ari/server/server.go`

## Verification

go build ./pkg/ari/...
