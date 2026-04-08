---
estimated_steps: 26
estimated_files: 2
skills_used: []
---

# T01: Wire session lifecycle to DB workspace refs and persist Source spec

## Description

The DB has `AcquireWorkspace`/`ReleaseWorkspace` methods (implemented and tested in `pkg/meta/workspace.go`) but the live session path never calls them. This task wires the acquire side into `handleSessionNew` and fixes `handleWorkspacePrepare` to persist the full Source spec.

## Steps

1. In `pkg/ari/server.go` `handleWorkspacePrepare`, serialize `p.Spec.Source` as JSON into the `meta.Workspace.Source` field before calling `store.CreateWorkspace`. Currently the Source field is omitted (defaults to `json.RawMessage("{}")` in CreateWorkspace). Use `json.Marshal(p.Spec.Source)` and set `workspace.Source = sourceJSON`.

2. In `pkg/ari/server.go` `handleSessionNew`, after successful `sessions.Create`, call `h.srv.store.AcquireWorkspace(ctx, p.WorkspaceId, sessionId)` to record the session→workspace ref in DB. Also call `h.srv.registry.Acquire(p.WorkspaceId, sessionId)` to keep the in-memory registry consistent. If AcquireWorkspace fails, log the error but don't fail the RPC (mirrors the pattern used in handleWorkspacePrepare for CreateWorkspace). The release side is already handled: `meta.DeleteSession` already deletes `workspace_refs` rows, and the trigger decrements `ref_count`. Do NOT add an explicit `ReleaseWorkspace` call in `handleSessionRemove` — that would double-release.

3. Add test `TestARISessionNewAcquiresWorkspaceRef` in `pkg/ari/server_test.go`: prepare a workspace, create a session via `session/new`, then query DB `store.GetWorkspace` and assert `RefCount == 1`. Create a second session on same workspace, assert `RefCount == 2`.

4. Add test `TestARISessionRemoveReleasesWorkspaceRef` in `pkg/ari/server_test.go`: prepare workspace, create session, assert `RefCount == 1`, then stop + remove the session, assert `RefCount == 0`.

5. Add test `TestARIWorkspacePrepareSourcePersisted` in `pkg/ari/server_test.go`: prepare a workspace with a git source spec, then query DB `store.GetWorkspace` and assert Source is not `{}` — it should contain the serialized Source with `type: "git"`.

## Must-Haves

- [ ] `handleWorkspacePrepare` persists Source spec (not `{}`) to DB
- [ ] `handleSessionNew` calls `store.AcquireWorkspace` after session creation
- [ ] `handleSessionNew` calls `registry.Acquire` to keep in-memory state consistent
- [ ] No explicit `ReleaseWorkspace` in `handleSessionRemove` (avoid double-release)
- [ ] 3 new tests pass proving ref acquire, ref release, and source persistence

## Verification

- `go test ./pkg/ari/... -count=1 -run 'TestARISessionNewAcquiresWorkspaceRef|TestARISessionRemoveReleasesWorkspaceRef|TestARIWorkspacePrepareSourcePersisted' -v` — all 3 PASS
- `go test ./pkg/ari/... -count=1` — all existing tests still pass
- `go vet ./pkg/ari/...` — clean

## Inputs

- `pkg/ari/server.go` — handleWorkspacePrepare and handleSessionNew need modification
- `pkg/ari/server_test.go` — existing test harness to extend
- `pkg/meta/workspace.go` — AcquireWorkspace/ReleaseWorkspace already implemented
- `pkg/meta/session.go` — DeleteSession already cleans up workspace_refs

## Expected Output

- `pkg/ari/server.go` — modified: Source serialization in handleWorkspacePrepare, AcquireWorkspace call in handleSessionNew
- `pkg/ari/server_test.go` — modified: 3 new test functions added

## Inputs

- ``pkg/ari/server.go` — handleWorkspacePrepare and handleSessionNew to modify`
- ``pkg/ari/server_test.go` — existing test harness to extend`
- ``pkg/meta/workspace.go` — AcquireWorkspace already implemented, called by new code`
- ``pkg/meta/session.go` — DeleteSession already deletes workspace_refs (no changes needed)`

## Expected Output

- ``pkg/ari/server.go` — Source serialization in handleWorkspacePrepare, AcquireWorkspace+registry.Acquire in handleSessionNew`
- ``pkg/ari/server_test.go` — 3 new test functions: TestARISessionNewAcquiresWorkspaceRef, TestARISessionRemoveReleasesWorkspaceRef, TestARIWorkspacePrepareSourcePersisted`

## Verification

go test ./pkg/ari/... -count=1 -run 'TestARISessionNewAcquiresWorkspaceRef|TestARISessionRemoveReleasesWorkspaceRef|TestARIWorkspacePrepareSourcePersisted' -v && go test ./pkg/ari/... -count=1 && go vet ./pkg/ari/...
