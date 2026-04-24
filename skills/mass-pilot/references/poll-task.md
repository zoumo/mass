# poll-task.sh

Poll a task until the agent completes it or an error/timeout occurs. Uses massctl task API.

## Usage

```bash
scripts/poll-task.sh <workspace> <agent-name> <task-id> [interval=10] [timeout=1800]
```

| Argument | Required | Default | Description |
|----------|----------|---------|-------------|
| `workspace` | yes | — | Workspace name |
| `agent-name` | yes | — | AgentRun name |
| `task-id` | yes | — | Task ID (from task create) |
| `interval` | no | `10` | Poll interval in seconds |
| `timeout` | no | `1800` | Max wait time in seconds |

## Logic

Each poll cycle:

1. Check agent state via `massctl agentrun get`
2. Get task via `massctl agentrun task get`, check `completed` field
3. Route:
   - `completed == true` → exit 0
   - Agent `error`/`stopped` → exit 2
   - Agent `idle` but not completed → re-prompt (max 2 times), then exit 1
   - Agent `running` → continue polling
   - Timeout reached → exit 3

## Exit Codes

| Code | Meaning | Suggested Action |
|------|---------|------------------|
| 0 | Task completed | Read `response.status` from task get |
| 1 | Agent idle, task not completed, retries exhausted | Manual inspection |
| 2 | Agent error/stopped | Restart agent or escalate |
| 3 | Timeout | Escalate to human |

## Example

```bash
# Poll task with ID task-abc123, default interval/timeout
scripts/poll-task.sh my-ws reviewer task-abc123

# Custom interval and timeout
scripts/poll-task.sh my-ws worker task-abc123 5 600
```
