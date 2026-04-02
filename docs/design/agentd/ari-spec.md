# ARI — Agent Runtime Interface

## 架构参照

CRI（Container Runtime Interface）是 kubelet 和 containerd 之间的 gRPC 接口。
它定义了编排器如何请求容器操作，而不耦合到任何特定的运行时实现。

ARI 服务于相同目的：orchestrator 和 agentd 之间的接口。
它定义了如何创建 session、管理 workspace、与运行中的 agent 交互以及管理 room。

### CRI → ARI 映射

| CRI Service | ARI 对应 | 说明 |
|-------------|---------|------|
| `RuntimeService.RunPodSandbox` | `room/*` 方法 | 创建协同调度组 |
| `RuntimeService.CreateContainer` | `session/new` | 创建工作负载实例 |
| `RuntimeService.StartContainer` | （隐含在 session/new 中） | 推迟分离 |
| `RuntimeService.StopContainer` | `session/stop` | 停止工作负载 |
| `RuntimeService.RemoveContainer` | `session/remove` | 清理元数据 |
| `RuntimeService.ListContainers` | `session/list` | 查询工作负载 |
| `RuntimeService.ContainerStatus` | `session/status` | 查询工作负载状态 |
| `RuntimeService.ExecSync` | `session/prompt` | 在 session 中执行工作 |
| `ImageService.PullImage` | `workspace/prepare` | 准备工作环境 |
| `ImageService.RemoveImage` | `workspace/cleanup` | 移除工作环境 |
| `ImageService.ListImages` | `workspace/list` | 查询可用环境 |

### CRI 有但 ARI 不需要的

| CRI 功能 | 为什么不在 ARI 中 |
|----------|-----------------|
| `ContainerStats` / `PodSandboxStats` | Agent 不需要资源监控 |
| `UpdateContainerResources` | 不需要运行时资源限制 |
| `PortForward` | 没有 network namespace 可以转发 |
| `Attach`（kubectl exec/attach） | ARI 有自己的 attach 语义用于交互式 session |

### ARI 有但 CRI 没有的

| ARI 功能 | 为什么需要 |
|----------|----------|
| `session/prompt` | Agent 处理 prompt。容器不会。这是核心的 agent 交互模型 |
| `session/cancel` | 取消进行中的 agent 工作。遵循 ACP 语义 |
| `session/attach` / `session/detach` | 交互式观察 agent 工作。没有容器对应概念 |
| `room/send` / `room/broadcast` | Room 内的 agent 间通信 |

## 传输

**协议**：JSON-RPC 2.0 over Unix Socket

**路径**：`/run/agentd/agentd.sock`（可配置）

**为什么使用 JSON-RPC over Unix Socket**：

1. 仅本地通信 — 不需要 TCP
2. Unix socket 权限（0600）提供访问控制，不需要 token
3. JSON-RPC 2.0 支持双向 notification（用于流式事件）
4. 远程访问：SSH 隧道，零代码改动

**为什么不用 gRPC**（CRI 使用 gRPC）：

CRI 选择 gRPC 是因为 protobuf schema 强制校验和代码生成。
ARI 使用 JSON-RPC 是因为更简单，并且下游协议 ACP 也是 JSON-RPC —
跨层保持相同的 wire format 减少了翻译开销。

## 方法

### Workspace 方法

Workspace 方法管理 agent 的工作环境。对标 CRI 的 ImageService。

#### `workspace/prepare`

从 Workspace Spec 准备一个 workspace。在 workspace 准备就绪
（source 已解析，postPrepare hooks 已执行）后返回。

```json
// 请求
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "workspace/prepare",
  "params": {
    "spec": {
      "oarVersion": "0.1.0",
      "metadata": { "name": "my-project" },
      "source": {
        "type": "local",
        "path": "/home/user/project"
      }
    }
  }
}

// 响应
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "workspaceId": "ws-abc123",
    "path": "/home/user/project",
    "status": "ready"
  }
}
```

#### `workspace/list`

