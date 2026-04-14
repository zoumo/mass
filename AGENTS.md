# 开发指南

- use `make build` to build go binary 

<!-- GSD:AUTO:START -->
## Project Intelligence (auto-generated after M012)

> Full details: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) · [docs/DECISIONS.md](docs/DECISIONS.md) · [docs/CONVENTIONS.md](docs/CONVENTIONS.md)

### Architecture

OAR is a local AI-agent daemon (`agentd`) + CLI (`agentdctl`). It manages **AgentTemplates** (config), **AgentRuns** (running instances backed by a shim process), and **Workspaces** (filesystem + grouping). Orchestrators communicate via JSON-RPC 2.0 ARI socket. Agents communicate with the shim via ACP protocol.

**Components:** agentd daemon · agentdctl CLI · pkg/agentd (ProcessManager/AgentManager/RecoveryManager) · pkg/ari/server (adapter pattern) · pkg/ari/client · pkg/jsonrpc (transport-agnostic RPC) · pkg/shim/server + client · pkg/events (Translator/JSONL log) · pkg/meta (bbolt store) · pkg/workspace · pkg/runtime (ACP client) · api/ari · api/shim · api/runtime

→ See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)

### Key Decisions (10 most recent active)

| # | Decision | Choice |
|---|----------|--------|
| D112 | ARI server multi-interface conflict resolution | Adapter pattern: central Service + 3 thin unexported adapters |
| D110 | bbolt bucket naming for agent/agentrun model | v1/agents (templates), v1/agentruns (instances) — fresh start, no migration |
| D109 | API model: agent=template, agentrun=instance | Aligns with containerd Container/Task model |
| D106 | Capabilities struct deleted | ACP-level negotiation handled at shim layer |
| D105 | AgentTemplate storage: bbolt CRUD via ARI | agent/set, agent/get, agent/list, agent/delete; persistent across restarts |
| D104 | agentd startup: --root flag replaces config.yaml | Derives all 5 paths deterministically; matches containerd --root pattern |
| D103 | Binary consolidation: 5→2 binaries | agentd (server/shim/workspace-mcp) + agentdctl |
| D092 | Post-bootstrap DB state via runtime/stateChange only | buildNotifHandler is the single post-bootstrap state change path |
| D089 | RestartPolicy: tryReload vs alwaysNew | tryReload reads ACP sessionId from state file; fallback to alwaysNew on any failure |
| D088 | Shim write authority boundary | agentd NEVER writes idle/running/stopped/error directly after bootstrap |

→ See [docs/DECISIONS.md](docs/DECISIONS.md)

### Rules

| # | Scope | Rule |
|---|-------|------|
| K023 | agentd | NEVER use `exec.CommandContext` for long-running daemon processes. Use `exec.Command` + explicit lifecycle. |
| K025 | testing | macOS Unix socket path limit: 104 chars. Use `/tmp/oar-{pid}-{counter}.sock`, never `t.TempDir()` for sockets. |
| K037 | agentd | Fail-closed recovery gates must be time-bounded. `RecoverSessions` MUST transition to Complete on every exit path. |
| K039 | agentd | TOCTOU in socket cleanup: use unconditional `os.Remove` (ignore `ErrNotExist`), not Stat→Remove. |
| K042 | meta | DB cascade deletes eliminate explicit ReleaseWorkspace. Adding a release call after `meta.DeleteSession` causes double-release. |
| K054 | agentd | Subscribe-before-Load is a correctness invariant in tryReload. Subscribe first, then session/load. |
| K059 | docs | Use affirmative phrasing in design docs. `identity is (workspace, name)` not `there is no agentId`. |
| K060 | agentd | forkShim must pass `filepath.Base(stateDir)` (hyphen) as `--id`, not the slash-separated `agentKey`. |
| K061 | agentd | Bootstrap DB state from `runtime/status` after Subscribe, not from stateChange hook (hook is registered too late). |
| K063 | agentd | Always `os.Remove(socketPath)` before forking the shim. Stale sockets from crashes cause bind failures. |
| K069 | testing | Self-fork requires `OAR_SHIM_BINARY` override in integration tests (`go test` binary ≠ agentd binary). |
| K074 | refactoring | Three-layer rename (meta→ari types→server+CLI) must compile as a unit — never layer-by-layer. |
| K079 | testing | Before deleting a test file, `grep -l <symbol> *_test.go` for cross-file dependencies. Extract shared infra first. |
| K080 | testing | jsonrpc.Server cleanup: `ln.Close()` before `srv.Shutdown()` to unblock `Accept()` in `Serve()`. |

→ See [docs/CONVENTIONS.md](docs/CONVENTIONS.md)
<!-- GSD:AUTO:END -->

## 设计一致性

- Code changes **must be** aligned with `docs/design`
- No need to consider compatibility Now

## Language-Agnostic Coding Principles

These principles apply to all programming languages and should guide code review and development decisions.

### Clarity

- Code must explain "what" and "why"
- Descriptive variable names over brevity
- Clarity trumps cleverness
- Write code as readable as narrative

### Simplicity

- Prefer standard tools over custom solutions
- Least mechanism principle
- Top-to-bottom readability
- Avoid unnecessary abstraction levels

### Concision

- High signal-to-noise ratio
- Avoid redundant comments (don't repeat code)
- Use common idioms
- Boost important signals with comments

### Maintainability

- Code is modified more than written
- Easy to modify correctly
- Clear assumptions
- No hidden critical details
- Predictable naming patterns
- Minimize dependencies

### Consistency

- Follow existing patterns in codebase
- Local consistency acceptable when not harming readability
- Style deviations not acceptable if they worsen existing issues

### DRY (Don't Repeat Yourself)

- Avoid duplicate logic
- Use abstraction when complexity justifies it

### KISS (Keep It Simple, Stupid)

- Prefer simple solutions
- Avoid premature optimization
