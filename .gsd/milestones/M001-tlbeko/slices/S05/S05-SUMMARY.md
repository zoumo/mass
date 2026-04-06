---
id: S05
parent: M001-tlbeko
milestone: M001-tlbeko
provides:
  - ARI workspace/prepare method — accepts WorkspaceSpec, generates UUID, calls WorkspaceManager.Prepare, tracks in Registry, returns WorkspaceId/Path/Status
  - ARI workspace/list method — returns all tracked workspaces with WorkspaceId/Name/Path/Status/Refs
  - ARI workspace/cleanup method — validates RefCount=0, calls WorkspaceManager.Cleanup, removes from Registry
requires:
  - slice: S04
    provides: WorkspaceManager.Prepare/Cleanup methods with source handlers and ref counting
affects:
  []
key_files:
  - pkg/ari/types.go
  - pkg/ari/server.go
  - pkg/ari/registry.go
  - pkg/ari/server_test.go
key_decisions:
  - D004: github.com/google/uuid for workspace ID generation — RFC 4122 compliant UUIDs for workspace IDs
  - External test package (ari_test) pattern following pkg/rpc/server_test.go — enables testing without internal package access
  - Unix socket end-to-end test pattern — tests connect via actual socket using jsonrpc2.Conn for realistic verification
patterns_established:
  - ARI Registry pattern — thread-safe workspaceId → WorkspaceMeta mapping with Add/Get/List/Remove/Acquire/Release operations, RefCount prevents premature cleanup
  - ARI method routing pattern — workspace/prepare → manager.Prepare + UUID generation + registry.Add, workspace/list → registry.List + WorkspaceInfo conversion, workspace/cleanup → RefCount check + manager.Cleanup + registry.Remove
observability_surfaces:
  - WorkspaceError Phase field in JSON-RPC error responses identifies failure phase (prepare-source, prepare-hooks, cleanup-delete)
  - Registry Refs list tracks session references for debugging — Acquire/Release methods record sessionID
drill_down_paths:
  - .gsd/milestones/M001-tlbeko/slices/S05/tasks/T01-SUMMARY.md
  - .gsd/milestones/M001-tlbeko/slices/S05/tasks/T02-SUMMARY.md
  - .gsd/milestones/M001-tlbeko/slices/S05/tasks/T03-SUMMARY.md
duration: ""
verification_result: passed
completed_at: 2026-04-02T19:54:04.314Z
blocker_discovered: false
---

# S05: ARI Workspace Methods

**ARI workspace/* methods work end-to-end: prepare generates UUIDs, creates workspaces via WorkspaceManager, tracks in Registry; list returns all workspaces; cleanup validates refs and calls manager.Cleanup**

## What Happened

S05 implemented ARI workspace/* methods for declarative workspace provisioning via JSON-RPC:

**T01 (Types):** Created pkg/ari/types.go with 6 struct types following ARI spec exactly: WorkspacePrepareParams (Spec field reuses workspace.WorkspaceSpec), WorkspacePrepareResult (WorkspaceId, Path, Status), WorkspaceListParams (empty), WorkspaceListResult (Workspaces []WorkspaceInfo), WorkspaceInfo (WorkspaceId, Name, Path, Status, Refs), WorkspaceCleanupParams (WorkspaceId). All structs use camelCase JSON tags matching ARI spec field names.

**T02 (Server + Registry):** Created pkg/ari/registry.go with thread-safe Registry for workspaceId → WorkspaceMeta mapping (Id, Name, Path, Spec, Status, RefCount, Refs). Registry tracks session references via Refs list for debugging visibility. Created pkg/ari/server.go with JSON-RPC server routing workspace/prepare → manager.Prepare with UUID generation, workspace/list → registry.List, workspace/cleanup → manager.Cleanup with RefCount validation. Added github.com/google/uuid v1.6.0 dependency.

**T03 (Tests):** Created pkg/ari/server_test.go with 16 integration tests using Unix socket connections: EmptyDir/Git/Local source preparation, list (empty/populated), cleanup (success/failure with refs), error handling (invalid specs, nil params, nonexistent workspaces, unknown methods), full lifecycle round-trip. All tests pass end-to-end over JSON-RPC.

The slice completes the workspace provisioning pipeline: orchestrator calls workspace/prepare → ARI server generates UUID → WorkspaceManager creates workspace → Registry tracks metadata → workspace/list returns all workspaces → workspace/cleanup validates refs → WorkspaceManager deletes managed directories.

## Verification

Slice-level verification passed: go build ./pkg/ari/... compiles without error, go test ./pkg/ari/... -v passes all 16 integration tests covering workspace/prepare (EmptyDir/Git/Local sources), workspace/list (empty/populated), workspace/cleanup (success/failure with refs), error handling (invalid specs, nil params, nonexistent workspaces, unknown methods), and full lifecycle round-trip.

## Requirements Advanced

- R012 — Integration tests verify prepare/list/cleanup methods work end-to-end via JSON-RPC with all source types (EmptyDir, Git, Local), error handling, and lifecycle round-trip

## Requirements Validated

- R012 — All 16 integration tests pass (go test ./pkg/ari/... -v), covering workspace/prepare with EmptyDir/Git/Local sources, workspace/list, workspace/cleanup with ref validation, error handling, and full lifecycle

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

None — executed exactly as planned. Registry Acquire/Release methods include sessionID parameter for Refs tracking, which enhances observability beyond the plan's RefCount-only specification.

## Known Limitations

None.

## Follow-ups

None.

## Files Created/Modified

- `pkg/ari/types.go` — Request/response types for workspace/prepare, workspace/list, workspace/cleanup following ARI spec
- `pkg/ari/server.go` — JSON-RPC server with workspace/* method handlers wired to WorkspaceManager
- `pkg/ari/registry.go` — Thread-safe workspaceId → metadata registry with RefCount/Refs tracking
- `pkg/ari/server_test.go` — Integration tests for workspace/* methods via JSON-RPC (16 test cases)