```json
// 请求
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "workspace/list",
  "params": {}
}

// 响应
{
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "workspaces": [
      {
        "workspaceId": "ws-abc123",
        "name": "my-project",
        "path": "/home/user/project",
        "status": "ready",
        "refs": ["session-001", "session-002"]
      }
    ]
  }
}
```

#### `workspace/cleanup`

```json
// 请求
{
  "jsonrpc": "2.0",
  "id": 3,
  "method": "workspace/cleanup",
  "params": {
    "workspaceId": "ws-abc123"
  }
}
```

如果仍有 session 引用此 workspace 则失败。

### Session 方法

Session 方法管理 agent session。核心方法通过 shim RPC 转发给 agent；
ARI 特有的方法处理管理操作。

#### `session/new`

创建新 session 并启动 agent 进程。

```json
// 请求
{
  "jsonrpc": "2.0",
  "id": 10,
  "method": "session/new",
  "params": {
    "runtimeClass": "claude",
    "workspaceId": "ws-abc123",
    "systemPrompt": "你是一个编码 agent。",
    "prompt": "重构 auth 模块，使用 JWT token。",
    "env": ["GITHUB_TOKEN=${GITHUB_TOKEN}"],
    "mcpServers": [
      { "type": "http", "url": "http://localhost:3000/mcp" }
    ],
    "labels": {
      "task": "auth-refactor"
    },
    "room": "backend-refactor",
    "roomAgent": "architect"
  }
}

// 响应
{
  "jsonrpc": "2.0",
  "id": 10,
  "result": {
    "sessionId": "session-abc123"
  }
}
```

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `runtimeClass` | string | 是 | Agent 类型名称。agentd 查找对应的 handler 配置，生成 config.json |
| `workspaceId` | string | 是 | 使用的 workspace（来自 workspace/prepare）。路径写入 `acpAgent.session.cwd` |
| `systemPrompt` | string | 否 | Agent 的系统提示。写入 `acpAgent.systemPrompt` |
| `prompt` | string | 否 | 初始 prompt。session 创建后通过 `session/prompt` 发送。省略则 agent 启动后等待外部 prompt |
| `env` | []string | 否 | 额外环境变量，与 runtimeClass 配置中的 env 合并。写入 `acpAgent.process.env` |
| `mcpServers` | []McpServer | 否 | Agent 可用的 MCP 服务列表。写入 `acpAgent.session.mcpServers` |
| `labels` | map | 否 | 任意标签 |
| `room` | string | 否 | Room 名称（属于 room 时填写） |
| `roomAgent` | string | 否 | Room 内的 agent 名称 |

**内部流程**：
1. 查找 runtimeClass 配置（command、args、env）
2. 生成 OAR config.json：
   - `acpAgent.systemPrompt` ← 请求中的 systemPrompt
   - `acpAgent.process` ← runtimeClass 配置（command、args）+ 合并后的 env
   - `acpAgent.session` ← mcpServers
   - `workspace` ← workspace 路径
3. 写入 config.json 到 bundle 目录
4. fork/exec agent-shim --bundle \<dir\>
   agent-shim 内部完成：进程启动 → ACP 握手 → session/new
5. 返回 session ID

#### `session/prompt`

向运行中的 session 发送 prompt。agentd 通过 shim RPC 转发给 agent。

```json
// 请求
{
  "jsonrpc": "2.0",
  "id": 11,
  "method": "session/prompt",
  "params": {
    "sessionId": "session-abc123",
    "prompt": "将 auth 模块重构为使用 JWT token。"
  }
}
```

响应是 ACP prompt 响应。流式事件作为 JSON-RPC notification 送达
（见下方事件部分）。

#### `session/cancel`

取消当前 turn。agentd 通过 shim RPC 转发。

```json
{
  "jsonrpc": "2.0",
  "method": "session/cancel",
  "params": {
    "sessionId": "session-abc123"
  }
}
```

#### `session/stop`

停止 session。杀死 agent 进程。

