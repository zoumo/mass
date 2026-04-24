#!/usr/bin/env bash
# Poll a task until the agent completes it or an error/timeout occurs.
# Uses massctl task API.
#
# Usage: poll-task.sh <workspace> <agent-name> <task-id> [interval=10] [timeout=1800]
#
# Exit codes:
#   0 — task completed (completed==true), read response.status for routing
#   1 — Agent idle but task not completed after max retries
#   2 — Agent in error/stopped state
#   3 — 超时

set -euo pipefail

WORKSPACE="${1:?Usage: poll-task.sh <workspace> <agent-name> <task-id> [interval] [timeout]}"
AGENT_NAME="${2:?Missing agent-name}"
TASK_ID="${3:?Missing task-id}"
INTERVAL="${4:-10}"
TIMEOUT="${5:-1800}"

MAX_RETRIES=2
retry_count=0
elapsed=0

get_task() {
  massctl agentrun task get -w "$WORKSPACE" --name "$AGENT_NAME" --id "$TASK_ID" -o json 2>/dev/null
}

while true; do
  # 1. Check agent state
  agent_state=$(massctl agentrun get "$AGENT_NAME" -w "$WORKSPACE" -o json 2>/dev/null | jq -r '.status.state // "unknown"')

  # 2. Check task completed field via API
  task_json=$(get_task)
  task_completed=$(echo "$task_json" | jq -r '.completed // false')

  # 3. Route
  if [[ "$task_completed" == "true" ]]; then
    status=$(echo "$task_json" | jq -r '.response.status // "unknown"')
    echo "Task completed. Response status: $status"
    exit 0
  fi

  if [[ "$agent_state" == "error" || "$agent_state" == "stopped" ]]; then
    echo "Agent $AGENT_NAME is in $agent_state state."
    exit 2
  fi

  if [[ "$agent_state" == "idle" ]]; then
    if (( retry_count < MAX_RETRIES )); then
      retry_count=$((retry_count + 1))
      echo "Agent idle but task not completed. Retrying task ($retry_count/$MAX_RETRIES)..."
      massctl agentrun task retry -w "$WORKSPACE" --name "$AGENT_NAME" --id "$TASK_ID" 2>/dev/null || true
    else
      echo "Agent idle, task not completed after $MAX_RETRIES retries."
      exit 1
    fi
  fi

  # agent is running or we just retried — wait
  if (( elapsed >= TIMEOUT )); then
    echo "Timeout after ${TIMEOUT}s."
    exit 3
  fi

  sleep "$INTERVAL"
  elapsed=$((elapsed + INTERVAL))
done
