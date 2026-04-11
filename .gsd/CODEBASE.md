# Codebase Map

Generated: 2026-04-11T15:21:33Z | Files: 125 | Described: 0/125
<!-- gsd:codebase-meta {"generatedAt":"2026-04-11T15:21:33Z","fingerprint":"28693cebae20843166587508480186e2a3edc88e","fileCount":125,"truncated":false} -->

### (root)/
- `.gitignore`
- `.golangci.yaml`
- `AGENTS.md`
- `CLAUDE.md`
- `go.mod`
- `go.sum`
- `Makefile`
- `README.md`

### cmd/agentd/
- `cmd/agentd/main.go`

### cmd/agentd/subcommands/
- `cmd/agentd/subcommands/root.go`

### cmd/agentd/subcommands/server/
- `cmd/agentd/subcommands/server/command.go`

### cmd/agentd/subcommands/shim/
- `cmd/agentd/subcommands/shim/command.go`

### cmd/agentd/subcommands/workspacemcp/
- `cmd/agentd/subcommands/workspacemcp/command.go`

### cmd/agentdctl/
- `cmd/agentdctl/main.go`

### cmd/agentdctl/subcommands/
- `cmd/agentdctl/subcommands/root.go`

### cmd/agentdctl/subcommands/agent/
- `cmd/agentdctl/subcommands/agent/command.go`

### cmd/agentdctl/subcommands/agentrun/
- `cmd/agentdctl/subcommands/agentrun/command.go`

### cmd/agentdctl/subcommands/cliutil/
- `cmd/agentdctl/subcommands/cliutil/cliutil.go`

### cmd/agentdctl/subcommands/daemon/
- `cmd/agentdctl/subcommands/daemon/command.go`

### cmd/agentdctl/subcommands/shim/
- `cmd/agentdctl/subcommands/shim/command.go`

### cmd/agentdctl/subcommands/workspace/
- `cmd/agentdctl/subcommands/workspace/command.go`

### cmd/agentdctl/subcommands/workspace/create/
- `cmd/agentdctl/subcommands/workspace/create/command.go`
- `cmd/agentdctl/subcommands/workspace/create/empty.go`
- `cmd/agentdctl/subcommands/workspace/create/file.go`
- `cmd/agentdctl/subcommands/workspace/create/git.go`
- `cmd/agentdctl/subcommands/workspace/create/local.go`

### docs/
- `docs/CONVENTIONS.md`
- `docs/DECISIONS.md`

### docs/design/
- `docs/design/contract-convergence.md`
- `docs/design/README.md`
- `docs/design/roadmap.md`

### docs/design/agentd/
- `docs/design/agentd/agentd.md`
- `docs/design/agentd/ari-spec.md`

### docs/design/runtime/
- `docs/design/runtime/agent-shim.md`
- `docs/design/runtime/config-spec.md`
- `docs/design/runtime/design.md`
- `docs/design/runtime/runtime-spec.md`
- `docs/design/runtime/shim-rpc-spec.md`
- `docs/design/runtime/why-no-runa.md`

### docs/design/workspace/
- `docs/design/workspace/workspace-spec.md`

### docs/manual/
- `docs/manual/room-validation-runbook.md`

### docs/plan/
- `docs/plan/cli-consolidation.md`

### docs/research/
- `docs/research/a2a-protocol.md`
- `docs/research/acpx.md`
- `docs/research/containerd.md`

### docs/research/acp/
- `docs/research/acp/2026-04-10-acp-concurrent-prompt-behavior.md`
- `docs/research/acp/acp-protocol.md`
- `docs/research/acp/protocol-overview.md`

### docs/research/oci/
- `docs/research/oci/image-spec.md`
- `docs/research/oci/runc.md`
- `docs/research/oci/runtime-spec.md`

### docs/review/
- `docs/review/2026-04-11-workspace-cli-consolidation-review.md`

### internal/testutil/mockagent/
- `internal/testutil/mockagent/main.go`

### pkg/agentd/
- `pkg/agentd/agent_test.go`
- `pkg/agentd/agent.go`
- `pkg/agentd/options.go`
- `pkg/agentd/process_test.go`
- `pkg/agentd/process.go`
- `pkg/agentd/recovery_posture_test.go`
- `pkg/agentd/recovery_posture.go`
- `pkg/agentd/recovery_test.go`
- `pkg/agentd/recovery.go`
- `pkg/agentd/runtimeclass_test.go`
- `pkg/agentd/runtimeclass.go`
- `pkg/agentd/shim_boundary_test.go`
- `pkg/agentd/shim_client_test.go`
- `pkg/agentd/shim_client.go`

### pkg/ari/
- `pkg/ari/client_test.go`
- `pkg/ari/client.go`
- `pkg/ari/registry_test.go`
- `pkg/ari/registry.go`
- `pkg/ari/server_test.go`
- `pkg/ari/server.go`
- `pkg/ari/types.go`

### pkg/events/
- `pkg/events/envelope.go`
- `pkg/events/log_test.go`
- `pkg/events/log.go`
- `pkg/events/translator_test.go`
- `pkg/events/translator.go`
- `pkg/events/types.go`

### pkg/meta/
- `pkg/meta/agent_test.go`
- `pkg/meta/agent.go`
- `pkg/meta/models.go`
- `pkg/meta/runtime_test.go`
- `pkg/meta/runtime.go`
- `pkg/meta/store_test.go`
- `pkg/meta/store.go`
- `pkg/meta/workspace_test.go`
- `pkg/meta/workspace.go`

### pkg/rpc/
- `pkg/rpc/server_internal_test.go`
- `pkg/rpc/server_test.go`
- `pkg/rpc/server.go`

### pkg/runtime/
- `pkg/runtime/client_test.go`
- `pkg/runtime/client.go`
- `pkg/runtime/runtime_test.go`
- `pkg/runtime/runtime.go`

### pkg/spec/
- `pkg/spec/config_test.go`
- `pkg/spec/config.go`
- `pkg/spec/example_bundles_test.go`
- `pkg/spec/maxsockpath_darwin.go`
- `pkg/spec/maxsockpath_linux.go`
- `pkg/spec/state_test.go`
- `pkg/spec/state_types.go`
- `pkg/spec/state.go`
- `pkg/spec/types.go`

### pkg/workspace/
- `pkg/workspace/emptydir_test.go`
- `pkg/workspace/emptydir.go`
- `pkg/workspace/errors.go`
- `pkg/workspace/git_test.go`
- `pkg/workspace/git.go`
- `pkg/workspace/handler.go`
- `pkg/workspace/hook_test.go`
- `pkg/workspace/hook.go`
- `pkg/workspace/local_test.go`
- `pkg/workspace/local.go`
- `pkg/workspace/manager_test.go`
- `pkg/workspace/manager.go`
- `pkg/workspace/spec_test.go`
- `pkg/workspace/spec.go`

### tests/integration/
- `tests/integration/concurrent_test.go`
- `tests/integration/e2e_test.go`
- `tests/integration/real_cli_test.go`
- `tests/integration/restart_test.go`
- `tests/integration/runtime_test.go`
- `tests/integration/session_test.go`
