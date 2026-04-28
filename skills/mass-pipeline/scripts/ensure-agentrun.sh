#!/usr/bin/env bash
# Ensure an agentrun is ready (idle or running) before dispatching a task.
# Reads the agentRuns definition from the pipeline YAML to create on demand.
# On startup failure, tries each fallback agent type in order.
#
# Usage: ensure-agentrun.sh <workspace> <agentrun-name> <pipeline.yaml> [timeout=120]
#
# Exit codes:
#   0 — agentrun is ready (idle or running)
#   1 — all attempts failed (primary + fallbacks)
#   2 — invalid usage or missing dependency

set -euo pipefail

WORKSPACE="${1:?Usage: ensure-agentrun.sh <workspace> <agentrun-name> <pipeline.yaml> [timeout]}"
AGENTRUN_NAME="${2:?Missing agentrun-name}"
PIPELINE_FILE="${3:?Missing pipeline.yaml}"
TIMEOUT="${4:-120}"

if [[ ! -f "$PIPELINE_FILE" ]]; then
  echo "Error: pipeline file not found: $PIPELINE_FILE" >&2
  exit 2
fi

if ! python3 -c "import yaml" 2>/dev/null; then
  echo "Error: PyYAML not available. Install with: pip3 install pyyaml" >&2
  exit 2
fi

# Check current agentrun state. Returns phase or "notfound".
get_phase() {
  local phase
  phase=$(massctl agentrun get "$AGENTRUN_NAME" -w "$WORKSPACE" -o json 2>/dev/null \
    | jq -r '.status.phase // "unknown"') || echo "notfound"
  echo "$phase"
}

# Wait for agentrun to reach idle state within TIMEOUT seconds.
# Returns 0 on success, 1 on timeout/error.
wait_idle() {
  local elapsed=0
  local interval=3
  while (( elapsed < TIMEOUT )); do
    local phase
    phase=$(get_phase)
    case "$phase" in
      idle|running)
        echo "Agentrun $AGENTRUN_NAME is $phase."
        return 0
        ;;
      error|stopped)
        echo "Agentrun $AGENTRUN_NAME entered $phase during startup." >&2
        return 1
        ;;
    esac
    sleep "$interval"
    elapsed=$((elapsed + interval))
  done
  echo "Timeout (${TIMEOUT}s) waiting for $AGENTRUN_NAME to become idle." >&2
  return 1
}

# Create agentrun with a specific agent type. Reads systemPrompt/permissions/mcpServers
# from pipeline YAML agentRuns definition.
# Args: agent_type
create_agentrun() {
  local agent_type="$1"
  echo "Creating agentrun $AGENTRUN_NAME with agent=$agent_type..."

  # Extract agentrun definition from pipeline YAML
  local run_def
  run_def=$(python3 - "$PIPELINE_FILE" "$AGENTRUN_NAME" <<'PYEOF'
import sys, json, yaml
pipeline_file, run_name = sys.argv[1], sys.argv[2]
with open(pipeline_file) as f:
    pipeline = yaml.safe_load(f)
agent_runs = pipeline.get("agentRuns") or {}
run_def = agent_runs.get(run_name)
if not run_def:
    print("{}", end="")
    sys.exit(0)
json.dump(run_def, sys.stdout)
PYEOF
  )

  if [[ -z "$run_def" || "$run_def" == "{}" ]]; then
    echo "Error: agentrun '$AGENTRUN_NAME' not found in pipeline agentRuns." >&2
    return 1
  fi

  # Build massctl agentrun create command
  local cmd=(massctl agentrun create --name "$AGENTRUN_NAME" -w "$WORKSPACE"
    --agent "$agent_type"
    --wait)

  # Extract optional systemPrompt
  local system_prompt
  system_prompt=$(echo "$run_def" | jq -r '.systemPrompt // empty')
  if [[ -n "$system_prompt" ]]; then
    cmd+=(--system-prompt "$system_prompt")
  fi

  "${cmd[@]}" 2>&1
}

# Stop and delete an agentrun (best-effort cleanup for retry).
cleanup_agentrun() {
  massctl agentrun stop "$AGENTRUN_NAME" -w "$WORKSPACE" 2>/dev/null || true
  massctl agentrun delete "$AGENTRUN_NAME" -w "$WORKSPACE" 2>/dev/null || true
}

# Extract agent type candidates: primary + fallbacks from pipeline YAML.
# Output: one agent type per line.
get_agent_candidates() {
  python3 - "$PIPELINE_FILE" "$AGENTRUN_NAME" <<'PYEOF'
import sys, yaml
pipeline_file, run_name = sys.argv[1], sys.argv[2]
with open(pipeline_file) as f:
    pipeline = yaml.safe_load(f)
agent_runs = pipeline.get("agentRuns") or {}
run_def = agent_runs.get(run_name)
if not run_def:
    sys.exit(1)
# Primary agent type
print(run_def.get("agent", "claude"))
# Fallback agent types
for fb in run_def.get("fallback") or []:
    if isinstance(fb, dict) and fb.get("agent"):
        print(fb["agent"])
    elif isinstance(fb, str):
        print(fb)
PYEOF
}

# --- Main ---

phase=$(get_phase)

# Already ready
if [[ "$phase" == "idle" || "$phase" == "running" ]]; then
  echo "Agentrun $AGENTRUN_NAME already $phase."
  exit 0
fi

# Stopped or error — clean up before recreating
if [[ "$phase" == "stopped" || "$phase" == "error" ]]; then
  echo "Agentrun $AGENTRUN_NAME in $phase state. Cleaning up for recreation..."
  cleanup_agentrun
fi

# Creating — wait for it
if [[ "$phase" == "creating" ]]; then
  echo "Agentrun $AGENTRUN_NAME is creating, waiting..."
  if wait_idle; then
    exit 0
  fi
  echo "Agentrun $AGENTRUN_NAME failed during creation. Cleaning up..." >&2
  cleanup_agentrun
fi

# Get candidate agent types (primary + fallbacks)
candidates=()
while IFS= read -r line; do
  candidates+=("$line")
done < <(get_agent_candidates)

if [[ ${#candidates[@]} -eq 0 ]]; then
  echo "Error: no agent candidates found for '$AGENTRUN_NAME' in pipeline YAML." >&2
  exit 1
fi

# Try each candidate
for i in "${!candidates[@]}"; do
  agent_type="${candidates[$i]}"
  if (( i > 0 )); then
    echo "Falling back to agent=$agent_type..."
  fi

  if create_agentrun "$agent_type"; then
    if wait_idle; then
      exit 0
    fi
  fi

  echo "Agent $agent_type failed for $AGENTRUN_NAME." >&2
  cleanup_agentrun
done

echo "All agent candidates exhausted for $AGENTRUN_NAME." >&2
exit 1
