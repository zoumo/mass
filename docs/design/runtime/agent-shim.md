# agent-shim

## 定位

agent-shim 是 OAR 架构中的中间层，对标 containerd-shim。
它是 [OAR Runtime Specification](runtime-spec.md) 的**参考实现**。

每个 agent session 对应一个独立的 agent-shim 进程。
agent-shim 持有 agent 的 stdio，是 agent 进程唯一的 ACP client，
同时对外暴露 JSON-RPC server 供 agentd 管理。

```
agentd（可重启）
   │  JSON-RPC over Unix socket（shim RPC）
   ▼
agent-shim 进程（每个 session 一个，独立存活）
   │  ACP JSON-RPC over stdio
   ▼
agent 进程（claude-acp / pi-acp / gemini / ...）
```

在 OCI 体系里，运行时规范面向 runc（独立的运行时 CLI）。
OAR 没有独立的 runa 组件——相关原因见 [why-no-runa.md](why-no-runa.md)。
runtime spec 由 agent-shim 直接实现。

## 架构参照

| containerd 生态 | OAR 生态 | 说明 |
|----------------|----------|------|
| containerd | agentd | 高层守护进程，可重启 |
| containerd-shim | agent-shim | 中间层，独立进程，生命周期和工作负载绑定 |
| runc | — | agent 不需要内核隔离，职责内化到 agent-shim |
| 容器进程 | agent 进程 | 工作负载 |
| ttrpc over Unix socket | JSON-RPC over Unix socket | shim RPC 协议 |

**agent-shim = runc 的 fork/exec 职责 + containerd-shim 的 stdio 持有 + ACP 协议**

## 进程模型

每个 session 启动时，agentd fork/exec 一个 agent-shim 进程：

```
agentd fork/exec agent-shim --bundle <bundle-dir> --socket <socket-path>

agent-shim 内部：
  1. 读取 bundle/config.json
  2. fork/exec agent 进程（acpAgent.process.command + args + env）
  3. 持有 agent 的 stdin/stdout
  4. 发送 ACP initialize 握手
  5. 发送 ACP session/new（acpAgent.session）
  6. 在 <socket-path> 创建 JSON-RPC server
  7. 等待 agentd 连接并开始管理
```

## agent-shim 的双重角色

agent-shim 同时承担两个角色，朝向不同方向：

```
agentd
  │
  │  【角色 2：RPC Server】
  │  接受 agentd 的连接
  │  暴露管理接口
  │
agent-shim
  │
  │  【角色 1：ACP Client】
  │  持有 agent stdio
  │  处理 ACP 协议
  │
agent 进程
```

### 角色 1：ACP Client（朝下）

agent-shim 是 agent 进程唯一的 ACP client，负责：

- 完成 ACP `initialize` 握手
- 发送 `session/new` 建立会话
- 转发 `session/prompt` 给 agent
- 接收 `session/update` 流式推送
- 响应 agent 发起的 `fs/*` / `terminal/*` 请求（按权限策略）
- 在需要时发送 `session/load`（恢复历史会话）
- 管理 agent 进程的生命周期（监控退出、SIGTERM/SIGKILL）

### 角色 2：RPC Server（朝上）

agent-shim 暴露 JSON-RPC server，供 agentd 调用。
完整方法定义、事件类型和错误码见 [Shim RPC Spec](shim-rpc-spec.md)。

```
方法                说明
────────────────────────────────────────────────────────
Prompt(content)     向 agent 发送 prompt
Cancel()            取消当前 turn（转发 ACP session/cancel）
Subscribe()         订阅 typed event stream
GetState()          查询 agent 进程状态
GetHistory(fromSeq) 读取历史事件（重连补全）
Shutdown()          优雅关闭（SIGTERM → 等待 → SIGKILL）
```

agentd 重启后，通过扫描 `/run/agentd/shim/*/agent-shim.sock` 重新连接，
调用 `GetState()` 恢复 session 元数据，调用 `Subscribe()` 重新订阅事件流。

