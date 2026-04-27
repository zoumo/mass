---
name: mass-guide
description: |
  Manage workspaces, agent lifecycles, and task delegation in MASS via the massctl CLI.
  Triggered when the user mentions mass, massctl, agent lifecycle, workspace, task, or wants to start/manage AI agents.
  For multi-agent orchestration, see the mass-pipeline skill.
version: 0.3.0
---

# MASS Usage Guide

Run `massctl` to make workspaces, start agents, manage lifecycles.

## Health Check (Run Before Every Operation)

```bash
mass daemon status
```

- `daemon: running (pid: N)` → continue
- `daemon: not running` → **Stop. Tell user daemon not running. Do not start it yourself.**

> `--socket` defaults to `$HOME/.mass/mass.sock`. Add `--socket <path>` for custom path. Omitted in examples below.

### View Available Agents

After health check, confirm available agent definitions:

```bash
massctl agent get
```

Daemon ships with built-in agents `claude`, `codex`, `gsd-pi`; users may define custom agents. **Always rely on actual `agent get` output; do not assume only built-ins exist.**

## Core Concepts

| Object | Meaning | Identifier |
|--------|---------|------------|
| **Workspace** | Shared working dir for agents (git clone / local path / empty dir) | `name` |
| **Agent** | Reusable agent definition (command + args + env + disabled) | `name` |
| **AgentRun** | Running agent instance bound to workspace | `(workspace, name)` |
| **Task** | Structured task delegation (request → agent → response) | `(workspace, agent, task-id)` |

## Built-in Agents

| Name | Strengths | Best Role | Default State |
|------|-----------|-----------|---------------|
| `claude` | General-purpose — design, coding, planning, analysis | Planner, primary worker, coordinator | Enabled |
| `codex` | Rigorous, good at catching edge cases | Plan reviewer, QA gatekeeper | Enabled |
| `gsd-pi` | Long-running coding tasks, step-by-step execution | Executor (driven by `/gsd auto <plan>`) | **Disabled** |

> `gsd-pi` disabled by default (`disabled: true`). Enable: `massctl agent apply gsd-pi --disabled=false`

## End-to-End Flow

```
health check → compose apply → all agents idle
  → task do → poll until done → read reason
  → cleanup (stop agentruns → delete agentruns → delete workspace, or workspace delete --force)
```

---

## Part 1: Workspace Management

### Make Workspace

```bash
# Mount local dir (mass will not delete it)
massctl workspace create local --name my-ws --path /path/to/code --wait

# Clone git repo (mass manages dir)
massctl workspace create git --name my-ws --url https://github.com/org/repo.git --ref main --wait

# Shallow clone
massctl workspace create git --name my-ws --url https://github.com/org/repo.git --ref main --depth 1 --wait

# Empty dir
massctl workspace create empty --name my-ws --wait

# From YAML spec
massctl workspace create -f workspace.yaml --wait
```

`--wait` blocks until workspace enters ready state. Without `--wait`, poll manually:

```bash
massctl workspace get my-ws
# Wait until status.phase == "ready"
# If phase == "error" → creation failed, check source config
```

### View / Delete

```bash
massctl workspace get [NAME]              # list or view workspaces
massctl workspace get [NAME] -o json      # JSON output (supports table, wide, json, yaml)
massctl workspace delete NAME             # delete (all agentruns must be removed first)
massctl workspace delete NAME --force     # auto stop + delete all agentruns, then delete
```

### Workspace Send: Inter-Agent Messaging

```bash
# Note: long-form workspace flag for ws send is --name (not --workspace); short form -w also works
massctl workspace send -w my-ws --from agent-a --to agent-b --text "task complete"
```

Sends message to another agent within workspace, for agent-to-agent collaboration.

---

## Part 2: Start Agents (Preferred: Compose)

### Compose Apply: Declarative Multi-Agent Startup (Preferred)

```bash
massctl compose apply -f compose.yaml

# Override workspace name from compose file
massctl compose apply -f compose.yaml --workspace my-custom-ws
```

Auto: makes workspace → waits ready → makes all agents → waits all idle → prints socket path per agent.

**Preferred way to start agents.** One command replaces manual steps of making workspace, making agentruns one-by-one, polling each separately.

See [../mass-pipeline/references/compose-schema.md](../mass-pipeline/references/compose-schema.md) for compose file format.

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

### Compose Run: Quick Single-Agent Startup

