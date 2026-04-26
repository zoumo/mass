# mass-workflow Skill Design

**Date:** 2026-04-26  
**Status:** Approved  
**Author:** zoumo

## Problem

Every new multi-agent workflow requires writing a near-identical skill. The only differences are role combinations and stage ordering. A declarative, reusable orchestrator eliminates this repetition.

## Solution

A single `mass-workflow` skill that reads a YAML workflow definition and automatically executes the full multi-agent pipeline. Users define agents, stages, and routing; the skill handles workspace lifecycle, task dispatch, polling, and output collection.

---

## Workflow Configuration Format

Users pass a workflow YAML at invocation time (not stored in repo):

```
/mass-workflow path/to/workflow.yaml
/mass-workflow path/to/workflow.yaml --input requirements.md --input spec.md
```

`--input` files are injected into all stages that have no explicit `input_files`.

### Full Schema

```yaml
name: code-review-pipeline
description: "代码评审流程"

workspace:
  type: local          # local | git | empty
  path: ./             # local: project path; git: repo url; empty: ignored

agents:
  designer:
    system_prompt: "You are a software architect..."
  security_reviewer:
    system_prompt: "You are a security code reviewer..."
  perf_reviewer:
    system_prompt: "You are a performance code reviewer..."
  implementer:
    system_prompt: "You are a developer..."

stages:
  # Serial stage (default)
  - name: design
    agent: designer
    description: "分析需求，输出设计方案"  # semantic, LLM interprets freely
    input_files:
      - requirements.md
    max_retries: 2
    routes:
      - when: success
        goto: review
      - when: failed
        goto: design
      - when: needs_human
        goto: __escalate__

  # Parallel stage
  - name: parallel_review
    type: parallel          # default: serial
    tasks:
      - agent: security_reviewer
        description: "评审安全问题"
        input_from: [design]
      - agent: perf_reviewer
        description: "评审性能问题"
        input_from: [design]
    wait: all               # all | any
    routes:
      - when: all_success
        goto: implement
      - when: any_failed
        goto: design
      - when: all_failed
        goto: __escalate__

  - name: implement
    agent: implementer
    description: "按设计实现代码"
    input_from: [design, parallel_review]  # auto-collect upstream artifacts
    max_retries: 2
    routes:
      - when: success
        goto: __done__
      - when: failed
        goto: design
      - when: needs_human
        goto: __escalate__

output:
  collect_from: [implement, parallel_review]  # collect artifacts from these stages
  destination: ./output/                       # copy destination
  summary: true                                # print execution summary
```

### Routes

`when` maps to agent `response.status`. Built-in values: `success`, `failed`, `needs_human`. Custom statuses supported by convention.

`goto` targets:
- Stage name: jump to that stage
- `__done__`: successful termination
- `__escalate__`: halt with human intervention request

Parallel stage aggregated `when` values: `all_success`, `any_failed`, `all_failed`, `any_success`.

### Retry Counter

Per-stage, independent across route jumps. Each `goto` targeting the same stage increments its counter. Exceeding `max_retries` forces `__escalate__` regardless of routes.

---

## Orchestrator Execution Model

The orchestrator is the current Claude agent executing the skill. No separate process or binary.

```
load + validate workflow.yaml
  ↓
create workspace (via massctl)
  ↓
create all agentrun instances
  ↓
execute entry stage (stages[0])
  ↓
┌─ [serial stage]
│   create task → poll completion → read response.status
│   route via stage.routes
│   ↓
│  [parallel stage]
│   concurrent task create for all tasks
│   concurrent poll all
│   wait: all → wait for all; wait: any → cancel rest on first complete
│   aggregate status → route
│
│  success → goto next stage (reset that stage's retry counter)
│  failed  → goto target (increment retry counter, check max_retries)
│  retry exhausted → __escalate__
│
└─ loop until __done__ or __escalate__
  ↓
collect output: copy artifacts from specified stages to destination
  ↓
cleanup: stop → delete agentrun → delete workspace
  ↓
print execution summary (stage path, status, output location)
```

### Context Passing

`input_from: [stage_a, stage_b]` — orchestrator collects `.mass/{workspace}/{agent}/artifacts/` from all listed stages and passes as `--file` args to the new task.

For parallel stages, `input_from: [parallel_stage_name]` collects artifacts from **all** sub-task agents within that stage (all sub-agents' artifact directories are merged).

### Poll Timeout

Poll timeout treated as `failed`, routed through normal routes.

---

## Error Handling

| Scenario | Behavior |
|----------|----------|
| YAML validation error | Exit before creating any resources |
| Agent start failure | Immediate stop, cleanup all created resources |
| Stage retry exhausted | Print stage context + execution path, escalate |
| `__escalate__` reached | Print full execution path, stop, prompt human |
| Poll timeout | Treat as `failed`, route normally |
| Cleanup failure | Log warning, continue cleanup of remaining resources |

**Artifact preservation on failure:** `.mass/{workspace}/` is NOT deleted on failure or escalation. Kept for debugging. User must manually clean up or re-run.

---

## File Structure

```
skills/mass-workflow/
├── SKILL.md                    # skill entry: trigger, dependencies, execution flow
├── scripts/
│   └── poll-task.sh            # standalone poll script (not shared with mass-pilot)
└── references/
    └── workflow-schema.md      # complete YAML field reference
```

---

## Relationship to Existing Skills

| Skill | Role |
|-------|------|
| `mass-guide` | Prerequisite: workspace/agent lifecycle primitives |
| `mass-pilot` | Retained for hand-written complex orchestrators |
| `mass-workflow` | New: declarative config-driven general-purpose orchestrator |

`mass-workflow` is the declarative simplification of `mass-pilot`. Complex conditional logic or dynamic role selection still uses `mass-pilot` directly.

---

## Non-Goals

- Persisting workflow state across sessions (runs complete in one session)
- `massctl workflow` CLI subcommand (future consideration if needed)
- Built-in role templates (all agents are fully custom via `system_prompt`)
