# S05: Phase 4: Implementation Migration

**Goal:** Migrate implementations: pkg/shim/server/service.go (from pkg/rpc/server.go), pkg/shim/client/client.go (Dial helper), pkg/ari/server/{workspace,agentrun,agent}.go (from pkg/ari/server.go split), pkg/ari/client/client.go, update cmd entrypoints, migrate tests.
**Demo:** make build + go test ./... pass; integration tests pass

## Must-Haves

- Not provided.

## Proof Level

- This slice proves: Not provided.

## Integration Closure

Not provided.

## Verification

- Not provided.

## Tasks

- [x] **T01: Create pkg/shim/server/service.go and pkg/shim/client/client.go** `est:1h`
  Extract shim business logic from pkg/rpc/server.go into Service struct implementing api/shim.ShimService. Create pkg/shim/client/client.go Dial helper.
  - Files: `pkg/shim/server/service.go`, `pkg/shim/client/client.go`
  - Verify: go build ./pkg/shim/...

- [x] **T02: Create pkg/ari/server/ split implementations** `est:2h`
  Split pkg/ari/server.go into pkg/ari/server/server.go (combined service implementing all 3 interfaces via one struct with shared deps). Register with jsonrpc.Server using api/ari Register functions.
  - Files: `pkg/ari/server/server.go`
  - Verify: go build ./pkg/ari/...

- [x] **T03: Create pkg/ari/client/client.go and update cmd entrypoints** `est:1h`
  Create Dial helper. Update cmd/agentd/subcommands/server/command.go to use pkg/ari/server + jsonrpc.Server. Update cmd/agentd/subcommands/shim/command.go to use pkg/shim/server. Update pkg/agentd/process.go to use pkg/shim/client. Verify make build + go test ./...
  - Files: `pkg/ari/client/client.go`, `cmd/agentd/subcommands/server/command.go`, `cmd/agentd/subcommands/shim/command.go`, `pkg/agentd/process.go`
  - Verify: make build && go test ./...

## Files Likely Touched

- pkg/shim/server/service.go
- pkg/shim/client/client.go
- pkg/ari/server/server.go
- pkg/ari/client/client.go
- cmd/agentd/subcommands/server/command.go
- cmd/agentd/subcommands/shim/command.go
- pkg/agentd/process.go
