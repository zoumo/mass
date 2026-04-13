---
estimated_steps: 1
estimated_files: 2
skills_used: []
---

# T02: Write 18 protocol tests

Write server_test.go and client_test.go covering all 18 tests from the plan's test matrix.

## Inputs

- `pkg/jsonrpc/server.go`
- `pkg/jsonrpc/client.go`
- `pkg/jsonrpc/errors.go`
- `pkg/jsonrpc/peer.go`

## Expected Output

- `pkg/jsonrpc/server_test.go`
- `pkg/jsonrpc/client_test.go`

## Verification

go test ./pkg/jsonrpc/... -v -count=1
