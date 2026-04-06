# M001-tlbeko/S04 — Research

**Date:** 2026-04-03

## Summary

S04 implements WorkspaceManager to orchestrate the full workspace lifecycle: Prepare (source preparation + setup hooks) and Cleanup (teardown hooks + managed directory deletion). The slice integrates existing components from S01/S02/S03 (SourceHandler routing, HookExecutor, structured error types) and adds reference counting to prevent premature cleanup when multiple sessions share a workspace. The implementation follows established patterns with straightforward composition — no new technology or unfamiliar APIs.

## Recommendation

Implement WorkspaceManager as a composition layer over existing handlers and HookExecutor. Use a simple in-memory reference counting map (workspaceID → count) since metadata store is in M001-tvc4z0. Prepare workflow: validate spec → route to SourceHandler → execute setup hooks → track workspace → return path. Cleanup workflow: decrement ref count → if zero, execute teardown hooks → delete managed directory (skip Local). Follow GitError/HookError pattern for WorkspaceError structured diagnostics.

## Implementation Landscape

### Key Files

- `pkg/workspace/manager.go` — **NEW** WorkspaceManager implementation with Prepare/Cleanup, SourceHandler routing, HookExecutor integration, reference counting
- `pkg/workspace/manager_test.go` — **NEW** WorkspaceManager tests: Prepare success/failure, Cleanup success/failure, reference counting, hook execution integration, managed/unmanaged semantics
- `pkg/workspace/handler.go` — SourceHandler interface (S01), used for routing
- `pkg/workspace/git.go` — GitHandler (S01), managed workspace
- `pkg/workspace/emptydir.go` — EmptyDirHandler (S02), managed workspace
- `pkg/workspace/local.go` — LocalHandler (S02), unmanaged workspace (returns source.Local.Path, not targetDir)
- `pkg/workspace/hook.go` — HookExecutor (S03), ExecuteHooks method for setup/teardown
- `pkg/workspace/spec.go` — WorkspaceSpec types (S01), Source discriminated union

### Build Order

1. **WorkspaceManager struct + Prepare workflow** (unblocks everything)
   - SourceHandler routing: map[source.Type]SourceHandler
   - Prepare(ctx, spec, targetDir) → (workspacePath, error)
   - Call handler.Prepare → HookExecutor.ExecuteHooks(setup)
   - Track workspace (managed flag, workspaceID)

2. **Reference counting + Cleanup workflow** (depends on Prepare)
   - refCount map[workspaceID]int + mutex
   - Acquire(workspaceID) increments count
   - Release(workspaceID) decrements, triggers Cleanup if zero
   - Cleanup: ExecuteHooks(teardown) → os.RemoveAll for managed only

3. **WorkspaceError structured error type** (follows GitError/HookError pattern)
   - Phase field: "prepare-source", "prepare-hooks", "cleanup-hooks", "cleanup-delete"
   - WorkspaceID, SourceType, Err chain

4. **Integration tests** (proves full lifecycle)
   - Prepare → Cleanup round-trip
   - Git source with hooks
   - EmptyDir source
   - Local source (no deletion)
   - Reference counting: Acquire → Release → Cleanup
   - Hook failure aborts Prepare, cleans up source
   - Teardown hook failure still deletes managed directory

### Verification Approach

- `go build ./pkg/workspace/...` — Build succeeds
- `go test ./pkg/workspace/... -v -count=1` — All tests pass (existing + new)
- Integration test: `go test ./pkg/workspace/... -v -run "Manager" -count=1`

Key verification points:
- Prepare returns workspacePath matching source preparation result
- Setup hook failure aborts Prepare and cleans up source directory (for managed)
- Local source Cleanup skips directory deletion (unmanaged)
- Reference counting: Cleanup only triggered when ref count reaches zero
- Teardown hook failure does not prevent managed directory deletion

## Constraints

- **No metadata store in this milestone** — Reference counting uses in-memory map with sync.Mutex. Metadata store (R003) is in M001-tvc4z0, separate milestone.
- **Managed vs unmanaged semantics** — LocalHandler returns source.Local.Path (not targetDir). Local workspaces are NOT deleted on Cleanup.
- **Hook failure cleanup** — If setup hooks fail, the prepared source directory must be cleaned up (for managed sources) to avoid leaking partial workspaces.
- **Teardown failure handling** — Teardown hook failure should still delete the managed directory. Best-effort cleanup.

## Common Pitfalls

- **Local workspace deletion** — LocalHandler returns source.Local.Path, not targetDir. Cleanup must NOT delete Local workspaces. Check managed flag before os.RemoveAll.
- **Hook failure cleanup race** — If setup hooks fail, cleanup the source directory immediately. Don't wait for explicit Cleanup call. Prepare should not leave partial workspaces.
- **Reference counting thread safety** — Use sync.Mutex for refCount map. Acquire/Release/Cleanup must be atomic operations.
- **TargetDir for Local sources** — Local workspaces don't use targetDir. The Prepare signature still accepts targetDir (for consistency), but LocalHandler ignores it. WorkspaceManager should pass targetDir anyway (handler decides whether to use it).

## Open Risks

- **WorkspaceID definition** — Need to define what workspaceID is. Options: (1) targetDir path, (2) spec.Metadata.Name, (3) UUID generated by Prepare. Recommendation: use targetDir path as workspaceID (canonical, unique per workspace instance).
- **Multiple Acquire calls** — Same workspace can be acquired multiple times (e.g., multiple sessions). Acquire should increment count, not fail if already tracked.