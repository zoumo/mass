# S01 Research: Auto-fix gci + gofumpt formatting (56 issues)

## Summary

This is a **light research** slice. The fix is a single built-in golangci-lint command. No manual edits, no ambiguity.

## Recommendation

Run `golangci-lint fmt ./...` — it applies both gci and gofumpt rewrites in one pass. Verify with `golangci-lint run ./...` and confirm zero gci/gofumpt findings remain.

## Implementation Landscape

### Tool availability

- `golangci-lint` v2.11.4 is installed at `/opt/homebrew/bin/golangci-lint`
- `gci` and `gofumpt` are **not** installed as standalone binaries — both are bundled inside golangci-lint v2 and invoked via `golangci-lint fmt`
- `golangci-lint fmt` subcommand is confirmed available and working

### Config (`.golangci.yml`)

Both formatters are enabled under the `formatters:` section:

```yaml
formatters:
  enable:
    - gofumpt     # drop-in replacement for gofmt, extra-rules: true
    - gci         # import ordering, custom-order: true
  settings:
    gci:
      custom-order: true
      sections:
        - standard
        - blank
        - dot
        - default
        - localmodule
    gofumpt:
      extra-rules: true
  exclusions:
    generated: lax
    paths:
      - third_party$
      - builtin$
      - examples$
```

### Current issue count

| Linter   | Issues |
|----------|--------|
| gci      | 50     |
| gofumpt  | 6      |
| **Total**| **56** |

### Files affected

`golangci-lint fmt --diff ./...` reports **67 files** needing changes (more than the 56 issue count because each file produces one issue diagnostic but may have multiple diff hunks):

```
cmd/agent-shim-cli/main.go
cmd/agent-shim/main.go
cmd/agentd/main.go
cmd/agentdctl/agent.go
cmd/agentdctl/daemon.go
cmd/agentdctl/main.go
cmd/agentdctl/room.go
cmd/agentdctl/workspace.go
cmd/room-mcp-server/main.go
internal/testutil/mockagent/main.go
pkg/agentd/agent_test.go
pkg/agentd/config.go
pkg/agentd/process.go
pkg/agentd/process_test.go
pkg/agentd/recovery_posture_test.go
pkg/agentd/recovery_test.go
pkg/agentd/runtimeclass.go
pkg/agentd/runtimeclass_test.go
pkg/agentd/session.go
pkg/agentd/session_test.go
pkg/agentd/shim_client.go
pkg/agentd/shim_client_test.go
pkg/ari/client.go
pkg/ari/client_test.go
pkg/ari/registry.go
pkg/ari/server.go
pkg/ari/server_test.go
pkg/events/envelope.go
pkg/meta/agent.go
pkg/meta/integration_test.go
pkg/meta/models.go
pkg/meta/room.go
pkg/meta/room_test.go
pkg/meta/session.go
pkg/meta/session_test.go
pkg/meta/store.go
pkg/meta/store_test.go
pkg/meta/workspace.go
pkg/meta/workspace_test.go
pkg/rpc/server.go
pkg/rpc/server_test.go
pkg/runtime/client.go
pkg/runtime/client_test.go
pkg/runtime/runtime.go
pkg/runtime/runtime_test.go
pkg/runtime/terminal.go
pkg/runtime/terminal_test.go
pkg/spec/config_test.go
pkg/spec/example_bundles_test.go
pkg/spec/state.go
pkg/spec/state_test.go
pkg/workspace/emptydir.go
pkg/workspace/emptydir_test.go
pkg/workspace/errors.go
pkg/workspace/git.go
pkg/workspace/git_test.go
pkg/workspace/handler.go
pkg/workspace/hook.go
pkg/workspace/hook_test.go
pkg/workspace/local.go
pkg/workspace/local_test.go
pkg/workspace/manager.go
pkg/workspace/manager_test.go
pkg/workspace/spec.go
pkg/workspace/spec_test.go
tests/integration/e2e_test.go
tests/integration/real_cli_test.go
tests/integration/restart_test.go
tests/integration/session_test.go
```

### What gci changes

Import block re-ordering to match the configured sections: `standard → blank → dot → default → localmodule`. Most violations are third-party imports placed before local module imports (or vice versa).

### What gofumpt changes

6 files have minor formatting issues beyond standard `gofmt` (e.g. blank lines within struct definitions, extra newlines, spacing around assignments):
- `pkg/meta/agent.go`
- `pkg/meta/room.go`
- `pkg/meta/session.go`
- `pkg/meta/workspace.go`
- `pkg/runtime/runtime_test.go`
- `pkg/spec/state.go`

## Task decomposition for planner

**Single task is sufficient:**

1. Run `golangci-lint fmt ./...` — rewrites all 67 affected files in-place
2. Verify: `golangci-lint run ./... 2>&1 | grep -E "\(gci\)|\(gofumpt\)"` — must show zero lines
3. Also verify the build still compiles: `go build ./...`
4. Commit the changes

No manual edits required. No risk of logic changes — these are pure whitespace/import-order rewrites.

## Constraints / notes

- `golangci-lint fmt` respects the config exclusion paths (`third_party$`, `builtin$`, `examples$`) automatically
- The tool is idempotent — running it twice produces no further changes
- `go build ./...` should be run after to confirm no accidental breakage (unlikely for import reordering)
- Other linter issues (errorlint, copyloopvar, etc.) are visible in `golangci-lint run` output but are out of scope for S01 — do NOT fix them here
