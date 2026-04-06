# S05: ARI Workspace Methods — Research

**Date:** 2026-04-03

## Summary

Slice S05 ("ARI Workspace Methods") faces a **cross-milestone dependency mismatch**. The roadmap states S05 depends on S04 (complete) and should deliver "ARI workspace/* methods work; integration test: prepare → session → cleanup". However, the ARI service itself (from milestone M001-tvc4z0) hasn't been built — all slices in M001-tvc4z0 are marked as not started.

The WorkspaceManager backend is complete and tested (79 tests pass). It provides Prepare/Cleanup with reference counting and structured error handling. The ARI spec (docs/design/agentd/ari-spec.md) clearly defines workspace/prepare, workspace/list, workspace/cleanup methods. But without an ARI JSON-RPC server to wire these methods to, S05 cannot deliver its stated goal.

**Recommendation:** Build a minimal ARI server in this slice with just workspace/* methods. This is pragmatic because: (1) the WorkspaceManager is ready, (2) the ARI spec is defined, (3) session/* methods can be deferred to M001-tvc4z0. The minimal server would create pkg/ari/server.go, wire workspace methods to WorkspaceManager, and add integration tests proving prepare → cleanup works.

## Recommendation

**Build minimal ARI server with workspace/* methods only.**

Three options were considered:
1. **Block S05** — defer until M001-tvc4z0 S06 (ARI Service) is complete. This respects milestone boundaries but stalls workspace work.
2. **Build minimal ARI** — create pkg/ari/server.go with workspace methods only, leaving session/room/agent methods for M001-tvc4z0. Pragmatic, unblocks workspace integration testing.
3. **Repurpose S05** — change scope to WorkspaceManager integration tests or workspace CLI. Avoids ARI dependency but contradicts roadmap.

Option 2 is recommended because:
- WorkspaceManager is ready (S01-S04 complete)
- ARI workspace spec is clearly defined (docs/design/agentd/ari-spec.md)
- Session methods have higher complexity (Process Manager, shim lifecycle) — reasonable to defer
- Integration test "prepare → cleanup" is achievable without session involvement
- The roadmap goal "ARI workspace/* methods work" can be satisfied

The "prepare → session → cleanup" integration test in the roadmap demo would require session support. This can be deferred or scoped to just "prepare → cleanup" for now.

## Implementation Landscape

### Key Files

**Backend (complete):**
- `pkg/workspace/manager.go` — WorkspaceManager with Prepare/Cleanup/Acquire/Release methods
- `pkg/workspace/errors.go` — WorkspaceError with Phase field for structured diagnostics
- `pkg/workspace/spec.go` — WorkspaceSpec types with discriminated union JSON pattern
- `pkg/workspace/handler.go` — SourceHandler interface, GitHandler/EmptyDirHandler/LocalHandler implementations
- `pkg/workspace/hook.go` — HookExecutor for setup/teardown hooks

**Design specs:**
- `docs/design/agentd/ari-spec.md` — ARI JSON-RPC interface definition (workspace/prepare, workspace/list, workspace/cleanup)
- `docs/design/agentd/agentd.md` — agentd architecture (Workspace Manager, Session Manager, Process Manager, ARI Service)

**Reference for JSON-RPC pattern:**
- `pkg/rpc/server.go` — Shim-level JSON-RPC server (Prompt, Cancel, Subscribe, GetState, GetHistory, Shutdown methods)
- `pkg/rpc/server_test.go` — Shim RPC integration test pattern (server harness, dial, method testing)

**To create:**
- `pkg/ari/server.go` — ARI JSON-RPC server with workspace/* methods
- `pkg/ari/server_test.go` — Integration tests for workspace methods
- `pkg/ari/types.go` — Request/response types for workspace methods (WorkspacePrepareParams, WorkspacePrepareResult, etc.)

### Build Order

1. **Define request/response types** (`pkg/ari/types.go`)
   - WorkspacePrepareParams (spec: WorkspaceSpec)
   - WorkspacePrepareResult (workspaceId, path, status)
   - WorkspaceListParams (empty or filter)
   - WorkspaceListResult (workspaces array)
   - WorkspaceCleanupParams (workspaceId)

2. **Create minimal ARI server** (`pkg/ari/server.go`)
   - JSON-RPC 2.0 handler over Unix socket
   - Method routing: workspace/prepare, workspace/list, workspace/cleanup
   - Wire to WorkspaceManager methods
   - Generate workspace IDs (UUID or similar)

3. **Add workspace tracking** (simple in-memory or SQLite)
   - ARI needs to track workspace metadata (id → path, refs)
   - WorkspaceManager.Acquire/Release already handles ref counting
   - Need a WorkspaceRegistry to map IDs to workspace state

4. **Integration tests** (`pkg/ari/server_test.go`)
   - Test workspace/prepare with Git/EmptyDir/Local sources
   - Test workspace/list
   - Test workspace/cleanup (success and failure with refs)
   - Test prepare → cleanup round-trip

### Verification Approach

```bash
# Run existing workspace tests to confirm backend is stable
go test ./pkg/workspace/... -v

# Run new ARI tests after implementation
go test ./pkg/ari/... -v

# Integration test: prepare → cleanup
# Can use real git repository or local directory for testing
```

## Open Risks

- **Cross-milestone coordination**: Building ARI workspace methods here creates overlap with M001-tvc4z0 S06. Need to coordinate so M001-tvc4z0 can reuse/extend this work.
- **Workspace ID generation**: ARI expects workspace IDs for tracking. Need simple UUID generation or hash-based IDs.
- **Metadata persistence**: ARI workspace/list requires tracking workspaces across restarts. SQLite metadata store (from M001-tvc4z0 S02) would be ideal but isn't built. Simple in-memory tracking is acceptable for this slice.
- **Session integration deferred**: The roadmap demo says "prepare → session → cleanup" but session/* methods aren't available. Scope to "prepare → cleanup" or defer session integration test.

## Common Pitfalls

- **JSON-RPC method naming**: ARI uses "workspace/prepare" (slash separator), not "WorkspacePrepare" (camelCase). Follow the spec exactly.
- **WorkspaceSpec JSON marshaling**: The discriminated union pattern (Source.Type field) is already implemented in pkg/workspace/spec.go. Reuse this for ARI params.
- **Reference counting**: WorkspaceCleanup must fail if refs > 0. WorkspaceManager.Release returns count after decrement — check this before calling cleanup logic.
- **Managed vs unmanaged workspaces**: Local sources are unmanaged (not deleted on cleanup). Git/EmptyDir are managed. This is handled by WorkspaceManager.isManaged helper.

## Sources

- ARI method definitions: docs/design/agentd/ari-spec.md (workspace/prepare, workspace/list, workspace/cleanup)
- agentd architecture: docs/design/agentd/agentd.md (Workspace Manager, ARI Service)
- JSON-RPC pattern: pkg/rpc/server.go (method routing, handler structure)