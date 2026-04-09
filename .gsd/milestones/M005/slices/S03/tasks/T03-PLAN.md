---
estimated_steps: 21
estimated_files: 5
skills_used: []
---

# T03: Migrate agentdctl CLI and daemon wiring

Three mechanical changes:

1. **`cmd/agentdctl/agent.go`** (new file) — Create `agentCmd` cobra command with subcommands mirroring the session.go pattern:
   - `agent create` — flags: `--room`, `--name`, `--workspace-id`, `--runtime-class`, `--description`, `--system-prompt`; calls `agent/create`; prints agentId
   - `agent list` — flags: `--room`, `--state`; calls `agent/list`; prints JSON
   - `agent status <agent-id>` — calls `agent/status`; prints JSON
   - `agent prompt <agent-id>` — flag: `--text`; calls `agent/prompt`; prints stop reason
   - `agent stop <agent-id>` — calls `agent/stop`
   - `agent delete <agent-id>` — calls `agent/delete`
   - `agent attach <agent-id>` — calls `agent/attach`; prints socket path
   - `agent cancel <agent-id>` — calls `agent/cancel`
   Follow the same patterns as session.go (getClient(), cobra.ExactArgs(1), JSON output via json.Marshal, error handling via cmd.ErrOrStderr()).

2. **`cmd/agentdctl/main.go`** — Replace `rootCmd.AddCommand(sessionCmd)` with `rootCmd.AddCommand(agentCmd)`. Remove any import of session.go symbols. The file `session.go` itself can be deleted (or left in place but with its init/var removed so it compiles without contributing to rootCmd). Simplest approach: delete session.go entirely.

3. **`cmd/agentdctl/daemon.go`** — Change health check from `session/list` to `agent/list`: replace `client.Call("session/list", SessionListParams{}, &SessionListResult{})` with `client.Call("agent/list", AgentListParams{}, &AgentListResult{})`.

4. **`cmd/agentd/main.go`** — Construct `AgentManager` after `SessionManager` and pass it to `ari.New()`:
   ```go
   agents := agentd.NewAgentManager(store)
   // update ari.New() call to include agents parameter
   srv := ari.New(manager, registry, sessions, agents, processes, runtimeClasses, cfg, store, cfg.Socket, cfg.WorkspaceRoot)
   ```
   Import `agentd` is already present. Just add the two lines and update the ari.New() call.

Note: `cmd/agentdctl/session.go` should be deleted so that `sessionCmd` is no longer defined and registered. Verify the file is removed and the build compiles cleanly.

## Inputs

- ``pkg/ari/types.go` — AgentCreateParams, AgentListParams, AgentListResult, AgentStatusParams, AgentStatusResult, AgentPromptParams, AgentPromptResult, AgentStopParams, AgentDeleteParams, AgentAttachParams, AgentAttachResult, AgentCancelParams (from T02)`
- ``pkg/agentd/agent.go` — agentd.NewAgentManager, agentd.AgentManager (from T01)`
- ``pkg/ari/server.go` — updated ari.New() signature (from T02)`
- ``cmd/agentdctl/session.go` — existing session CLI to mirror structure from`
- ``cmd/agentdctl/main.go` — rootCmd registration to update`
- ``cmd/agentdctl/daemon.go` — health check to update`
- ``cmd/agentd/main.go` — daemon entry point to update`

## Expected Output

- ``cmd/agentdctl/agent.go` — new file: agentCmd + 8 subcommands (create/list/status/prompt/stop/delete/attach/cancel)`
- ``cmd/agentdctl/main.go` — agentCmd registered, sessionCmd removed`
- ``cmd/agentdctl/daemon.go` — health check uses agent/list`
- ``cmd/agentd/main.go` — AgentManager constructed and passed to ari.New()`
- ``cmd/agentdctl/session.go` — deleted`

## Verification

go build ./... && go build -o /tmp/agentdctl ./cmd/agentdctl && /tmp/agentdctl agent --help && ! /tmp/agentdctl --help 2>&1 | grep -q 'session'
