---
name: mass-guide
description: |
  Manage workspaces, agent lifecycles, and task delegation in MASS via the massctl CLI.
  Triggered when the user mentions mass, massctl, agent lifecycle, workspace, task, or wants to start/manage AI agents.
  For multi-agent orchestration, see the mass-pilot skill.
version: 0.3.0
---

# MASS Usage Guide

Use `massctl` to create workspaces, start agents, and manage lifecycles.

## Health Check (Required Before Every Operation)

```bash
mass daemon status
```

- `daemon: running (pid: N)` → continue
- `daemon: not running` → **Stop. Inform the user that the mass daemon is not running. Do not start it yourself.**

> `--socket` defaults to `$HOME/.mass/mass.sock`. Add `--socket <path>` for a custom path. This flag is omitted in the examples below.

### View Available Agents

After passing the health check, confirm the currently available agent definitions:

```bash
massctl agent get
```

Although the daemon ships with built-in agents `claude`, `codex`, and `gsd-pi`, users may have defined additional custom agents. **Always rely on the actual output of `agent get`; do not assume only built-in agents exist.**

## Core Concepts

| Object | Meaning | Identifier |
|--------|---------|------------|
| **Workspace** | Shared working directory for agents (git clone / local path / empty dir) | `name` |
| **Agent** | Reusable agent definition (command + args + env + disabled) | `name` |
| **AgentRun** | A running agent instance bound to a workspace | `(workspace, name)` |
| **Task** | Structured task delegation (request → agent → response) | `(workspace, agent, task-id)` |

## Built-in Agents

| Name | Strengths | Best Role | Default State |
|------|-----------|-----------|---------------|
| `claude` | General-purpose — design, coding, planning, analysis | Planner, primary worker, coordinator | Enabled |
| `codex` | Rigorous and strict, good at catching edge cases | Plan reviewer, QA gatekeeper | Enabled |
| `gsd-pi` | Long-running coding tasks, executes step-by-step | Executor (driven by `/gsd auto <plan>`) | **Disabled** |

> `gsd-pi` is disabled by default (`disabled: true`). To enable: `massctl agent apply gsd-pi --disabled=false`

## End-to-End Flow

```
health check → compose apply → all agents idle
  → task do → poll until done → read reason
  → compose down (stop + delete agents + delete workspace)
```

> For multi-agent orchestration, see the [mass-pilot](../mass-pilot/SKILL.md) skill.

---

## Part 1: Workspace Management

### Create a Workspace

```bash
# Mount a local directory (mass will not delete it)
massctl workspace create local --name my-ws --path /path/to/code --wait

# Clone a git repository (mass manages the directory)
massctl workspace create git --name my-ws --url https://github.com/org/repo.git --ref main --wait

# Shallow clone
massctl workspace create git --name my-ws --url https://github.com/org/repo.git --ref main --depth 1 --wait

# Empty directory
massctl workspace create empty --name my-ws --wait

# Create from a YAML spec file
massctl workspace create -f workspace.yaml --wait
```

`--wait` blocks until the workspace enters the ready state. Without `--wait`, poll manually:

```bash
massctl workspace get my-ws
# Wait until status.phase == "ready"
# If phase == "error" → creation failed, check source configuration
```

### View / Delete

```bash
massctl workspace get [NAME]              # list or view workspaces
massctl workspace get [NAME] -o json      # JSON output (supports table, wide, json, yaml)
massctl workspace delete NAME             # delete (all agentruns must be removed first)
massctl workspace delete NAME --force     # automatically stop + delete all agentruns, then delete
```

### Workspace Send: Inter-Agent Messaging

```bash
# Note: the long-form workspace flag for ws send is --name (not --workspace); short form -w also works
massctl workspace send -w my-ws --from agent-a --to agent-b --text "task complete"
```

Sends a message to another agent within the workspace, used for agent-to-agent collaboration.

---

## Part 2: Starting Agents (Recommended: Compose)

### Compose Apply: Declarative Multi-Agent Startup (Recommended)

```bash
massctl compose apply -f compose.yaml

# Override the workspace name defined in the compose file
massctl compose apply -f compose.yaml --workspace my-custom-ws
```

Automatically: creates the workspace → waits for ready → creates all agents → waits for all to be idle → prints the socket path for each agent.

