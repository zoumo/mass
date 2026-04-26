#!/usr/bin/env bash
# Initialize workspace and agentrun instances for a mass-pipeline execution.
# Reads the compose template path from the pipeline YAML, injects the workspace
# name, then delegates all creation and readiness polling to `massctl compose apply`.
#
# Usage: init-workflow.sh <pipeline.yaml> <workspace-name>
#
# Exit codes:
#   0 — workspace ready + all agents idle
#   1 — creation or polling failed
#   2 — invalid usage or missing dependency

set -euo pipefail

PIPELINE_FILE="${1:?Usage: init-workflow.sh <pipeline.yaml> <workspace-name>}"
WORKSPACE_NAME="${2:?Missing workspace-name}"

if [[ ! -f "$PIPELINE_FILE" ]]; then
  echo "Error: pipeline file not found: $PIPELINE_FILE" >&2
  exit 1
fi

if ! python3 -c "import yaml" 2>/dev/null; then
  echo "Error: PyYAML not available. Install with: pip3 install pyyaml" >&2
  exit 2
fi

if ! command -v jq &>/dev/null; then
  echo "Error: jq not available. Install with: brew install jq" >&2
  exit 2
fi

# Read compose template path from pipeline YAML
COMPOSE_TEMPLATE=$(python3 - "$PIPELINE_FILE" <<'PYEOF'
import sys, yaml
with open(sys.argv[1]) as f:
    wf = yaml.safe_load(f)
print(wf.get("compose", ""))
PYEOF
)

if [[ -z "$COMPOSE_TEMPLATE" ]]; then
  echo "Error: pipeline YAML missing 'compose' field" >&2
  exit 1
fi

if [[ ! -f "$COMPOSE_TEMPLATE" ]]; then
  echo "Error: compose template not found: $COMPOSE_TEMPLATE" >&2
  exit 1
fi

# Generate a temp compose file with the workspace name injected
TEMP_COMPOSE=$(mktemp /tmp/mass-compose-XXXXXX.yaml)
trap 'rm -f "$TEMP_COMPOSE"' EXIT

python3 - "$COMPOSE_TEMPLATE" "$WORKSPACE_NAME" "$TEMP_COMPOSE" <<'PYEOF'
import sys, yaml
template_path, workspace_name, out_path = sys.argv[1], sys.argv[2], sys.argv[3]
with open(template_path) as f:
    cfg = yaml.safe_load(f)
cfg["metadata"]["name"] = workspace_name
with open(out_path, "w") as f:
    yaml.dump(cfg, f, allow_unicode=True, default_flow_style=False)
PYEOF

echo "Applying compose for workspace: $WORKSPACE_NAME"
massctl compose apply -f "$TEMP_COMPOSE"

echo "Initialization complete. Workspace '$WORKSPACE_NAME' ready."
