#!/usr/bin/env bash
# Poll a task until the agent completes it or an error/timeout occurs.
#
# Usage: poll-task.sh <workspace> <agent-name> <task-id> [interval=10] [timeout=1800]
#
# Exit codes:
#   0 — task completed (completed==true), read .status for routing
#   1 — agent idle but task not completed after max retries
#   2 — agent in error/stopped state
#   3 — timeout

set -euo pipefail

WORKSPACE="${1:?Usage: poll-task.sh <workspace> <agent-name> <task-id> [interval] [timeout]}"
AGENT_NAME="${2:?Missing agent-name}"
TASK_ID="${3:?Missing task-id}"
INTERVAL="${4:-10}"
TIMEOUT="${5:-1800}"

MAX_IDLE_RETRIES=2
idle_retry_count=0
elapsed=0

get_task() {
  massctl agentrun task get -w "$WORKSPACE" --run "$AGENT_NAME" "$TASK_ID" -o json 2>/dev/null
}

is_completed() {
  local val
  val=$(echo "$1" | jq -r '.done // false')
  [[ "$val" == "true" ]]
}

while true; do
  agent_state=$(massctl agentrun get "$AGENT_NAME" -w "$WORKSPACE" -o json 2>/dev/null \
    | jq -r '.status.phase // "unknown"')

  task_json=$(get_task)

  if is_completed "$task_json"; then
    status=$(echo "$task_json" | jq -r '.reason // "unknown"')
    echo "Task completed. Response status: $status"
    exit 0
  fi

  if [[ "$agent_state" == "error" || "$agent_state" == "stopped" ]]; then
    echo "Agent $AGENT_NAME is in $agent_state state." >&2
    exit 2
  fi

  if [[ "$agent_state" == "idle" ]]; then
    if (( idle_retry_count < MAX_IDLE_RETRIES )); then
      idle_retry_count=$((idle_retry_count + 1))
      echo "Agent idle but task not completed. Retrying ($idle_retry_count/$MAX_IDLE_RETRIES)..." >&2
      massctl agentrun task retry -w "$WORKSPACE" --run "$AGENT_NAME" "$TASK_ID" 2>/dev/null || true
    else
      echo "Agent idle, task not completed after $MAX_IDLE_RETRIES retries." >&2
      exit 1
    fi
  fi

  if (( elapsed >= TIMEOUT )); then
    echo "Timeout after ${TIMEOUT}s waiting for task $TASK_ID." >&2
    exit 3
  fi

  sleep "$INTERVAL"
  elapsed=$((elapsed + INTERVAL))
done
