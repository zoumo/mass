---
name: mass-pipeline
description: |
  Declarative multi-agent pipeline orchestration. Reads a YAML pipeline config, automatically creates workspaces and agents,
  executes tasks stage by stage, routes via .reason, collects outputs, and cleans up resources.
  Trigger: user runs /mass-pipeline, or mentions "execute with pipeline" or "multi-agent collaboration to complete a task".
  Built-in standard development workflow: plan → review → execute → code review → fix (using the dev-pipeline template).
version: 0.2.0
---

# mass-pipeline — Multi-Agent Pipeline Orchestrator

Reads YAML pipeline config, orchestrates multi-agent execution. Built-in dev pipeline template included.

> **Prerequisite**: Depends on **mass-guide** for workspace/agent lifecycle. Confirm `mass daemon status` healthy before running.

## Orchestrator Boundary Rules

**Conductor, not performer.**

### DO
- Make tasks via `massctl agentrun task do`
- Poll via `scripts/poll-task.sh`
- Read `.reason`, route to next stage
- Pass artifacts as `--input-files`
- Call scripts (`validate-pipeline.sh`, `poll-task.sh`) for deterministic ops
- Make routing decisions (next stage, when to escalate)

### DO NOT
- Write code/docs/designs — agent work
- Analyze agent artifact content — pass to next agent
- Judge design quality — reviewer agent's job
- Retry agent work differently — make new task with updated context
- Skip stages because result seems obvious

**About to "help" by doing task directly instead of delegating — stop. Make a task for the agent.**

---

## Trigger Format

### Built-in coding-pipeline (recommended)

Describe task; built-in workflow (plan → review → execute → code review → fix) used:

```
/mass-pipeline
Implement [task description] with pipeline
```

Auto-uses:
- compose: `skills/mass-pipeline/templates/coding-compose.yaml`
- pipeline: `skills/mass-pipeline/templates/coding-pipeline.yaml`

Review loop max 3 rounds; exceeding triggers escalate.

### Custom pipeline

```
/mass-pipeline /path/to/pipeline.yaml
/mass-pipeline /path/to/pipeline.yaml --input file1.md --input file2.md
```

`--input` injected into all stages without explicit `input_files`.

**Custom file write rule**: Write generated compose/pipeline to temp dir:

```bash
TMPDIR=$(mktemp -d /tmp/mass-pipeline-XXXXXX)
compose_file="$TMPDIR/compose.yaml"
pipeline_file="$TMPDIR/pipeline.yaml"
```

Refs:
- Compose YAML: [references/compose-schema.md](references/compose-schema.md)
- Pipeline YAML: [references/pipeline-schema.md](references/pipeline-schema.md)

Templates:
- `templates/coding-compose.yaml` — workspace-compose with planner/reviewer/worker
- `templates/coding-pipeline.yaml` — plan → review → execute → code review → fix

---

## Execution Workflow

```
Step 0: Health check + read/validate workflow.yaml
Step 1: Make workspace + all agentrun instances
Step 2: Run stage loop
Step 3: Collect output artifacts
Step 4: Clean up workspace + agents
Step 5: Print execution summary
```

---

## Step 0: Health Check + Read Pipeline + Determine Compose

### 0a. Health Check

```bash
mass daemon status
```

`daemon: running` → continue. Otherwise → stop, notify user.

### 0b. Read Pipeline + Determine Compose File

Extract from pipeline YAML:
- `name` — workspace name prefix: `{name}-{random4hex}`
- `description` — display only
- `stages` — ordered list
- `output` — optional

**Compose file determined solely by orchestrator**, not referenced in pipeline YAML:
- Built-in → use `skills/mass-pipeline/templates/coding-compose.yaml` directly
- Custom → generate compose file, write to temp dir

```bash
WORKSPACE_NAME="{pipeline.name}-$(openssl rand -hex 2)"
```

### 0c. Pre-flight Validation (before startup)

```bash
skills/mass-pipeline/scripts/validate-pipeline.sh {pipeline_file}
```

- Exit 0: passed. Show summary.
- Exit 1: failed. Report errors, stop.
- Exit 2: missing dependency. Report, stop.

After validation: ask "Confirm execution?" Wait for confirmation.

---

## Step 1: Make Workspace + Agents

```bash
massctl compose apply -f {compose_file} --workspace {workspace_name}
```

`--workspace` overrides `metadata.name`. Wait for workspace ready + all agents idle. On failure, run Step 4 cleanup, report error.

**Get workspace absolute path immediately — all subsequent paths relative to this:**

```bash
WORKSPACE_PATH=$(massctl workspace get {workspace_name} -o json | jq -r '.status.path')
```