```bash
# Use current dir, quickly start agent
massctl compose run -w my-ws --agent claude

# Specify name and system prompt
massctl compose run -w my-ws --agent claude --name reviewer \
  --system-prompt "You are a code reviewer."

# Return immediately without waiting for idle
massctl compose run -w my-ws --agent claude --no-wait

# Use workflow file
massctl compose run -w my-ws --agent claude --workflow workflow.yaml
```

If workspace already exists and ready, reused automatically; otherwise new local workspace made from current dir.

---

## Part 3: Manual AgentRun Management (Fallback)

Use for fine-grained control over individual agentruns. Prefer `compose apply` for bulk startup.

AgentRun belongs to workspace, identified by `(workspace, name)`.

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
| `running` | Processing prompt | cancel, stop |
| `stopped` | Stopped, resumable | restart, delete |
| `error` | Failed | restart, delete |

### Create

```bash
massctl agentrun create \
  -w my-ws --name worker --agent claude \
  --system-prompt "You are a senior engineer."
```

Optional flags:
- `--permissions approve_all|approve_reads|deny_all`
- `--wait` — wait for agentrun to enter idle (avoids manual polling)
- `--workflow <path>` — path to workflow file

Startup is **asynchronous**:

```bash
massctl agentrun get worker -w my-ws   # poll until status.phase == "idle"
```

### Lifecycle Operations

```bash
massctl agentrun stop worker -w my-ws       # → stopped
massctl agentrun restart worker -w my-ws     # stopped/error → creating → idle
massctl agentrun cancel worker -w my-ws      # cancel current turn (running → idle)
massctl agentrun delete worker -w my-ws      # delete record (needs stopped/error state)
```

### View

```bash
massctl agentrun get -w my-ws                    # list all agentruns in workspace
massctl agentrun get -w my-ws --phase idle       # filter by state
massctl agentrun get worker -w my-ws             # view specific agentrun
massctl agentrun get worker -w my-ws -o json     # JSON output (supports table, wide, json, yaml)
```

---

## Part 4: Interacting with Agents

### Send Prompt

Only when agent state is `idle`.

```bash
# Fire and forget
massctl agentrun prompt worker -w my-ws --text "Fix the auth bug"

# Wait for result (5-min timeout)
massctl agentrun prompt worker -w my-ws --text "Fix the auth bug" --wait
```

### Task Lifecycle

Task = structured way to delegate work, auto-handles prompts, file passing, result collection.

#### Task State Machine

```
[do] → agent running → [agent calls task done] → done=true
                                                    │
                                          reason + updatedAt populated
```

Agent calls `massctl agentrun task done` to complete task. `done` field written as bool `true` by Go code — type-safe.

#### Make Task (Auto-Prompts Agent)

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
| `--input-files` | no | Input file paths (repeatable) |
| `--output-dir` | no | Dir for agent output files (default: tasks/{task-id}/output/) |

`task do` will:
1. Check agent is idle (error if not)
2. Make task file (ID system-generated)
3. Auto-prompt agent (with built-in task protocol)
4. Transition agent idle → running

Returns `task.id` and `taskPath` for subsequent queries.

#### Complete Task (Called by Agent)

After agent finishes work:

```bash
massctl agentrun task done \
  --task-file {task-path} \
  --reason {reason} \
  --response '{"description":"...","filePaths":["..."]}'
```

| Flag | Required | Description |
|------|----------|-------------|
| `--task-file` | yes | Path to task JSON file (provided in task do request prompt) |
| `--reason` | yes | Result string, e.g. success / failed / needs_human |
| `--response` | yes | JSON with at least `description`; may include `filePaths` |

CLI writes: `done=true` (bool), `reason`, `updatedAt=now()`, atomically replacing file.

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
  "reason": "",             // ← result string set by agent (populated after done=true)
  "updatedAt": null,        // ← auto set to current time when task done
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

#### Retry Task

```bash
massctl agentrun task retry -w {workspace} --run {agent} {task-id}
```

Increments `attempt` counter, clears old response / reason / done, auto-re-prompts agent.

### Interactive Chat

```bash
massctl agentrun chat worker -w my-ws
```

### End-to-End Example (compose + task)

```bash
# 1. Start up
massctl compose apply -f compose.yaml   # workspace + agents, all in one command

# 2. Delegate task
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

For detailed error diagnosis, recovery procedures, decision trees, see [references/error-handling.md](references/error-handling.md).

### Agent Disabled Diagnosis

If `agentrun/create` returns `agent <name> is disabled` error:

```bash
# Check if agent disabled
massctl agent get

# Enable specified agent
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

Manual cleanup order required: **stop agent → delete agent → delete workspace** — must not reverse.
Or use `massctl workspace delete NAME --force` to do all in one step (auto stops + deletes all agentruns).
