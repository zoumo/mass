# S06: Phase 5: Cleanup

**Goal:** Delete three legacy packages (pkg/rpc, pkg/ari/server.go monolith, pkg/agentd/shim_client.go) that were superseded by the typed service interface packages built in S03–S05. Migrate the one test file that depends on the old server (pkg/ari/server_test.go). After deletion, make build and go test ./... pass with zero references to the deleted files.
**Demo:** make build + go test ./... pass; no references to deleted packages

## Must-Haves

- make build exits 0 (both binaries). go test ./... -count=1 exits 0. rg 'pkg/rpc' --type go returns zero matches. rg '\"github.com/zoumo/oar/pkg/rpc\"' --type go returns zero matches. rg 'ari\\.New\\b' --type go returns zero matches in remaining files. pkg/ari/server.go and pkg/agentd/shim_client.go do not exist.

## Proof Level

- This slice proves: contract

## Integration Closure

All production callers already migrated in S05. No new wiring introduced — only removal and one test adaptation. After this slice the codebase has a single coherent set of typed RPC packages with no legacy dead code.

## Verification

- Not provided.

## Tasks

- [x] **T01: Delete pkg/rpc and pkg/agentd/shim_client.go; fix process.go ParseShimEvent call** `est:20 min`
  Remove three files/directories with no surviving production callers. Fix the single remaining call site in pkg/agentd/process.go.

## Steps

1. Delete the entire pkg/rpc directory (server.go + server_test.go + server_internal_test.go). These are the old sourcegraph/jsonrpc2-based shim server and its tests. No production caller imports pkg/rpc — only pkg/rpc/server_test.go imports it.

2. Delete pkg/agentd/shim_client.go. This is the old internal ShimClient backed by sourcegraph/jsonrpc2. All production callers (process.go, recovery.go) were migrated to pkg/shim/client in S05.

3. Delete pkg/agentd/shim_client_test.go. This test file is in package agentd and tests the now-deleted shim_client.go implementation.

4. Fix pkg/agentd/process.go: change `ParseShimEvent(params)` to `shimclient.ParseShimEvent(params)`. The shimclient import alias is already present in process.go from the S05 migration. This is a one-line change (around line 139).

5. Run `make build` and `go test ./pkg/agentd/... -count=1`. Confirm exit 0.
  - Files: `pkg/rpc/server.go`, `pkg/rpc/server_test.go`, `pkg/rpc/server_internal_test.go`, `pkg/agentd/shim_client.go`, `pkg/agentd/shim_client_test.go`, `pkg/agentd/process.go`
  - Verify: make build exits 0. go test ./pkg/agentd/... -count=1 exits 0. rg '"github.com/zoumo/oar/pkg/rpc"' --type go returns zero matches.

- [x] **T02: Migrate pkg/ari/server_test.go to ariserver API; delete pkg/ari/server.go** `est:45 min`
  The 801-line pkg/ari/server_test.go uses ari.New() and ari.Server from the old monolithic pkg/ari/server.go. Migrate its test harness to use the new API. Then delete the old monolith.

## Steps

1. Read pkg/ari/server_test.go in full. The changes are confined to the test harness (lines 36–101): the `testEnv` struct and `newTestServer()` helper. No individual test function directly accesses `env.srv`.

2. Update pkg/ari/server_test.go:
   a. Add imports: `ariserver "github.com/zoumo/oar/pkg/ari/server"` and `"github.com/zoumo/oar/pkg/jsonrpc"`.
   b. Change `testEnv.srv *ari.Server` → `testEnv.srv *jsonrpc.Server`.
   c. Rewrite the server-creation block in `newTestServer()`:
      ```go
      sockPath := shortSockPath(t)
      t.Cleanup(func() { _ = os.Remove(sockPath) })

      svc := ariserver.New(mgr, registry, agents, processes, store, tmpDir, slog.Default())
      srv := jsonrpc.NewServer(slog.Default())
      ariserver.Register(srv, svc)

      ln, err := net.Listen("unix", sockPath)
      require.NoError(t, err)

      serveErr := make(chan error, 1)
      go func() { serveErr <- srv.Serve(ln) }()
      ```
   d. Update the cleanup lambda: replace `_ = srv.Shutdown(context.Background())` with `_ = ln.Close()` then `_ = srv.Shutdown(context.Background())`.
   e. Keep `ari.NewRegistry()` and `ari.NewClient()` references — both come from pkg/ari/registry.go and pkg/ari/client.go which are NOT deleted. The `"github.com/zoumo/oar/pkg/ari"` import stays.
   f. The socket-wait `require.Eventually` block is unchanged.

3. Compile-check before deletion: `go build ./pkg/ari/... && go test ./pkg/ari/... -count=1 -run TestServer -v` must pass.

4. Delete pkg/ari/server.go (1235-line old monolith).

5. Run `make build` then `go test ./... -count=1`. All packages must pass.

6. Final reference check: `rg 'ari\.New\b' --type go` returns zero matches.
  - Files: `pkg/ari/server_test.go`, `pkg/ari/server.go`
  - Verify: make build exits 0. go test ./... -count=1 exits 0 (all packages). rg 'ari\.New\b' --type go returns zero matches. pkg/ari/server.go no longer exists.

## Files Likely Touched

- pkg/rpc/server.go
- pkg/rpc/server_test.go
- pkg/rpc/server_internal_test.go
- pkg/agentd/shim_client.go
- pkg/agentd/shim_client_test.go
- pkg/agentd/process.go
- pkg/ari/server_test.go
- pkg/ari/server.go
