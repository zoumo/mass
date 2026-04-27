---
last_updated: 2026-04-17
---

# AgentRun 与 agent-run 状态机

本文档描述 AgentRun（DB 持久化状态）和 agent-run（运行时状态）的状态定义、变更时机、同步机制，以及 Prompt 的完整生命周期。

## 状态枚举

AgentRun 和 agent-run 共用同一组状态枚举：

| 状态 | 含义 |
|------|------|
| `creating` | Agent 正在创建，ACP 握手尚未完成 |
| `idle` | Agent 进程运行中，ACP session 已建立，等待 prompt |
| `running` | Agent 正在处理 prompt |
| `restarting` | 重启已接受，正在停止现有进程（阻塞新 prompt） |
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
`idle`/`running` + `agentrun/restart` → `restarting` → `creating`（先停止现有进程再重新 bootstrap）。

## 核心原则

**agent-run 是状态的权威源（source of truth）**，AgentRun 的 DB 状态是其镜像。

唯一例外：`idle → running` 由 mass **先写 DB** 再投递 prompt，用于防止并发 prompt 竞争。

## 逐阶段状态变更对照

### 1. 创建（→ creating）

| 角色 | 行为 |
|------|------|
| mass | ARI `Create` 写入 DB `phase=creating` |
| agent-run | 刚 fork，尚未启动 ACP |

两者对齐。

### 2. Bootstrap 完成（creating → idle）

| 角色 | 行为 |
|------|------|
| agent-run | ACP 握手完成，`writeState(idle)` 触发 `StateChangeHook` |
| mass | 收到 `runtime_update`（含 Phase）通知 → `startEventConsumer` 写 DB |
| mass（补偿） | 若通知在 Subscribe 之前已发出而丢失，`client.Status()` 主动查询状态并同步 DB |

两者对齐，有补偿机制。

### 3. 发送 Prompt（idle → running）

| 角色 | 行为 |
|------|------|
| mass | **先** 原子 CAS 写 DB `idle→running` |
| mass | **然后** 异步投递 `session/prompt` RPC 到 agent-run |
| agent-run | 收到 RPC 后，`Manager.Prompt()` 内 `writeState(running)` |

**存在短暂不一致窗口**：DB 已是 `running` 但 agent-run 可能仍为 `idle`（prompt 还在传输中）。这是故意设计——DB 先 reserve 防止并发 prompt。

### 4. Prompt 完成（running → idle）

| 角色 | 行为 |
|------|------|
| agent-run | ACP `conn.Prompt()` 返回后，`writeState(idle)` 触发 `runtime_update` 通知 |
| mass | `startEventConsumer` 收到通知，写 DB `idle` |

**agent-run 先变，DB 后变**——异步通知有微小延迟。

### 5. 进程退出（→ stopped）

| 角色 | 行为 |
|------|------|
| agent-run | 进程退出 |
| mass | `watchProcess` 检测到 `Done` 信号，写 DB `stopped` |

两者对齐。

### 6. 主动 Stop

| 角色 | 行为 |
|------|------|
| mass | 发 `runtime/stop` RPC → agent-run 退出 → `watchProcess` 写 DB `stopped` |
| mass | 若 2 秒未退出则 SIGKILL |

### 7. 启动失败（→ error）

| 角色 | 行为 |
|------|------|
| mass | Start goroutine 写 DB `error` + ErrorMessage |

### 8. Restart（任意状态 → creating）

| 角色 | 行为 |
|------|------|
| mass | 写 DB `creating` → 若有活 agent-run 则 Stop → 再次写 `creating` → Start 新 agent-run |

### 9. Recovery（daemon 重启后）

| 角色 | 行为 |
|------|------|
| mass | 读 `runtime/status` 获取真实状态，覆盖 DB |
| mass | 不可达 → 标记 `stopped` |
| mass | 卡在 `creating` → 标记 `error` |

## 防止不一致的保护机制

| 机制 | 说明 |
|------|------|
| Stale guard | DB 已 `stopped` 时，丢弃非 stopped 的 `runtime_update` 通知 |
| CAS 原子转换 | `TransitionState` 只有当 DB 当前状态匹配预期才允许转换 |
| Recovery 阻塞 | `IsRecovering()` 期间拒绝所有 prompt/操作请求 |
| Bootstrap 补偿 | Subscribe 后主动 `client.Status()` 防止错过 creating→idle 通知 |

## Prompt 完整生命周期

### 调用链

```
mass ARI server                         agent-run                    agent 进程
       │                                │                                   │
  1.   │ DB: idle → running (CAS)       │                                   │
  2.   │── session/prompt RPC ─────────►│                                   │
       │                                │                                   │
       │                         Service.Prompt()                           │
       │                                │                                   │
       │                    3.   trans.NotifyTurnStart()                     │
       │                    4.   trans.NotifyUserPrompt()                    │
       │                                │                                   │
       │                         Manager.Prompt()                           │
       │                                │                                   │
       │                    5.   writeState(running)                         │
       │◄── runtime_update 通知 ────────│   reason="prompt-started"         │
       │    (startEventConsumer)         │                                   │
       │                                │                                   │
       │                    6.   conn.Prompt() ── ACP request ─────────────►│
       │                         （同步阻塞等待）                             │
       │                                │                                   │
       │                                │◄── notification ─────────────────│ 思考中...
       │◄── runtime/event_update ───────│    agent_thinking                  │
       │                                │◄── notification ─────────────────│ 调用工具...
       │◄── runtime/event_update ───────│    tool_call                      │
       │                                │◄── notification ─────────────────│ 文本输出...
       │◄── runtime/event_update ───────│    agent_message                  │
       │                                │                                   │
       │                                │◄── ACP PromptResponse ──────────│ 处理完成
       │                                │    (含 StopReason)                │
       │                                │                                   │
       │                    7.   writeState(idle)                            │
       │◄── runtime_update 通知 ────────│   reason="prompt-completed"       │
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
◄── notification (无 id): agent_thinking ───────────  │ 思考片段
◄── notification (无 id): tool_call ────────────────  │ 发起工具调用
◄── notification (无 id): tool_result ──────────────  │ 工具结果
◄── notification (无 id): agent_message ────────────  │ 文本回复片段
                                                     │
◄── JSON-RPC response (id:42): stopReason ──────────  │ 处理完成
         （conn.Prompt() 解除阻塞）
```

这是标准 JSON-RPC 2.0 双工特性：request/response 通道（靠 `id` 匹配）与 notification 通道（无 `id`）并行工作。

agent-run 内部将 ACP notification 非阻塞推入缓冲 channel，Translator 协程持续消费并广播为 `AgentRunEvent`。

### Prompt 没有超时

整个 prompt 调用链上**没有设置任何超时**——从 mass ARI server 到 agent-run 的 `client.Prompt()`，再到 `Manager.Prompt()` 和最终的 `conn.Prompt()`，全部使用 `context.Background()` 无限等待。

**设计原因**：Agent 一次 prompt 可能涉及多轮 LLM 推理、多次工具调用（编译、测试、网络请求等），时长不可预测，固定超时会误杀正常的长任务。

**替代方案**：不靠超时，靠主动控制：

| 机制 | 说明 |
|------|------|
| `agentrun/cancel` | 取消当前 prompt，调 agent-run 的 `Cancel()` |
| `agentrun/stop` | 停掉整个 agent-run 进程，2 秒未退出则 SIGKILL |
| 进程崩溃 | Agent 进程崩溃 → pipe 断开 → `conn.Prompt()` 返回 error → `watchProcess` 设为 `stopped` |
