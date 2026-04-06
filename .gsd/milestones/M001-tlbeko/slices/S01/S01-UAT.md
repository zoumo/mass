# S01: Workspace Spec + Git Handler — UAT

**Milestone:** M001-tlbeko
**Written:** 2026-04-02T17:39:28.886Z

# S01: Workspace Spec + Git Handler — UAT

**Milestone:** M001-tlbeko
**Written:** 2026-04-03

## UAT Type

- UAT mode: artifact-driven
- Why this mode is sufficient: This slice defines types and implements a handler with comprehensive test coverage. All behavior is verified through automated tests including integration tests with real git operations.

## Preconditions

- Go toolchain installed
- git CLI installed and available in PATH
- Network access for integration tests (github.com/octocat/Hello-World.git)
- Project repository cloned with dependencies

## Smoke Test

```bash
go test ./pkg/workspace/... -v -count=1
```

**Expected:** All tests pass (28 spec tests + 7 GitHandler unit tests + 6 GitHandler integration tests)

## Test Cases

### 1. Parse WorkspaceSpec JSON

1. Create JSON matching design doc schema with git source
2. Call `ParseWorkspaceSpec(data)`
3. **Expected:** Returns WorkspaceSpec with Type=SourceTypeGit, Git.URL populated, Ref/Depth if specified

### 2. Validate WorkspaceSpec

1. Parse spec with missing required fields (empty metadata.name, missing git.url)
2. Call `ValidateWorkspaceSpec(spec)`
3. **Expected:** Returns error describing missing field

### 3. Git Clone Default Branch

1. Create Source{Type: SourceTypeGit, Git: GitSource{URL: "https://github.com/octocat/Hello-World.git"}}
2. Call `GitHandler.Prepare(ctx, source, targetDir)` with temp targetDir
3. **Expected:** Returns targetDir path, .git directory exists, README file exists

### 4. Git Clone with Shallow Depth

1. Create Source with Depth: 1
2. Call Prepare, then run `git rev-list --count HEAD` in cloned repo
3. **Expected:** Clone succeeds, rev-list returns "1"

### 5. Git Clone with Branch Ref

1. Create Source with Ref: "test" (known branch in Hello-World repo)
2. Call Prepare, then run `git branch --show-current` in cloned repo
3. **Expected:** Clone succeeds, current branch is "test"

### 6. Git Clone with Commit SHA

1. Run `git ls-remote https://github.com/octocat/Hello-World.git HEAD` to get SHA
2. Create Source with Ref: commitSHA (40 hex chars)
3. Call Prepare, then run `git rev-parse HEAD` in cloned repo
4. **Expected:** Clone succeeds, checkout succeeds, HEAD matches requested SHA

### 7. Git Error Handling

1. Test with git not in PATH (set PATH to empty temp dir)
2. Call Prepare
3. **Expected:** Returns GitError{Phase: "lookup", Message: "git executable not found"}

4. Test with invalid URL (nonexistent repo)
5. Call Prepare
6. **Expected:** Returns GitError{Phase: "clone", ExitCode: non-zero}

## Edge Cases

### Empty URL

1. Create Source{Type: SourceTypeGit, Git: GitSource{URL: ""}}
2. Call Prepare
3. **Expected:** Returns error "URL is required"

### Invalid Source Type

1. Create Source{Type: SourceType("unknown")}
2. Call GitHandler.Prepare
3. **Expected:** Returns error "cannot handle source type"

### Malformed JSON

1. Parse WorkspaceSpec from invalid JSON bytes
2. **Expected:** Returns JSON syntax error

### Context Cancellation

1. Create context with `context.WithCancel`, cancel before Prepare
2. Call Prepare
3. **Expected:** Returns context.Canceled error

## Failure Signals

- Test failures indicate type definition bugs or handler logic errors
- GitError with Phase="lookup" indicates git CLI not available
- GitError with Phase="clone" indicates repository access failure (URL, auth, network)
- GitError with Phase="checkout" indicates commit SHA not found in cloned repo

## Not Proven By This UAT

- EmptyDirHandler and LocalHandler (deferred to S02)
- Hook execution (setup/teardown) (deferred to S03)
- WorkspaceManager lifecycle (Prepare/Cleanup orchestration, reference counting) (deferred to S04)
- Git authentication (SSH keys, tokens) - tests use public repos only
- Git bundled support (requires system git CLI)

## Notes for Tester

- Integration tests require network access to github.com
- Hello-World repository (octocat/Hello-World) is a small, stable test fixture
- If network unavailable, integration tests skip automatically (git not installed also skips)
- All unit tests run without network/git requirements
