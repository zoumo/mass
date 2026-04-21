#!/usr/bin/env bash
# Poll a task file until the agent completes it or an error/timeout occurs.
#
# Usage: poll-task.sh <workspace> <agent-name> <task-path> [interval=10] [timeout=1800]
#
# Exit codes:
#   0 — task completed (completed==true), read response.status for routing
#   1 — agent idle but task not completed after max re-prompts
#   2 — agent in error/stopped state
#   3 — timeout

set -euo pipefail

WORKSPACE="${1:?Usage: poll-task.sh <workspace> <agent-name> <task-path> [interval] [timeout]}"
AGENT_NAME="${2:?Missing agent-name}"
TASK_PATH="${3:?Missing task-path}"
INTERVAL="${4:-10}"
TIMEOUT="${5:-1800}"

MAX_REPROMPTS=2
reprompt_count=0
elapsed=0

prompt_text="Your task is at: ${TASK_PATH}. Read it, complete the work, update the task file."

while true; do
  # 1. Check agent state
  agent_state=$(massctl agentrun get "$AGENT_NAME" -w "$WORKSPACE" -o json 2>/dev/null | jq -r '.status.state // "unknown"')

  # 2. Check task completed field
  task_completed="false"
  if [[ -f "$TASK_PATH" ]]; then
    task_completed=$(jq -r '.completed // false' "$TASK_PATH")
  fi

  # 3. Route
  if [[ "$task_completed" == "true" ]]; then
    echo "Task completed. Response status: $(jq -r '.response.status // "unknown"' "$TASK_PATH")"
    exit 0
  fi

  if [[ "$agent_state" == "error" || "$agent_state" == "stopped" ]]; then
    echo "Agent $AGENT_NAME is in $agent_state state."
    exit 2
  fi

  if [[ "$agent_state" == "idle" ]]; then
    if (( reprompt_count < MAX_REPROMPTS )); then
      reprompt_count=$((reprompt_count + 1))
      echo "Agent idle but task not completed. Re-prompting ($reprompt_count/$MAX_REPROMPTS)..."
      massctl agentrun prompt "$AGENT_NAME" -w "$WORKSPACE" --text "$prompt_text" 2>/dev/null || true
    else
      echo "Agent idle, task not completed after $MAX_REPROMPTS re-prompts."
      exit 1
    fi
  fi

  # agent is running or we just re-prompted — wait
  if (( elapsed >= TIMEOUT )); then
    echo "Timeout after ${TIMEOUT}s."
    exit 3
  fi

  sleep "$INTERVAL"
  elapsed=$((elapsed + INTERVAL))
done
