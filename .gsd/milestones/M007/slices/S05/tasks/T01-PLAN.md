---
estimated_steps: 4
estimated_files: 2
skills_used: []
---

# T01: Fix rpc/server_test.go StatusCreated reference + build workspace-mcp-server

Two small fixes needed before the full integration test rewrite:

1. `pkg/rpc/server_test.go` references `spec.StatusCreated` (deleted in M007 — renamed to `spec.StatusIdle`). Two lines at line 230 and 277. Replace both with `spec.StatusIdle`.

2. `bin/workspace-mcp-server` is missing from `bin/`. Run `go build -o bin/workspace-mcp-server ./cmd/workspace-mcp-server` to produce the binary.

After both fixes, `golangci-lint run ./...` must return 0 issues.

## Inputs

- `pkg/rpc/server_test.go`
- `cmd/workspace-mcp-server/main.go`

## Expected Output

- `pkg/rpc/server_test.go`
- `bin/workspace-mcp-server`

## Verification

golangci-lint run ./... && test -f bin/workspace-mcp-server