```json
// 请求
{
  "jsonrpc": "2.0",
  "id": 12,
  "method": "session/stop",
  "params": {
    "sessionId": "session-abc123"
  }
}
```

#### `session/remove`

移除 session 元数据并释放 workspace 引用。Session 必须已停止。

```json
{
  "jsonrpc": "2.0",
  "id": 13,
  "method": "session/remove",
  "params": {
    "sessionId": "session-abc123"
  }
}
```

#### `session/list`

```json
// 请求
{
  "jsonrpc": "2.0",
  "id": 14,
  "method": "session/list",
  "params": {
    "filter": {
      "runtimeClass": "claude",
      "room": "backend-refactor",
      "state": "running",
      "labels": { "task": "auth-refactor" }
    }
  }
}

// 响应
{
  "jsonrpc": "2.0",
  "id": 14,
  "result": {
    "sessions": [
      {
        "sessionId": "session-abc123",
        "runtimeClass": "claude",
        "state": "running",
        "room": "backend-refactor",
        "roomAgent": "architect",
        "labels": { "task": "auth-refactor" },
        "workspace": "ws-abc123"
      }
    ]
  }
}
```

#### `session/status`

```json
// 请求
{
  "jsonrpc": "2.0",
  "id": 15,
  "method": "session/status",
  "params": {
    "sessionId": "session-abc123"
  }
}

// 响应
{
  "jsonrpc": "2.0",
  "id": 15,
  "result": {
    "sessionId": "session-abc123",
    "state": "running",
    "activity": "processing",
    "processStatus": "alive",
    "pid": 12345,
    "runtimeClass": "claude",
    "workspace": "ws-abc123",
    "room": "backend-refactor",
    "roomAgent": "architect",
    "turnCount": 3,
    "uptimeMs": 120000
  }
}
```

#### `session/attach` / `session/detach`

Attach 到运行中的 session，订阅 event stream 并可注入 prompt。

**Attach 不是 ACP 接管**。agentd 始终是 agent 的唯一 ACP client，持有 agent stdio。
Attach 连接只传递 ARI 子集消息（见下方），`fs/*` / `terminal/*` 等 ACP client-side
请求由 agent-shim 按权限策略处理，不经过 attach 连接。

多个连接可以同时 attach 同一个 session（fan-out），均可收到 `session/update`。
`session/prompt` 注入会进入 agentd 内部队列，串行转发给 agent，不存在竞争。

```json
// Attach
{
  "jsonrpc": "2.0",
  "id": 16,
  "method": "session/attach",
  "params": {
    "sessionId": "session-abc123"
  }
}

// 响应（连接建立，后续消息为 notification 推送）
{
  "jsonrpc": "2.0",
  "id": 16,
  "result": {
    "sessionId": "session-abc123",
    "state": "running"
  }
}

// Detach（或直接断开连接，效果相同）
{
  "jsonrpc": "2.0",
  "id": 17,
  "method": "session/detach",
  "params": {
    "sessionId": "session-abc123"
  }
}
```

**Attach 连接上的消息**：

| 方向 | 方法 | 说明 |
|---|---|---|
| agentd → client | `session/update` | agent 流式输出（thinking / content / tool_call） |
| agentd → client | `session/stateChange` | session 状态变更通知 |
| client → agentd | `session/prompt` | 注入 prompt，进入队列后串行发给 agent |
| client → agentd | `session/cancel` | 取消当前 turn |

连接断开即自动 detach，无需显式调用 `session/detach`。

### Room 方法

Room 方法管理 agent 组。对标 CRI 的 PodSandbox 操作。

#### `room/create`

```json
{
  "jsonrpc": "2.0",
  "id": 20,
  "method": "room/create",
  "params": {
    "name": "backend-refactor",
    "labels": { "project": "auth-service" },
    "communication": { "mode": "mesh" }
  }
}
```

#### `room/status`

