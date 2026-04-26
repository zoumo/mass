#!/usr/bin/env bash
# Validate a mass-workflow YAML file before execution.
# Usage: validate-workflow.sh <workflow.yaml>
# Exit codes:
#   0 — validation passed
#   1 — validation failed (errors printed to stderr)
#   2 — invalid usage or missing dependency

set -euo pipefail

WORKFLOW_FILE="${1:?Usage: validate-workflow.sh <workflow.yaml>}"

if [[ ! -f "$WORKFLOW_FILE" ]]; then
  echo "Error: workflow file not found: $WORKFLOW_FILE" >&2
  exit 1
fi

if ! python3 -c "import yaml" 2>/dev/null; then
  echo "Error: PyYAML not available. Install with: pip3 install pyyaml" >&2
  exit 2
fi

python3 - "$WORKFLOW_FILE" <<'PYEOF'
import sys
import yaml

errors = []
workflow_file = sys.argv[1]

with open(workflow_file) as f:
    try:
        wf = yaml.safe_load(f)
    except yaml.YAMLError as e:
        print(f"Error: invalid YAML: {e}", file=sys.stderr)
        sys.exit(1)

if not isinstance(wf, dict):
    print("Error: workflow must be a YAML mapping", file=sys.stderr)
    sys.exit(1)

# name
if not wf.get("name"):
    errors.append("'name' field is required")

# workspace
ws = wf.get("workspace") or {}
ws_type = ws.get("type", "")
if ws_type not in ("local", "git", "empty"):
    errors.append(f"'workspace.type' must be one of: local, git, empty (got: {ws_type!r})")
if ws_type in ("local", "git") and not ws.get("path"):
    errors.append(f"'workspace.path' is required when workspace.type is '{ws_type}'")

# agents
agents = wf.get("agents") or {}
if not isinstance(agents, dict) or len(agents) == 0:
    errors.append("'agents' must be a non-empty map")
else:
    for agent_name, agent_cfg in agents.items():
        if not isinstance(agent_cfg, dict) or not agent_cfg.get("system_prompt"):
            errors.append(f"agent '{agent_name}' must have a non-empty 'system_prompt'")

agent_names = set(agents.keys()) if isinstance(agents, dict) else set()

# stages
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
                        f"stage '{stage_name}' route goto '{goto}' is not a known stage name, __done__, or __escalate__"
                    )

        if stage_type == "parallel":
            tasks = stage.get("tasks") or []
            if not tasks:
                errors.append(f"stage '{stage_name}': parallel stage must have non-empty 'tasks'")
            for j, task in enumerate(tasks):
                if not isinstance(task, dict):
                    errors.append(f"stage '{stage_name}' tasks[{j}] must be a mapping")
                    continue
                task_agent = task.get("agent")
                if not task_agent:
                    errors.append(f"stage '{stage_name}' tasks[{j}] missing 'agent'")
                elif task_agent not in agent_names:
                    errors.append(
                        f"stage '{stage_name}' tasks[{j}].agent '{task_agent}' not defined in agents"
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
            # serial stage
            agent = stage.get("agent")
            if not agent:
                errors.append(f"stage '{stage_name}' missing 'agent' field")
            elif agent not in agent_names:
                errors.append(f"stage '{stage_name}'.agent '{agent}' not defined in agents")
            if not stage.get("description"):
                errors.append(f"stage '{stage_name}' missing 'description' field")
            for from_stage in stage.get("input_from") or []:
                if from_stage not in stage_names:
                    errors.append(
                        f"stage '{stage_name}' input_from '{from_stage}' not a known stage"
                    )

if errors:
    print(f"Workflow validation failed ({len(errors)} error(s)):", file=sys.stderr)
    for err in errors:
        print(f"  - {err}", file=sys.stderr)
    sys.exit(1)

print(f"Workflow '{wf.get('name')}' validated successfully.")
agents_obj = wf.get("agents") or {}
stages_list = wf.get("stages") or []
print(f"  Agents: {', '.join(agents_obj.keys())}")
print(f"  Stages: {' → '.join(s.get('name', '?') for s in stages_list)}")
PYEOF
