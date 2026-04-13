# S04: Phase 3: Service Interface + Register + Typed Clients

**Goal:** Define api/ari/service.go (3 Service Interfaces + Register functions), api/ari/client.go (typed clients), api/shim/service.go (ShimService + Register with Peer abstraction), api/shim/client.go (typed ShimClient).
**Demo:** make build passes; interfaces compile cleanly

## Must-Haves

- Not provided.

## Proof Level

- This slice proves: Not provided.

## Integration Closure

Not provided.

## Verification

- Not provided.

## Tasks

- [x] **T01: Create api/ari/service.go + api/ari/client.go** `est:1h`
  Define WorkspaceService, AgentRunService, AgentService interfaces + Register functions using pkg/jsonrpc.Server. Create typed ARI clients using pkg/jsonrpc.Client.
  - Files: `api/ari/service.go`, `api/ari/client.go`
  - Verify: go build ./api/ari/...

- [x] **T02: Create api/shim/service.go + api/shim/client.go** `est:1h`
  Define ShimService interface + RegisterShimService function. Subscribe uses Peer abstraction. Create typed ShimClient using pkg/jsonrpc.Client.
  - Files: `api/shim/service.go`, `api/shim/client.go`
  - Verify: go build ./api/shim/...

## Files Likely Touched

- api/ari/service.go
- api/ari/client.go
- api/shim/service.go
- api/shim/client.go