```json
// 请求
{
  "jsonrpc": "2.0",
  "id": 21,
  "method": "room/status",
  "params": {
    "name": "backend-refactor"
  }
}

// 响应
{
  "jsonrpc": "2.0",
  "id": 21,
  "result": {
    "name": "backend-refactor",
    "members": [
      { "agentName": "architect", "sessionId": "session-abc123", "state": "running" },
      { "agentName": "coder", "sessionId": "session-def456", "state": "paused:warm" },
      { "agentName": "reviewer", "sessionId": "session-ghi789", "state": "running" }
    ],
    "communication": { "mode": "mesh" }
  }
}
```

#### `room/delete`

```json
{
  "jsonrpc": "2.0",
  "id": 22,
  "method": "room/delete",
  "params": {
    "name": "backend-refactor"
  }
}
```

删除 room 前，所有成员 session 必须已停止。

### Agent 方法

#### `agent/list`

列出已注册的 agent 类型（从 Runtime Spec 注册表加载）。

```json
// 请求
{
  "jsonrpc": "2.0",
  "id": 30,
  "method": "agent/list",
  "params": {}
}

// 响应
{
  "jsonrpc": "2.0",
  "id": 30,
  "result": {
    "agents": [
      {
        "name": "claude",
        "annotations": {
          "org.openagents.vendor": "anthropic"
        },
        "protocol": { "type": "acp", "transport": "stdio" }
      },
      {
        "name": "gemini",
        "annotations": {
          "org.openagents.vendor": "google"
        },
        "protocol": { "type": "acp", "transport": "stdio" }
      }
    ]
  }
}
```

### Daemon 方法

#### `daemon/status`

```json
{
  "jsonrpc": "2.0",
  "id": 40,
  "method": "daemon/status",
  "params": {}
}

// 响应
{
  "jsonrpc": "2.0",
  "id": 40,
  "result": {
    "version": "0.1.0",
    "uptime": 3600,
    "sessions": { "total": 5, "running": 2, "paused": 2, "stopped": 1 },
    "workspaces": { "total": 3, "ready": 3 },
    "rooms": { "total": 1 }
  }
}
```

## 事件（JSON-RPC Notification）

事件是 agentd 向已连接客户端的推送通知。
使用 JSON-RPC 2.0 notification（没有 `id` 字段）。

### `session/update`

agent-shim typed event 推送。在 session/prompt 执行期间流式推送。

```json
{
  "jsonrpc": "2.0",
  "method": "session/update",
  "params": {
    "sessionId": "session-abc123",
    "type": "text",
    "content": "我将从分析 auth 模块开始..."
  }
}
```

### `session/stateChange`

Session 状态转换通知。

```json
{
  "jsonrpc": "2.0",
  "method": "session/stateChange",
  "params": {
    "sessionId": "session-abc123",
    "from": "running",
    "to": "paused:warm",
    "reason": "turn_completed"
  }
}
```

## 方法汇总

| 方法 | 方向 | 说明 |
|------|------|------|
| `workspace/prepare` | 请求 | 从 spec 准备 workspace |
| `workspace/list` | 请求 | 列出 workspace |
| `workspace/cleanup` | 请求 | 移除 workspace |
| `session/new` | 请求 | 创建 session + 启动 agent |
| `session/prompt` | 请求 | 发送 prompt（通过 shim RPC 转发） |
| `session/cancel` | 通知 | 取消当前 turn（通过 shim RPC 转发） |
| `session/stop` | 请求 | 停止 session + 杀死 agent |
| `session/remove` | 请求 | 移除 session 元数据 |
| `session/list` | 请求 | 列出 session |
| `session/status` | 请求 | 获取 session 详情 |
| `session/attach` | 请求 | Attach 到 session |
| `session/detach` | 请求 | 从 session detach |
| `room/create` | 请求 | 创建 room |
| `room/status` | 请求 | 获取 room 状态 |
| `room/delete` | 请求 | 删除 room |
| `agent/list` | 请求 | 列出已注册的 agent 类型 |
| `daemon/status` | 请求 | Daemon 健康状态 |
| `session/update` | 通知 | typed event 流（推送） |
| `session/stateChange` | 通知 | 状态转换（推送） |
