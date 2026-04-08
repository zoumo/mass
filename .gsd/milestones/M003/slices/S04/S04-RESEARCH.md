# S04 — Research: Reconciled Workspace Ref Truth and Safe Cleanup

**Date:** 2026-04-08
**Depth:** Targeted research — known codebase, known patterns, but three distinct subsystem gaps to close

## Summary

Workspace reference truth is currently split across three independent refcount systems, **none of which survive a daemon restart**. The milestone research correctly identified this as one of the exact failure modes M003 exists to stop. After exploring the code, the situation is worse than expected: the SQLite `workspace_refs` table and its trigger-maintained `ref_count` column exist and work correctly in isolation (proven by `pkg/meta/workspace_test.go`), but **the live session path never calls `AcquireWorkspace` or `ReleaseWorkspace`**. There are zero non-test callers.

After a daemon restart:
1. The `ari.Registry` (in-memory) is empty — never repopulated from DB
2. `workspace/list` returns nothing — it reads from registry only
3. `workspace/cleanup` returns "not found" — it reads from registry only
4. Session→workspace refs are invisible — never recorded to `workspace_refs` table
5. There is no mechanism to block cleanup of a workspace that has live recovered sessions

The work decomposes into three connected tasks: (1) wire session lifecycle to DB workspace refs, (2) rebuild the ARI registry from DB on startup, and (3) make `workspace/cleanup` consult persisted truth (DB ref_count) instead of volatile in-memory registry RefCount, and block cleanup during incomplete recovery.

## Requirements Targeted

- **R037** (active, primary) — Workspace identity, reuse rules, cleanup boundaries, and shared access expectations must be explicit in both design and implementation direction. This slice makes cleanup boundaries enforceable by moving ref truth to persisted storage and gating cleanup on reconciled state.
- **R044** (active, supporting) — S04 delivers the cleanup-safety portion of the restart/reconnect hardening tracked by R044.

## Recommendation

Three tasks, ordered by dependency:

**T01 — Wire session lifecycle to DB workspace refs.** Add `store.AcquireWorkspace(workspaceID, sessionID)` at session creation time in `handleSessionNew`, and `store.ReleaseWorkspace(workspaceID, sessionID)` in `handleSessionRemove`. Also persist the workspace source spec to DB in `handleWorkspacePrepare` (currently defaults to `{}`). These are mechanical wiring changes to existing, tested APIs.

**T02 — Rebuild ARI registry and workspace manager refcounts from DB on startup.** After the recovery pass in `cmd/agentd/main.go`, load all active workspaces from DB (`store.ListWorkspaces`) and re-populate the `ari.Registry`. This makes `workspace/list` and `workspace/cleanup` work after restart. Also repopulate `workspace.WorkspaceManager` refcounts from DB `ref_count` values so cleanup gating is consistent.

**T03 — Gate workspace/cleanup on DB truth and recovery phase.** Change `handleWorkspaceCleanup` to check DB `ref_count` (persisted truth) instead of, or in addition to, in-memory registry `RefCount`. Block cleanup during active recovery phase (reuse the `recoveryGuard` pattern from S01). Add tests proving: cleanup blocked with active refs, cleanup blocked during recovery, cleanup allowed when refs are zero and recovery is complete.

## Implementation Landscape

### Key Files

- **`pkg/ari/server.go` — ARI method handlers**
  - `handleWorkspacePrepare` (line ~249): Persists workspace to DB but doesn't serialize `Source` spec — it defaults to `{}`. Needs to serialize `p.Spec.Source` as JSON into `workspace.Source`.
  - `handleSessionNew` (line ~388): Creates session but never calls `store.AcquireWorkspace`. Needs to add acquire call after successful session creation.
  - `handleSessionRemove` (line ~509): Deletes session via `sessions.Delete` which calls `store.DeleteSession`. The `DeleteSession` already explicitly deletes `workspace_refs` rows (line 318 of `pkg/meta/session.go`), but since no refs are ever created, this is a no-op. After T01 wires acquire, this existing cleanup path will work correctly.
  - `handleWorkspaceCleanup` (line ~328): Gates on `meta.RefCount` from in-memory registry. Needs to gate on DB `ref_count` instead.
  
