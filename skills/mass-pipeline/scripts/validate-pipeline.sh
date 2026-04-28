#!/usr/bin/env bash
# Validate a mass-pipeline YAML file before execution.
# Validates: name, workspace, agentRuns, stages, routes.
#
# Usage: validate-pipeline.sh <pipeline.yaml>
# Exit codes:
#   0 — validation passed
#   1 — validation failed (errors printed to stderr)
#   2 — invalid usage or missing dependency

set -euo pipefail

PIPELINE_FILE="${1:?Usage: validate-pipeline.sh <pipeline.yaml>}"

if [[ ! -f "$PIPELINE_FILE" ]]; then
  echo "Error: pipeline file not found: $PIPELINE_FILE" >&2
  exit 1
fi

if ! python3 -c "import yaml" 2>/dev/null; then
  echo "Error: PyYAML not available. Install with: pip3 install pyyaml" >&2
  exit 2
fi

python3 - "$PIPELINE_FILE" <<'PYEOF'
import sys
import yaml

errors = []
pipeline_file = sys.argv[1]

with open(pipeline_file) as f:
    try:
        wf = yaml.safe_load(f)
    except yaml.YAMLError as e:
        print(f"Error: invalid YAML: {e}", file=sys.stderr)
        sys.exit(1)

if not isinstance(wf, dict):
    print("Error: pipeline must be a YAML mapping", file=sys.stderr)
    sys.exit(1)

if not wf.get("name"):
    errors.append("'name' field is required")

# Validate workspace
ws = wf.get("workspace")
if not isinstance(ws, dict):
    errors.append("'workspace' field is required and must be a mapping")
else:
    source = ws.get("source")
    if not isinstance(source, dict):
        errors.append("'workspace.source' is required and must be a mapping")
    else:
        src_type = source.get("type")
        if src_type not in ("local", "git", "empty"):
            errors.append(f"'workspace.source.type' must be 'local', 'git', or 'empty' (got: {src_type!r})")
        if src_type == "local" and not source.get("path"):
            errors.append("'workspace.source.path' is required for type 'local'")
        if src_type == "git" and not source.get("url"):
            errors.append("'workspace.source.url' is required for type 'git'")

# Validate agentRuns
agent_runs = wf.get("agentRuns") or {}
if not isinstance(agent_runs, dict) or len(agent_runs) == 0:
    errors.append("'agentRuns' must be a non-empty mapping")
else:
    for run_name, run_def in agent_runs.items():
        if not isinstance(run_def, dict):
            errors.append(f"agentRuns.{run_name} must be a mapping")
            continue
        if not run_def.get("agent"):
            errors.append(f"agentRuns.{run_name} missing 'agent' field")
        fallback = run_def.get("fallback")
        if fallback is not None:
            if not isinstance(fallback, list):
                errors.append(f"agentRuns.{run_name}.fallback must be a list")
            else:
                for i, fb in enumerate(fallback):
                    if isinstance(fb, dict):
                        if not fb.get("agent"):
                            errors.append(f"agentRuns.{run_name}.fallback[{i}] missing 'agent' field")
                    elif not isinstance(fb, str):
                        errors.append(f"agentRuns.{run_name}.fallback[{i}] must be a mapping with 'agent' or a string")

agent_run_names = set(agent_runs.keys()) if isinstance(agent_runs, dict) else set()

# Validate stages
stages = wf.get("stages") or []
if not isinstance(stages, list) or len(stages) == 0:
    errors.append("'stages' must be a non-empty list")
else:
    stage_names = {s.get("name") for s in stages if isinstance(s, dict) and s.get("name")}
    valid_goto = stage_names | {"__done__", "__escalate__"}

    for i, stage in enumerate(stages):
        if not isinstance(stage, dict):
            errors.append(f"stages[{i}] must be a mapping")
            continue

        stage_name = stage.get("name") or f"stages[{i}]"
        stage_type = stage.get("type", "serial")

        if not stage.get("name"):
            errors.append(f"stages[{i}] missing 'name' field")

        if not stage.get("routes"):
            errors.append(f"stage '{stage_name}' missing 'routes'")
        else:
            for route in stage.get("routes", []):
                if not isinstance(route, dict):
                    errors.append(f"stage '{stage_name}' route must be a mapping")
                    continue
                goto = route.get("goto")
                if not goto:
                    errors.append(f"stage '{stage_name}' route missing 'goto'")
                elif goto not in valid_goto:
                    errors.append(
                        f"stage '{stage_name}' route goto '{goto}' is not a known stage, __done__, or __escalate__"
                    )

        if stage_type == "parallel":
            tasks = stage.get("tasks") or []
            if not tasks:
                errors.append(f"stage '{stage_name}': parallel stage must have non-empty 'tasks'")
            for j, task in enumerate(tasks):
                if not isinstance(task, dict):
                    errors.append(f"stage '{stage_name}' tasks[{j}] must be a mapping")
                    continue
                task_run = task.get("agentRun")
                if not task_run:
                    errors.append(f"stage '{stage_name}' tasks[{j}] missing 'agentRun'")
                elif task_run not in agent_run_names:
                    errors.append(
                        f"stage '{stage_name}' tasks[{j}].agentRun '{task_run}' not defined in agentRuns"
                    )
                if not task.get("description"):
                    errors.append(f"stage '{stage_name}' tasks[{j}] missing 'description'")
                for from_stage in task.get("input_from") or []:
                    if from_stage not in stage_names:
                        errors.append(
                            f"stage '{stage_name}' tasks[{j}].input_from '{from_stage}' not a known stage"
                        )
            wait_val = stage.get("wait", "all")
            if wait_val not in ("all", "any"):
                errors.append(f"stage '{stage_name}' wait must be 'all' or 'any' (got: {wait_val!r})")
        else:
            agent_run = stage.get("agentRun")
            if not agent_run:
                errors.append(f"stage '{stage_name}' missing 'agentRun' field")
            elif agent_run not in agent_run_names:
                errors.append(
                    f"stage '{stage_name}' agentRun '{agent_run}' not defined in agentRuns"
                )
            if not stage.get("description"):
                errors.append(f"stage '{stage_name}' missing 'description' field")
            for from_stage in stage.get("input_from") or []:
                if from_stage not in stage_names:
                    errors.append(
                        f"stage '{stage_name}' input_from '{from_stage}' not a known stage"
                    )

if errors:
    print(f"Pipeline validation failed ({len(errors)} error(s)):", file=sys.stderr)
    for err in errors:
        print(f"  - {err}", file=sys.stderr)
    sys.exit(1)

stages_list = wf.get("stages") or []
agent_runs_map = wf.get("agentRuns") or {}
print(f"Pipeline '{wf.get('name')}' validated successfully.")
print(f"  AgentRuns: {', '.join(agent_runs_map.keys())}")
print(f"  Stages: {' → '.join(s.get('name', '?') for s in stages_list)}")
PYEOF
