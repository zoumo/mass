---
id: S01
parent: M001-tlbeko
milestone: M001-tlbeko
provides:
  - WorkspaceSpec type definitions matching design doc schema
  - Source discriminated union type with git/emptyDir/local variants
  - SourceHandler interface for downstream handlers (EmptyDir/Local in S02)
  - GitHandler implementation with ref/depth support
  - GitError structured error type for git failure diagnostics
requires:
  []
affects:
  - S02
  - S03
  - S04
key_files:
  - pkg/workspace/spec.go
  - pkg/workspace/spec_test.go
  - pkg/workspace/git.go
  - pkg/workspace/git_test.go
  - pkg/workspace/handler.go
key_decisions:
  - Custom UnmarshalJSON/MarshalJSON for Source discriminated union to cleanly handle type-based JSON parsing
  - GitError type with Phase field distinguishes lookup/clone/checkout failures for targeted remediation
  - Git clone runs from filepath.Dir(targetDir) (parent directory) since targetDir doesn't exist yet
  - --single-branch flag for all clones to minimize fetch time and disk usage
patterns_established:
  - Source discriminated union pattern with custom JSON marshaling for polymorphic workspace sources
  - SourceHandler interface pattern for polymorphic workspace preparation handlers
  - GitError structured error pattern with Phase field for failure diagnostics and Unwrap() for errors.Is/errors.As
  - SemVer validation pattern reuse from pkg/spec/config.go parseMajor
observability_surfaces:
  - GitError structured error with Phase field (lookup/clone/checkout) enables targeted failure diagnostics
drill_down_paths:
  - .gsd/milestones/M001-tlbeko/slices/S01/tasks/T01-SUMMARY.md
  - .gsd/milestones/M001-tlbeko/slices/S01/tasks/T02-SUMMARY.md
duration: ""
verification_result: passed
completed_at: 2026-04-02T17:39:28.886Z
blocker_discovered: false
---

# S01: Workspace Spec + Git Handler

**WorkspaceSpec types with discriminated union JSON marshaling and GitHandler with ref/depth clone support and structured GitError diagnostics**

## What Happened

This slice defined the foundational types for workspace provisioning and implemented the primary source handler for Git repositories.

**T01 (WorkspaceSpec Types):** Defined WorkspaceSpec, Source discriminated union (git/emptyDir/local), GitSource, EmptyDirSource, LocalSource, Hook, and Hooks types with JSON tags matching the design doc schema. Implemented SourceType constants with IsValid() and String() methods. Created custom UnmarshalJSON for Source to handle discriminated union cleanly - parse type field first, then unmarshal into appropriate concrete type. Also implemented MarshalJSON to produce correct JSON output based on active type. Added ParseWorkspaceSpec and ValidateWorkspaceSpec functions with comprehensive validation (SemVer major==0, metadata.name required, source.type valid, git.url required, local.path required and absolute, hook.command non-empty). All 28 spec tests pass covering parsing, marshaling, validation, and design doc examples.

**T02 (GitHandler):** Implemented GitHandler that shells out to git CLI via exec.CommandContext. Handles three clone modes: (1) default clone with --single-branch, (2) branch/tag ref clone with --branch flag, (3) commit SHA ref clone with post-clone checkout. Depth option adds --depth N --single-branch flags for shallow clones. Created GitError structured error type with Phase field (lookup/clone/checkout) for targeted failure diagnostics, implementing Unwrap() for errors.Is/errors.As compatibility. Key implementation fix: git clone runs from filepath.Dir(targetDir) (parent directory), not targetDir itself which doesn't exist yet. All unit tests (wrong source type, empty URL, git-not-found, GitError structure, GitError Unwrap, isCommitSHA, buildCloneArgs) and integration tests (default clone, shallow clone, branch ref, SHA ref, context cancellation, invalid URL) pass. Integration tests clone from github.com/octocat/Hello-World.git and verify .git directory, README file, shallow depth, branch selection, SHA checkout, and error phases.

**Deviations:** Fixed handler.go syntax error from T01 (interface return signature). Fixed git_test.go test bug (isCommitSHA test strings had incorrect lengths - labeled "39 chars" was 40, labeled "40 chars" was 41). These were bugs in the original implementation, not design deviations.

## Verification

All tests pass: go test ./pkg/workspace/... -v -count=1

**Spec tests (28 cases):** ParseWorkspaceSpec for all source types (git with ref/depth, emptyDir, local), custom UnmarshalJSON discriminated union handling, MarshalJSON round-trip for all source types, ValidateWorkspaceSpec for all required fields and error paths, SourceType.IsValid() and String() methods, complete design doc examples (Go project, hooks, no-hooks).

**GitHandler unit tests (7 groups):** wrong source type rejection, empty URL rejection, git-not-found handling, GitError structure and Error() method, GitError Unwrap for errors.Is/errors.As, isCommitSHA boundary cases, buildCloneArgs argument construction.

**GitHandler integration tests (6 cases):** Default clone (verified .git directory and README), shallow clone with depth=1 (verified git rev-list count=1), branch ref clone (verified git branch shows 'test'), commit SHA clone (verified git rev-parse matches requested SHA), context cancellation (verified context.Canceled error), invalid URL (verified GitError phase='clone' with non-zero exit code).

## Requirements Advanced

- R009 — WorkspaceSpec types and SourceHandler interface defined, enabling downstream WorkspaceManager implementation in S04

## Requirements Validated

- R010 — GitHandler integration tests pass with real git clone operations: default clone, shallow clone with depth, branch ref, commit SHA checkout, context cancellation, invalid URL error handling

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

Fixed handler.go syntax error (T01 created SourceHandler interface with syntax error `(workspacePath string, error)` instead of `(workspacePath string, err error)`). Fixed git_test.go test bug (isCommitSHA test strings had incorrect lengths - strings labeled as "39 chars" were actually 40 chars, strings labeled as "40 chars" were actually 41 chars). Corrected test strings to have actual lengths matching the test intent.

## Known Limitations

EmptyDir and Local handlers not implemented (deferred to S02). Hook execution not implemented (deferred to S03). WorkspaceManager with Prepare/Cleanup lifecycle and reference counting not implemented (deferred to S04). GitHandler requires git CLI installed on system (no bundled git support).

## Follow-ups

S02 will implement EmptyDirHandler and LocalHandler using the SourceHandler interface pattern established here. S03 will implement hook execution (sequential setup/teardown with failure handling). S04 will implement WorkspaceManager with Prepare/Cleanup lifecycle, hook execution orchestration, and reference counting.

## Files Created/Modified

- `pkg/workspace/spec.go` — WorkspaceSpec types with discriminated union Source type, custom JSON marshaling, validation functions
- `pkg/workspace/spec_test.go` — 28 test cases covering parsing, marshaling, validation, design doc examples
- `pkg/workspace/git.go` — GitHandler implementation with Prepare method, GitError structured error, clone modes (default/branch/SHA), depth support
- `pkg/workspace/git_test.go` — Unit tests (7 groups) and integration tests (6 cases) for GitHandler; fixed isCommitSHA test string length bug
- `pkg/workspace/handler.go` — SourceHandler interface definition; fixed syntax error in return signature
- `.gsd/DECISIONS.md` — Created with 4 architectural decisions from this slice
- `.gsd/KNOWLEDGE.md` — Created with 5 patterns and lessons learned from this slice
