---
last_updated: 2026-04-18
---

# 配置规范

本配置文件包含对 agent 执行[标准操作](runtime-spec.md#操作)所需的元数据，
包括 agent 进程的启动方式、环境变量注入、会话参数等。

本文档定义了规范的权威 schema。
设计思路详见[设计文档](design.md)。

## 规范版本

* **`massVersion`** (string, REQUIRED) 必须符合 [SemVer v2.0.0][semver] 格式，
  指定该 bundle 所遵循的 MASS Runtime Specification 版本。
  MASS Runtime Specification 遵循语义化版本控制，在主版本内保持向前和向后兼容。
  消费方必须拒绝未知的主版本号。

### 示例

```json
{
  "massVersion": "0.1.0"
}
```

## 元数据

* **`metadata`** (object, REQUIRED) 描述 agent 实例的身份信息。

  * **`name`** (string, REQUIRED) 是该 agent 实例的名称。
    这是实例名（如 "auth-refactor-agent"），
    而非 agent 类型名（如 "claude"）。
    Agent 类型信息体现在 `process` 的具体值中。
    这与 OCI 一致 —— config.json 描述的是一个具体的容器实例，而非镜像。

  * **`annotations`** (map[string]string, OPTIONAL) 包含任意元数据。
    键应使用反向域名表示法（如 `org.openagents.task`）。
    如果未提供注解，此属性可以不存在或为空 map。

### 示例

```json
{
  "metadata": {
    "name": "auth-refactor-agent",
    "annotations": {
      "org.openagents.task": "backend-refactor",
      "org.openagents.owner": "zoumo"
    }
  }
}
```

## Agent Root

* **`agentRoot`** (object, REQUIRED) specifies the agent's root working directory
  within the bundle. Analogous to OCI `config.json`'s `root` field.

  * **`path`** (string, REQUIRED) is the path to the agent root directory,
    **relative to the bundle directory**. Must not be an absolute path.
    Typically `"workspace"`, which agentd symlinks to the actual workspace path
    that the Workspace Manager prepared.

  agent-run resolves this path at `create` time:
  1. Joins the bundle directory with `agentRoot.path` to get an absolute path.
  2. Calls `EvalSymlinks` to follow any symlink — producing a canonical path.
  3. Uses the resolved path as `cmd.Dir` (agent process working directory)
     and passes it to the protocol-specific bootstrap (e.g., ACP `session/new cwd`).

  **与 OCI `root` 的对比**：

  | OCI | MASS |
  |-----|-----|
  | `root.path` — relative path to rootfs inside the bundle | `agentRoot.path` — relative path to workspace link inside the bundle |
  | containerd prepares rootfs (via snapshotter), places it in the bundle | agentd's Workspace Manager prepares the workspace directory, symlinks it into the bundle |
  | runc reads `root.path`, resolves it, uses it as the container's `/` | agent-run reads `agentRoot.path`, resolves it, uses it as the agent's cwd |
  | rootfs is isolated per container | workspace is shared across AgentRuns in the same workspace |

  Key difference from the old `workspace` field: the path is **relative**, not absolute.
  The absolute path is never stored in config.json — it is derived by agent-run at runtime
  by joining with the bundle directory and resolving symlinks. This keeps config.json
  portable within the bundle and mirrors how OCI treats `root.path`.

### 示例

```json
{
  "agentRoot": {
    "path": "workspace"
  }
}
```

Bundle directory layout after agentd prepares it:

```
/var/lib/agentd/bundles/session-abc123/
├── config.json                ← mass writes
└── workspace -> /var/lib/agentd/workspaces/ws-def456/   ← agentd symlinks
```

agent-run resolves `workspace` → `/var/lib/agentd/workspaces/ws-def456/` and uses
that canonical path as the agent process working directory.

## Client Protocol

* **`clientProtocol`** (string, REQUIRED) 指定 agent-run 与 agent 进程之间的通信协议。
  决定了 bootstrap 握手、prompt 投递、事件流的处理方式。

  当前支持的取值：

  | 值 | 说明 |
  |------|------|
  | `"acp"` | 通过 stdio 上的 [ACP 协议][acp] 通信。执行 ACP initialize 握手 → session/new → prompt |

  此字段为枚举类型，未来可扩展支持其他 agent 协议（如原生 CLI 协议）。
  新增协议只需在此枚举中添加新值，并在 agent-run 内部实现对应的 Protocol 适配器。

### 示例

```json
{
  "clientProtocol": "acp"
}
```

## Process

* **`process`** (object, REQUIRED) 指定如何启动 agent 进程。
  运行时使用这些字段进行 fork/exec。
  此配置与 `clientProtocol` 无关——所有协议共享相同的进程启动方式。

  * **`command`** (string, REQUIRED) 是 agent 可执行文件。
    类似于 OCI 的 `process.args[0]`。

  * **`args`** (array of strings, OPTIONAL) 是命令行参数。

  * **`env`** (array of strings, OPTIONAL) 是进程环境变量覆盖列表，
    格式为 `KEY=VALUE`。agentd 在写入此字段前
    会合并 runtimeClass 配置中的环境变量（如 `ANTHROPIC_API_KEY`）
    和编排器请求中的环境变量（如 `GITHUB_TOKEN`）。
    运行时以父进程环境为基础，将此列表中的变量覆盖其上，
    确保 agent 进程始终拥有完整的系统环境（`PATH`、`HOME` 等），
    同时 config.json 中指定的变量优先级更高。

### 示例

```json
{
  "process": {
    "command": "npx",
    "args": ["-y", "@anthropic-ai/claude-code-acp"],
    "env": [
      "ANTHROPIC_API_KEY=sk-ant-xxx",
      "GITHUB_TOKEN=ghp_xxx"
    ]
  }
}
```

## Session

* **`session`** (object, OPTIONAL) 指定会话级配置，包括系统提示、权限策略和 MCP 服务。
  这些配置在 bootstrap 阶段生效，具体投递方式由 `clientProtocol` 决定。

### System Prompt

* **`session.systemPrompt`** (string, OPTIONAL) 是 agent 的角色定义和能力约束。
  它属于 session bootstrap 配置，而不是外部工作 turn。
  Runtime 在 `Create` 阶段必须先落实这份 bootstrap 语义，再对外暴露
  `idle` 状态。

  投递方式取决于 `clientProtocol`：

  | clientProtocol | systemPrompt 投递方式 |
  |----------------|----------------------|
  | `acp` | 通过 ACP bootstrap 流程（session/new 或首条 prompt） |

  未来新增协议时，需在此表中补充对应的投递方式。

  这是 agent 的核心身份属性，与 `process`（如何启动）平级。

### Permissions

* **`session.permissions`** (string, OPTIONAL) 指定 agent-run 处理 agent 发起的
  `fs/*` / `terminal/*` 请求时的权限策略。
  默认值为 `"approve_all"`。

  取值：

  | 值 | 行为 |
  |------|------|
  | `"approve_all"` | 所有 fs/terminal 操作自动批准 |
  | `"approve_reads"` | 只读操作批准，写操作返回 deny |
  | `"deny_all"` | 所有操作返回 deny |

  策略在 session 创建时确定，运行时不可更改。
  不支持权限模型的 `clientProtocol` 会忽略此字段。

### MCP Servers

* **`session.mcpServers`** (array of McpServer, OPTIONAL) 是 agent 可用的 MCP 服务列表。
  默认为 `[]`。

  注入方式取决于 `clientProtocol`：

  | clientProtocol | MCP 注入方式 |
  |----------------|-------------|
  | `acp` | 通过 ACP session/new 的 mcpServers 参数 |

  未来新增协议时，需在此表中补充对应的注入方式。

  注意：agentd 在创建每个 AgentRun 时，会自动注入一个名为 `workspace` 的 stdio MCP server，
  提供 `workspace_status` 和 `workspace_send` 工具。该 server 无需在 config.json 中显式声明。

#### McpServer

每个 MCP 服务条目支持两种模式：HTTP/SSE 远程服务，或 stdio 本地进程。

**HTTP / SSE 模式：**

  * **`type`** (string, REQUIRED) 是传输类型。取值：`"http"`、`"sse"`。
  * **`url`** (string, REQUIRED) 是 MCP 服务的 URL。

**stdio 模式：**

  * **`type`** (string, REQUIRED) 取值：`"stdio"`。
  * **`name`** (string, OPTIONAL) 是 MCP 服务的显示名称。
  * **`command`** (string, REQUIRED) 是 MCP 服务的可执行文件。
  * **`args`** ([]string, REQUIRED) 是命令行参数。必须显式指定为数组，不会做 shell 拆分。
  * **`env`** ([]EnvVar, REQUIRED) 是额外环境变量。格式为 `[{"name": "KEY", "value": "VALUE"}]`。

#### 示例

HTTP MCP server：

```json
{
  "session": {
    "mcpServers": [
      {
        "type": "http",
        "url": "http://localhost:3000/mcp"
      }
    ]
  }
}
```

stdio MCP server：

```json
{
  "session": {
    "mcpServers": [
      {
        "type": "stdio",
        "name": "my-tool",
        "command": "/usr/local/bin/my-mcp-server",
        "args": ["--port", "0"],
        "env": [{"name": "API_KEY", "value": "xxx"}]
      }
    ]
  }
}
```

## 完整示例

### 带角色设定的 ACP Agent

agentd 生成的完整 config.json（runtimeClass "claude" 解析后）：

```json
{
  "massVersion": "0.1.0",

  "metadata": {
    "name": "auth-refactor",
    "annotations": {
      "org.openagents.task": "PROJ-1234"
    }
  },

  "agentRoot": {
    "path": "workspace"
  },

  "clientProtocol": "acp",

  "process": {
    "command": "npx",
    "args": ["-y", "@anthropic-ai/claude-code-acp"],
    "env": [
      "ANTHROPIC_API_KEY=sk-ant-xxx",
      "GITHUB_TOKEN=ghp_xxx"
    ]
  },

  "session": {
    "systemPrompt": "你是专注于安全和认证系统的高级后端工程师。请遵循项目的编码规范。",
    "permissions": "approve_all",
    "mcpServers": [
      {
        "type": "http",
        "url": "http://localhost:3000/mcp"
      }
    ]
  }
}
```

启动后，agent 等待编排器通过 ARI `agentrun/prompt` 发送任务。

### 最小配置

```json
{
  "massVersion": "0.1.0",
  "metadata": { "name": "quick-task" },
  "agentRoot": { "path": "workspace" },
  "clientProtocol": "acp",
  "process": {
    "command": "npx",
    "args": ["-y", "@anthropic-ai/claude-code-acp"]
  }
}
```

## 可扩展性

本规范有意保持精简。已知的未来扩展点：

- 新 `clientProtocol` 值 —— 随新 agent 协议的支持添加

这些字段已预留但尚未定义，将在真实需求出现时添加。

[semver]: https://semver.org/spec/v2.0.0.html
[acp]: https://github.com/anthropics/agent-communication-protocol
