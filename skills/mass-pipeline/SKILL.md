---
name: mass-pipeline
description: |
  Declarative multi-agent pipeline orchestration. Reads a YAML pipeline config, automatically creates workspaces and agents,
  executes tasks stage by stage, routes via .reason, collects outputs, and cleans up resources.
  Trigger: user runs /mass-pipeline, or mentions "execute with pipeline" or "multi-agent collaboration to complete a task".
  Built-in standard development workflow: plan → review → execute → code review → fix (using the dev-pipeline template).
version: 0.2.0
---

# mass-pipeline — Declarative Multi-Agent Pipeline Orchestrator

Reads a YAML pipeline config and automatically orchestrates a multi-agent execution workflow. Includes a built-in standard development pipeline template.

> **Prerequisite**: This skill depends on the **mass-guide** skill for workspace and agent lifecycle management.
> Before executing, call mass-guide to confirm `mass daemon status` is healthy.

## Orchestrator Boundary Rules

**You are the conductor, not the performer.**

### DO
- Create tasks for agents via `massctl agentrun task do`
- Poll task completion via `scripts/poll-task.sh`
- Read `.reason` and route to the next stage
- Pass artifacts between stages as `--input-files` inputs
- Call scripts (`validate-pipeline.sh`, `poll-task.sh`) for deterministic operations
- Make routing decisions (which stage to run next, when to escalate)

### DO NOT
- Write code, documents, or designs yourself — that is agent work
- Analyze content in agent artifacts — pass them to the next agent
- Make judgment calls about whether a design is "good" — the reviewer agent does that
- Retry agent work differently — create a new task with updated context instead
- Skip stages because you think the result is obvious

**If you catch yourself about to "help" by doing a task directly instead of delegating it — stop. Create a task for the agent.**

---

## Trigger Format

### Built-in coding-pipeline (recommended)

Describe the task directly; the built-in standard development workflow is used (plan → review → execute → code review → fix):

```
/mass-pipeline
Implement [task description] with pipeline
```

The orchestrator automatically uses:
- compose: `skills/mass-pipeline/templates/coding-compose.yaml`
- pipeline: `skills/mass-pipeline/templates/coding-pipeline.yaml`

Each review loop converges within at most 3 rounds; exceeding the limit triggers an escalate.

### Custom pipeline

```
/mass-pipeline /path/to/pipeline.yaml
/mass-pipeline /path/to/pipeline.yaml --input file1.md --input file2.md
```

`--input` files are injected into all stages that do not have `input_files` explicitly configured.

**Custom file write rule**: When the orchestrator needs to generate a custom compose or pipeline file, write it to a temporary directory:

```bash
TMPDIR=$(mktemp -d /tmp/mass-pipeline-XXXXXX)
compose_file="$TMPDIR/compose.yaml"
pipeline_file="$TMPDIR/pipeline.yaml"
```

Full field reference:
- Compose YAML: [references/compose-schema.md](references/compose-schema.md)
- Pipeline YAML: [references/pipeline-schema.md](references/pipeline-schema.md)

Built-in template reference:
- `templates/coding-compose.yaml` — workspace-compose with planner/reviewer/worker agents
- `templates/coding-pipeline.yaml` — plan → review → execute → code review → fix

---

## Execution Workflow

```
Step 0: Health check + read and validate workflow.yaml
Step 1: Create workspace + all agentrun instances
Step 2: Execute the stage loop
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

- `daemon: running` → continue
- Otherwise → stop and notify the user

### 0b. Read Pipeline + Determine Compose File

Extract from the pipeline YAML:
- `name` — pipeline name, used as workspace name prefix: `{name}-{random4hex}`
- `description` — display only
- `stages` — ordered list
- `output` — output configuration (optional)

**The compose file is determined solely by the orchestrator** and is not referenced inside the pipeline YAML:
- Built-in coding pipeline → use `skills/mass-pipeline/templates/coding-compose.yaml` directly
- Custom pipeline → orchestrator generates the compose file and writes it to a temporary directory

Generate workspace name:
```bash
WORKSPACE_NAME="{pipeline.name}-$(openssl rand -hex 2)"
```

### 0c. Pre-flight Validation (before startup)

Validate only pipeline fields (stages/routes):

```bash
skills/mass-pipeline/scripts/validate-pipeline.sh {pipeline_file}
```

Exit code 0: validation passed. Show summary to user.
Exit code 1: validation failed, prints errors. Report and stop.
Exit code 2: missing dependency. Report and stop.

After successful validation, ask the user: "Confirm execution?" Wait for confirmation before proceeding.

---

## Step 1: Create Workspace + Agents

```bash
massctl compose apply -f {compose_file} --workspace {workspace_name}
```

`--workspace` overrides `metadata.name` in the compose file. Wait for workspace ready + all agents idle. On failure, execute Step 4 cleanup and report the error.

**Immediately after successful creation, retrieve the workspace absolute path — all subsequent paths are relative to this:**

```bash
WORKSPACE_PATH=$(massctl workspace get {workspace_name} -o json | jq -r '.status.path')
```

---

## Step 2: Stage Execution Loop

Start from `stages[0]`, follow routes to jump between stages, until `__done__` or `__escalate__` is reached.

Maintain in-memory state (within this session):
- `current_stage` — current stage name
- `retry_counters` — map: stage_name → retry_count (initialized to all 0)
- `stage_artifacts` — map: stage_name → list of artifact file paths (populated after execution)
- `WORKSPACE_PATH` — workspace absolute path retrieved in Step 1

### 2a. Execute Serial Stage

**① Build task input files list**

All paths must be absolute paths:

```bash
input_files=()

