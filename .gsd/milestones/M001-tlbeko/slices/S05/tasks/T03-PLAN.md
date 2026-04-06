---
estimated_steps: 12
estimated_files: 1
skills_used: []
---

# T03: Integration tests for ARI workspace methods

Create pkg/ari/server_test.go with integration tests for workspace/prepare, workspace/list, workspace/cleanup via JSON-RPC. Test all source types (Git, EmptyDir, Local). Test cleanup failure when refs > 0. Test prepare → list → cleanup round-trip. Follow test pattern from pkg/rpc/server_test.go.

## Failure Modes

<!-- Q5: Tests verify error handling in ARI methods -->

| Test case | Expected behavior | Error verification |
|-----------|-------------------|-------------------|
| Prepare with invalid spec | InvalidParams error | JSON-RPC error code match |
| Cleanup with refs > 0 | InternalError with message | RefCount check proven |
| Cleanup nonexistent workspace | InternalError | Registry lookup failure |
| Prepare hook failure | WorkspaceError in response | Phase field preserved |

## Steps

1. Create pkg/ari/server_test.go with package declaration `package ari_test`
2. Import testing, context, os, path/filepath, net, time, github.com/sourcegraph/jsonrpc2, github.com/stretchr/testify/require, pkg/workspace, pkg/ari
3. Define testHarness struct (pattern from pkg/rpc/server_test.go): manager, registry, server, socket, baseDir, serveErr chan
4. Implement newTestHarness(t) helper: create temp baseDir, create WorkspaceManager, create Registry, create Server, start Serve goroutine, wait for socket, return harness. Add cleanup function.
5. Implement dial(t, harness, handler) helper: connect to Unix socket, create jsonrpc2.Conn, return connection
6. Write TestARIWorkspacePrepareEmptyDir: create harness, dial client, call workspace/prepare with EmptyDir spec, assert WorkspaceId is non-empty, assert Path exists, assert Status="ready"
7. Write TestARIWorkspacePrepareGit: create harness, use test git repo (pattern from pkg/workspace/git_test.go), call workspace/prepare with Git spec, verify clone succeeded
8. Write TestARIWorkspacePrepareLocal: create harness, create temp directory, call workspace/prepare with Local spec pointing to temp dir, verify Path matches input path
9. Write TestARIWorkspaceList: create harness, prepare workspace, call workspace/list, assert workspaces array has 1 entry with correct workspaceId
10. Write TestARIWorkspaceCleanup: create harness, prepare EmptyDir workspace, call workspace/cleanup, verify directory deleted, call workspace/list, assert empty array
11. Write TestARIWorkspaceCleanupWithRefs: create harness, prepare workspace, call registry.Acquire manually (simulate session), call workspace/cleanup, assert error returned, verify workspace still in list
12. Write TestARIWorkspaceLifecycle: integration test — prepare → list (verify present) → cleanup → list (verify absent)
13. Run tests: go test ./pkg/ari/... -v

## Must-Haves

- [ ] testHarness struct defined with manager, registry, server, socket cleanup
- [ ] TestARIWorkspacePrepareEmptyDir passes (EmptyDir spec → workspace created)
- [ ] TestARIWorkspacePrepareGit passes (Git spec → clone succeeds)
- [ ] TestARIWorkspacePrepareLocal passes (Local spec → existing path validated)
- [ ] TestARIWorkspaceList passes (workspace appears in list after prepare)
- [ ] TestARIWorkspaceCleanup passes (managed workspace deleted on cleanup)
- [ ] TestARIWorkspaceCleanupWithRefs passes (cleanup fails when RefCount > 0)
- [ ] TestARIWorkspaceLifecycle passes (prepare → list → cleanup round-trip)
- [ ] All tests use JSON-RPC client connection (prove end-to-end)
- [ ] Cleanup harness properly: shutdown server, remove temp dirs

## Negative Tests

<!-- Q7: Tests verify error handling -->

- **Malformed inputs**: Test with empty/nil params → InvalidParams error
- **Error paths**: Test cleanup with refs > 0 → InternalError with "workspace still referenced" message
- **Boundary conditions**: Test workspace/list on empty registry → returns empty array (not error)

## Verification

go test ./pkg/ari/... -v passes all tests

## Observability Impact

Tests verify JSON-RPC error messages are structured and informative. WorkspaceError Phase field preserved in error responses. RefCount visibility in cleanup failure message enables debugging.

## Inputs

- `pkg/ari/server.go` — ARI server implementation to test
- `pkg/ari/types.go` — Request/response types to use in tests
- `pkg/ari/registry.go` — Registry to manipulate for refs test
- `pkg/rpc/server_test.go` — Test harness pattern to follow
- `pkg/workspace/manager_test.go` — Workspace test patterns (Git repo setup)

## Expected Output

- `pkg/ari/server_test.go` — Integration tests for workspace methods