- **`pkg/meta/workspace.go` — Persisted workspace ops (already complete)**
  - `AcquireWorkspace` (line 272): Fully implemented and tested. Creates `workspace_refs` row, trigger increments `ref_count`.
  - `ReleaseWorkspace` (line 317): Fully implemented and tested. Deletes `workspace_refs` row, trigger decrements `ref_count`.
  - `DeleteWorkspace` (line 220): Already checks `ref_count > 0` and refuses deletion. This is the correct safety gate.
  - `GetWorkspace` (line 93): Returns workspace with current `ref_count` from DB.

- **`pkg/meta/session.go` — Session persistence**
  - `DeleteSession` (line 311): Already deletes `workspace_refs WHERE session_id = ?` — this means release-on-delete is already wired at the DB layer. The missing piece is the acquire side.

- **`pkg/ari/registry.go` — In-memory workspace metadata (volatile)**
  - `Registry.Add/Get/List/Remove/Acquire/Release`: All in-memory only. After restart, the registry is empty.
  - The registry is used by `workspace/list`, `workspace/cleanup`, and session attach flows.

- **`pkg/workspace/manager.go` — In-memory refcount (volatile)**
  - `WorkspaceManager.refCount`: Third refcount system, also in-memory. Used by `Cleanup` to gate directory deletion.
  - After restart, all refcounts are zero, so cleanup would proceed on workspaces that still have live sessions.

- **`cmd/agentd/main.go` — Daemon entrypoint**
  - After `processes.RecoverSessions(recoverCtx)` (line ~88), there is no registry rebuild step. The ARI server starts with an empty registry.
  - The recovery pass reconnects to shims but does not repopulate workspace state.

- **`pkg/meta/schema.sql` — Already has workspace_refs table with triggers**
  - `trg_workspace_refs_insert` / `trg_workspace_refs_delete` — trigger-maintained `ref_count`. Working correctly, just unused by the session path.

### Build Order

**T01 first** because it establishes the DB ref truth that everything else depends on. Without workspace refs being recorded, neither the registry rebuild nor the cleanup safety gate would have correct data.

**T02 second** because the registry rebuild gives `workspace/list` and `workspace/cleanup` the ability to find workspaces after restart. This also makes T03's DB-gated cleanup testable through the ARI surface.

**T03 last** because it adds the safety gate (DB-based cleanup blocking + recovery-phase blocking) that requires both the ref truth (T01) and registry presence (T02) to function.

### Verification Approach

**Unit tests (T01):**
- Verify `handleSessionNew` creates a `workspace_ref` (check DB `ref_count` after session creation)
- Verify `handleSessionRemove` decrements `ref_count` (already handled by `DeleteSession`)
- Verify workspace source spec is persisted to DB (not `{}`)

**Unit tests (T02):**
- Add a function that rebuilds registry from DB, test it loads workspaces and refcounts correctly
- Test that after rebuild, `workspace/list` returns workspaces from DB

**Unit tests (T03):**
- Test `workspace/cleanup` fails when DB `ref_count > 0`
- Test `workspace/cleanup` is blocked during recovery phase
- Test `workspace/cleanup` succeeds when `ref_count == 0` and recovery complete

**Integration verification:**
- `go test ./pkg/ari/... -count=1 -v`
- `go test ./pkg/meta/... -count=1`
- `go vet ./pkg/ari/... ./pkg/meta/... ./cmd/agentd/...`
- `go build ./cmd/agentd/... ./cmd/agent-shim/... ./pkg/...`

### What Changed Since Research

The milestone research identified the right gaps: "the live session path does not actually call `AcquireWorkspace` / `ReleaseWorkspace`." Exploration confirms this is exactly the case, and also reveals two additional issues:

