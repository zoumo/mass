---
estimated_steps: 7
estimated_files: 6
skills_used: []
---

# T01: Delete pkg/rpc and pkg/agentd/shim_client.go; fix process.go ParseShimEvent call

Remove three files/directories with no surviving production callers. Fix the single remaining call site in pkg/agentd/process.go.

## Steps

1. Delete the entire pkg/rpc directory (server.go + server_test.go + server_internal_test.go). These are the old sourcegraph/jsonrpc2-based shim server and its tests. No production caller imports pkg/rpc — only pkg/rpc/server_test.go imports it.

2. Delete pkg/agentd/shim_client.go. This is the old internal ShimClient backed by sourcegraph/jsonrpc2. All production callers (process.go, recovery.go) were migrated to pkg/shim/client in S05.

3. Delete pkg/agentd/shim_client_test.go. This test file is in package agentd and tests the now-deleted shim_client.go implementation.

4. Fix pkg/agentd/process.go: change `ParseShimEvent(params)` to `shimclient.ParseShimEvent(params)`. The shimclient import alias is already present in process.go from the S05 migration. This is a one-line change (around line 139).

5. Run `make build` and `go test ./pkg/agentd/... -count=1`. Confirm exit 0.

## Inputs

- ``pkg/rpc/server.go``
- ``pkg/rpc/server_test.go``
- ``pkg/rpc/server_internal_test.go``
- ``pkg/agentd/shim_client.go``
- ``pkg/agentd/shim_client_test.go``
- ``pkg/agentd/process.go``

## Expected Output

- ``pkg/agentd/process.go` (modified — ParseShimEvent call updated to shimclient.ParseShimEvent)`

## Verification

make build exits 0. go test ./pkg/agentd/... -count=1 exits 0. rg '"github.com/zoumo/oar/pkg/rpc"' --type go returns zero matches.
