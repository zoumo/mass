---
estimated_steps: 26
estimated_files: 2
skills_used: []
---

# T01: Create ARI client package

Create pkg/ari/client.go with reusable JSON-RPC client for ARI socket communication. Simplified from agent-shim-cli pattern: dial() + call() only, no event handling. Single-shot RPC calls for management commands.

## Steps

1. Read `cmd/agent-shim-cli/main.go` to understand JSON-RPC client pattern (dial, call, notify functions)
2. Create `pkg/ari/client.go` with package declaration and imports (net, encoding/json, fmt)
3. Define rpcRequest struct with JSONRPC, ID, Method, Params fields
4. Define rpcResponse struct with JSONRPC, ID, Result, Error fields
5. Define rpcError struct with Code and Message fields
6. Define Client struct with conn (net.Conn), encoder, decoder, mutex, nextID fields
7. Implement NewClient(socketPath string) (*Client, error) — dial Unix socket, initialize encoder/decoder, return client
8. Implement Call(method string, params any, result any) error — send request with ID, wait for response, unmarshal result
9. Implement Close() error — close connection
10. Run `go build ./pkg/ari/...` to verify compilation

## Must-Haves

- [ ] NewClient(socketPath) connects to Unix socket and returns Client
- [ ] Call(method, params, result) sends JSON-RPC request and unmarshals response
- [ ] No event handling (simplified from agent-shim-cli)
- [ ] go build ./pkg/ari/... passes

## Failure Modes

| Dependency | On error | On timeout | On malformed response |
|------------|----------|-----------|----------------------|
| ARI socket | Return connection error | No timeout (blocking) | Return parse error |

## Negative Tests

- Socket file missing: NewClient returns error
- Daemon unavailable: connection refused error
- Malformed JSON response: Call returns parse error
- RPC error response: Call returns error with code/message

## Inputs

- ``cmd/agent-shim-cli/main.go` — JSON-RPC client pattern to copy/simplify`
- ``pkg/ari/types.go` — Existing ARI types for params/results`

## Expected Output

- ``pkg/ari/client.go` — New ARI client package with NewClient(), Call(), Close() methods`

## Verification

go build ./pkg/ari/... passes, go test ./pkg/ari/... passes

## Observability Impact

None — client package is library code with no runtime state
