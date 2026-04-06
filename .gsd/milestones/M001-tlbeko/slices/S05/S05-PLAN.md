# S05: ARI Workspace Methods

**Goal:** ARI JSON-RPC server exposes workspace/* methods (prepare/list/cleanup) wired to WorkspaceManager, enabling workspace provisioning via the ARI interface
**Demo:** After this: ARI workspace/* methods work; integration test: prepare → session → cleanup

## Tasks
- [x] **T01: Defined ARI workspace request/response types for prepare/list/cleanup methods** — Create pkg/ari/types.go with request/response structs for workspace/prepare, workspace/list, workspace/cleanup methods. Follow ARI spec exactly for field names and types. Reuse WorkspaceSpec from pkg/workspace/spec.go for prepare params.
  - Estimate: 15m
  - Files: pkg/ari/types.go
  - Verify: go build ./pkg/ari/... compiles without error
- [x] **T02: Created ARI JSON-RPC server with workspace/prepare, workspace/list, workspace/cleanup methods wired to WorkspaceManager** — Create pkg/ari/server.go with JSON-RPC server implementing workspace/prepare, workspace/list, workspace/cleanup. Create pkg/ari/registry.go for workspaceId → metadata tracking. Generate UUIDs for workspace IDs. Wire to WorkspaceManager.Prepare/Cleanup. Handle JSON-RPC errors appropriately.
  - Estimate: 1h
  - Files: pkg/ari/server.go, pkg/ari/registry.go
  - Verify: go build ./pkg/ari/... compiles without error
- [x] **T03: Created pkg/ari/server_test.go with comprehensive integration tests for workspace/prepare, workspace/list, workspace/cleanup via JSON-RPC** — Create pkg/ari/server_test.go with integration tests for workspace/prepare, workspace/list, workspace/cleanup via JSON-RPC. Test all source types (Git, EmptyDir, Local). Test cleanup failure when refs > 0. Test prepare → list → cleanup round-trip. Follow test pattern from pkg/rpc/server_test.go.
  - Estimate: 45m
  - Files: pkg/ari/server_test.go
  - Verify: go test ./pkg/ari/... -v passes all tests
