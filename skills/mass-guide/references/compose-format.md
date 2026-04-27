# Compose Format

## compose apply

`massctl compose apply -f <file>` declaratively creates a workspace and multiple agentruns.

## compose run (Quick Start)

`massctl compose run` quickly starts a single agent run using the current directory, without a YAML file.

```bash
# Minimal usage
massctl compose run -w my-ws --agent claude

# Specify run name
massctl compose run -w my-ws --agent claude --name my-claude

# With system prompt
massctl compose run -w my-ws --agent claude --system-prompt "You are a reviewer"
```

| Flag | Required | Description |
|------|----------|-------------|
| `-w, --workspace` | Yes | Workspace name |
| `--agent` | Yes | Agent definition name |
| `--name` | No | AgentRun name (defaults to agent name) |
| `--system-prompt` | No | System prompt |
| `--workflow` | No | Workflow file path |
| `--no-wait` | No | Do not wait for agentrun to enter idle |

If the workspace already exists and is ready, it is reused automatically; otherwise a new workspace is created with `cwd` as the local source.

## compose apply YAML Format

`massctl compose apply -f <file>` declaratively creates a workspace and multiple agentruns (the workspace must not already exist).

## Full Format

```yaml
kind: workspace-compose
metadata:
  name: my-ws                    # Workspace name
spec:
  source:
    type: local                  # local | git | emptyDir
    path: /path/to/code          # Required for local
    # url: https://...           # Required for git
    # ref: main                  # Optional for git (branch/tag/commit)
  runs:
    - name: agent-name           # AgentRun name (unique within workspace)
      agent: claude              # Built-in agent definition name
      systemPrompt: |            # System prompt
        Your role description...
      permissions: approve_all   # approve_all | approve_reads | deny_all
```

## Field Reference

### source

| type | Required fields | Description |
|------|-----------------|-------------|
| `local` | `path` | Mounts a local directory; mass does not manage its lifecycle |
| `git` | `url`, optional `ref` | Clones a git repository; mass manages the directory |
| `empty` | none | Creates an empty directory; mass manages it |

### runs[]

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `name` | Yes | — | AgentRun name (unique within workspace) |
| `agent` | Yes | — | Referenced agent definition name (claude / codex / gsd-pi or custom) |
| `systemPrompt` | No | — | System prompt for this agentrun instance |
| `permissions` | No | `approve_all` | File/terminal permission policy |

## compose Execution Flow

1. Create workspace → poll until phase == `ready`
2. Create each agentrun in sequence
3. Poll until all agentrun states == `idle`
4. Print the socket path for every agent
