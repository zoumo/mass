# AgentRun 与 Shim 状态机

本文档描述 AgentRun（DB 持久化状态）和 Shim（运行时状态）的状态定义、变更时机、同步机制，以及 Prompt 的完整生命周期。

## 状态枚举

AgentRun 和 Shim 共用同一组状态枚举（`pkg/runtime-spec/api/types.go`）：

| 状态 | 含义 |
|------|------|
| `creating` | Agent 正在创建，ACP 握手尚未完成 |
| `idle` | Agent 进程运行中，ACP session 已建立，等待 prompt |
| `running` | Agent 正在处理 prompt |
| `stopped` | Agent 进程已退出 |
| `error` | Agent 遇到不可恢复的错误 |

## 状态机图

```
         create
           │
           ▼
      ┌──────────┐
      │ creating  │
      └────┬──────┘
           │ ACP bootstrap 完成
           ▼
      ┌──────────┐       agentrun/prompt        ┌──────────┐
      │   idle   │ ───────────────────────────► │  running  │
      │          │ ◄─────────────────────────── │           │
      └────┬─────┘       prompt 完成             └────┬──────┘
           │                                          │
           │ stop / exit / error                      │ stop / exit / error
           ▼                                          ▼
      ┌──────────┐                               ┌──────────┐
      │ stopped  │                               │ stopped  │
      └────┬─────┘                               └────┬─────┘
           │ delete                                    │ delete
           ▼                                           ▼
       (removed)                                   (removed)
```

任意非终态 + `agentrun/restart` → `creating`（重新走一遍流程）。

## 核心原则

**Shim 是状态的权威源（source of truth）**，AgentRun 的 DB 状态是 shim 状态的镜像。

唯一例外：`idle → running` 由 mass **先写 DB** 再投递 prompt，用于防止并发 prompt 竞争。

## 逐阶段状态变更对照

### 1. 创建（→ creating）

| 角色 | 行为 | 代码位置 |
|------|------|----------|
| mass | ARI `Create` 写入 DB `state=creating` | `pkg/ari/server/server.go:373` |
| shim | 刚 fork，尚未启动 ACP | — |

两者对齐。

### 2. Bootstrap 完成（creating → idle）

| 角色 | 行为 | 代码位置 |
|------|------|----------|
| shim | ACP 握手完成，`writeState(idle)` 触发 `StateChangeHook` | `pkg/runtime/runtime.go` Create 流程 |
| mass | 收到 `state_change` 通知 → `startEventConsumer` 写 DB | `pkg/agentd/process.go:175` |
| mass（补偿） | 若通知在 Subscribe 之前已发出而丢失，`client.Status()` 主动查询 shim 状态并同步 DB | `pkg/agentd/process.go:322-337` |

两者对齐，有补偿机制。

### 3. 发送 Prompt（idle → running）

| 角色 | 行为 | 代码位置 |
|------|------|----------|
| mass | **先** 原子 CAS 写 DB `idle→running` | `pkg/ari/server/server.go:445` |
| mass | **然后** 异步投递 `session/prompt` RPC 到 shim | `pkg/ari/server/server.go:474` |
| shim | 收到 RPC 后，`Manager.Prompt()` 内 `writeState(running)` | `pkg/runtime/runtime.go:259-261` |

**存在短暂不一致窗口**：DB 已是 `running` 但 shim 可能仍为 `idle`（prompt 还在传输中）。这是故意设计——DB 先 reserve 防止并发 prompt。

### 4. Prompt 完成（running → idle）

| 角色 | 行为 | 代码位置 |
|------|------|----------|
| shim | ACP `conn.Prompt()` 返回后，`writeState(idle)` 触发 `state_change` 通知 | `pkg/runtime/runtime.go:269-276` |
| mass | `startEventConsumer` 收到通知，写 DB `idle` | `pkg/agentd/process.go:175-184` |

**shim 先变，DB 后变**——异步通知有微小延迟。

### 5. 进程退出（→ stopped）

| 角色 | 行为 | 代码位置 |
|------|------|----------|
| shim | 进程退出 | — |
| mass | `watchProcess` 检测到 `Done` 信号，写 DB `stopped` | `pkg/agentd/process.go:694` |

两者对齐。

### 6. 主动 Stop

| 角色 | 行为 | 代码位置 |
|------|------|----------|
| mass | 发 `runtime/stop` RPC → shim 退出 → `watchProcess` 写 DB `stopped` | `pkg/agentd/process.go:710-764` |
| mass | 若 10 秒未退出则 SIGKILL | `pkg/agentd/process.go:750-756` |

### 7. 启动失败（→ error）

| 角色 | 行为 | 代码位置 |
|------|------|----------|
| mass | Start goroutine 写 DB `error` + ErrorMessage | `pkg/ari/server/server.go:394-397` |

### 8. Restart（任意状态 → creating）

| 角色 | 行为 | 代码位置 |
|------|------|----------|
| mass | 写 DB `creating` → 若有活 shim 则 Stop → 再次写 `creating` → Start 新 shim | `pkg/ari/server/server.go:556-613` |

### 9. Recovery（daemon 重启后）

| 角色 | 行为 | 代码位置 |
|------|------|----------|
| mass | 读 shim `runtime/status` 获取真实状态，覆盖 DB | `pkg/agentd/recovery.go:128-136` |
| mass | shim 不可达 → 标记 `stopped` | `pkg/agentd/recovery.go:91-98` |
| mass | 卡在 `creating` → 标记 `error` | `pkg/agentd/recovery.go:156` |

