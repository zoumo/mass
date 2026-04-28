---
name: mass-pipeline
description: |
  Declarative multi-agent pipeline orchestration. Reads a self-contained YAML pipeline config (workspace + agentRuns + stages),
  creates workspace, launches agents on demand with fallback support, executes tasks stage by stage, routes via .reason, collects outputs, and cleans up resources.
  Trigger: user runs /mass-pipeline, or mentions "execute with pipeline" or "multi-agent collaboration to complete a task".
  Built-in standard development workflow: plan → review → execute → code review → fix (using the dev-pipeline template).
version: 0.3.0
---

# mass-pipeline — Multi-Agent Pipeline Orchestrator

Reads self-contained YAML pipeline config, orchestrates multi-agent execution. Built-in dev pipeline template included.

> **Prerequisite**: Depends on **mass-guide** for workspace/agent lifecycle. Confirm `mass daemon status` healthy before running.

## Orchestrator Boundary Rules

**Conductor, not performer.**

### DO
- Make tasks via `massctl agentrun task do`
- Poll via `scripts/poll-task.sh`
- Read `.reason`, route to next stage
- Pass artifacts as `--input-files`
- Call scripts (`validate-pipeline.sh`, `ensure-agentrun.sh`, `poll-task.sh`) for deterministic ops
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
- pipeline: `skills/mass-pipeline/templates/coding-pipeline.yaml`

Review loop max 3 rounds; exceeding triggers escalate.

### Custom pipeline

```
/mass-pipeline /path/to/pipeline.yaml
/mass-pipeline /path/to/pipeline.yaml --input file1.md --input file2.md
```

`--input` injected into all stages without explicit `input_files`.

**Custom file write rule**: Write generated pipeline to temp dir:

```bash
TMPDIR=$(mktemp -d /tmp/mass-pipeline-XXXXXX)
pipeline_file="$TMPDIR/pipeline.yaml"
```

Refs:
- Pipeline YAML: [references/pipeline-schema.md](references/pipeline-schema.md)

Templates:
- `templates/coding-pipeline.yaml` — self-contained plan → review → execute → code review → fix
- `templates/doc-audit-pipeline.yaml` — Audit docs for outdated content and produce a fix plan.

---

## Execution Workflow

```
Step 0: Health check + read/validate pipeline.yaml
Step 1: Create workspace only (agents launched on demand)
Step 2: Run stage loop (ensure agentrun before each stage)
Step 3: Collect output artifacts
Step 4: Clean up workspace + agents
Step 5: Print execution summary
```

---

## Step 0: Health Check + Read Pipeline

### 0a. Health Check

```bash
mass daemon status
```

`daemon: running` → continue. Otherwise → stop, notify user.

### 0b. Read Pipeline

Extract from pipeline YAML:
- `name` — workspace name prefix: `{name}-{random4hex}`
- `description` — display only
- `workspace` — workspace source config
- `agentRuns` — agent run definitions (created on demand)
- `stages` — ordered list
- `output` — optional

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

## Step 1: Create Workspace Only

Create workspace from `pipeline.workspace` config. **Do NOT create any agentRuns yet** — they are created on demand in Step 2.

```bash
massctl workspace create {workspace_name} \
  --source-type {workspace.source.type} \
  --path {workspace.source.path}        # for local type
  # --url {workspace.source.url}        # for git type
  # --ref {workspace.source.ref}        # for git type, optional
```

Wait for workspace ready:

```bash
# Poll until workspace phase is Ready
massctl workspace get {workspace_name} -o json | jq -r '.status.phase'
```

On failure, report error, stop.

**Get workspace absolute path immediately — all subsequent paths relative to this:**

```bash
WORKSPACE_PATH=$(massctl workspace get {workspace_name} -o json | jq -r '.status.path')
```

Track created agents for cleanup:

```bash
created_agents=()  # populated by ensure-agentrun.sh calls
```

---

## Step 2: Stage Execution Loop

Start from `stages[0]`, follow routes until `__done__` or `__escalate__`.

In-memory state:
- `current_stage`
- `retry_counters` — map: stage_name → count (init 0)
- `stage_artifacts` — map: stage_name → file paths
- `WORKSPACE_PATH`
- `created_agents` — list of agent names created during this run

### 2a. Run Serial Stage

**⓪ Ensure agentrun is ready** (on-demand creation with fallback):

```bash
skills/mass-pipeline/scripts/ensure-agentrun.sh \
  {workspace} {stage.agentRun} {pipeline_file}
ensure_exit=$?
```

| ensure exit | handling |
|-------------|----------|
| 0 | agent ready, continue |
| 1 | all candidates failed → `__escalate__` directly |
| 2 | invalid usage → stop pipeline |

Track agent for cleanup:

```bash
if [[ ! " ${created_agents[*]} " =~ " ${stage.agentRun} " ]]; then
  created_agents+=("${stage.agentRun}")
fi
```

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
STAGE_OUTPUT_DIR="${WORKSPACE_PATH}/.mass/{workspace}/{stage.agentRun}/output/{stage.name}"
mkdir -p "$STAGE_OUTPUT_DIR"

