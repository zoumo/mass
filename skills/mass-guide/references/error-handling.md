# Error Handling

## Pre-flight Check Errors

| Error | Cause | Resolution |
|-------|-------|------------|
| `daemon: not running` | Daemon is not started | Inform the user to start it; do not start it yourself |
| Connection refused | `--socket` path is wrong | Confirm the socket path with the user |

## Workspace Errors

| Error | Cause | Resolution |
|-------|-------|------------|
| Phase stuck at `pending` | Source preparation is slow (large repo clone) | Keep polling. If it exceeds 5 minutes, ask the user to check daemon logs |
| Phase becomes `error` | Invalid source: path does not exist, git URL unreachable, ref not found | `workspace delete` → fix the config → recreate |
| Delete fails: "workspace has active agents" | There are agentruns still attached | Stop and delete all agentruns first, then delete the workspace |

## AgentRun Errors

| Error | Cause | Resolution |
|-------|-------|------------|
| Create fails: workspace not ready | Workspace is still `pending` | Wait for workspace to be `ready`, then retry |
| Create fails: agent not found | `--agent` name is wrong | Use `agent get` to view the list of available agents |
| Stuck in `creating` for over 2 minutes | Agent binary not installed or ACP handshake timed out | Use `agentrun get` to check errorMessage; ask the user to verify agent binary availability |
| Prompt rejected: "not idle" | Agent is not in `idle` state | If `running` → `cancel`, then wait for idle; if `stopped`/`error` → `restart`, then wait for idle |
| Enters `error` while working | Runtime crash, OOM, or shim process died | `agentrun restart`. If it keeps failing, check daemon logs |
| Agent `error` after daemon restart | Shim process did not survive | `agentrun restart` |
| Delete fails: "not stopped" | Agent is still running or idle | `stop` first, then `delete` |

## Decision Tree

```
Agent unresponsive?
├─ agentrun get <name> -w <ws>  to check state
├─ running for too long?
│  └─ cancel → wait for idle → re-prompt
├─ error?
│  └─ restart → wait for idle → re-prompt
├─ stopped?
│  └─ restart → wait for idle → re-prompt
├─ creating for over 2 minutes?
│  └─ has errorMessage → ask user to check agent binary
│  └─ no errorMessage → keep waiting
└─ idle but prompt has no effect?
   └─ stop → delete → recreate → re-prompt
```

> For Task protocol-related errors, see the **mass-pilot** skill.

## Full Rebuild

When partial recovery is not possible, tear everything down and start over:

```bash
for agent in $(massctl agentrun get -w my-ws -o json | jq -r '.[].metadata.name'); do
  massctl agentrun stop $agent -w my-ws 2>/dev/null
  massctl agentrun delete $agent -w my-ws 2>/dev/null
done
massctl workspace delete my-ws
# Then rebuild with compose
massctl compose apply -f compose.yaml
```