1. **Workspace source spec not persisted** — `handleWorkspacePrepare` creates the DB record with `Source: json.RawMessage("{}")`, not the actual spec. After restart, the registry is gone and the DB doesn't know if the workspace is managed (git/emptyDir) or unmanaged (local). The `WorkspaceManager.Cleanup` needs `source.Type` to decide whether to `os.RemoveAll`.

2. **No registry rebuild at startup** — Even if refs were correctly tracked, after restart the registry is empty. No workspace operations work. This is orthogonal to ref safety but must be solved in the same slice.

3. **WorkspaceManager refcount is a third system** — In addition to registry.RefCount and DB workspace_refs, the `WorkspaceManager` has its own in-memory `refCount` map. After T02, the rebuild should also initialize this from DB, or cleanup should bypass it and use DB directly.

### Patterns from S02/S03 to Reuse

- **Recovery guard pattern** (S01/server.go `recoveryGuard`): Reuse for blocking `workspace/cleanup` during recovery phase.
- **Shim-vs-DB reconciliation** (S02): The pattern of comparing live truth against DB and acting on mismatches applies here as DB refcount vs live session presence.
- **Atomic operations under lock** (S03): Not directly needed here, but the "rebuild under lock" pattern for registry repopulation follows the same principle.

## Constraints

- The `workspace.WorkspaceManager.Cleanup` method requires a `WorkspaceSpec` to determine if the workspace is managed. After restart, this spec must come from DB, not registry. **This means T01 must persist the source spec.**
- `meta.DeleteSession` already deletes `workspace_refs` — this is the release path. T01 only needs to wire the acquire side.
- The `ari.Registry.Spec` field stores the full `workspace.WorkspaceSpec`. After rebuild from DB, the registry spec needs to be reconstructed from the DB source JSON + metadata. The `workspace.WorkspaceSpec` struct has `Source`, `Metadata`, and `Hooks` — only `Source` needs to survive restart for cleanup safety. `Metadata.Name` is also persisted.
- After restart, hooks (setup/teardown) in the workspace spec are not persisted. This means teardown hooks won't run on cleanup after restart. This is acceptable: teardown hooks are best-effort today (D003), and losing them after restart is documented behavior.

## Common Pitfalls

- **Don't double-release workspace refs** — `DeleteSession` already deletes refs. If `handleSessionRemove` explicitly calls `ReleaseWorkspace` *and* then `sessions.Delete`, the ref would be decremented twice. The safest approach: rely on `DeleteSession`'s existing cleanup for the release path; only add `AcquireWorkspace` on the create side.
- **Don't rebuild registry before recovery completes** — If the registry is rebuilt too early, workspace/cleanup might succeed while sessions are still being recovered. Rebuild must happen after the recovery pass.
- **Don't assume Source spec is available after restart** — Currently it's `{}` in DB. The persist-source-spec fix must be in T01 so T02's registry rebuild has the data.
- **Don't forget WorkspaceManager refcount** — If cleanup still flows through `WorkspaceManager.Cleanup`, its internal refcount must also be initialized. Alternative: have `handleWorkspaceCleanup` check DB refcount directly and bypass `WorkspaceManager.Cleanup`'s refcount gate.

## Skills Discovered

| Technology | Skill | Status |
|------------|-------|--------|
| Go | none relevant found | not installed |
| SQLite | none relevant | — |

## Sources

- `pkg/meta/workspace.go` — AcquireWorkspace/ReleaseWorkspace already implemented and tested, just never called from session lifecycle
- `pkg/meta/workspace_test.go` — Comprehensive tests proving the ref/trigger system works
- `pkg/ari/server.go` — workspace/cleanup reads registry.RefCount (volatile) not DB ref_count
- `cmd/agentd/main.go` — No registry rebuild after recovery pass
- `docs/plan/unified-modification-plan.md` — Identifies workspace refcount hardening as planned work