stage_start_time=$(date +%s)

task_id=$(massctl agentrun task do -w {workspace} --run {stage.agentRun} \
  --prompt "{stage.prompt}" \
  --output-dir "$STAGE_OUTPUT_DIR" \
  $(for f in "${input_files[@]}"; do echo "--input-files $f"; done) \
  | jq -r '.id')
```

**③ Poll**

```bash
skills/mass-pipeline/scripts/poll-task.sh {workspace} {stage.agentRun} {task_id}
poll_exit=$?
stage_elapsed=$(( $(date +%s) - stage_start_time ))
stage_durations[{stage.name}]=$stage_elapsed
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
task_json=$(massctl agentrun task get -w {workspace} --run {stage.agentRun} {task_id} -o json)
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

**⓪ Ensure all sub-task agentRuns are ready:**

```bash
for sub_task in "${stage.tasks[@]}"; do
  skills/mass-pipeline/scripts/ensure-agentrun.sh \
    {workspace} {sub_task.agentRun} {pipeline_file}
  # Track for cleanup
  if [[ ! " ${created_agents[*]} " =~ " ${sub_task.agentRun} " ]]; then
    created_agents+=("${sub_task.agentRun}")
  fi
done
```

**① Concurrently make all sub-tasks** (input files per ① from 2a):

```bash
# Make task for each sub-task, collect task_ids
declare -A sub_task_ids
for sub_task in "${stage.tasks[@]}"; do
  SUB_OUTPUT_DIR="${WORKSPACE_PATH}/.mass/{workspace}/${sub_task.agentRun}/output/{stage.name}"
  mkdir -p "$SUB_OUTPUT_DIR"
  task_id=$(massctl agentrun task do -w {workspace} --run {sub_task.agentRun} \
    --prompt "{sub_task.prompt}" \
    --output-dir "$SUB_OUTPUT_DIR" \
    $(for f in "${sub_task_input_files[@]}"; do echo "--input-files $f"; done) \
    | jq -r '.id')
  sub_task_ids[{sub_task.agentRun}]=$task_id
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

Don't move files. Agents wrote to `.mass/{workspace}/{agentRun}/output/{stage}/` via `--output-dir`. Scan, record for Step 5:

```bash
for stage_name in all_executed_stages; do
  STAGE_OUTPUT_DIR="${WORKSPACE_PATH}/.mass/{workspace}/{stage.agentRun}/output/{stage_name}"
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

Use **mass-guide**, run sequentially. **Only clean up agents that were actually created:**

```bash
# 1. Stop all created agentruns
for agent in "${created_agents[@]}"; do
  massctl agentrun stop "$agent" -w {workspace}
done

# 2. Delete all created agentruns (skip when preserve_workspace=true)
for agent in "${created_agents[@]}"; do
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
    agentRun:  worker (claude)
    output:    .mass/{workspace}/worker/output/audit_plan/
               ├── audit-report.md
               └── fix-plan.md

  review       success   attempt 1/3   18s
    prompt:    "Review audit-report.md and fix-plan.md ..."
    agentRun:  reviewer (claude)
    output:    .mass/{workspace}/reviewer/output/review/
               └── final-fix-plan.md
```

Escalate additionally prints:
```
=== ESCALATION ===
Stage:       {stage_name}
Reason:      {response.description}
Retry count: {n}/{max_retries}

Artifacts:   .mass/{workspace}/{agentRun}/output/{stage_name}/

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
| ensure-agentrun failed (all fallbacks exhausted) | `__escalate__` directly |
| poll exit 2 (agent error) | Skip routes → `__escalate__` directly |
| poll exit 1/3 (idle/timeout) | Treat as `failed`, follow normal routes |
| retry limit exceeded | Force `__escalate__`, ignore routes |
| `__escalate__` | Print full context, retain artifacts, clean up processes |
| No matching route | Semantic judgment; if unable → `__escalate__` |
| cleanup failed | Log warning, continue remaining cleanup |

---

## Design Principles

1. **Self-contained pipeline YAML** — workspace, agentRuns, stages all in one file; no separate compose file
2. **On-demand agent creation** — agents launched only when a stage needs them; fallback chain on startup failure
3. **YAML = semantic description, not template** — orchestrator reads `description`, constructs prompts; no hardcoded templates
4. **Agents don't communicate directly** — all coordination via orchestrator + task API
5. **Preserve artifacts on failure** — not auto-deleted; kept for debugging
6. **Validation front-loaded** — YAML issues before startup, not mid-run
7. **Cleanup guaranteed** — all termination paths run cleanup

---

## Related Skills

| Skill | Responsibility |
|-------|----------------|
| `mass-guide` | Prerequisite: workspace/agent lifecycle primitives |
| `mass-pipeline` | This skill: declarative config-driven orchestrator |
