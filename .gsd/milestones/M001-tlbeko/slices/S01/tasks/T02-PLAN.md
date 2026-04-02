---
estimated_steps: 1
estimated_files: 2
skills_used: []
---

# T02: Implement GitHandler with SourceHandler interface

Define SourceHandler interface with Prepare(ctx context.Context, source Source, targetDir string) (workspacePath string, error). Implement GitHandler struct that shells out to git CLI via exec.CommandContext. Handle three clone modes: (1) default clone (no ref) — git clone URL targetDir with --single-branch for shallow clones, (2) ref clone (branch/tag) — git clone --branch ref --single-branch URL targetDir, (3) SHA clone — clone first then git checkout SHA in targetDir. Support depth option: add --depth N --single-branch flags. Error handling: git not found (exec.LookPath or exec error), clone failure (exit code), checkout failure (exit code). Wrap all errors with context (operation phase, URL, ref). Follow pkg/runtime/runtime.go exec pattern: CommandContext, working dir, error wrapping.

## Inputs

- ``pkg/workspace/spec.go``
- ``pkg/runtime/runtime.go``

## Expected Output

- ``pkg/workspace/git.go``
- ``pkg/workspace/git_test.go``

## Verification

go test ./pkg/workspace/... -run Git

## Observability Impact

Signals added: GitHandler returns structured errors with phase (clone/checkout), URL, ref, and wrapped underlying error (exec.ExitError with exit code). Context cancellation during long clone operations surfaces as ctx.Err(). Future agent inspects: error type contains all context fields; test suite verifies error messages for git-not-found, clone-failed, checkout-failed cases.
