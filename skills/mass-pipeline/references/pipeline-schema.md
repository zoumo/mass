# Pipeline YAML Schema Reference

Pipeline files define stages, routing logic, output. Reference compose file for workspace + agent definitions.

## Top-level fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Pipeline identifier, used as workspace name prefix |
| `description` | string | no | Human-readable purpose |
| `stages` | list | yes | Ordered stages to execute |
| `output` | object | no | Output collection config |
| `cleanup` | object | no | Cleanup behavior config |

## `cleanup`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `preserve_workspace` | bool | no | If `true`, keep workspace dir + agentrun records after completion for debugging. Default: `false` |

> **Note:** Pipeline files do not reference a compose file. Compose file (workspace + agents) managed separately by orchestrator, applied via `massctl compose apply`.

---

## `stages`

Ordered list. Execution starts at `stages[0]`. Routing via `routes` determines next stage.

### Serial stage (default)

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Unique stage identifier, used in `goto` and `input_from` |
| `type` | enum | no | `serial` (default) \| `parallel` |
| `agent` | string | yes | Agent run name from compose `spec.runs` |
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

Parallel sub-task fields: `agent`, `description`, `input_from`, `input_files` (same as serial).

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

Agents write files directly via `--output-dir` to `.mass/{workspace}/{agent}/output/{stage}/`.
No collection/copying. Summary prints output paths per stage.

---

## Example

```yaml
name: design-review-implement
description: "设计 → 评审 → 实现"

stages:
  - name: design
    agent: designer
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
      - agent: security_reviewer
        description: "Review the design for security issues"
        input_from: [design]
      - agent: perf_reviewer
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
    agent: implementer
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
  preserve_workspace: false  # set true to keep workspace + artifacts for debugging
```
