# Compose YAML Schema Reference

Compose files follow the `massctl compose apply` format. They define the workspace and all agent runs.

## Top-level fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `kind` | string | yes | Must be `workspace-compose` |
| `metadata.name` | string | yes | Workspace name placeholder. Convention: write `WORKSPACE_NAME`. The actual name is supplied via `--workspace` flag at runtime (`massctl compose apply -f file.yaml --workspace real-name`) and overrides this field entirely. |
| `spec.source` | object | yes | Workspace source configuration |
| `spec.runs` | list | yes | Agent run definitions |

---

## `spec.source`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `type` | enum | yes | `local` \| `git` \| `empty` |
| `path` | string | conditional | Absolute path for `local`; repo URL for `git`. Ignored for `empty`. |
| `ref` | string | no | Git ref (branch/tag/sha). Only for `git` type. |

---

## `spec.runs`

List of agent run entries.

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Agent run name — must match names referenced in pipeline `agent` fields |
| `agent` | string | yes | Agent type (e.g. `claude`) |
| `systemPrompt` | string | yes | Full system prompt for this agent |
| `permissions` | object | no | Permission policy |
| `mcpServers` | list | no | MCP server configs |
| `workflowFile` | string | no | Path to a workflow file |

### System prompt requirements

Every agent's system prompt **must** include the task completion instruction:

```
When done, run:
  massctl agentrun task done --file <task-path> --reason <reason> --response '<json>'
Where reason is a short string describing the outcome (e.g. success, failed, needs_human)
And json is a JSON object with at least {"description": "..."}
```

---

## Example

```yaml
kind: workspace-compose
metadata:
  name: WORKSPACE_NAME
spec:
  source:
    type: local
    path: /absolute/path/to/project
  runs:
    - name: designer
      agent: claude
      systemPrompt: |
        You are a software architect. Produce a design document.

        When done, run:
          massctl agentrun task done --file <task-path> --reason <reason> --response '<json>'

    - name: reviewer
      agent: claude
      systemPrompt: |
        You are a senior engineer. Review designs and code.

        When done, run:
          massctl agentrun task done --file <task-path> --reason <reason> --response '<json>'
```