**This is the recommended way to start agents.** A single command replaces the manual steps of creating a workspace, creating agentruns one by one, and polling each one separately.

See [../mass-pipeline/references/compose-schema.md](../mass-pipeline/references/compose-schema.md) for the compose file format.

```yaml
# compose.yaml minimal example
kind: workspace-compose
metadata:
  name: my-ws
spec:
  source:
    type: local
    path: /path/to/code
  runs:
    - name: worker
      agent: claude
      systemPrompt: "You are a senior engineer."
```

### Compose Run: Quickly Start a Single Agent

```bash
# Use the current directory and quickly start an agent
massctl compose run -w my-ws --agent claude

# Specify a name and system prompt
massctl compose run -w my-ws --agent claude --name reviewer \
  --system-prompt "You are a code reviewer."

# Return immediately without waiting for the agent to become idle
massctl compose run -w my-ws --agent claude --no-wait

# Use a workflow file
massctl compose run -w my-ws --agent claude --workflow workflow.yaml
```

If the workspace already exists and is ready, it is reused automatically; otherwise a new local workspace is created from the current directory.

---

## Part 3: Manual AgentRun Management (Fallback)

Manual management is for scenarios requiring fine-grained control over individual agentruns. Prefer `compose apply` for bulk startup.

An AgentRun belongs to a workspace and is identified by `(workspace, name)`.

### State Machine

```
creating ──┐
           ├──> idle ──> running ──> stopped
           |              │
    error <─┴─────────────┘
```

| State | Meaning | Allowed Operations |
|-------|---------|--------------------|
| `creating` | Starting up | Poll and wait |
| `idle` | Ready | prompt, task do, stop |
| `running` | Processing a prompt | cancel, stop |
| `stopped` | Stopped, can be resumed | restart, delete |
| `error` | Failed | restart, delete |

### Create

```bash
massctl agentrun create \
  -w my-ws --name worker --agent claude \
  --system-prompt "You are a senior engineer."
```

Optional flags:
- `--permissions approve_all|approve_reads|deny_all`
- `--wait` — wait for the agentrun to enter idle state (avoids manual polling)
- `--workflow <path>` — path to a workflow file

Startup is **asynchronous**:

```bash
massctl agentrun get worker -w my-ws   # poll until status.phase == "idle"
```

### Lifecycle Operations

```bash
massctl agentrun stop worker -w my-ws       # → stopped
massctl agentrun restart worker -w my-ws     # stopped/error → creating → idle
massctl agentrun cancel worker -w my-ws      # cancel the current turn (running → idle)
massctl agentrun delete worker -w my-ws      # delete the record (requires stopped/error state)
```

### View

```bash
massctl agentrun get -w my-ws                    # list all agentruns in the workspace
massctl agentrun get -w my-ws --phase idle       # filter by state
massctl agentrun get worker -w my-ws             # view a specific agentrun
massctl agentrun get worker -w my-ws -o json     # JSON output (supports table, wide, json, yaml)
```

---

## Part 4: Interacting with Agents

### Send a Prompt

Only available when the agent state is `idle`.

```bash
# Fire and forget
massctl agentrun prompt worker -w my-ws --text "Fix the auth bug"

# Wait for the result (5-minute timeout)
massctl agentrun prompt worker -w my-ws --text "Fix the auth bug" --wait
```

### Task Lifecycle

A task is a structured way to delegate work, automatically handling prompts, file passing, and result collection.

#### Task State Machine

```
[do] → agent running → [agent calls task done] → done=true
                                                    │
                                          reason + updatedAt populated
```

The agent calls `massctl agentrun task done` to complete a task. The `done` field is written as a bool `true` by Go code — type-safe.

#### Create a Task (Automatically Prompts the Agent)

```bash
massctl agentrun task do -w {workspace} --run {agent} \
  --prompt "{task_prompt}" \
  --input-files {file_1} --input-files {file_2} \
  --output-dir {output_path}
```

| Flag | Required | Description |
|------|----------|-------------|
| `-w, --workspace` | yes | Workspace name |
| `--run` | yes | AgentRun name |
| `--prompt` | yes | Task prompt / description |
| `--input-files` | no | Input file paths (can be specified multiple times) |
| `--output-dir` | no | Directory for agent output files (default: tasks/{task-id}/output/) |

