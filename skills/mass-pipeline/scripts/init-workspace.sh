#!/usr/bin/env bash
# Apply a compose file with a specific workspace name.
# Injects the workspace name into metadata.name, then calls `massctl compose apply`.
#
# Usage: init-workspace.sh <compose_file> <workspace_name>
#
# Exit codes:
#   0 — workspace ready + all agents idle
#   1 — creation or polling failed
#   2 — missing dependency

set -euo pipefail

COMPOSE_FILE="${1:?Usage: init-workspace.sh <compose_file> <workspace_name>}"
WORKSPACE_NAME="${2:?Missing workspace_name}"

if [[ ! -f "$COMPOSE_FILE" ]]; then
  echo "Error: compose file not found: $COMPOSE_FILE" >&2
  exit 1
fi

if ! python3 -c "import yaml" 2>/dev/null; then
  echo "Error: PyYAML not available. Install with: pip3 install pyyaml" >&2
  exit 2
fi

# Inject workspace name into metadata.name, write to temp file
TEMP_COMPOSE=$(mktemp /tmp/mass-compose-XXXXXX.yaml)
trap 'rm -f "$TEMP_COMPOSE"' EXIT

python3 - "$COMPOSE_FILE" "$WORKSPACE_NAME" "$TEMP_COMPOSE" <<'PYEOF'
import sys, yaml
compose_path, workspace_name, out_path = sys.argv[1], sys.argv[2], sys.argv[3]
with open(compose_path) as f:
    cfg = yaml.safe_load(f)
cfg["metadata"]["name"] = workspace_name
with open(out_path, "w") as f:
    yaml.dump(cfg, f, allow_unicode=True, default_flow_style=False)
PYEOF

echo "Applying compose for workspace: $WORKSPACE_NAME"
massctl compose apply -f "$TEMP_COMPOSE"

echo "Initialization complete. Workspace '$WORKSPACE_NAME' ready."
