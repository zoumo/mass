# poll-task.sh

Poll a task file until the agent completes it or an error/timeout occurs.

## Usage

```bash
scripts/poll-task.sh <workspace> <agent-name> <task-path> [interval=10] [timeout=1800]
```

| Argument | Required | Default | Description |
|----------|----------|---------|-------------|
| `workspace` | yes | — | Workspace name |
| `agent-name` | yes | — | AgentRun name |
| `task-path` | yes | — | Path to the task JSON file |
| `interval` | no | `10` | Poll interval in seconds |
| `timeout` | no | `1800` | Max wait time in seconds |

## Logic

Each poll cycle:

1. Check agent state via `massctl agentrun get`
2. Read task file, check `completed` field
3. Route:
   - `completed == true` → exit 0
   - Agent `error`/`stopped` → exit 2
   - Agent `idle` but not completed → re-prompt (max 2 times), then exit 1
   - Agent `running` → continue polling
   - Timeout reached → exit 3

## Exit Codes

| Code | Meaning | Suggested Action |
|------|---------|------------------|
| 0 | Task completed | Read `response.status` for routing |
| 1 | Agent idle, task not completed, re-prompts exhausted | Manual inspection |
| 2 | Agent error/stopped | Restart agent or escalate |
| 3 | Timeout | Escalate to human |

## Example

```bash
# Poll every 10s, timeout after 30 minutes
scripts/poll-task.sh my-ws reviewer .mass/my-ws/reviewer/task-0001.json

# Custom interval and timeout
scripts/poll-task.sh my-ws worker .mass/my-ws/worker/task-0001.json 5 600
```
