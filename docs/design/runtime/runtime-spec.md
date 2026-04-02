# OAR Runtime Specification

## 概述

本文档定义 OAR Runtime 的规范部分——agent 的状态模型、bundle 格式、
生命周期状态机、操作语义，以及文件系统布局约定。

这些规范是**实现无关**的——任何符合此规范的 runtime 实现（无论是直接集成
还是独立 shim 进程）都必须遵守这里定义的行为。

## State

The state of an agent includes the following properties:

* **`oarVersion`** (string, REQUIRED) is the version of the OAR Runtime Specification
  with which the state complies.
* **`id`** (string, REQUIRED) is the agent's ID.
  This MUST be unique across all agents on this host.
* **`status`** (string, REQUIRED) is the runtime state of the agent.
  The value MAY be one of:

  * `creating`: the agent is being created (step 2 in the [lifecycle](#lifecycle))
  * `created`: the runtime has finished the [create operation](#create),
    the agent process is running and the ACP session has been established
  * `running`: the agent is executing a prompt (processing a `session/prompt`)
  * `stopped`: the agent process has exited (step 7 in the [lifecycle](#lifecycle))

  Additional values MAY be defined by the runtime, however,
  they MUST be used to represent new runtime states not defined above.

* **`pid`** (int, REQUIRED when `status` is `created` or `running`) is the ID
  of the agent process as seen by the host OS.
* **`bundle`** (string, REQUIRED) is the absolute path to the agent's bundle directory.
  This is provided so that consumers can find the agent's configuration on the host.
* **`annotations`** (map, OPTIONAL) contains the list of annotations associated with the agent.
  If no annotations were provided then this property MAY either be absent or an empty map.

The state MAY include additional properties.

### Comparison with OCI State

| OCI state.json | OAR state.json | Notes |
|---------------|----------------|-------|
| `ociVersion` | `oarVersion` | Same |
| `id` | `id` | Same |
| `status` | `status` | Same pattern: creating/created/running/stopped |
| `pid` | `pid` | Same |
| `bundle` (rootfs + config path) | `bundle` (config.json path) | Same concept |
| `annotations` | `annotations` | Same |
| `namespace_paths` | — | No kernel namespaces |
| `cgroup_paths` | — | No cgroups |

### Example

```json
{
  "oarVersion": "0.1.0",
  "id": "session-abc123",
  "status": "created",
  "pid": 12345,
  "bundle": "/var/lib/agentd/bundles/session-abc123",
  "annotations": {
    "org.openagents.task": "PROJ-1234"
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

```
/var/lib/agentd/bundles/<agent-id>/   ← bundle dir (bundle field in state.json)
├── config.json
└── workspace -> /var/lib/agentd/workspaces/ws-abc/

/run/agentd/shim/<agent-id>/          ← state dir (ephemeral)
├── state.json
├── agent-shim.sock
└── events.jsonl
```

Socket 路径约定：`/run/agentd/shim/<session-id>/agent-shim.sock`

socket 与 state.json、events.jsonl 同在一个 state dir 下。
agentd 重启后扫描 `/run/agentd/shim/*/agent-shim.sock` 即可重连所有存活的 runtime，
无需额外记录 socket 路径。

## Bundle

A **bundle** is a directory containing all information needed to create an agent.
The runtime reads a bundle directory to obtain the configuration.

A bundle MUST contain the following file:

* **`config.json`** (REQUIRED) — the agent configuration as defined in [config.md](config-spec.md).

agentd MUST prepare the following before invoking the runtime:

* **`workspace`** (symlink at `agentRoot.path`, REQUIRED) — a symbolic link at the
  path named by `config.json`'s `agentRoot.path` (typically `"workspace"`), pointing
  to the workspace directory prepared by the Workspace Manager.
  agentd creates this symlink — the runtime only reads it.

```
<bundle-dir>/
├── config.json       ← agentd writes (OAR Runtime Spec)
└── workspace -> /var/lib/agentd/workspaces/ws-abc123/   ← agentd symlinks
```

The runtime resolves `agentRoot.path` at `create` time by joining the bundle directory
with the path and calling `EvalSymlinks`, yielding the canonical absolute path.
This resolved path is used as `cmd.Dir` and as the ACP `session/new cwd` parameter.

### agentRoot as OCI root analogy

OCI's `root.path` is a relative path inside the bundle pointing to the container's
rootfs. containerd (via the snapshotter) prepares the rootfs and places it at that
path before handing the bundle to runc.

OAR follows the same pattern: `agentRoot.path` is a relative path inside the bundle.
agentd's Workspace Manager prepares the workspace directory and creates a symlink at
`agentRoot.path` inside the bundle. The runtime reads and resolves the path — it never
creates or modifies the workspace directory.

The key difference: OCI rootfs is isolated per container. OAR workspace is shared —
multiple agents in a Room each have their own bundle, but their `agentRoot.path`
symlinks all point to the same underlying workspace directory.

### Bundle Lifecycle

agentd is responsible for creating bundle directories and preparing their contents:

1. Workspace Manager prepares the workspace directory (git clone / emptyDir / local).
2. agentd creates the bundle directory.
3. agentd writes `config.json` with `agentRoot.path = "workspace"`.
4. agentd creates the symlink: `bundle/workspace → <workspace-dir>`.
5. agentd invokes the runtime's `create` operation with the bundle path.
6. The runtime reads `config.json`, resolves `agentRoot.path` → canonical absolute path, starts agent.

This mirrors OCI's bundle concept:
containerd creates the bundle directory + rootfs → runc reads config.json from it.
agentd creates the bundle directory + workspace symlink → the runtime reads config.json from it.

## Lifecycle

The lifecycle describes the timeline of events from when an agent is created
to when it ceases to exist.

1. The runtime's [`create`](#create) command is invoked with a reference to
   the bundle location and a unique identifier.
2. The agent's runtime environment MUST be created according to the configuration
   in [`config.json`](config-spec.md). If the runtime is unable to create the environment,
   it MUST [generate an error](#errors).
3. The runtime MUST start the agent process using `acpAgent.process`
   (command, args, env) via fork/exec.
4. The runtime MUST complete the ACP `initialize` handshake via stdio JSON-RPC.
   If the handshake fails, the runtime MUST [generate an error](#errors),
   kill the process, and continue the lifecycle at step 8.
5. If `acpAgent.session` is present, the runtime MUST send ACP `session/new`
   with cwd from the resolved `agentRoot.path` and mcpServers from `acpAgent.session.mcpServers`.
   If `acpAgent.systemPrompt` is non-empty, the runtime MUST then send it as the first
   ACP prompt (before returning `created` state). This seed prompt is sent silently:
   its events are not delivered to subscribers and its outcome is not written to `LastTurn`.
   If session creation fails, the runtime MUST [generate an error](#errors),
   kill the process, and continue the lifecycle at step 8.
6. The agent is now in `created` state — process is running,
   ACP session is established, and the agent is ready to receive prompts.
7. The agent process exits.
   This MAY happen due to error, the runtime's [`kill`](#kill) operation being invoked,
   or the process terminating on its own.
8. The runtime's [`delete`](#delete) command is invoked with the unique identifier.
9. The agent MUST be destroyed by undoing the steps performed during the create phase (step 2).

**Key difference from OCI**: In OCI, `create` sets up the environment but does NOT
run the user program — that happens at `start`. In OAR, `create` both starts the
process and completes ACP initialization, because ACP is the runtime's core protocol.
If ACP handshake fails, the agent instance is not considered successfully created.
The `start` operation is currently a no-op, reserved for future use.

### Lifecycle Diagram

```
         create
           │
           ▼
      ┌──────────┐
      │ creating  │
      └────┬──────┘
           │ process started + ACP initialized + session established
           ▼
      ┌──────────┐          session/prompt           ┌──────────┐
      │ created   │ ──────────────────────────────► │ running   │
      │ (idle)    │ ◄────────────────────────────── │(prompting)│
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
3. Starting the agent process using `acpAgent.process` (fork/exec, with `cmd.Dir` set to the resolved path)
4. Completing ACP `initialize` handshake (stdio JSON-RPC)
5. Sending ACP `session/new` with parameters from `acpAgent.systemPrompt`, resolved path (as `cwd`), and `acpAgent.session` (if present)
6. Writing state.json

Any changes made to the [`config.json`](config-spec.md) file after this operation
will not have an effect on the agent.

### Start

`start <agent-id>`

This operation MUST [generate an error](#errors) if it is not provided the agent ID.
Attempting to `start` an agent that is not [`created`](#state) MUST have no effect
on the agent and MUST [generate an error](#errors).

In the current specification, `create` already starts the agent process and
establishes the ACP session. `start` is a no-op, reserved for future use
and for API compatibility with OCI's create/start separation.

### Kill

`kill <agent-id> <signal>`

This operation MUST [generate an error](#errors) if it is not provided the agent ID.
Attempting to send a signal to an agent that is neither [`created` nor `running`](#state)
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
(e.g. the `/run/agentd/shim/<agent-id>/` state directory).
Once an agent is deleted its ID MAY be used by a subsequent agent.

## config.json

The runtime reads `config.json` from the bundle directory. Fields:

```json
{
  "oarVersion": "0.1.0",
  "metadata": { "name": "session-abc123" },
  "agentRoot": { "path": "workspace" },
  "acpAgent": {
    "systemPrompt": "你是后端工程师",
    "process": {
      "command": "npx",
      "args": ["-y", "@anthropic-ai/claude-code-acp"],
      "env": ["ANTHROPIC_API_KEY=sk-ant-xxx"]
    },
    "session": {
      "mcpServers": [
        { "type": "http", "url": "http://localhost:3000/mcp" }
      ]
    }
  },
  "permissions": "approve-all"
}
```

* `agentRoot.path` → 相对路径，runtime 在 create 时解析为绝对路径（EvalSymlinks），用作 cmd.Dir 和 ACP session/new cwd
* `acpAgent.process` → fork/exec agent 进程
* `acpAgent.systemPrompt` + `acpAgent.session` → ACP `session/new` 参数
* `permissions` → fs/terminal 权限策略

详见 [config.md](config-spec.md)。

## fs / terminal 权限策略

agent 会向 runtime（ACP client）发起 `fs/*` / `terminal/*` 请求。
runtime 按创建时指定的权限策略处理：

| 策略 | 行为 |
|------|------|
| `approve-all` | 所有操作自动批准 |
| `approve-reads` | 只读操作批准，写操作返回 deny |
| `deny-all` | 所有操作返回 deny |

策略通过 config.json 的 `permissions` 字段指定，运行时不可更改。

## Typed Event Stream

Runtime 必须产出结构化的 typed event stream，供上层消费。

### 事件类型

| 事件类型 | 来源 | 说明 |
|---------|------|------|
| `ThinkingEvent` | ACP `thought_message_chunk` | Agent 的推理/思考过程 |
| `TextEvent` | ACP `agent_message_chunk` | Agent 的回复文本片段 |
| `ToolCallEvent` | ACP `tool_call` | 工具调用开始 |
| `ToolResultEvent` | ACP `tool_call_update` | 工具调用完成/失败 |
| `FileWriteEvent` | ACP `fs/write_text_file` (runtime 处理后) | 文件写入操作及结果 |
| `FileReadEvent` | ACP `fs/read_text_file` (runtime 处理后) | 文件读取操作及结果 |
| `CommandEvent` | ACP `terminal/*` (runtime 处理后) | Shell 命令执行及输出 |
| `PlanEvent` | ACP `plan` / `plan_update` | Agent 执行计划及状态更新 |
| `TurnStartEvent` | prompt 开始处理 | 标记一个 turn 的开始 |
| `TurnEndEvent` | ACP prompt_response | 标记一个 turn 的结束 |
| `ErrorEvent` | ACP 错误或进程异常 | 错误信息 |

### 事件持久化

Runtime MUST 将 typed events 追加写入 state dir 下的 `events.jsonl`，
供 agentd 重连后回放。

## Agent 进程分类

runtime 启动 agent 进程后，从自身角度看所有 agent 都说 ACP over stdio，
是否经过 wrapper 是透明的：

```
# 原生 ACP agent — 直接对接
runtime → ACP stdio → gemini（原生 ACP）

# ACP wrapper — 协议翻译后对接（对 runtime 透明）
runtime → ACP stdio → claude-acp → Claude Code 原生协议
runtime → ACP stdio → pi-acp     → pi RPC → GSD
runtime → ACP stdio → codex-acp  → Codex 原生协议
```

wrapper 是否存在是 runtimeClass 配置的细节，runtime 不感知。
