# S03: Manual: misspell + unparam (17 issues)

**Goal:** Remove the single remaining misspell and unparam lint finding: drop the unused `ctx context.Context` parameter from `(*ProcessManager).forkShim` in `pkg/agentd/process.go`.
**Demo:** After this: golangci-lint run ./... shows no misspell or unparam findings.

## Tasks
- [x] **T01: Dropped unused ctx and rc parameters from forkShim, clearing all remaining unparam lint findings** — The golangci-lint unparam linter reports that `ctx context.Context` is unused in `(*ProcessManager).forkShim` (pkg/agentd/process.go:398). The function intentionally avoids exec.CommandContext so that the shim process outlives the request context — the parameter was threaded through from the call site but never consumed.

**Steps:**
1. Read `pkg/agentd/process.go` around lines 148-160 (call site) and lines 392-400 (function signature).
2. Remove `ctx context.Context` from the function signature at line 398: change `func (m *ProcessManager) forkShim(ctx context.Context, session *meta.Session, rc *RuntimeClass, bundlePath, stateDir string) (*ShimProcess, error)` → `func (m *ProcessManager) forkShim(session *meta.Session, rc *RuntimeClass, bundlePath, stateDir string) (*ShimProcess, error)`.
3. Update the single call site at line 153: change `m.forkShim(ctx, session, runtimeClass, bundlePath, stateDir)` → `m.forkShim(session, runtimeClass, bundlePath, stateDir)`.
4. Run `go build ./...` — must exit 0.
5. Run `golangci-lint run ./... 2>&1 | grep -E '(misspell|unparam)'` — must produce no output.
6. Run `go test ./pkg/agentd/...` — must pass.
  - Estimate: 10 minutes
  - Files: pkg/agentd/process.go
  - Verify: go build ./... && golangci-lint run ./... 2>&1 | grep -E '(misspell|unparam)'; [ $? -eq 1 ] && echo 'PASS: zero findings' || echo 'FAIL: findings remain'