## ACP 是实现细节，Typed Events 是核心协议

### 关键洞察

agent-shim 对上暴露的 **不是** raw ACP notifications，而是 **typed events**——
经过 agent-shim 翻译、结构化的事件流。ACP 协议被封装在 agent-shim 内部，
agentd 和上层消费者完全不需要理解 ACP。

```
agentd ↔ agent-shim:  typed event stream  ← 系统的核心协议
agent-shim ↔ agent:   ACP over stdio      ← 实现细节，封装在 shim 内部
```

这意味着：
- **agentd 不感知 ACP**。它消费的是 typed events，发送的是 Prompt/Cancel 等高层命令
- **替换 ACP 不影响上层**。如果未来某个 agent 用 gRPC 而不是 ACP，只需要写一个新的 shim 实现
- **事件语义由 shim 定义**，不被底层协议绑定

### ACP → Typed Event 翻译示例

```
ACP 层（shim 内部）                  Typed Event（shim 对外）
─────────────────────────────────    ─────────────────────────────────
session/update:                      ThinkingEvent {
  event: thought_message_chunk         text: "让我先看看项目结构..."
  content: [{text: "让我先看看..."}]   }

agent → fs/read_text_file            FileReadEvent {
  path: "src/main.rs"                   path: "src/main.rs"
  shim 读取文件，返回内容                 status: "success"
  （agentd 完全不参与）                   size: 1234
                                       }

session/update:                      ToolCallEvent {
  event: tool_call                     id: "tc-1"
  kind: "execute"                      kind: "execute"
  title: "Run cargo test"             title: "Run cargo test"
                                       }
```

### 设计原则

1. **agentd 是 typed event 的消费者**，不是 ACP 消息的路由器
2. **agent-shim 是 ACP 协议的唯一理解者**，翻译为上层友好的事件
3. **fs/terminal 操作对 agentd 是只读的**——shim 自行处理，上报结果事件
4. **事件类型为程序消费优化**，不是 ACP 消息的 1:1 映射

## 爆炸半径隔离

agent-shim 独立进程的核心价值是爆炸半径隔离：

```
agentd 重启
    │
    ├── agent-shim 1（session-abc）→ 不受影响，继续跑
    ├── agent-shim 2（session-def）→ 不受影响，继续跑
    └── agent-shim 3（session-ghi）→ 不受影响，继续跑

agentd 重启完成后：
    扫描 /run/agentd/shim/*/agent-shim.sock
    → 重新连接每个 shim
    → GetState() 恢复元数据
    → Subscribe() 重新订阅事件流
    → 恢复正常管理
```

## 实现路径

### 短期：agentd 直接持有 stdio

agentd 直接 fork/exec agent 进程并持有 stdio，作为 ACP client。
不引入独立 shim 进程。

代价：agentd 重启会杀死所有 agent。爆炸半径不隔离。

适用于：早期开发和验证阶段，简化实现。

### 中期：agent-shim 独立进程

引入 agent-shim 作为独立进程，实现本文档描述的完整架构。
agentd 和 agent-shim 通过 JSON-RPC over Unix socket 通信。

### 长期：agent-shim 作为标准组件

agent-shim 作为 OAR 生态的标准组件发布，
支持原生 ACP agent 和 ACP wrapper 两种形态。

## 命名

`agent-shim` 命名对标 containerd-shim，明确架构定位。

名字强调的是**架构角色**（agentd 和 agent 进程之间的中间层），而非内部实现细节。
agent-shim 内部使用 ACP 协议与 agent 进程通信，但对外暴露的是自己的 shim RPC
（JSON-RPC 2.0 + typed event stream），消费方无需感知 ACP。

| 组件 | 命名 | 对标 |
|------|------|------|
| 高层守护进程 | agentd | containerd |
| 中间层 | agent-shim | containerd-shim |
| 工作负载 | agent 进程 | 容器进程 |
��器进程 |
