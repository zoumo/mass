---
id: T01
parent: S05
milestone: M007
key_files:
  - pkg/rpc/server_test.go
  - bin/workspace-mcp-server
  - cmd/agentdctl/helpers.go
  - pkg/agentd/agent_test.go
  - pkg/meta/store.go
  - pkg/workspace/manager.go
key_decisions:
  - Removed 6 unused helpers from cmd/agentdctl/helpers.go — parseLabels/splitComma/splitKeyValue/splitBy/trimSpace/isWhitespace had no callers
  - Removed unused createTestWorkspace from pkg/agentd/agent_test.go — defined but never called
  - Fixed British spellings via sed: initialise→initialize in meta/store.go and workspace/manager.go
  - Used golangci-lint --fix to auto-correct gci import ordering across 7 files
duration: 
verification_result: mixed
completed_at: 2026-04-09T22:17:29.340Z
blocker_discovered: false
---

# T01: Fixed StatusCreated→StatusIdle in server_test.go, built bin/workspace-mcp-server, and cleaned all pre-existing pkg/cmd lint issues (gci, misspell, unused) leaving only tests/integration/ errors for T02

**Fixed StatusCreated→StatusIdle in server_test.go, built bin/workspace-mcp-server, and cleaned all pre-existing pkg/cmd lint issues (gci, misspell, unused) leaving only tests/integration/ errors for T02**

## What Happened

Two primary T01 fixes: (1) replaced spec.StatusCreated with spec.StatusIdle at lines 230 and 277 of pkg/rpc/server_test.go — the StatusCreated constant was removed in M007 and renamed StatusIdle; (2) built bin/workspace-mcp-server from ./cmd/workspace-mcp-server (7.2 MB binary). Additionally fixed 18 pre-existing lint issues in ./pkg/... and ./cmd/... to meet the T01 verification bar: removed 6 unused helper functions from cmd/agentdctl/helpers.go, removed 1 unused createTestWorkspace from pkg/agentd/agent_test.go, fixed 3 British spelling instances (initialise→initialize) in pkg/meta/store.go and pkg/workspace/manager.go, and auto-fixed gci import ordering across 7 files. After all fixes, golangci-lint on ./pkg/... ./cmd/... returns 0 issues. Only tests/integration/ typecheck errors remain, which are T02's scope (deleted M007 API surface: ari.RoomCreateResult, ari.WorkspacePrepareResult, ari.SessionNewResult, etc.).

## Verification

grep -n StatusCreated pkg/rpc/server_test.go returns no output (both replaced); test -f bin/workspace-mcp-server exits 0 with 7.2 MB binary; golangci-lint run ./pkg/... ./cmd/... returns 0 issues in 5.2s; golangci-lint run ./... shows only tests/integration/ typecheck errors (T02 scope).

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `grep -n StatusCreated pkg/rpc/server_test.go` | 1 | ✅ pass — no StatusCreated references remain | 50ms |
| 2 | `test -f bin/workspace-mcp-server` | 0 | ✅ pass — 7.2 MB binary present | 10ms |
| 3 | `golangci-lint run ./pkg/... ./cmd/...` | 0 | ✅ pass — 0 issues | 5200ms |
| 4 | `golangci-lint run ./...` | 1 | ⚠️ partial — only tests/integration/ typecheck (T02 scope) | 6300ms |

## Deviations

Fixed pre-existing lint issues (gci formatting, misspell, unused functions) in ./pkg/... and ./cmd/... beyond the two stated fixes — necessary to meet the T01 verification bar of golangci-lint returning 0 issues on non-integration packages.

## Known Issues

golangci-lint run ./... still fails with typecheck errors in tests/integration/ referencing deleted M007 API types — these are T02's responsibility to rewrite.

## Files Created/Modified

- `pkg/rpc/server_test.go`
- `bin/workspace-mcp-server`
- `cmd/agentdctl/helpers.go`
- `pkg/agentd/agent_test.go`
- `pkg/meta/store.go`
- `pkg/workspace/manager.go`
