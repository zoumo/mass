# Error Handling

## Pre-flight Check Errors

| Error | Cause | Resolution |
|-------|-------|------------|
| `daemon: not running` | Daemon not started | Tell user to start it; don't start yourself |
| Connection refused | `--socket` path wrong | Confirm socket path with user |

## Workspace Errors

| Error | Cause | Resolution |
|-------|-------|------------|
| Phase stuck at `pending` | Source prep slow (large repo clone) | Keep polling. If >5 min, ask user to check daemon logs |
| Phase becomes `error` | Invalid source: path missing, git URL unreachable, ref not found | `workspace delete` → fix config → recreate |
| Delete fails: "workspace has active agents" | Agentruns still attached | Stop+delete all agentruns first, then delete workspace |

## AgentRun Errors

| Error | Cause | Resolution |
|-------|-------|------------|
| Create fails: workspace not ready | Workspace still `pending` | Wait for `ready`, then retry |
| Create fails: agent not found | `--agent` name wrong | Use `agent get` to list available agents |
| Stuck in `creating` >2 min | Agent binary not installed or ACP handshake timed out | `agentrun get` to check errorMessage; ask user to verify agent binary |
| Prompt rejected: "not idle" | Agent not in `idle` state | If `running` → `cancel`, wait for idle; if `stopped`/`error` → `restart`, wait for idle |
| Enters `error` while working | Runtime crash, OOM, or shim died | `agentrun restart`. If keeps failing, check daemon logs |
| Agent `error` after daemon restart | Shim didn't survive | `agentrun restart` |
| Delete fails: "not stopped" | Agent still running or idle | `stop` first, then `delete` |

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

> For Task protocol-related errors, see **mass-pilot** skill.

## Full Rebuild

When partial recovery not possible, tear down and restart:

```bash
for agent in $(massctl agentrun get -w my-ws -o json | jq -r '.[].metadata.name'); do
  massctl agentrun stop $agent -w my-ws 2>/dev/null
  massctl agentrun delete $agent -w my-ws 2>/dev/null
done
massctl workspace delete my-ws
# Then rebuild with compose
massctl compose apply -f compose.yaml
```
