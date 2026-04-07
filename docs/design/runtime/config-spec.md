# 配置规范

本配置文件包含对 agent 执行[标准操作](runtime-spec.md#操作)所需的元数据，
包括 ACP agent 进程的启动方式、环境变量注入、ACP 会话参数等。

本文档定义了规范的权威 schema。
设计思路详见[设计文档](design.md)。

## 规范版本

* **`oarVersion`** (string, REQUIRED) 必须符合 [SemVer v2.0.0][semver] 格式，
  指定该 bundle 所遵循的 OAR Runtime Specification 版本。
  OAR Runtime Specification 遵循语义化版本控制，在主版本内保持向前和向后兼容。
  消费方必须拒绝未知的主版本号。

### 示例

```json
{
  "oarVersion": "0.1.0"
}
```

## 元数据

* **`metadata`** (object, REQUIRED) 描述 agent 实例的身份信息。

  * **`name`** (string, REQUIRED) 是该 agent 实例的名称。
    这是实例名（如 "auth-refactor-agent"），
    而非 agent 类型名（如 "claude"）。
    Agent 类型信息体现在 `acpAgent.process` 的具体值中。
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

  agent-shim resolves this path at `create` time:
  1. Joins the bundle directory with `agentRoot.path` to get an absolute path.
  2. Calls `EvalSymlinks` to follow any symlink — producing a canonical path.
  3. Uses the resolved path as both `cmd.Dir` (agent process working directory)
     and the `cwd` parameter of ACP `session/new`.

  **与 OCI `root` 的对比**：

  | OCI | OAR |
  |-----|-----|
  | `root.path` — relative path to rootfs inside the bundle | `agentRoot.path` — relative path to workspace link inside the bundle |
  | containerd prepares rootfs (via snapshotter), places it in the bundle | agentd's Workspace Manager prepares the workspace directory, symlinks it into the bundle |
  | runc reads `root.path`, resolves it, uses it as the container's `/` | agent-shim reads `agentRoot.path`, resolves it, uses it as the agent's cwd |
  | rootfs is isolated per container | workspace is shared across agents in a Room |

  Key difference from the old `workspace` field: the path is **relative**, not absolute.
  The absolute path is never stored in config.json — it is derived by agent-shim at runtime
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
├── config.json                ← agentd writes
└── workspace -> /var/lib/agentd/workspaces/ws-def456/   ← agentd symlinks
```

agent-shim resolves `workspace` → `/var/lib/agentd/workspaces/ws-def456/` and uses
that canonical path for both `cmd.Dir` and ACP `session/new cwd`.

## ACP Agent

* **`acpAgent`** (object, REQUIRED) 描述 ACP agent 的完整运行时配置。
  字段名中显式包含 "acp"，表明本规范仅支持 [ACP 协议][acp] agent。

  包含三个部分：
  - `systemPrompt` —— agent 的角色定义和能力约束（来自 runtimeClass 配置 + 编排器请求）
  - `process` —— 如何启动 agent 进程（来自 runtimeClass 配置）
  - `session` —— ACP `session/new` 参数（来自 runtimeClass 配置 + 编排器请求）

### System Prompt

* **`acpAgent.systemPrompt`** (string, OPTIONAL) 是 agent 的角色定义和能力约束。
  它属于 session bootstrap 配置，而不是外部工作 turn。
  Runtime 在 `Create` 阶段必须先落实这份 bootstrap 语义，再对外暴露可接收
  `session/prompt` 的 `created` 状态。

  当前 ACP v0.6.3 的 `NewSessionRequest` 还没有 `systemPrompt` 字段，
  因此 runtime 需要在内部把该字段翻译为 ACP 兼容的 bootstrap 流程。
  这个兼容步骤属于创建期的内部 session 建立，不改变上层看到的
  “`session/new` 负责配置、`session/prompt` 负责工作” 合约。

  这是 agent 的核心身份属性，与 `process`（如何启动）和 `session`（会话资源）平级。

### Process

* **`acpAgent.process`** (object, REQUIRED) 指定如何启动 ACP agent 进程。
  运行时使用这些字段进行 fork/exec。
  这些字段通常由 agentd 从 runtimeClass 配置中填充。

  * **`command`** (string, REQUIRED) 是 ACP agent 可执行文件。
    类似于 OCI 的 `process.args[0]`。

  * **`args`** (array of strings, OPTIONAL) 是命令行参数。

  * **`env`** (array of strings, OPTIONAL) 是进程环境变量覆盖列表，
    格式为 `KEY=VALUE`。agentd 在写入此字段前
    会合并 runtimeClass 配置中的环境变量（如 `ANTHROPIC_API_KEY`）
    和编排器请求中的环境变量（如 `GITHUB_TOKEN`）。
    运行时以父进程环境为基础，将此列表中的变量覆盖其上，
    确保 agent 进程始终拥有完整的系统环境（`PATH`、`HOME` 等），
    同时 config.json 中指定的变量优先级更高。

#### 示例

```json
{
  "acpAgent": {
    "process": {
      "command": "npx",
      "args": ["-y", "@anthropic-ai/claude-code-acp"],
      "env": [
        "ANTHROPIC_API_KEY=sk-ant-xxx",
        "GITHUB_TOKEN=ghp_xxx"
      ]
    }
  }
}
```

### Session

* **`acpAgent.session`** (object, OPTIONAL) 指定 ACP bootstrap 所需的会话参数。
  在启动进程并完成 ACP `initialize` 握手后，
  runtime 使用这些字段建立 ACP session，并在同一 bootstrap 过程中落实
  `acpAgent.systemPrompt` 语义。

  字段定义与 [ACP 协议规范][acp] 的 `NewSessionRequest` 对齐：

  * **`mcpServers`** (array of McpServer, OPTIONAL) 是 agent 可用的 MCP 服务列表。
    对应 ACP `session/new` 的 `mcpServers` 参数。默认为 `[]`。

  注意：resolved `cwd` 由 runtime 通过解析 `agentRoot.path`（相对于 bundle 目录）得到，
  `systemPrompt` 作为 bootstrap 配置与这里的字段一起生效，但不要求在 config.json 中
  伪装成独立的外部工作 turn。

  如果 ACP 协议未来添加新的 `session/new` 参数，将直接在此处扩展。
  这确保 OAR Runtime Spec 与 ACP 协议保持对齐。

#### McpServer

每个 MCP 服务条目：

  * **`type`** (string, REQUIRED) 是传输类型。取值：`"http"`、`"sse"`。
  * **`url`** (string, REQUIRED) 是 MCP 服务的 URL。

#### 示例

```json
{
  "acpAgent": {
    "session": {
      "mcpServers": [
        {
          "type": "http",
          "url": "http://localhost:3000/mcp"
        }
      ]
    }
  }
}
```

## Permissions

* **`permissions`** (string, OPTIONAL) 指定 agent-shim 处理 agent 发起的
  `fs/*` / `terminal/*` 请求时的权限策略。
  默认值为 `"approve-all"`。

  取值：

  | 值 | 行为 |
  |------|------|
  | `"approve-all"` | 所有 fs/terminal 操作自动批准 |
  | `"approve-reads"` | 只读操作批准，写操作返回 deny |
  | `"deny-all"` | 所有操作返回 deny |

  策略在 session 创建时确定，运行时不可更改。

### 示例

```json
{
  "permissions": "approve-all"
}
```

## 完整示例

### 带角色设定的 Agent

agentd 生成的完整 config.json（runtimeClass "claude" 解析后）：

```json
{
  "oarVersion": "0.1.0",

  "metadata": {
    "name": "auth-refactor",
    "annotations": {
      "org.openagents.task": "PROJ-1234"
    }
  },

  "agentRoot": {
    "path": "workspace"
  },

  "acpAgent": {
    "systemPrompt": "你是专注于安全和认证系统的高级后端工程师。请遵循项目的编码规范。",
    "process": {
      "command": "npx",
      "args": ["-y", "@anthropic-ai/claude-code-acp"],
      "env": [
        "ANTHROPIC_API_KEY=sk-ant-xxx",
        "GITHUB_TOKEN=ghp_xxx"
      ]
    },
    "session": {
      "mcpServers": [
        {
          "type": "http",
          "url": "http://localhost:3000/mcp"
        }
      ]
    }
  },

  "permissions": "approve-all"
}
```

启动后，agent 等待编排器通过 ARI `session/prompt` 发送任务。

### 空闲 Agent（Room 成员）

runtimeClass "gemini" 解析后：

```json
{
  "oarVersion": "0.1.0",

  "metadata": {
    "name": "code-reviewer"
  },

  "agentRoot": {
    "path": "workspace"
  },

  "acpAgent": {
    "systemPrompt": "你是代码审查专家，负责检查代码变更的正确性、安全性和风格。",
    "process": {
      "command": "gemini"
    }
  }
}
```

启动后，agent 等待 Room 中其他 agent 的审查请求（通过 ARI `session/prompt`）。

### 最小配置

```json
{
  "oarVersion": "0.1.0",
  "metadata": { "name": "quick-task" },
  "agentRoot": { "path": "workspace" },
  "acpAgent": {
    "process": {
      "command": "npx",
      "args": ["-y", "@anthropic-ai/claude-code-acp"]
    }
  }
}
```

## 可扩展性

本规范有意保持精简。已知的未来扩展点：

- `acpAgent.process.user` —— 以特定用户身份运行 agent（agentd 以 root 运行时可能需要）
- `acpAgent.session.*` —— 随 ACP 协议 `session/new` 演进自动扩展（注意：`systemPrompt` 和 `cwd` 已提升到更高层级）
- `hooks` —— 如果 agent 产生基础设施级别的准备需求（如本地模型的 GPU 分配）
- `resources` —— 如果需要资源限制
- `isolation` —— 如果需要沙箱支持

这些字段已预留但尚未定义，将在真实需求出现时添加。

[semver]: https://semver.org/spec/v2.0.0.html
[acp]: https://github.com/anthropics/agent-communication-protocol
