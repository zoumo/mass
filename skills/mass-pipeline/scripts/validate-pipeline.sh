#!/usr/bin/env bash
# Validate a mass-pipeline YAML file before execution.
# Only validates pipeline-owned fields: name, stages, routes, output.
# Compose file and workspace are orchestrator concerns, not validated here.
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
                if not task.get("agent"):
                    errors.append(f"stage '{stage_name}' tasks[{j}] missing 'agent'")
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
            if not stage.get("agent"):
                errors.append(f"stage '{stage_name}' missing 'agent' field")
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
print(f"Pipeline '{wf.get('name')}' validated successfully.")
print(f"  Stages: {' → '.join(s.get('name', '?') for s in stages_list)}")
PYEOF
