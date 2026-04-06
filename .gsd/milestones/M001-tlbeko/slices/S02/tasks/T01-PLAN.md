---
estimated_steps: 18
estimated_files: 2
skills_used: []
---

# T01: Implement EmptyDirHandler

Create EmptyDirHandler that implements SourceHandler for SourceTypeEmptyDir. Follows GitHandler pattern from pkg/workspace/git.go: type check, operation, return path.

## Steps

1. Create pkg/workspace/emptydir.go with EmptyDirHandler struct and NewEmptyDirHandler() constructor (follow GitHandler pattern)
2. Implement Prepare(ctx context.Context, source Source, targetDir string) (string, error) method:
   - Check source.Type == SourceTypeEmptyDir, return fmt.Errorf("workspace: EmptyDirHandler cannot handle source type %q", source.Type) if not
   - Create directory with os.MkdirAll(targetDir, 0755)
   - Return targetDir as workspace path (same as GitHandler returns targetDir)
3. Create pkg/workspace/emptydir_test.go following git_test.go pattern:
   - TestEmptyDirHandlerRejectsNonEmptyDirSource: test with SourceTypeGit and SourceTypeLocal, verify error contains "cannot handle source type"
   - TestEmptyDirHandlerIntegration: use t.TempDir() as parent, call Prepare, verify directory created with os.Stat()
4. Run tests: go test ./pkg/workspace/... -v -count=1 -run EmptyDir

## Must-Haves

- [ ] EmptyDirHandler struct with Prepare method signature matching SourceHandler interface
- [ ] Type mismatch returns fmt.Errorf with "workspace: EmptyDirHandler cannot handle source type"
- [ ] os.MkdirAll(targetDir, 0755) creates directory
- [ ] Returns targetDir (the created directory path)
- [ ] TestEmptyDirHandlerRejectsNonEmptyDirSource passes for git and local source types
- [ ] TestEmptyDirHandlerIntegration verifies directory exists after Prepare

## Inputs

- `pkg/workspace/handler.go`
- `pkg/workspace/spec.go`
- `pkg/workspace/git.go`

## Expected Output

- `pkg/workspace/emptydir.go`
- `pkg/workspace/emptydir_test.go`

## Verification

go test ./pkg/workspace/... -v -count=1 -run EmptyDir

## Observability Impact

none
