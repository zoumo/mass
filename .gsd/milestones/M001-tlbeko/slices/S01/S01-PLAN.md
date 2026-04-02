# S01: Workspace Spec + Git Handler

**Goal:** Define WorkspaceSpec types matching the design doc JSON schema and implement Git source handler that shells out to `git clone` with ref/depth support, defining SourceHandler interface for downstream handlers (EmptyDir/Local in S02).
**Demo:** After this: WorkspaceSpec types defined; Git clone works with ref/depth support

## Tasks
- [x] **T01: Defined WorkspaceSpec, Source discriminated union (git/emptyDir/local), Hook types with JSON parsing/validation matching design doc schema** — Define WorkspaceSpec, Source (discriminated union with type field), GitSource, EmptyDirSource, LocalSource, Hook, Hooks types with JSON tags matching design doc schema. Add SourceType constants (SourceTypeGit, SourceTypeEmptyDir, SourceTypeLocal) with IsValid() method. Implement ParseWorkspaceSpec(data []byte) and ValidateWorkspaceSpec(spec) functions. Implement custom UnmarshalJSON for Source to handle discriminated union cleanly. Follow pkg/spec/types.go pattern: JSON tags, typed constants, validation methods, doc comments.
  - Estimate: 1h
  - Files: pkg/workspace/spec.go, pkg/workspace/spec_test.go
  - Verify: go test ./pkg/workspace/... -run Spec
- [ ] **T02: Implement GitHandler with SourceHandler interface** — Define SourceHandler interface with Prepare(ctx context.Context, source Source, targetDir string) (workspacePath string, error). Implement GitHandler struct that shells out to git CLI via exec.CommandContext. Handle three clone modes: (1) default clone (no ref) — git clone URL targetDir with --single-branch for shallow clones, (2) ref clone (branch/tag) — git clone --branch ref --single-branch URL targetDir, (3) SHA clone — clone first then git checkout SHA in targetDir. Support depth option: add --depth N --single-branch flags. Error handling: git not found (exec.LookPath or exec error), clone failure (exit code), checkout failure (exit code). Wrap all errors with context (operation phase, URL, ref). Follow pkg/runtime/runtime.go exec pattern: CommandContext, working dir, error wrapping.
  - Estimate: 1h
  - Files: pkg/workspace/git.go, pkg/workspace/git_test.go
  - Verify: go test ./pkg/workspace/... -run Git
