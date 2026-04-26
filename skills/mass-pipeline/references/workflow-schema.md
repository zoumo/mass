# Workflow YAML Schema Reference

## Top-level fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Workflow identifier |
| `description` | string | no | Human-readable purpose |
| `workspace` | object | yes | Workspace configuration |
| `agents` | map | yes | Agent definitions keyed by name |
| `stages` | list | yes | Ordered list of stages to execute |
| `output` | object | no | Output collection configuration |

---

## `workspace`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `type` | enum | yes | `local` \| `git` \| `empty` |
| `path` | string | conditional | Required for `local` (project path) and `git` (repo URL). Ignored for `empty`. |

---

## `agents`

Map of agent name → agent config. Agent names must be unique and are referenced by stages.

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `system_prompt` | string | yes | Full system prompt for this agent |

Example:
```yaml
agents:
  designer:
    system_prompt: "You are a software architect. Analyze requirements and produce a design document."
  reviewer:
    system_prompt: "You are a code reviewer. Review the design for correctness and risk."
```

---

## `stages`

Ordered list. Execution starts at `stages[0]`. Routing via `routes` determines what runs next.

### Serial stage (default)

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Unique stage identifier, used in `goto` and `input_from` |
| `type` | enum | no | `serial` (default) \| `parallel` |
| `agent` | string | yes | Agent name from `agents` map |
| `description` | string | yes | Semantic task description — LLM interprets this to build the task prompt |
| `input_files` | list | no | Static files to pass to this stage's task |
| `input_from` | list | no | Stage names whose artifacts to collect and pass as task input files |
| `max_retries` | int | no | Max times this stage can be re-entered via `goto`. Default: 3 |
| `routes` | list | yes | Routing rules based on response status |

### Parallel stage

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Unique stage identifier |
| `type` | enum | yes | Must be `parallel` |
| `tasks` | list | yes | List of parallel sub-tasks (each has `agent`, `description`, `input_from`, `input_files`) |
| `wait` | enum | no | `all` (default) — wait for all sub-tasks \| `any` — proceed when first completes |
| `max_retries` | int | no | Max retries for this stage. Default: 3 |
| `routes` | list | yes | Routing rules using aggregated status values |

Parallel sub-task fields:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `agent` | string | yes | Agent name from `agents` map |
| `description` | string | yes | Semantic task description |
| `input_from` | list | no | Stage names whose artifacts to collect |
| `input_files` | list | no | Static files |

---

## `routes`

List of routing rules evaluated in order. First matching `when` wins.

### Serial stage `when` values

| Value | Meaning |
|-------|---------|
| `success` | `response.status == "success"` |
| `failed` | `response.status == "failed"` |
| `needs_human` | `response.status == "needs_human"` |
| Any string | Custom `response.status` value |

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
| `collect_from` | list | no | Stage names whose artifacts to copy to `destination` |
| `destination` | string | no | Local path to copy artifacts to. Default: `./mass-pipeline-output/` |
| `summary` | bool | no | Print execution summary on completion. Default: `true` |

When `collect_from` references a parallel stage, artifacts from **all** sub-task agents within that stage are merged into `destination`.

---

## Built-in `--input` flag

```
/mass-pipeline workflow.yaml --input file1.md --input file2.md
```

Files passed via `--input` are injected into every stage that has no explicit `input_files` defined.

---

## Complete example

```yaml
name: design-review-implement
description: "设计 → 评审 → 实现"

workspace:
  type: local
  path: ./

agents:
  designer:
    system_prompt: "You are a software architect. Analyze the requirements and produce a detailed design document with clear component boundaries and interfaces."
  security_reviewer:
    system_prompt: "You are a security reviewer. Review the design for security vulnerabilities, authentication gaps, and data exposure risks."
  perf_reviewer:
    system_prompt: "You are a performance reviewer. Review the design for scalability bottlenecks, inefficient data access patterns, and resource contention."
  implementer:
    system_prompt: "You are a senior developer. Implement the design faithfully, following the reviewed design document exactly."

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
  collect_from: [implement]
  destination: ./output/
  summary: true
```
