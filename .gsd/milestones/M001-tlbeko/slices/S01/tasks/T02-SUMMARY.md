---
id: T02
parent: S01
milestone: M001-tlbeko
provides: []
requires: []
affects: []
key_files: ["pkg/workspace/git.go", "pkg/workspace/git_test.go", "pkg/workspace/handler.go"]
key_decisions: ["Using filepath.Dir(targetDir) as working directory for git clone to allow git to create the target directory", "GitError type with Phase field distinguishes lookup/clone/checkout failures for targeted remediation", "Using --single-branch flag for all clones to minimize fetch time"]
patterns_established: []
drill_down_paths: []
observability_surfaces: []
duration: ""
verification_result: "go test ./pkg/workspace/... -run Git passed all unit tests (7 test groups: wrong source type, empty URL, git-not-found, GitError structure, GitError Unwrap, isCommitSHA, buildCloneArgs) and integration tests (default clone, shallow clone, branch ref, SHA ref, context cancellation, invalid URL). Integration tests cloned from github.com/octocat/Hello-World.git and verified .git directory, README file, shallow clone depth, branch selection, SHA checkout, and error phases."
completed_at: 2026-04-02T17:34:17.277Z
blocker_discovered: false
---

# T02: Implemented GitHandler with Prepare method for cloning git repos with ref/depth support and structured GitError for agent inspection.

> Implemented GitHandler with Prepare method for cloning git repos with ref/depth support and structured GitError for agent inspection.

## What Happened
---
id: T02
parent: S01
milestone: M001-tlbeko
key_files:
  - pkg/workspace/git.go
  - pkg/workspace/git_test.go
  - pkg/workspace/handler.go
key_decisions:
  - Using filepath.Dir(targetDir) as working directory for git clone to allow git to create the target directory
  - GitError type with Phase field distinguishes lookup/clone/checkout failures for targeted remediation
  - Using --single-branch flag for all clones to minimize fetch time
duration: ""
verification_result: passed
completed_at: 2026-04-02T17:34:17.278Z
blocker_discovered: false
---

# T02: Implemented GitHandler with Prepare method for cloning git repos with ref/depth support and structured GitError for agent inspection.

**Implemented GitHandler with Prepare method for cloning git repos with ref/depth support and structured GitError for agent inspection.**

## What Happened

Implemented GitHandler that shells out to git CLI via exec.CommandContext, following the pkg/runtime/runtime.go exec pattern. The handler supports three clone modes: (1) default clone with --single-branch, (2) branch/tag ref clone with --branch flag, (3) SHA ref clone with post-clone checkout. Depth option adds --depth N --single-branch flags for shallow clones. Error handling wraps failures with structured GitError containing phase (lookup/clone/checkout), URL, ref, exit code, and underlying error. Context cancellation surfaces as ctx.Err(). Key implementation fix: git clone runs from parent directory of targetDir (filepath.Dir(targetDir)), not inside targetDir which doesn't exist yet.

## Verification

go test ./pkg/workspace/... -run Git passed all unit tests (7 test groups: wrong source type, empty URL, git-not-found, GitError structure, GitError Unwrap, isCommitSHA, buildCloneArgs) and integration tests (default clone, shallow clone, branch ref, SHA ref, context cancellation, invalid URL). Integration tests cloned from github.com/octocat/Hello-World.git and verified .git directory, README file, shallow clone depth, branch selection, SHA checkout, and error phases.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go test ./pkg/workspace/... -run Git` | 0 | ✅ pass | 13230ms |


## Deviations

Fixed handler.go syntax error (T01 created SourceHandler interface with syntax error `(workspacePath string, error)` instead of `(workspacePath string, err error)`); removed duplicate interface definition from git.go (T01 already defined it in handler.go); fixed cloneCmd.Dir assignment to use parent directory (filepath.Dir(targetDir)) instead of targetDir itself.

## Known Issues

None. All tests pass, including integration tests with real git clone operations.

## Files Created/Modified

- `pkg/workspace/git.go`
- `pkg/workspace/git_test.go`
- `pkg/workspace/handler.go`


## Deviations
Fixed handler.go syntax error (T01 created SourceHandler interface with syntax error `(workspacePath string, error)` instead of `(workspacePath string, err error)`); removed duplicate interface definition from git.go (T01 already defined it in handler.go); fixed cloneCmd.Dir assignment to use parent directory (filepath.Dir(targetDir)) instead of targetDir itself.

## Known Issues
None. All tests pass, including integration tests with real git clone operations.
