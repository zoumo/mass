---
last_updated: 2026-04-18
---

# MASS Runtime Specification

## 概述

本文档定义 MASS Runtime 的规范部分——agent 的状态模型、bundle 格式、
生命周期状态机、操作语义，以及文件系统布局约定。

这些规范是**实现无关**的——任何符合此规范的 runtime 实现（无论是直接集成
还是独立 shim 进程）都必须遵守这里定义的行为。

## State

The state of an agent includes the following properties:

* **`massVersion`** (string, REQUIRED) is the version of the MASS Runtime Specification
  with which the state complies.
* **`id`** (string, REQUIRED) is the MASS runtime object's ID.
  In agentd deployments this is the MASS `sessionId`, and it MUST be unique across all agents on this host.
  It is distinct from any protocol-level session ID created inside the agent protocol session (e.g., ACP `sessionId`).
* **`status`** (string, REQUIRED) is the runtime state of the agent.
  The value MAY be one of:

  * `creating`: the agent is being created (step 2 in the [lifecycle](#lifecycle))
  * `idle`: the runtime has finished the [create operation](#create),
    the agent process is running, the protocol session has been established,
    and the agent is ready to receive prompts
  * `running`: the agent is executing a prompt (processing a `session/prompt`)
  * `stopped`: the agent process has exited (step 7 in the [lifecycle](#lifecycle))
  * `error`: an unrecoverable failure occurred during creating, idle, or running

  Additional values MAY be defined by the runtime, however,
  they MUST be used to represent new runtime states not defined above.

* **`pid`** (int, REQUIRED when `status` is `idle` or `running`) is the ID
  of the agent process as seen by the host OS.
* **`bundle`** (string, REQUIRED) is the absolute path to the agent's bundle directory.
  This is provided so that consumers can find the agent's configuration on the host.
* **`annotations`** (map, OPTIONAL) contains the list of annotations associated with the agent.
  If no annotations were provided then this property MAY either be absent or an empty map.

The state MAY include additional properties. The following optional fields are
populated by the shim during runtime:

* **`updatedAt`** (string, OPTIONAL) is the RFC3339Nano timestamp of the last state write.
  Updated on every `state.json` persist cycle.
* **`session`** (object, OPTIONAL) contains agent session metadata populated progressively
  as the agent reports notifications (e.g. `agentInfo`, `capabilities`, `availableCommands`,
  `configOptions`, `sessionInfo`, `currentMode`). See Go type `SessionState` for the full
  structure.
  * **`models`** (object, OPTIONAL) contains model switching state populated when
    the agent supports model selection.
    * **`availableModels`** ([]ModelInfo) list of available models.
    * **`currentModelId`** (string) the currently selected model ID.
  * ModelInfo structure:
    * **`modelId`** (string, REQUIRED) unique model identifier.
    * **`name`** (string, REQUIRED) display name.
    * **`description`** (string, OPTIONAL) model description.
* **`eventCounts`** (map, OPTIONAL) maps event type strings (e.g. `"agent_message"`, `"tool_call"`)
  to their cumulative counts. This is a derived field — set on every state write, not
  independently settable.

### Comparison with OCI State

| OCI state.json | MASS state.json | Notes |
|---------------|----------------|-------|
| `ociVersion` | `massVersion` | Same |
| `id` | `id` | Same |
| `status` | `status` | Same pattern: creating/idle/running/stopped/error |
| `pid` | `pid` | Same |
| `bundle` (rootfs + config path) | `bundle` (config.json path) | Same concept |
| `annotations` | `annotations` | Same |
| `namespace_paths` | — | No kernel namespaces |
| `cgroup_paths` | — | No cgroups |

### Example

```json
{
  "massVersion": "0.1.0",
  "id": "my-project-architect",
  "status": "idle",
  "pid": 12345,
  "bundle": "/var/lib/agentd/bundles/my-project-architect",
  "annotations": {
    "org.openagents.task": "PROJ-1234"
  },
  "updatedAt": "2026-04-07T10:00:00.123456789Z",
  "session": {
    "agentInfo": { "name": "claude-code", "version": "1.0.0" },
    "models": {
      "availableModels": [
        { "modelId": "claude-sonnet", "name": "Claude Sonnet" }
      ],
      "currentModelId": "claude-sonnet"
    }
  },
  "eventCounts": {
    "agent_message": 42,
    "tool_call": 7,
    "turn_start": 3
  }
}
```

### State Storage

The runtime MUST store state in a volatile location (e.g. `/run/`).
State is ephemeral — lost on host restart.
Persistent metadata is the responsibility of agentd.

The `bundle` field points to the bundle directory (where `config.json` lives),
NOT the state directory. This mirrors OCI state where `bundle` points to the
bundle, not to `/run/runc/<id>/`.

## File System Layout

In agentd-managed deployments, bundle, state, and socket are co-located under the bundle root:

```
<bundleRoot>/<workspace>-<name>/      ← bundle dir (bundle field in state.json)
├── config.json                       ← mass writes (MASS Runtime Spec)
├── workspace -> <workspace-dir>      ← agentd symlinks to workspace directory
├── state.json                        ← shim writes
├── agent-run.sock                   ← shim creates (Unix domain socket)
└── events.jsonl                      ← shim appends
```

When running `mass run` in standalone mode, the default state directory is `<cwd>`.

agentd persists `ShimSocketPath` and `ShimStateDir` in AgentRun metadata,
so recovery uses persisted metadata rather than filesystem scanning.

## Bundle

A **bundle** is a directory containing all information needed to create an agent.
The runtime reads a bundle directory to obtain the configuration.

A bundle MUST contain the following file:

* **`config.json`** (REQUIRED) — the agent configuration as defined in [config.md](config-spec.md).

agentd MUST prepare the following before invoking the runtime:

* **`workspace`** (symlink at `agentRoot.path`, REQUIRED) — a symbolic link at the
  path named by `config.json`'s `agentRoot.path` (typically `"workspace"`), pointing
  to the workspace directory prepared by the Workspace Manager.
  mass creates this symlink — the runtime only reads it.

```
<bundle-dir>/
├── config.json       ← mass writes (MASS Runtime Spec)
└── workspace -> /var/lib/agentd/workspaces/ws-abc123/   ← agentd symlinks
```

The runtime resolves `agentRoot.path` at `create` time by joining the bundle directory
with the path and calling `EvalSymlinks`, yielding the canonical absolute path.
This resolved path is used as `cmd.Dir` and as the agent working directory
(e.g., ACP `session/new cwd` parameter, or CLI `--cwd` argument).

### agentRoot as OCI root analogy

OCI's `root.path` is a relative path inside the bundle pointing to the container's
rootfs. containerd (via the snapshotter) prepares the rootfs and places it at that
path before handing the bundle to runc.

MASS follows the same pattern: `agentRoot.path` is a relative path inside the bundle.
agentd's Workspace Manager prepares the workspace directory and creates a symlink at
`agentRoot.path` inside the bundle. The runtime reads and resolves the path — it never
creates or modifies the workspace directory.

The key difference: OCI rootfs is isolated per container. MASS workspace is shared —
multiple AgentRuns sharing a workspace each have their own bundle, but their `agentRoot.path`
symlinks all point to the same underlying workspace directory.

### Bundle Lifecycle

mass is responsible for creating bundle directories and preparing their contents:

1. Workspace Manager prepares the workspace directory (git clone / emptyDir / local).
2. mass creates the bundle directory.
3. mass writes `config.json` with `agentRoot.path = "workspace"`.
4. mass creates the symlink: `bundle/workspace → <workspace-dir>`.
5. agentd invokes the runtime's `create` operation with the bundle path.
6. The runtime reads `config.json`, resolves `agentRoot.path` → canonical absolute path, starts agent.

This mirrors OCI's bundle concept:
containerd creates the bundle directory + rootfs → runc reads config.json from it.
mass creates the bundle directory + workspace symlink → the runtime reads config.json from it.

## Lifecycle

The lifecycle describes the timeline of events from when an agent is created
to when it ceases to exist.

1. The runtime's [`create`](#create) command is invoked with a reference to
   the bundle location and a unique identifier.
2. The agent's runtime environment MUST be created according to the configuration
   in [`config.json`](config-spec.md). If the runtime is unable to create the environment,
   it MUST [generate an error](#errors).
3. The runtime MUST start the agent process using `process` config
   (command, args, env) via fork/exec.
4. The runtime MUST complete the protocol-specific handshake determined by `clientProtocol`.
   For example, ACP performs `initialize` via stdio JSON-RPC; Claude Code uses CLI arguments.
   If the handshake fails, the runtime MUST [generate an error](#errors),
   kill the process, and continue the lifecycle at step 8.
5. If `session` config is present, the runtime MUST establish bootstrap configuration
   using the resolved `cwd` and `session` values (systemPrompt, mcpServers, permissions).
   The delivery method depends on `clientProtocol`:
   - ACP: `session/new` carries the resolved `cwd` plus `mcpServers`; compatibility
     exchange for `systemPrompt` happens inside `create`
   - Claude Code: system prompt via `--system-prompt` CLI argument
   - Others: protocol-specific mechanism
   This bootstrap work is internal session establishment, not an external user turn.
   If session creation fails, the runtime MUST [generate an error](#errors),
   kill the process, and continue the lifecycle at step 8.
6. The agent is now in `idle` state — process is running,
   protocol bootstrap is complete, and the agent is ready to receive prompts.
7. The agent process exits.
   This MAY happen due to error, the runtime's [`kill`](#kill) operation being invoked,
   or the process terminating on its own.
8. The runtime's [`delete`](#delete) command is invoked with the unique identifier.
9. The agent MUST be destroyed by undoing the steps performed during the create phase (step 2).

**Key difference from OCI**: In OCI, `create` sets up the environment but does NOT
run the user program — that happens at `start`. In MASS, `create` both starts the
process and completes the protocol handshake (determined by `clientProtocol`).
If the handshake fails, the agent instance is not considered successfully created.
The `start` operation is currently a no-op, reserved for future use.

### Lifecycle Diagram

```
         create
           │
           ▼
      ┌──────────┐
      │ creating  │
      └────┬──────┘
           │ process started + protocol initialized + session established
           ▼
      ┌──────────┐          session/prompt           ┌──────────┐
      │  idle     │ ──────────────────────────────► │ running   │
      │           │ ◄────────────────────────────── │(prompting)│
      └────┬──────┘          prompt completed        └────┬──────┘
           │                                              │
           │ kill / exit / error                          │ kill / exit / error
           ▼                                              ▼
      ┌──────────┐                                   ┌──────────┐
      │ stopped   │                                   │ stopped   │
      └────┬──────┘                                   └────┬──────┘
           │ delete                                        │ delete
           ▼                                              ▼
       (removed)                                      (removed)
```

## State Mapping and Identity Authority

The design set uses the following cross-layer mapping. `status` in this document is the
runtime-owned state, not the mass daemon session state, and not the ACP peer's session identifier.

| MASS runtime `status` | agentd AgentRun state | Process status | Protocol session ID authority | Notes |
|---|---|---|---|---|
| `creating` | `creating` — bootstrap in progress | process may be absent or starting | none yet, or not yet durable | agentd has allocated the AgentRun identity, but protocol bootstrap is not complete. |
| `idle` | `idle` | running | Protocol peer may now return its own session ID; it is subordinate protocol state | Runtime bootstrap is complete and the agent is ready to receive prompts. |
| `running` | `running` | running | same session ID established during bootstrap | External work is flowing through `agentrun/prompt`. |
| `stopped` | `stopped` | stopped or exited | last known session ID is historical only | Process has exited; runtime state is terminal until delete. |
| `error` | `error` | stopped or absent | last known session ID is historical only | Unrecoverable failure; agent must be restarted or deleted. |

Identity authority stays split:

- AgentRun identity `(workspace, name)` is allocated and owned by mass/ARI and names the runtime object.
- Protocol session ID (e.g., ACP `sessionId`) is allocated by the agent peer during bootstrap and only identifies the inner protocol session.
- Implementations MUST NOT imply that the two identifiers are equal, interchangeable, or durably mirrored unless later persistence work explicitly records that mapping.

## Errors

In cases where a specified operation generates an error, this specification does not
mandate how, or even if, that error is returned or exposed to the user of an implementation.
Unless otherwise stated, generating an error MUST leave the state of the environment
as if the operation were never attempted — modulo any possible trivial ancillary changes such as logging.

## Operations

Unless otherwise stated, runtimes MUST support the following operations.

Note: these operations are not specifying any command-line APIs,
and the parameters are inputs for general operations.

### Query State

`state <agent-id>`

This operation MUST [generate an error](#errors) if it is not provided the ID of an agent.
Attempting to query an agent that does not exist MUST [generate an error](#errors).
This operation MUST return the state of an agent as specified in the [State](#state) section.

### Create

`create <agent-id> <path-to-bundle>`

This operation MUST [generate an error](#errors) if it is not provided a path to the bundle
and the agent ID to associate with the agent.
If the ID provided is not unique across all agents within the scope of the runtime,
or is not valid in any other way, the implementation MUST [generate an error](#errors)
and a new agent MUST NOT be created.

This operation MUST create a new agent by:
1. Reading [`config.json`](config-spec.md) from the bundle directory
2. Resolving the agent root: `filepath.Join(bundleDir, agentRoot.path)` + `EvalSymlinks` → canonical absolute path
3. Starting the agent process using `process` config (fork/exec, with `cmd.Dir` set to the resolved path)
4. Completing the protocol-specific handshake determined by `clientProtocol`
5. Establishing session bootstrap configuration from the resolved `cwd` and `session` config (systemPrompt, mcpServers, permissions)
6. Writing state.json

Any changes made to the [`config.json`](config-spec.md) file after this operation
will not have an effect on the agent.

### Start

`start <agent-id>`

This operation MUST [generate an error](#errors) if it is not provided the agent ID.
Attempting to `start` an agent that is not [`idle`](#state) MUST have no effect
on the agent and MUST [generate an error](#errors).

In the current specification, `create` already starts the agent process and
establishes the protocol session. `start` is a no-op, reserved for future use
and for API compatibility with OCI's create/start separation.

### Kill

`kill <agent-id> <signal>`

This operation MUST [generate an error](#errors) if it is not provided the agent ID.
Attempting to send a signal to an agent that is neither [`idle` nor `running`](#state)
MUST have no effect on the agent and MUST [generate an error](#errors).
This operation MUST send the specified signal to the agent process.

The default signal is SIGTERM. If the process does not exit within a grace period,
the caller (agentd) is responsible for sending SIGKILL.

### Delete

`delete <agent-id>`

This operation MUST [generate an error](#errors) if it is not provided the agent ID.
Attempting to `delete` an agent that is not [`stopped`](#state) MUST have no effect
on the agent and MUST [generate an error](#errors).
Deleting an agent MUST delete the resources that were created during the `create` step
(e.g. the bundle/state directory under `<bundleRoot>/<workspace>-<name>/`).
Once an agent is deleted its ID MAY be used by a subsequent agent.

## config.json

The runtime reads `config.json` from the bundle directory. Fields:

```json
{
  "massVersion": "0.1.0",
  "metadata": { "name": "session-abc123" },
  "agentRoot": { "path": "workspace" },
  "clientProtocol": "acp",
  "process": {
    "command": "npx",
    "args": ["-y", "@anthropic-ai/claude-code-acp"],
    "env": ["ANTHROPIC_API_KEY=sk-ant-xxx"]
  },
  "session": {
    "systemPrompt": "你是后端工程师",
    "permissions": "approve_all",
    "mcpServers": [
      { "type": "http", "url": "http://localhost:3000/mcp" }
    ]
  }
}
```

* `agentRoot.path` → 相对路径，runtime 在 create 时解析为绝对路径（EvalSymlinks），用作 cmd.Dir 和 agent 工作目录
* `clientProtocol` → 选择 agent-run 与 agent 进程之间的通信协议，默认 `"acp"`
* `process` → fork/exec agent 进程（所有协议共享）
* `session.systemPrompt` + `session.mcpServers` → 会话 bootstrap 配置，投递方式由 `clientProtocol` 决定
* `session.permissions` → fs/terminal 权限策略

详见 [config.md](config-spec.md)。

## fs / terminal 权限策略

agent 会向 runtime 发起 `fs/*` / `terminal/*` 请求。
runtime 按创建时指定的权限策略处理：

| 策略 | 行为 |
|------|------|
| `approve_all` | 所有操作自动批准 |
| `approve_reads` | 只读操作批准，写操作返回 deny |
| `deny_all` | 所有操作返回 deny |

策略通过 config.json 的 `session.permissions` 字段指定，运行时不可更改。
不支持权限模型的 `clientProtocol` 会忽略此字段。

## Typed Event Stream

Runtime 必须产出结构化的 typed event stream，供上层消费。

### 事件类型

| 事件类型 | 来源（协议无关） | ACP 对应 | 说明 |
|---------|----------------|---------|------|
| `agent_thinking` | agent 思考通知 | `thought_message_chunk` | Agent 的推理/思考过程 |
| `agent_message` | agent 消息通知 | `agent_message_chunk` | Agent 的回复文本片段 |
| `user_message` | prompt 回显 | `user_message_chunk` / `session/prompt` | 用户输入回显 |
| `tool_call` | 工具调用通知 | `tool_call` | 工具调用开始 |
| `tool_result` | 工具结果通知 | `tool_call_update` | 工具调用完成/失败 |
| `plan` | 计划通知 | `plan` / `plan_update` | Agent 执行计划及状态更新 |
| `turn_start` | prompt 开始处理 | — | 标记一个 turn 的开始 |
| `turn_end` | prompt 完成 | prompt_response | 标记一个 turn 的结束 |
| `error` | 错误或进程异常 | ACP 错误 | 错误信息 |
| `runtime_update` | 元数据更新 / 进程状态变更 | metadata updates | 运行时状态与 session 元数据更新（status, availableCommands, currentMode, configOptions, sessionInfo, usage） |

> **Content Block Streaming**：`agent_message`、`agent_thinking`、`user_message` 三种事件
> 携带 `status` 字段（`start` / `streaming` / `end`）标识 content block 生命周期。
> 详见 [run-rpc-spec.md § Content Block Streaming](run-rpc-spec.md#content-block-streaming)。

> **Payload 保留策略**：事件 payload 尽可能保留协议原始字段（包括 `_meta`）。
> 对于 ACP 协议，JSON wire shape 与 ACP SDK marshal 结果一致，仅省略 `sessionUpdate` 鉴别器字段。
> 其他协议的翻译器负责将原生事件映射到上述统一事件类型。
> union 类型（如 `ContentBlock`、`ToolCallContent`）使用 flat JSON shape + `type` 鉴别器。

### 事件持久化

Runtime MUST 将 typed events 追加写入 state dir 下的 `events.jsonl`，
供 agentd 重连后回放。

## Agent 进程分类

runtime 启动 agent 进程后，根据 `clientProtocol` 选择对应的协议适配器通信。
不同 agent 使用不同的原生协议：

```
# ACP agent — 通过 ACP 协议通信（clientProtocol: "acp"）
runtime → ACP stdio → agent（原生 ACP 或 ACP wrapper）

# Claude Code — 通过原生 JSON-RPC 协议通信（clientProtocol: "claude-code"）
runtime → Claude Code JSON-RPC stdio → claude

# Codex — 通过 Codex 原生协议通信（clientProtocol: "codex"）
runtime → Codex stdio → codex
```

每种 `clientProtocol` 对应一个 Protocol 适配器实现，负责：
- Bootstrap 握手（如 ACP initialize、Claude Code CLI 参数注入）
- Prompt 投递
- 事件翻译为统一的 RuntimeEvent
