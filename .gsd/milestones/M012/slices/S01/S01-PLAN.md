# S01: pkg/jsonrpc/ Transport-Agnostic Framework

**Goal:** Build pkg/jsonrpc/ transport-agnostic framework: Server+ServiceDesc+Interceptor, Client wrapping jsonrpc2 with bounded FIFO notification worker, RPCError, Peer abstraction, and 18 protocol tests.
**Demo:** make build passes; go test ./pkg/jsonrpc/... passes all 18 protocol tests

## Must-Haves

- Not provided.

## Proof Level

- This slice proves: Not provided.

## Integration Closure

Not provided.

## Verification

- Not provided.

## Tasks

- [x] **T01: Create pkg/jsonrpc/ core files** `est:2h`
  Create errors.go, peer.go, server.go, client.go with all types and logic per the final plan.
  - Files: `pkg/jsonrpc/errors.go`, `pkg/jsonrpc/peer.go`, `pkg/jsonrpc/server.go`, `pkg/jsonrpc/client.go`
  - Verify: go build ./pkg/jsonrpc/...

- [x] **T02: Write 18 protocol tests** `est:2h`
  Write server_test.go and client_test.go covering all 18 tests from the plan's test matrix.
  - Files: `pkg/jsonrpc/server_test.go`, `pkg/jsonrpc/client_test.go`
  - Verify: go test ./pkg/jsonrpc/... -v -count=1

## Files Likely Touched

- pkg/jsonrpc/errors.go
- pkg/jsonrpc/peer.go
- pkg/jsonrpc/server.go
- pkg/jsonrpc/client.go
- pkg/jsonrpc/server_test.go
- pkg/jsonrpc/client_test.go
