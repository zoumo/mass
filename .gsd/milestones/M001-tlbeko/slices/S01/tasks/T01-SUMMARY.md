---
id: T01
parent: S01
milestone: M001-tlbeko
provides: []
requires: []
affects: []
key_files: ["pkg/workspace/spec.go", "pkg/workspace/spec_test.go"]
key_decisions: ["Custom UnmarshalJSON for Source discriminated union to cleanly handle type-based parsing", "MarshalJSON for Source to produce correct JSON output based on active type", "Reused parseMajor pattern from pkg/spec/config.go for SemVer validation"]
patterns_established: []
drill_down_paths: []
observability_surfaces: []
duration: ""
verification_result: "All 28 tests pass with go test ./pkg/workspace/... -run Spec. Tests cover: ParseWorkspaceSpec for all source types (git with ref/depth, emptyDir, local), custom UnmarshalJSON discriminated union handling, MarshalJSON round-trip for all source types, ValidateWorkspaceSpec for all required fields and error paths, SourceType.IsValid() and String() methods, complete design doc examples (Go project, hooks, no-hooks)"
completed_at: 2026-04-02T17:17:08.147Z
blocker_discovered: false
---

# T01: Defined WorkspaceSpec, Source discriminated union (git/emptyDir/local), Hook types with JSON parsing/validation matching design doc schema

> Defined WorkspaceSpec, Source discriminated union (git/emptyDir/local), Hook types with JSON parsing/validation matching design doc schema

## What Happened
---
id: T01
parent: S01
milestone: M001-tlbeko
key_files:
  - pkg/workspace/spec.go
  - pkg/workspace/spec_test.go
key_decisions:
  - Custom UnmarshalJSON for Source discriminated union to cleanly handle type-based parsing
  - MarshalJSON for Source to produce correct JSON output based on active type
  - Reused parseMajor pattern from pkg/spec/config.go for SemVer validation
duration: ""
verification_result: passed
completed_at: 2026-04-02T17:17:08.148Z
blocker_discovered: false
---

# T01: Defined WorkspaceSpec, Source discriminated union (git/emptyDir/local), Hook types with JSON parsing/validation matching design doc schema

**Defined WorkspaceSpec, Source discriminated union (git/emptyDir/local), Hook types with JSON parsing/validation matching design doc schema**

## What Happened

Read design doc (docs/design/workspace/workspace-spec.md) and existing pkg/spec pattern (types.go, config.go, config_test.go) to understand the type structure and validation patterns. Created pkg/workspace/spec.go with all required types: WorkspaceSpec (top-level), WorkspaceMetadata, Source (discriminated union), GitSource, EmptyDirSource, LocalSource, Hook, Hooks. Implemented SourceType constants (SourceTypeGit, SourceTypeEmptyDir, SourceTypeLocal) with IsValid() and String() methods following the PermissionPolicy pattern.

Implemented custom UnmarshalJSON for Source to handle discriminated union cleanly - first parse type field, then unmarshal into appropriate concrete type. Also implemented MarshalJSON to produce correct JSON output based on active type (emptyDir only outputs type field, git/local output type plus their specific fields).

Added ParseWorkspaceSpec(data []byte) and ValidateWorkspaceSpec(spec) functions. Validation checks: oarVersion SemVer with major==0, metadata.name non-empty, source.type valid, git source url required, local source path required and absolute, hook commands non-empty.

Created comprehensive test suite in pkg/workspace/spec_test.go with 28 test cases covering: parsing valid specs, git/emptyDir/local source types, hooks, malformed JSON, unknown source types, marshaling round-trips, all validation error paths, and the complete example from design doc.

## Verification

All 28 tests pass with go test ./pkg/workspace/... -run Spec. Tests cover: ParseWorkspaceSpec for all source types (git with ref/depth, emptyDir, local), custom UnmarshalJSON discriminated union handling, MarshalJSON round-trip for all source types, ValidateWorkspaceSpec for all required fields and error paths, SourceType.IsValid() and String() methods, complete design doc examples (Go project, hooks, no-hooks)

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go test ./pkg/workspace/... -run Spec -v` | 0 | ✅ pass | 1140ms |


## Deviations

None.

## Known Issues

None.

## Files Created/Modified

- `pkg/workspace/spec.go`
- `pkg/workspace/spec_test.go`


## Deviations
None.

## Known Issues
None.
