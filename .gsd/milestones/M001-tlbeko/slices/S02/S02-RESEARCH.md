# S02 — Research

**Date:** 2026-04-03

## Summary

Slice S02 implements two additional SourceHandler implementations following the pattern established in S01: EmptyDirHandler creates managed empty directories, and LocalHandler validates existing host paths. This is straightforward work - the SourceHandler interface is defined, the Source discriminated union types (EmptyDirSource, LocalSource) already exist in spec.go, and GitHandler provides a clear reference implementation. The key semantic difference is that LocalHandler returns the source.Local.Path directly (not targetDir) since local workspaces use unmanaged host directories.

## Recommendation

Implement both handlers in separate files (emptydir.go, local.go) following GitHandler's pattern. Each handler: (1) checks source.Type matches, (2) performs source-specific logic, (3) returns workspace path. Add corresponding test files with unit tests for validation/error cases and integration tests for actual directory operations. The LocalHandler semantic is critical: it returns source.Local.Path, not targetDir, because local workspaces are unmanaged.

## Implementation Landscape

### Key Files

- `pkg/workspace/handler.go` — SourceHandler interface (already defined, no changes needed)
- `pkg/workspace/spec.go` — EmptyDirSource and LocalSource types already defined; validation already requires local.path to be absolute and non-empty
- `pkg/workspace/git.go` — Reference implementation pattern: type check, validation, operation, return path
- `pkg/workspace/git_test.go` — Test pattern: wrong source type rejection, validation errors, integration tests with t.TempDir()
- `docs/design/workspace/workspace-spec.md` — Defines semantics: EmptyDir creates managed directory, Local validates existing path and does NOT manage it (cleanup won't delete)

### Build Order

1. **EmptyDirHandler** (lower risk) — Create emptydir.go with NewEmptyDirHandler() and Prepare() that calls os.MkdirAll(targetDir, 0755). Add emptydir_test.go with unit tests (wrong type rejection) and integration tests (directory creation verification).

2. **LocalHandler** (semantic nuance) — Create local.go with NewLocalHandler() and Prepare() that validates source.Local.Path exists via os.Stat(), checks it's a directory, returns source.Local.Path (NOT targetDir). Add local_test.go with unit tests (wrong type, path doesn't exist, path is file not directory) and integration tests (existing directory validation).

3. **Verification** — Run `go test ./pkg/workspace/... -v -count=1` to verify all tests pass alongside existing S01 tests.

### Verification Approach

```bash
go test ./pkg/workspace/... -v -count=1
```

Expected: All S01 tests (28 spec + 7 unit + 6 integration) pass, plus new S02 tests:
- EmptyDirHandler: wrong type rejection, successful directory creation
- LocalHandler: wrong type rejection, path doesn't exist, path is file not directory, successful validation

## Constraints

- **Local workspace semantics**: LocalHandler must return source.Local.Path, not targetDir. Local workspaces are unmanaged - agentd doesn't create or delete them.
- **EmptyDir semantic purity**: Just create empty directory - no git init. If agent needs git, it runs `git init` itself (design doc explicitly states this).
- **Error handling**: Follow GitHandler's pattern - return fmt.Errorf with "workspace:" prefix for validation errors, no custom error type needed (no multi-phase operations like git).
- **Directory permissions**: EmptyDir should use 0755 (rwxr-xr-x) - standard directory permissions allowing owner write, group/others read/execute.

## Common Pitfalls

- **LocalHandler returning targetDir** — Must return source.Local.Path. The WorkspaceManager (S04) needs the actual workspace path, which for local sources is the existing host directory, not the generated targetDir parameter.
- **EmptyDir creating nested directories** — os.MkdirAll handles this, but test with nested paths to verify behavior.
- **Local path validation redundancy** — spec.ValidateWorkspaceSpec already checks path is absolute and non-empty. LocalHandler only needs to check path exists and is a directory (not a file).
- **Testing LocalHandler with relative paths** — Tests should use absolute paths. spec.go validation already rejects relative paths, so LocalHandler tests focus on existence/directory checks.