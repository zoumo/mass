---
estimated_steps: 25
estimated_files: 2
skills_used: []
---

# T02: Migrate pkg/ari/server_test.go to ariserver API; delete pkg/ari/server.go

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

## Inputs

- ``pkg/ari/server_test.go``
- ``pkg/ari/server.go``
- ``pkg/ari/server/server.go``
- ``pkg/jsonrpc/server.go``

## Expected Output

- ``pkg/ari/server_test.go` (modified — test harness migrated from ari.New/ari.Server to ariserver.New/ariserver.Register/jsonrpc.Server)`

## Verification

make build exits 0. go test ./... -count=1 exits 0 (all packages). rg 'ari\.New\b' --type go returns zero matches. pkg/ari/server.go no longer exists.