## 防止不一致的保护机制

| 机制 | 说明 | 代码位置 |
|------|------|----------|
| Stale guard | DB 已 `stopped` 时，丢弃非 stopped 的 state_change 通知 | `pkg/agentd/process.go:168-173` |
| CAS 原子转换 | `TransitionState` 只有当 DB 当前状态匹配预期才允许转换 | `pkg/agentd/agent.go` |
| Recovery 阻塞 | `IsRecovering()` 期间拒绝所有 prompt/操作请求 | `pkg/ari/server/server.go:420-424` |
| Bootstrap 补偿 | Subscribe 后主动 `client.Status()` 防止错过 creating→idle 通知 | `pkg/agentd/process.go:322-337` |

## Prompt 完整生命周期

### 调用链

```
mass ARI server                      shim                              agent 进程
       │                                │                                   │
  1.   │ DB: idle → running (CAS)       │                                   │
  2.   │── session/prompt RPC ─────────►│                                   │
       │                                │                                   │
       │                         Service.Prompt()                           │
       │                         [pkg/shim/server/service.go:28]            │
       │                                │                                   │
       │                    3.   trans.NotifyTurnStart()                     │
       │                    4.   trans.NotifyUserPrompt()                    │
       │                                │                                   │
       │                         Manager.Prompt()                           │
       │                         [pkg/runtime/runtime.go:249]               │
       │                                │                                   │
       │                    5.   writeState(running)                         │
       │◄── state_change 通知 ──────────│   reason="prompt-started"         │
       │    (startEventConsumer)         │                                   │
       │                                │                                   │
       │                    6.   conn.Prompt() ── ACP request ─────────────►│
       │                         （同步阻塞等待）                             │
       │                                │                                   │
       │                                │◄── notification ─────────────────│ 思考中...
       │◄── shim/event ────────────────│    agent_thought_chunk            │
       │                                │◄── notification ─────────────────│ 调用工具...
       │◄── shim/event ────────────────│    tool_call                      │
       │                                │◄── notification ─────────────────│ 文本输出...
       │◄── shim/event ────────────────│    agent_message_chunk            │
       │                                │                                   │
       │                                │◄── ACP PromptResponse ──────────│ 处理完成
       │                                │    (含 StopReason)                │
       │                                │                                   │
       │                    7.   writeState(idle)                            │
       │◄── state_change 通知 ──────────│   reason="prompt-completed"       │
       │    (startEventConsumer 写 DB)   │                                   │
       │                                │                                   │
       │                    8.   trans.NotifyTurnEnd(stopReason)             │
       │                                │                                   │
       │◄── RPC 返回 stopReason ────────│                                   │
```

### Prompt 阻塞与流式并行

`conn.Prompt()` 是**阻塞调用**，等 agent 走完整个 turn（所有思考 + 工具调用循环）才返回 `StopReason`。

但中间结果通过 ACP `session/update` **notification 异步流式推送**，不阻塞 prompt 响应：

```
JSON-RPC request (id:42)  ──────────────────────►  agent 收到 prompt
                                                     │
         （conn.Prompt() 阻塞中）                      │ 处理中...
                                                     │
◄── notification (无 id): agent_thought_chunk ──────  │ 思考片段
◄── notification (无 id): tool_call ────────────────  │ 发起工具调用
◄── notification (无 id): tool_call_update ─────────  │ 工具结果
◄── notification (无 id): agent_message_chunk ──────  │ 文本回复片段
                                                     │
◄── JSON-RPC response (id:42): stopReason ──────────  │ 处理完成
         （conn.Prompt() 解除阻塞）
```

这是标准 JSON-RPC 2.0 双工特性：request/response 通道（靠 `id` 匹配）与 notification 通道（无 `id`）并行工作。

Shim 内部通过 `acpClient.SessionUpdate()` 将 notification 非阻塞推入 1024 缓冲 channel，`Translator.run()` 协程持续消费并广播为 `ShimEvent`。

### Prompt 没有超时

整个 prompt 调用链上**没有设置任何超时**：

```
server.go:474    client.Prompt(context.Background(), ...)   ← 无超时
  └► client.go:36   c.c.Call(ctx, ...)
      └► jsonrpc/client.go:74   c.conn.Call(ctx, ...)       ← 透传 context.Background()
          └► shim service.go:34   s.mgr.Prompt(ctx, ...)
              └► runtime.go:264   conn.Prompt(ctx, ...)     ← 透传，无限等待 ACP agent
```

**设计原因**：Agent 一次 prompt 可能涉及多轮 LLM 推理、多次工具调用（编译、测试、网络请求等），时长不可预测，固定超时会误杀正常的长任务。

**替代方案**：不靠超时，靠主动控制：

| 机制 | 说明 | 代码位置 |
|------|------|----------|
| `agentrun/cancel` | 取消当前 prompt，调 shim 的 `Cancel()` | `pkg/ari/server/server.go:486` |
| `agentrun/stop` | 停掉整个 shim 进程，10 秒未退出则 SIGKILL | `pkg/agentd/process.go:710-764` |
| 进程崩溃 | Agent 进程崩溃 → pipe 断开 → `conn.Prompt()` 返回 error → `watchProcess` 设为 `stopped` | `pkg/agentd/process.go:675-698` |