# 1. --input global injection (only when stage has no explicit input_files)
if [[ ${#stage.input_files[@]} -eq 0 ]]; then
  for f in "${global_inputs[@]}"; do
    input_files+=($(realpath "$f"))
  done
fi

# 2. Explicit input_files (relative paths are resolved against WORKSPACE_PATH)
for f in "${stage.input_files[@]}"; do
  [[ "$f" = /* ]] && input_files+=("$f") || input_files+=("${WORKSPACE_PATH}/$f")
done

# 3. input_from: collect upstream stage artifacts (already absolute paths)
for upstream_stage in "${stage.input_from[@]}"; do
  for artifact in "${stage_artifacts[$upstream_stage][@]}"; do
    input_files+=("$artifact")
  done
done
```

**② Create task**

```bash
STAGE_OUTPUT_DIR="${WORKSPACE_PATH}/.mass/{workspace}/{stage.agent}/output/{stage.name}"
mkdir -p "$STAGE_OUTPUT_DIR"

task_id=$(massctl agentrun task do -w {workspace} --run {stage.agent} \
  --prompt "{stage.prompt}" \
  --output-dir "$STAGE_OUTPUT_DIR" \
  $(for f in "${input_files[@]}"; do echo "--input-files $f"; done) \
  | jq -r '.id')
```

**③ Poll and wait**

```bash
skills/mass-pipeline/scripts/poll-task.sh {workspace} {stage.agent} {task_id}
poll_exit=$?
```

| poll exit | handling |
|-----------|----------|
| 0 | read .reason, execute routing |
| 1 | agent idle retries exhausted → treat as `failed`, follow routes |
| 2 | agent error/stopped → stop, require manual intervention (skip routes, escalate directly) |
| 3 | timeout → treat as `failed`, follow routes |

**④ Collect artifacts**

```bash
# artifacts are written by agent to the directory specified by --output-dir
stage_artifacts[{stage.name}]=$(find "$STAGE_OUTPUT_DIR" -type f 2>/dev/null)
```

**⑤ Read .reason and route**

```bash
task_json=$(massctl agentrun task get -w {workspace} --run {stage.agent} {task_id} -o json)
response_status=$(echo "$task_json" | jq -r '.reason // "unknown"')
```

Match `when == response_status` in `stage.routes` order, find the first matching `goto`:

- `goto` is a stage name:
  - `retry_counters[goto]++`
  - If `retry_counters[goto] > stage.max_retries` (default 3): → `__escalate__`
  - Otherwise: `current_stage = goto`, continue loop
- `goto: __done__`: proceed to Step 3
- `goto: __escalate__`: print execution path + response.description, stop

No matching `when`: make the best semantic judgment; if unable to determine, treat as `needs_human` → `__escalate__`.

---

### 2b. Execute Parallel Stage

**① Concurrently create all sub-tasks**

For each `tasks[i]`, build input files following the ① logic from 2a, then execute concurrently:

```bash
# Create a task for each sub-task, collect task_ids
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

**② Concurrent polling**

Run poll-task.sh concurrently for each sub-task (background processes), wait for results:

```bash
declare -A sub_poll_exits
for agent in "${!sub_task_ids[@]}"; do
  (
    skills/mass-pipeline/scripts/poll-task.sh {workspace} "$agent" "${sub_task_ids[$agent]}"
    echo $? > /tmp/poll_exit_{workspace}_{agent}
  ) &
done
wait  # wait for all background polls to complete (wait: all)
# wait: any — use wait -n to wait for the first to complete, cancel the rest (if massctl supports task cancel)
```

**③ Aggregate reason**

Read each sub-task's .reason and compute the aggregated result:

| Aggregation rule | Trigger condition |
|------------------|-------------------|
| `all_success` | all sub-task .reason == success |
| `all_failed` | all sub-task .reason == failed |
| `any_failed` | at least one failed (and not all_failed) |
| `any_success` | at least one success (for wait: any scenarios) |

**④ Collect all sub-task artifacts**

```bash
for agent in "${!sub_task_ids[@]}"; do
  SUB_OUTPUT_DIR="${WORKSPACE_PATH}/.mass/{workspace}/${agent}/output/{stage.name}"
  stage_artifacts[{stage.name}]+=$(find "$SUB_OUTPUT_DIR" -type f 2>/dev/null)
done
```

**⑤ Route** — same logic as serial stage, using the aggregated reason to match `when`.

---

## Step 3: Collect Artifact Paths

Do not move files. Agents have already written files to `.mass/{workspace}/{agent}/output/{stage}/` via `--output-dir`.

This step only scans each stage's output dir, records the actually produced file paths, for display in the Step 5 summary:

```bash
for stage_name in all_executed_stages; do
  STAGE_OUTPUT_DIR="${WORKSPACE_PATH}/.mass/{workspace}/{stage.agent}/output/{stage_name}"
  stage_artifacts[{stage_name}]=$(find "$STAGE_OUTPUT_DIR" -type f 2>/dev/null)
done
```

---

## Step 4: Cleanup

**Execute cleanup regardless of success, failure, or escalate.** On failure, retain `.mass/{workspace}/` artifacts (do not delete files inside the workspace; only stop agent processes and delete agentrun records).

### preserve_workspace Logic

Read `cleanup.preserve_workspace` from the pipeline YAML (default `false`):

- `false` (default):
  - Success (`__done__`) → stop agentrun + delete agentrun + delete workspace
  - Failure/escalate → stop agentrun + delete agentrun, **retain workspace directory**, ask user whether to delete
- `true`:
  - Any termination path → only stop agentrun processes, **retain agentrun records and workspace directory**
  - Print notice: `Workspace preserved for debugging: {workspace_name}` + artifact paths

Use **mass-guide** skill to execute sequentially:

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

On cleanup failure, log a warning, continue cleaning up remaining resources, and do not interrupt the flow.

---

## Step 5: Print Execution Summary

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

escalate additionally prints:
```
=== ESCALATION ===
Stage:       {stage_name}
Reason:      {response.description}
Retry count: {n}/{max_retries}

Artifacts:   .mass/{workspace}/{agent}/output/{stage_name}/

Next steps:
  - Review artifacts above
  - Re-run with adjusted pipeline or fix the issue manually
```

---

## Error Handling Quick Reference

| Scenario | Behavior |
|----------|----------|
| YAML file not found | Stop immediately, report path error, create no resources |
| YAML validation failed | Stop immediately, report specific field errors, create no resources |
| workspace creation failed | Stop immediately, do not create agentrun |
| agentrun creation failed | Stop, clean up already-created agentruns + workspace |
| poll exit 2 (agent error) | Skip routes, go directly to __escalate__ |
| poll exit 1/3 (idle/timeout) | Treat as `failed`, follow normal routes |
| retry limit exceeded | Force __escalate__, ignore routes config |
| `__escalate__` | Print full context, retain artifacts, clean up process resources |
| No matching route | Make semantic judgment from .reason; if unable to determine → __escalate__ |
| cleanup failed | Log warning, continue cleaning up remaining resources |

---

## Design Principles

1. **YAML is a semantic description, not a template** — the orchestrator (LLM) reads the `description` field and decides how to construct task prompts; no hardcoded templates
2. **Agents do not communicate directly** — all coordination goes through the orchestrator via the task API
3. **Preserve artifacts on failure** — retained for debugging; not deleted automatically
4. **Validation is front-loaded** — YAML issues are surfaced before startup, not mid-execution
5. **Cleanup is guaranteed** — any termination path executes cleanup

---

## Relationship to Other Skills

| Skill | Responsibility |
|-------|----------------|
| `mass-guide` | Prerequisite dependency: workspace/agent lifecycle primitives |
| `mass-pipeline` | This skill: declarative config-driven general-purpose orchestrator |