---

## Step 2: Stage Execution Loop

Start from `stages[0]`, follow routes until `__done__` or `__escalate__`.

In-memory state:
- `current_stage`
- `retry_counters` — map: stage_name → count (init 0)
- `stage_artifacts` — map: stage_name → file paths
- `WORKSPACE_PATH`

### 2a. Run Serial Stage

**① Build input files list** (all absolute paths):

```bash
input_files=()

# 1. --input global injection (only when stage has no explicit input_files)
if [[ ${#stage.input_files[@]} -eq 0 ]]; then
  for f in "${global_inputs[@]}"; do
    input_files+=($(realpath "$f"))
  done
fi

# 2. Explicit input_files (relative paths resolved against WORKSPACE_PATH)
for f in "${stage.input_files[@]}"; do
  [[ "$f" = /* ]] && input_files+=("$f") || input_files+=("${WORKSPACE_PATH}/$f")
done

# 3. input_from: collect upstream stage artifacts (already absolute)
for upstream_stage in "${stage.input_from[@]}"; do
  for artifact in "${stage_artifacts[$upstream_stage][@]}"; do
    input_files+=("$artifact")
  done
done
```

**② Make task**

```bash
STAGE_OUTPUT_DIR="${WORKSPACE_PATH}/.mass/{workspace}/{stage.agent}/output/{stage.name}"
mkdir -p "$STAGE_OUTPUT_DIR"

task_id=$(massctl agentrun task do -w {workspace} --run {stage.agent} \
  --prompt "{stage.prompt}" \
  --output-dir "$STAGE_OUTPUT_DIR" \
  $(for f in "${input_files[@]}"; do echo "--input-files $f"; done) \
  | jq -r '.id')
```

**③ Poll**

```bash
skills/mass-pipeline/scripts/poll-task.sh {workspace} {stage.agent} {task_id}
poll_exit=$?
```

| poll exit | handling |
|-----------|----------|
| 0 | read .reason, route |
| 1 | idle retries exhausted → treat as `failed`, follow routes |
| 2 | agent error/stopped → skip routes, escalate directly |
| 3 | timeout → treat as `failed`, follow routes |

**④ Collect artifacts**

```bash
# artifacts written by agent to --output-dir directory
stage_artifacts[{stage.name}]=$(find "$STAGE_OUTPUT_DIR" -type f 2>/dev/null)
```

**⑤ Read .reason, route**

```bash
task_json=$(massctl agentrun task get -w {workspace} --run {stage.agent} {task_id} -o json)
response_status=$(echo "$task_json" | jq -r '.reason // "unknown"')
```

Match `when == response_status` in `stage.routes` order, first matching `goto`:

- `goto` is stage name → `retry_counters[goto]++`
  - `> stage.max_retries` (default 3): → `__escalate__`
  - Otherwise: `current_stage = goto`, continue
- `goto: __done__` → Step 3
- `goto: __escalate__` → print path + response.description, stop

No match → best semantic judgment; if unable → `needs_human` → `__escalate__`.

---

### 2b. Run Parallel Stage

**① Concurrently make all sub-tasks** (input files per ① from 2a):

```bash
# Make task for each sub-task, collect task_ids
declare -A sub_task_ids
for sub_task in "${stage.tasks[@]}"; do
  SUB_OUTPUT_DIR="${WORKSPACE_PATH}/.mass/{workspace}/${sub_task.agent}/output/{stage.name}"
  mkdir -p "$SUB_OUTPUT_DIR"
  task_id=$(massctl agentrun task do -w {workspace} --run {sub_task.agent} \
    --prompt "{sub_task.prompt}" \
    --output-dir "$SUB_OUTPUT_DIR" \
    $(for f in "${sub_task_input_files[@]}"; do echo "--input-files $f"; done) \
    | jq -r '.id')
  sub_task_ids[{sub_task.agent}]=$task_id
done
```

**② Concurrent polling** (background, wait all):

```bash
declare -A sub_poll_exits
for agent in "${!sub_task_ids[@]}"; do
  (
    skills/mass-pipeline/scripts/poll-task.sh {workspace} "$agent" "${sub_task_ids[$agent]}"
    echo $? > /tmp/poll_exit_{workspace}_{agent}
  ) &
done
wait  # wait: all — wait for all background polls to complete
# wait: any — use wait -n to wait for first to complete, cancel rest (if massctl supports task cancel)
```

**③ Aggregate .reason**

| Rule | Condition |
|------|-----------|
| `all_success` | all .reason == success |
| `all_failed` | all .reason == failed |
| `any_failed` | ≥1 failed, not all_failed |
| `any_success` | ≥1 success (wait: any) |

