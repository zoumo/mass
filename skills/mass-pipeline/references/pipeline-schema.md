# Pipeline YAML Schema Reference

Pipeline files are self-contained: define workspace source, agent run definitions, stages, and routing logic.

## Top-level fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Pipeline identifier, used as workspace name prefix |
| `description` | string | no | Human-readable purpose |
| `workspace` | object | yes | Workspace source config |
| `agentRuns` | map | yes | Agent run definitions keyed by name |
| `stages` | list | yes | Ordered stages to execute |
| `output` | object | no | Output collection config |
| `cleanup` | object | no | Cleanup behavior config |

## `cleanup`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `preserve_workspace` | bool | no | If `true`, keep workspace dir + agentrun records after completion for debugging. Default: `false` |

---

## `workspace`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `source.type` | enum | yes | `local` \| `git` \| `empty` |
| `source.path` | string | conditional | Absolute path to local dir. Required for `local` only. |
| `source.url` | string | conditional | Git repo URL. Required for `git` only. |
| `source.ref` | string | no | Git ref (branch/tag/sha). `git` type only. |

---

## `agentRuns`

Map of agent run definitions. Key is the agent run name referenced by `stages[].agentRun`.

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `agent` | string | yes | Agent type (e.g. `claude`, `codex`) |
| `systemPrompt` | string | no | System prompt for this agent |
| `permissions` | object | no | Permission policy |
| `mcpServers` | list | no | MCP server configs |
| `workflowFile` | string | no | Path to workflow file |
| `fallback` | list | no | Ordered fallback agent types if primary fails to start |

### `fallback`

List of fallback entries. Each entry replaces only the `agent` type; all other fields (systemPrompt, permissions, etc.) are inherited from the parent definition.

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `agent` | string | yes | Fallback agent type |

Agent runs are created **on demand** when a stage references them, not at pipeline startup. This avoids wasting resources on agents that may not be needed until later stages.

---

## `stages`

Ordered list. Execution starts at `stages[0]`. Routing via `routes` determines next stage.

### Serial stage (default)

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Unique stage identifier, used in `goto` and `input_from` |
| `type` | enum | no | `serial` (default) \| `parallel` |
| `agentRun` | string | yes | Agent run name from `agentRuns` keys |
| `description` | string | yes | Semantic task description — orchestrator builds task prompt from this |
| `input_files` | list | no | Static files to pass to stage's task |
| `input_from` | list | no | Stage names whose artifacts to collect + pass as task input |
| `max_retries` | int | no | Max re-entries via `goto`. Default: 3 |
| `routes` | list | yes | Routing rules based on task reason |

### Parallel stage

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Unique stage identifier |
| `type` | enum | yes | Must be `parallel` |
| `tasks` | list | yes | Parallel sub-tasks |
| `wait` | enum | no | `all` (default) \| `any` |
| `max_retries` | int | no | Default: 3 |
| `routes` | list | yes | Routing rules using aggregated reason values |

Parallel sub-task fields: `agentRun`, `description`, `input_from`, `input_files` (same as serial).

---

## `routes`

List of routing rules evaluated in order. First matching `when` wins.

Routing matches against task's `.reason` field (set by `massctl agentrun task done --reason`).

### Serial stage `when` values

| Value | Meaning |
|-------|---------|
| `success` | Agent reported `reason=success` |
| `failed` | Agent reported `reason=failed` |
| `needs_human` | Agent reported `reason=needs_human` |
| Any string | Matches any custom reason value |

### Parallel stage `when` values (aggregated)

| Value | Meaning |
|-------|---------|
| `all_success` | All sub-tasks reported `success` |
| `any_failed` | At least one sub-task reported `failed` |
| `all_failed` | All sub-tasks reported `failed` |
| `any_success` | At least one sub-task reported `success` (used with `wait: any`) |

### `goto` targets

| Value | Meaning |
|-------|---------|
| Stage name | Jump to that stage |
| `__done__` | Successful termination |
| `__escalate__` | Halt with human intervention message |

---

## `output`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `summary` | bool | no | Print execution summary on completion. Default: `true` |

Agents write files directly via `--output-dir` to `.mass/{workspace}/{agentRun}/output/{stage}/`.
No collection/copying. Summary prints output paths per stage.

---

## Example

```yaml
name: design-review-implement
description: "设计 → 评审 → 实现"

workspace:
  source:
    type: local
    path: ./

agentRuns:
  designer:
    agent: claude
    systemPrompt: |
      You are a software architect. Produce a design document.

  security_reviewer:
    agent: claude
    systemPrompt: |
      You are a security expert. Review designs for security issues.

  perf_reviewer:
    agent: claude
    systemPrompt: |
      You are a performance engineer. Review designs for performance issues.

  implementer:
    agent: codex
    systemPrompt: |
      You are a senior developer. Implement the design.
    fallback:
      - agent: claude

stages:
  - name: design
    agentRun: designer
    description: "Analyze the requirements and produce a design document"
    input_files:
      - requirements.md
    max_retries: 2
    routes:
      - when: success
        goto: parallel_review
      - when: failed
        goto: design
      - when: needs_human
        goto: __escalate__

  - name: parallel_review
    type: parallel
    wait: all
    tasks:
      - agentRun: security_reviewer
        description: "Review the design for security issues"
        input_from: [design]
      - agentRun: perf_reviewer
        description: "Review the design for performance issues"
        input_from: [design]
    max_retries: 2
    routes:
      - when: all_success
        goto: implement
      - when: any_failed
        goto: design
      - when: all_failed
        goto: __escalate__

  - name: implement
    agentRun: implementer
    description: "Implement the design based on the design document and review feedback"
    input_from: [design, parallel_review]
    max_retries: 2
    routes:
      - when: success
        goto: __done__
      - when: failed
        goto: design
      - when: needs_human
        goto: __escalate__

output:
  summary: true

cleanup:
  preserve_workspace: false
```
