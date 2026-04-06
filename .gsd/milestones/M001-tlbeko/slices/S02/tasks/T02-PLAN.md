---
estimated_steps: 25
estimated_files: 2
skills_used: []
---

# T02: Implement LocalHandler

Create LocalHandler that implements SourceHandler for SourceTypeLocal. Validates source.Local.Path exists and is a directory via os.Stat(), returns source.Local.Path directly (NOT targetDir) because local workspaces are unmanaged - agentd doesn't create or delete them.

## Steps

1. Create pkg/workspace/local.go with LocalHandler struct and NewLocalHandler() constructor (follow GitHandler pattern)
2. Implement Prepare(ctx context.Context, source Source, targetDir string) (string, error) method:
   - Check source.Type == SourceTypeLocal, return fmt.Errorf("workspace: LocalHandler cannot handle source type %q", source.Type) if not
   - Validate source.Local.Path exists via os.Stat(source.Local.Path)
   - If os.Stat returns error, return fmt.Errorf("workspace: local source path %q does not exist", source.Local.Path)
   - Check result.IsDir() is true, return fmt.Errorf("workspace: local source path %q is not a directory", source.Local.Path) if false
   - Return source.Local.Path as workspace path (CRITICAL: NOT targetDir)
3. Create pkg/workspace/local_test.go following git_test.go pattern:
   - TestLocalHandlerRejectsNonLocalSource: test with SourceTypeGit and SourceTypeEmptyDir, verify error contains "cannot handle source type"
   - TestLocalHandlerPathDoesNotExist: test with non-existent path, verify error contains "does not exist"
   - TestLocalHandlerPathIsFile: create a file with t.TempDir() + os.Create(), test with file path, verify error contains "not a directory"
   - TestLocalHandlerIntegration: use t.TempDir() as existing directory, verify Prepare returns the same path (NOT targetDir)
4. Run tests: go test ./pkg/workspace/... -v -count=1 -run Local

## Must-Haves

- [ ] LocalHandler struct with Prepare method signature matching SourceHandler interface
- [ ] Type mismatch returns fmt.Errorf with "workspace: LocalHandler cannot handle source type"
- [ ] Path doesn't exist returns fmt.Errorf with "workspace: local source path ... does not exist"
- [ ] Path is file (not directory) returns fmt.Errorf with "workspace: local source path ... is not a directory"
- [ ] Returns source.Local.Path (NOT targetDir parameter) - verified in TestLocalHandlerIntegration
- [ ] TestLocalHandlerRejectsNonLocalSource passes for git and emptyDir source types
- [ ] TestLocalHandlerPathDoesNotExist passes with appropriate error
- [ ] TestLocalHandlerPathIsFile passes with appropriate error
- [ ] TestLocalHandlerIntegration verifies correct return value (source.Local.Path)

## Inputs

- `pkg/workspace/handler.go`
- `pkg/workspace/spec.go`
- `pkg/workspace/git.go`
- `pkg/workspace/emptydir.go`

## Expected Output

- `pkg/workspace/local.go`
- `pkg/workspace/local_test.go`

## Verification

go test ./pkg/workspace/... -v -count=1 -run Local

## Observability Impact

none