`task do` will:
1. Check whether the agent is idle (returns an error if not)
2. Create a task file (ID is system-generated)
3. Automatically prompt the agent (with the built-in task protocol)
4. Transition agent state from idle → running

Returns `task.id` and `taskPath` for subsequent queries.

#### Complete a Task (Called by the Agent)

After the agent finishes its work, it calls:

```bash
massctl agentrun task done \
  --task-file {task-path} \
  --reason {reason} \
  --response '{"description":"...","filePaths":["..."]}'
```

| Flag | Required | Description |
|------|----------|-------------|
| `--task-file` | yes | Path to the task JSON file (provided in the task do request prompt) |
| `--reason` | yes | Result description string, e.g. success / failed / needs_human |
| `--response` | yes | JSON object containing at least `description`; may include `filePaths` |

The CLI writes: `done=true` (bool), `reason`, `updatedAt=now()`, atomically replacing the file.

#### Query Task Status

```bash
massctl agentrun task get -w {workspace} --run {agent} {task-id} [-o json|table]
```

Task JSON structure (`AgentTask`):

```json
{
  "id": "task-0001",
  "assignee": "worker",
  "attempt": 1,
  "createdAt": "2026-04-27T00:00:00Z",
  "request": {
    "prompt": "...",
    "inputFiles": ["..."],
    "outputDir": "..."
  },
  "done": false,            // ← bool, written by the task done CLI
  "reason": "",             // ← result string set by the agent (populated after done=true)
  "updatedAt": null,        // ← automatically set to current time when task is done
  "response": { ... }       // ← additional JSON (description, filePaths, etc.)
}
```

Polling example:

```bash
# Poll until done == true
while true; do
  done=$(cat /path/to/task.json | jq -r '.done')
  [[ "$done" == "true" ]] && break
  sleep 5
done
reason=$(cat /path/to/task.json | jq -r '.reason')
echo "Task finished with reason: $reason"
```

#### List Tasks

```bash
massctl agentrun task get -w {workspace} --run {agent}
```

#### Retry a Task

```bash
massctl agentrun task retry -w {workspace} --run {agent} {task-id}
```

Increments the `attempt` counter, clears the old response / reason / done, and automatically re-prompts the agent.

> For multi-agent orchestration (task-based workflow), see the [mass-pilot](../mass-pilot/SKILL.md) skill.

### Interactive Chat

```bash
massctl agentrun chat worker -w my-ws
```

### End-to-End Example (compose + task)

```bash
# 1. Start up
massctl compose apply -f compose.yaml   # workspace + agents, all in one command

# 2. Delegate a task
massctl agentrun task do -w my-ws --run worker \
  --prompt "Fix nil pointer in pkg/auth/handler.go:42"
# → returns task-id and taskPath

# 3. Poll until complete (or use poll-task.sh)
# done=true → read reason

# 4. Clean up
massctl agentrun stop worker -w my-ws
massctl agentrun delete worker -w my-ws
massctl workspace delete my-ws
```

---

## Part 5: Error Handling

For detailed error diagnosis, recovery procedures, and decision trees, see [references/error-handling.md](references/error-handling.md).

### Agent Disabled Diagnosis

If `agentrun/create` returns an `agent <name> is disabled` error:

```bash
# Check whether the agent is disabled
massctl agent get

# Enable the specified agent
massctl agent apply <name> --disabled=false
```

### Quick Recovery

```bash
# Check status
massctl agentrun get -w my-ws

# error state → restart
massctl agentrun restart <name> -w my-ws

# stuck in running → cancel → re-prompt
massctl agentrun cancel <name> -w my-ws

# Rebuild everything
for agent in $(massctl agentrun get -w my-ws -o json | jq -r '.[].metadata.name'); do
  massctl agentrun stop $agent -w my-ws 2>/dev/null
  massctl agentrun delete $agent -w my-ws 2>/dev/null
done
massctl workspace delete my-ws
```

### Cleanup Order

For manual cleanup, the required order is: **stop agent → delete agent → delete workspace** — the order must not be reversed.
Alternatively, use `massctl workspace delete NAME --force` to do it all in one step (automatically stops + deletes all agentruns).
