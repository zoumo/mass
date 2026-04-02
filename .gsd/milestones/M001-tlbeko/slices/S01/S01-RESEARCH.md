# S01 (Workspace Spec + Git Handler) — Research

**Date:** 2026-04-03

## Summary

This slice defines the workspace spec Go types and implements the Git source handler. The design doc (`docs/design/workspace/workspace-spec.md`) is comprehensive and unambiguous — it specifies exact JSON shapes for source types (git, emptyDir, local) and hooks (setup, teardown). The codebase already has a clean pattern for spec types in `pkg/spec/types.go` (JSON tags, typed constants, validation functions) that this work should mirror in a new `pkg/workspace` package.

The Git handler shells out to `git` CLI rather than using a Go git library — the project has no go-git dependency and the exec pattern aligns with how `pkg/runtime` launches agent processes. The handler needs to support ref (branch/tag/SHA) and depth (shallow clone) options. Testing is straightforward using `git init` + `git commit` to create local bare repos as test fixtures.

This slice is the foundation for all downstream work — S02 (EmptyDir/Local), S03 (Hooks), S04 (Lifecycle), and S05 (ARI methods) all depend on the types and handler interface defined here.

## Recommendation

Create `pkg/workspace/` with three files:

1. **`spec.go`** — WorkspaceSpec types matching the design doc JSON schema. Use a discriminated union pattern for Source (type field + variant structs), same pattern as `spec.PermissionPolicy` for source type constants.
2. **`git.go`** — Git source handler that shells out to `git clone` with `--branch`, `--depth`, `--single-branch` flags. Returns the path to the cloned workspace directory.
3. **`spec_test.go` / `git_test.go`** — Testify suites matching the project's test conventions.

Keep the handler interface simple: a `SourceHandler` with `Prepare(ctx, spec, targetDir) (string, error)` — this lets S02 add EmptyDir/Local handlers implementing the same interface. Don't build the full WorkspaceManager in this slice; just the types and the Git handler that it will delegate to.

## Implementation Landscape

### Key Files

- `docs/design/workspace/workspace-spec.md` — Authoritative spec. Defines JSON shapes for source (git/emptyDir/local), hooks (setup/teardown), and the full workspace lifecycle.
- `pkg/spec/types.go` — Pattern to follow: JSON-tagged structs, typed string constants, `IsValid()` methods, doc comments. This is the gold standard for how spec types look in this codebase.
- `pkg/spec/config.go` — Pattern for parse/validate functions (`ParseConfig`, `ValidateConfig`). Workspace spec will need similar `ParseWorkspaceSpec` and `ValidateWorkspaceSpec`.
- `pkg/spec/config_test.go` — Pattern for tests: testify suites, temp dirs, helper functions like `validConfig()`, table-driven validation tests.
- `pkg/runtime/runtime.go` — Shows how `os/exec` is used: `exec.CommandContext`, env merging, working dir setup. The Git handler follows the same patterns.
- `go.mod` — Module: `github.com/open-agent-d/open-agent-d`. Go 1.24. No go-git dependency — confirms shelling out to `git` CLI.

### New Files (this slice creates)

- `pkg/workspace/spec.go` — WorkspaceSpec, Source, GitSource, EmptyDirSource, LocalSource, Hook, Hooks types + SourceType constants + ParseWorkspaceSpec + ValidateWorkspaceSpec
- `pkg/workspace/git.go` — GitHandler struct implementing source preparation via `git clone`
- `pkg/workspace/spec_test.go` — Spec parsing and validation tests
- `pkg/workspace/git_test.go` — Git handler tests using local bare repos

### Build Order

1. **Types first** (`spec.go`) — defines the data model that everything else depends on. Include parse (from JSON) and validate. This is zero-risk, pure data modeling.
2. **Git handler second** (`git.go`) — the risky part. Needs to handle: default clone, ref-specific clone (branch/tag/SHA), shallow clone (depth), and error cases (bad URL, bad ref, git not found). Define the `SourceHandler` interface here so S02 can implement it for EmptyDir/Local.
3. **Tests throughout** — spec tests confirm JSON round-trip and validation; git tests confirm clone behavior against local bare repos.

### Verification Approach

- `go test ./pkg/workspace/...` — all tests pass
- `go vet ./pkg/workspace/...` — no vet warnings
- Spec types round-trip through JSON marshal/unmarshal matching the design doc examples
- Git handler clones a local bare repo with default branch, specific ref, and shallow depth
- Git handler returns appropriate errors for invalid URLs and missing refs

## Constraints

- **No new dependencies.** The Git handler must use `os/exec` + `git` CLI. No go-git or similar. This matches project convention (runtime uses exec for agent processes).
- **Source type is a discriminated union in JSON** (`"type": "git"` / `"emptyDir"` / `"local"`). Go doesn't have native union types. Use the pattern: a Source struct with a Type field and optional variant fields (GitSource, EmptyDirSource, LocalSource as embedded or pointer fields). Custom UnmarshalJSON may be needed for clean validation.
- **Workspace directory naming** uses `ws-<id>` pattern under a configurable root (per agentd config). For this slice, the handler just needs a target directory path — the WorkspaceManager (S04) handles ID generation and root path management.
- **Hook types are defined in this slice** (they're part of the spec) but hook execution is S03's scope. Just define the types.

## Common Pitfalls

- **SHA refs with `--branch`** — `git clone --branch` works for branches and tags but not for commit SHAs. For SHA refs, clone first then `git checkout <sha>`. Need to detect whether ref looks like a SHA (40 or 7+ hex chars) or treat all refs uniformly with a clone-then-checkout approach.
- **`--single-branch` with `--depth`** — shallow clones should use `--single-branch` to avoid fetching history of other branches. But `--single-branch` without `--branch` tracks only the default branch, which is correct for the no-ref case.
- **macOS `/tmp` symlinks** — `os.MkdirTemp` returns paths under `/var/...` which is symlinked to `/private/var/...` on macOS. The existing test suite handles this with `filepath.EvalSymlinks` (see `config_test.go`). Follow the same pattern.
- **`git` not in PATH** — The handler should return a clear error if `git` is not found (exec.LookPath or just let exec.Command fail naturally with a descriptive wrap).

## Open Risks

- **Context cancellation during clone** — Long-running `git clone` operations should respect context cancellation via `exec.CommandContext`. This is straightforward but worth testing.
- **Auth for private repos** — The design doc doesn't mention auth. For this slice, support only public repos / repos accessible via ambient credentials (SSH keys, credential helpers). Private repo auth is a future concern.