**④ Collect sub-task artifacts**

```bash
for agent in "${!sub_task_ids[@]}"; do
  SUB_OUTPUT_DIR="${WORKSPACE_PATH}/.mass/{workspace}/${agent}/output/{stage.name}"
  stage_artifacts[{stage.name}]+=$(find "$SUB_OUTPUT_DIR" -type f 2>/dev/null)
done
```

**⑤ Route** — same as serial, using aggregated reason.

---

## Step 3: Collect Artifact Paths

Don't move files. Agents wrote to `.mass/{workspace}/{agent}/output/{stage}/` via `--output-dir`. Scan, record for Step 5:

```bash
for stage_name in all_executed_stages; do
  STAGE_OUTPUT_DIR="${WORKSPACE_PATH}/.mass/{workspace}/{stage.agent}/output/{stage_name}"
  stage_artifacts[{stage_name}]=$(find "$STAGE_OUTPUT_DIR" -type f 2>/dev/null)
done
```

---

## Step 4: Cleanup

**Always run cleanup — success, failure, or escalate.** On failure, retain `.mass/{workspace}/` artifacts (stop processes + delete agentrun records only; don't delete files).

### preserve_workspace Logic

`cleanup.preserve_workspace` in pipeline YAML (default `false`):

- `false`:
  - `__done__` → stop agentrun + delete agentrun + delete workspace
  - failure/escalate → stop + delete agentrun, **retain workspace dir**, ask user to delete
- `true`:
  - Any path → stop processes only, **retain agentrun records + workspace dir**
  - Print: `Workspace preserved for debugging: {workspace_name}` + paths

Use **mass-guide**, run sequentially:

```bash
# 1. Stop all agentruns
for agent in all_agent_names; do
  massctl agentrun stop "$agent" -w {workspace}
done

# 2. Delete all agentruns (skip when preserve_workspace=true)
for agent in all_agent_names; do
  massctl agentrun delete "$agent" -w {workspace}
done

# 3. Delete workspace (skip when preserve_workspace=true; ask user on failure/escalate)
massctl workspace delete {workspace}
```

On cleanup failure, log warning, continue remaining cleanup, don't stop flow.

---

## Step 5: Execution Summary

```
=== mass-pipeline execution summary ===
Pipeline:   {name}
Status:     done | escalated
Duration:   {total_elapsed}s

Stages:
  audit_plan   success   attempt 1/3   42s
    prompt:    "Audit CLI ↔ docs drift in this repo"
    output:    .mass/{workspace}/worker/output/audit_plan/
               ├── audit-report.md
               └── fix-plan.md

  review       success   attempt 1/3   18s
    prompt:    "Review audit-report.md and fix-plan.md ..."
    output:    .mass/{workspace}/reviewer/output/review/
               └── final-fix-plan.md
```

Escalate additionally prints:
```
=== ESCALATION ===
Stage:       {stage_name}
Reason:      {response.description}
Retry count: {n}/{max_retries}

Artifacts:   .mass/{workspace}/{agent}/output/{stage_name}/

Next steps:
  - Review artifacts above
  - Re-run with adjusted pipeline or fix issue manually
```

---

## Error Handling

| Scenario | Behavior |
|----------|----------|
| YAML not found | Stop, report path error, make no resources |
| YAML validation failed | Stop, report field errors, make no resources |
| workspace creation failed | Stop, don't make agentrun |
| agentrun creation failed | Stop, clean up already-made agentruns + workspace |
| poll exit 2 (agent error) | Skip routes → `__escalate__` directly |
| poll exit 1/3 (idle/timeout) | Treat as `failed`, follow normal routes |
| retry limit exceeded | Force `__escalate__`, ignore routes |
| `__escalate__` | Print full context, retain artifacts, clean up processes |
| No matching route | Semantic judgment; if unable → `__escalate__` |
| cleanup failed | Log warning, continue remaining cleanup |

---

## Design Principles

1. **YAML = semantic description, not template** — orchestrator reads `description`, constructs prompts; no hardcoded templates
2. **Agents don't communicate directly** — all coordination via orchestrator + task API
3. **Preserve artifacts on failure** — not auto-deleted; kept for debugging
4. **Validation front-loaded** — YAML issues before startup, not mid-run
5. **Cleanup guaranteed** — all termination paths run cleanup

---

## Related Skills

| Skill | Responsibility |
|-------|----------------|
| `mass-guide` | Prerequisite: workspace/agent lifecycle primitives |
| `mass-pipeline` | This skill: declarative config-driven orchestrator |
