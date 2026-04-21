# Task Protocol

File-based task protocol for multi-agent collaboration. Orchestrator creates task files, agents read and complete them.

## File Path

```
{workspace-root}/.mass/{workspace-name}/{agent-name}/task-{NNNN}.json
```

- `NNNN`: 4-digit number, starting from `0001`, incrementing per agent
- Each agent has its own numbering space
- Orchestrator creates the file; agent updates it on completion

## Task JSON Format

### Orchestrator creates:

```json
{
  "id": "task-0001",
  "assignee": "planner",
  "created_at": "2026-04-21T10:00:00+08:00",
  "request": {
    "description": "Review the implementation plan. Check for completeness and correctness.",
    "file_paths": [".mass/my-ws/planner/artifacts/plan-v1.md"]
  }
}
```

### Agent completes:

```json
{
  "id": "task-0001",
  "assignee": "planner",
  "created_at": "2026-04-21T10:00:00+08:00",
  "request": {
    "description": "Review the implementation plan. Check for completeness and correctness.",
    "file_paths": [".mass/my-ws/planner/artifacts/plan-v1.md"]
  },
  "completed": true,
  "response": {
    "status": "success",
    "description": "Plan reviewed. 2 warnings found, no blockers.",
    "file_paths": [".mass/my-ws/reviewer/artifacts/review-report.md"],
    "updated_at": "2026-04-21T10:15:00+08:00"
  }
}
```

## Fields

### Orchestrator writes (on creation)

| Field | Description |
|-------|-------------|
| `id` | Unique identifier, matches filename (e.g. `task-0001`) |
| `assignee` | Agent name — who does this task |
| `created_at` | Creation time, ISO8601 |
| `request.description` | What to do (free text, includes all context and instructions) |
| `request.file_paths` | Files the agent should read (optional, omit if none) |

### Agent writes (on completion)

| Field | Description |
|-------|-------------|
| `completed` | `true` — marks the task as processed |
| `response.status` | `success` / `failed` / `needs_human` |
| `response.description` | Result summary (free text) |
| `response.file_paths` | Files the agent produced (optional, omit if none) |
| `response.updated_at` | Completion time, ISO8601 |

## Protocol Rules

1. **Orchestrator creates** the task file (id, assignee, created_at, request)
2. **Orchestrator prompts agent** with the task file path
3. **Agent reads task** → executes request.description → sets completed=true and adds response
4. **Task file update is the agent's last write**
5. **Agents never communicate directly** — all coordination goes through the orchestrator
6. **One task file is handled by exactly one agent**
7. **Orchestrator checks completed==true** to determine completion, reads response.status for routing

## response.status Values

| Value | Meaning |
|-------|---------|
| `success` | Task completed successfully |
| `failed` | Task failed |
| `needs_human` | Requires human intervention |

These are the base set. Orchestrators may extend with additional values for specific workflows.

## Workflow File

Before creating any task, the orchestrator must copy the role's workflow template into the agent's task directory:

```
{workspace-root}/.mass/{workspace-name}/{agent-name}/workflow.md
```

Source templates are at `skills/mass-pilot/templates/{role}.md` (planner, reviewer, worker, verifier). The orchestrator has access to these; agents do not.

## Prompt Template

```
Your task is at: {task-path}
Read your workflow at .mass/{workspace-name}/{agent-name}/workflow.md, then read the task file.
Complete the task described in request.description. Read files listed in request.file_paths if present.
When done, set completed=true and add a response object with: status, description, file_paths (if you produced files), updated_at.
Task file update is ALWAYS your last write.
```
