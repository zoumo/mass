---
estimated_steps: 2
estimated_files: 69
skills_used: []
---

# T01: Apply gci + gofumpt auto-fixes via golangci-lint fmt

Run golangci-lint fmt ./... to rewrite all 67 affected files in-place. This fixes all 50 gci import-ordering violations and 6 gofumpt whitespace violations in one idempotent pass. No manual edits are needed — the formatter applies the rules from .golangci.yml (standard → blank → dot → default → localmodule import order; gofumpt extra-rules).

The fix is purely cosmetic: import block reordering and minor whitespace adjustments. No logic, types, or API signatures change.

## Inputs

- `.golangci.yml`
- `cmd/agentd/main.go`
- `pkg/agentd/session.go`
- `pkg/meta/agent.go`
- `pkg/spec/state.go`

## Expected Output

- `cmd/agent-shim-cli/main.go`
- `cmd/agent-shim/main.go`
- `cmd/agentd/main.go`
- `cmd/agentdctl/agent.go`
- `cmd/agentdctl/daemon.go`
- `cmd/agentdctl/main.go`
- `cmd/agentdctl/room.go`
- `cmd/agentdctl/workspace.go`
- `cmd/room-mcp-server/main.go`
- `internal/testutil/mockagent/main.go`
- `pkg/agentd/agent_test.go`
- `pkg/agentd/config.go`
- `pkg/agentd/process.go`
- `pkg/agentd/session.go`
- `pkg/meta/agent.go`
- `pkg/meta/room.go`
- `pkg/meta/session.go`
- `pkg/meta/workspace.go`
- `pkg/runtime/runtime_test.go`
- `pkg/spec/state.go`

## Verification

golangci-lint run ./... 2>&1 | grep -E '\(gci\)|\(gofumpt\)'; test $? -ne 0 && echo 'FAIL: findings remain' || echo 'PASS: zero gci/gofumpt findings'
