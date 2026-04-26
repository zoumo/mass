#!/usr/bin/env bash
# Initialize workspace and agentrun instances for a mass-pipeline execution.
# Reads agent definitions from workflow YAML and creates all resources.
#
# Usage: init-workflow.sh <workflow.yaml> <workspace-name> [ws-timeout=120] [agent-timeout=90]
#
# Exit codes:
#   0 — workspace ready + all agents idle
#   1 — creation or polling failed
#   2 — invalid usage or missing dependency

set -euo pipefail

WORKFLOW_FILE="${1:?Usage: init-workflow.sh <workflow.yaml> <workspace-name>}"
WORKSPACE_NAME="${2:?Missing workspace-name}"
WS_TIMEOUT="${3:-120}"
AGENT_TIMEOUT="${4:-90}"
POLL_INTERVAL=5

if [[ ! -f "$WORKFLOW_FILE" ]]; then
  echo "Error: workflow file not found: $WORKFLOW_FILE" >&2
  exit 1
fi

if ! python3 -c "import yaml" 2>/dev/null; then
  echo "Error: PyYAML not available. Install with: pip3 install pyyaml" >&2
  exit 2
fi

# Extract workspace config and agent data from YAML
WORKSPACE_CONFIG=$(python3 - "$WORKFLOW_FILE" <<'PYEOF'
import sys, yaml, json
with open(sys.argv[1]) as f:
    wf = yaml.safe_load(f)
ws = wf.get("workspace") or {}
agents = {k: v.get("system_prompt", "") for k, v in (wf.get("agents") or {}).items()}
print(json.dumps({"workspace": ws, "agents": agents}))
PYEOF
)

WS_TYPE=$(echo "$WORKSPACE_CONFIG" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d['workspace']['type'])")
WS_PATH=$(echo "$WORKSPACE_CONFIG" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d['workspace'].get('path',''))")
AGENT_NAMES=$(echo "$WORKSPACE_CONFIG" | python3 -c "import sys,json; d=json.load(sys.stdin); print('\n'.join(d['agents'].keys()))")

# ── Create workspace ────────────────────────────────────────────────────────
echo "Creating workspace: $WORKSPACE_NAME (type=$WS_TYPE)"
case "$WS_TYPE" in
  local)
    massctl workspace create local --name "$WORKSPACE_NAME" --path "$WS_PATH"
    ;;
  git)
    massctl workspace create git --name "$WORKSPACE_NAME" --url "$WS_PATH"
    ;;
  empty)
    massctl workspace create empty --name "$WORKSPACE_NAME"
    ;;
  *)
    echo "Error: unknown workspace type: $WS_TYPE" >&2
    exit 1
    ;;
esac

# Poll workspace until ready
elapsed=0
while true; do
  state=$(massctl workspace get "$WORKSPACE_NAME" -o json 2>/dev/null \
    | python3 -c "import sys,json; print(json.load(sys.stdin).get('status',{}).get('state','unknown'))" 2>/dev/null \
    || echo "unknown")
  if [[ "$state" == "ready" ]]; then
    echo "Workspace $WORKSPACE_NAME is ready."
    break
  fi
  if (( elapsed >= WS_TIMEOUT )); then
    echo "Error: workspace $WORKSPACE_NAME not ready after ${WS_TIMEOUT}s (state=$state)" >&2
    exit 1
  fi
  sleep "$POLL_INTERVAL"
  elapsed=$((elapsed + POLL_INTERVAL))
done

# ── Create agentrun instances ────────────────────────────────────────────────
while IFS= read -r agent_name; do
  [[ -z "$agent_name" ]] && continue
  system_prompt=$(echo "$WORKSPACE_CONFIG" \
    | python3 -c "import sys,json; d=json.load(sys.stdin); print(d['agents']['$agent_name'])")
  echo "Creating agentrun: $agent_name"
  massctl agentrun create -w "$WORKSPACE_NAME" --name "$agent_name" --agent claude \
    --system-prompt "$system_prompt"
done <<< "$AGENT_NAMES"

# Poll all agents until idle
while IFS= read -r agent_name; do
  [[ -z "$agent_name" ]] && continue
  echo "Waiting for agent '$agent_name' to become idle..."
  elapsed=0
  while true; do
    state=$(massctl agentrun get "$agent_name" -w "$WORKSPACE_NAME" -o json 2>/dev/null \
      | python3 -c "import sys,json; print(json.load(sys.stdin).get('status',{}).get('state','unknown'))" 2>/dev/null \
      || echo "unknown")
    if [[ "$state" == "idle" ]]; then
      echo "Agent '$agent_name' is idle."
      break
    fi
    if [[ "$state" == "error" || "$state" == "stopped" ]]; then
      echo "Error: agent '$agent_name' entered '$state' state during initialization" >&2
      exit 1
    fi
    if (( elapsed >= AGENT_TIMEOUT )); then
      echo "Error: agent '$agent_name' not idle after ${AGENT_TIMEOUT}s (state=$state)" >&2
      exit 1
    fi
    sleep "$POLL_INTERVAL"
    elapsed=$((elapsed + POLL_INTERVAL))
  done
done <<< "$AGENT_NAMES"

echo "Initialization complete. Workspace '$WORKSPACE_NAME' ready with agents: $(echo "$AGENT_NAMES" | tr '\n' ' ')"
