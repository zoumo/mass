---
estimated_steps: 8
estimated_files: 1
skills_used: []
---

# T01: Remove unused ctx parameter from forkShim

The golangci-lint unparam linter reports that `ctx context.Context` is unused in `(*ProcessManager).forkShim` (pkg/agentd/process.go:398). The function intentionally avoids exec.CommandContext so that the shim process outlives the request context — the parameter was threaded through from the call site but never consumed.

**Steps:**
1. Read `pkg/agentd/process.go` around lines 148-160 (call site) and lines 392-400 (function signature).
2. Remove `ctx context.Context` from the function signature at line 398: change `func (m *ProcessManager) forkShim(ctx context.Context, session *meta.Session, rc *RuntimeClass, bundlePath, stateDir string) (*ShimProcess, error)` → `func (m *ProcessManager) forkShim(session *meta.Session, rc *RuntimeClass, bundlePath, stateDir string) (*ShimProcess, error)`.
3. Update the single call site at line 153: change `m.forkShim(ctx, session, runtimeClass, bundlePath, stateDir)` → `m.forkShim(session, runtimeClass, bundlePath, stateDir)`.
4. Run `go build ./...` — must exit 0.
5. Run `golangci-lint run ./... 2>&1 | grep -E '(misspell|unparam)'` — must produce no output.
6. Run `go test ./pkg/agentd/...` — must pass.

## Inputs

- ``pkg/agentd/process.go` — contains forkShim definition (line 398) and call site (line 153)`

## Expected Output

- ``pkg/agentd/process.go` — forkShim signature and call site updated to drop ctx parameter`

## Verification

go build ./... && golangci-lint run ./... 2>&1 | grep -E '(misspell|unparam)'; [ $? -eq 1 ] && echo 'PASS: zero findings' || echo 'FAIL: findings remain'
