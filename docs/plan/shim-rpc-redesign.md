# Shim RPC 协议重设计计划

> 状态：草案
> 日期：2026-04-07

---

## 1. 背景与问题

### 1.1 现状

当前 shim-rpc 的设计原则是 **"ACP 不穿透"**：agent-shim 把 ACP 协议完全封装在内部，对外暴露自己定义的 typed events 和高层命令。

方法名使用 PascalCase（`Prompt`、`Cancel`、`GetState`）；事件通过统一的 `$/event` notification 包装器推送，类型为自定义字符串（`text`、`tool_call` 等）。

### 1.2 问题

**命名风格与 ACP 割裂**

ACP 使用 `session/prompt`、`session/cancel` 这类 `namespace/verb_noun` 风格；shim-rpc 使用的 `Prompt`、`Cancel` 是 gRPC/tRPC 风格。二者在同一个 agent 生态里共存，阅读代码需要时刻在两套命名之间切换。

**翻译层带来的抽象泄漏**

shim-rpc 声称自己是"翻译后的 typed events"，但实际上 `tool_call`、`plan`、`tool_result` 等事件的语义与 ACP 的 `tool_call` / `plan` / `tool_call_update` 高度重合，翻译本身没有创造新的价值，只是换了个名字，却引入了额外的维护成本——每当 ACP 增加新的 `sessionUpdate` 变体，翻译层需要同步跟进。

**`file_read` / `file_write` / `command` 事件的语义错误**

当前 shim-rpc 事件中包含 `file_read`、`file_write`、`command` 三种事件类型，带有 `allowed: bool` 字段。这隐含了一个错误的假设：shim 是 fs/terminal 操作的看门人，agentd 需要知道每次文件读写和命令执行。

但实际上（见第 2 节），这些操作都是 agent 自己完成的，shim 完全不参与，这三种事件是永远不会被发出的"僵尸"事件。

**定位模糊**

当前文档说 shim-rpc 是"翻译层"；但翻译层如果忠实于原协议，就应该直接透传；如果要创造新抽象，就应该明确说明新抽象的价值在哪里。现在两者都不彻底。

---

## 2. fs/terminal 能力的设计立场

### 2.1 ACP fs/terminal 的真实用途

ACP 的 `fs/read_text_file`、`fs/write_text_file`、`terminal/*` 方法，是为 **IDE/Editor 类 Client** 专门设计的：

| ACP 方法 | IDE 场景价值 |
|---------|-------------|
| `fs/read_text_file` | 读取编辑器 buffer 中**未保存**的最新内容 |
| `fs/write_text_file` | 让编辑器感知文件修改，触发 diff 展示、undo 历史 |
| `terminal/*` | 在 IDE panel 中展示命令的实时输出流 |

这三个方法都是可选的 Client 能力，Agent 在调用前必须检查 Client 是否声明支持。文档原文：

> *"The filesystem methods allow Agents to read and write text files within the **Client's environment**. These methods enable Agents to access **unsaved editor state** and allow Clients to **track file modifications**."*

### 2.2 Agent 不需要 Client 来读写文件

当 Client 不声明 `fs`/`terminal` 能力时，**Agent 不会停止工作**。它会改用自己的工具（MCP tools 或内置工具，如 Claude Code 的 `Read`/`Write`/`Bash`）直接操作磁盘和 shell，完全绕过 ACP fs/terminal 方法。

```
Client 不支持 fs/terminal
         │
         ├── Agent 调用 fs/read_text_file？ ✗ MUST NOT（协议禁止）
         │
         └── Agent 用自己的 MCP 工具读文件？ ✓ 照常工作
             (Read tool / bash / grep 等)
```

### 2.3 runtime 的立场

**open-agent-runtime 的 agent-shim 不实现 `fs`/`terminal` Client 能力。**

理由：
- runtime 不是 IDE，没有 editor buffer，也不需要 IDE terminal panel
- Agent 通过自身工具直接操作 workspace，功能完整
- 不声明这两个能力，ACP agent 不会发起这些请求，没有任何功能缺失

在 ACP `initialize` 握手时，agent-shim 发送的 `clientCapabilities` 中：
- `fs` 字段：**不携带**（视为不支持）
- `terminal` 字段：**不携带**（视为不支持）

这是一个明确的、有意的架构决策，不是遗漏。

---

## 3. 重设计方向

### 3.1 定位：ACP 超集

shim-rpc 的定位从"翻译层"调整为 **"ACP 超集"**：

```
shim-rpc = ACP 核心方法（透传/对齐）+ runtime 扩展方法
```

- **ACP 部分**：方法名、参数、事件结构与 ACP 规范对齐，shim 作为协议中继，不做语义转换
- **Runtime 扩展部分**：agentd 生命周期管理、状态查询、重连、关闭等 ACP 没有定义的能力，使用 `runtime/` 命名空间

这样的好处：
- agentd 将来如果需要理解某个 ACP 字段（如 `stopReason`、`toolCallId`），不需要经过翻译层
- shim-rpc 的文档可以直接引用 ACP 规范，减少重复描述
- 接入新的 ACP agent 不需要更新翻译表

### 3.2 命名规范

采用 ACP 的 `namespace/verb_noun` 风格（全小写 + 下划线）：

| 当前命名 | 新命名 | 分类 |
|---------|--------|------|
| `Prompt` | `session/prompt` | ACP 对齐 |
| `Cancel` | `session/cancel` | ACP 对齐 |
| `Subscribe` | `runtime/subscribe` | Runtime 扩展 |
| `GetState` | `runtime/get_state` | Runtime 扩展 |
| `GetHistory` | `runtime/get_history` | Runtime 扩展 |
| `Shutdown` | `runtime/shutdown` | Runtime 扩展 |

### 3.3 事件模型

**事件通知方法名**：`$/event` → `session/update`（与 ACP 通知对齐）

**事件内容**：直接透传 ACP 的 `sessionUpdate` discriminated union，不再包裹一层自定义 `type` 字段：

```json
// 当前（翻译层）
{
  "method": "$/event",
  "params": { "type": "text", "payload": { "text": "..." } }
}

// 新（ACP 对齐）
{
  "method": "session/update",
  "params": {
    "sessionId": "sess_abc123",
    "update": {
      "sessionUpdate": "agent_message_chunk",
      "content": { "type": "text", "text": "..." }
    }
  }
}
```

**Runtime 自有事件**：turn_start / turn_end 等 runtime 概念，使用 `runtime/update` 通知 + `runtimeUpdate` discriminator（对应 ACP 的 `sessionUpdate` 设计模式）：

```json
{
  "method": "runtime/update",
  "params": {
    "sessionId": "sess_abc123",
    "update": {
      "runtimeUpdate": "turn_start"
    }
  }
}

{
  "method": "runtime/update",
  "params": {
    "sessionId": "sess_abc123",
    "update": {
      "runtimeUpdate": "turn_end",
      "stopReason": "end_turn"
    }
  }
}
```

**删除的事件类型**：`file_read`、`file_write`、`command`——这三类事件基于错误假设（shim 参与 fs/terminal 操作），应删除。

---

## 4. 新协议方法详解

### 4.1 `session/prompt`（对齐 ACP）

向 agent 发送 prompt，阻塞直到 turn 完成。

```json
// Request
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "session/prompt",
  "params": {
    "sessionId": "sess_abc123",
    "prompt": [
      { "type": "text", "text": "帮我重构这个函数" }
    ]
  }
}

// Response
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "stopReason": "end_turn"
  }
}
```

与 ACP `session/prompt` 完全对齐。

**关于 `sessionId` 的特别说明**

shim-rpc 中出现的 `sessionId` 是 **ACP sessionId**，由 ACP agent 在 `session/new` 时生成并返回，agentd 无法自行决定这个值。它与 OAR 的 agent ID（agentd 在创建 bundle 时指定的 `metadata.name`）是两个完全不同的标识符：

| 标识符 | 生成方 | 示例 | 用途 |
|--------|--------|------|------|
| OAR agent ID（`id`） | agentd（创建时指定） | `session-abc123` | runtime 内部标识，socket 路径、state dir 命名 |
| ACP sessionId（`sessionId`） | ACP agent（`session/new` 返回） | `sess_xyz789abc` | 所有 ACP 方法调用的 session 参数 |

agentd 首次连接 shim 时，通过 `runtime/get_state` 拿到已建立会话的 ACP sessionId，后续所有 `session/prompt`、`session/cancel` 调用均携带该值。

### 4.2 `session/cancel`（对齐 ACP）

取消当前 turn。shim 内部转发为 ACP `session/cancel` notification。

```json
// Request
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "session/cancel",
  "params": {
    "sessionId": "sess_abc123"
  }
}

// Response
{
  "jsonrpc": "2.0",
  "id": 2,
  "result": null
}
```

注意：ACP 的 `session/cancel` 是 notification（无 id，无响应）；shim-rpc 这里将其包装为 request/response，以便 agentd 确认取消指令已被 shim 接收。

### 4.3 `runtime/subscribe`（Runtime 扩展）

订阅事件流。立即返回空结果，后续事件通过 `session/update` 和 `runtime/update` notification 异步推送。

```json
// Request
{
  "jsonrpc": "2.0",
  "id": 3,
  "method": "runtime/subscribe",
  "params": {
    "sessionId": "sess_abc123"
  }
}

// Response
{
  "jsonrpc": "2.0",
  "id": 3,
  "result": null
}
```

### 4.4 `runtime/get_state`（Runtime 扩展）

查询 shim 和 agent 进程的当前状态。

```json
// Request
{
  "jsonrpc": "2.0",
  "id": 4,
  "method": "runtime/get_state",
  "params": null
}

// Response
{
  "jsonrpc": "2.0",
  "id": 4,
  "result": {
    "oarVersion": "1.0.0",
    "id": "session-abc123",
    "sessionId": "sess_xyz789abc",
    "status": "running",
    "pid": 12345,
    "bundle": "/var/lib/agentd/bundles/session-abc123",
    "annotations": {}
  }
}
```

响应字段直接对应 `state.json` 的内容。`id` 是 OAR agent ID，`sessionId` 是 ACP 会话 ID，二者的区别见 `session/prompt` 一节的说明。

### 4.5 `runtime/get_history`（Runtime 扩展）

读取事件日志，供 agentd 重连后补全断开期间错过的事件。

```json
// Request
{
  "jsonrpc": "2.0",
  "id": 5,
  "method": "runtime/get_history",
  "params": {
    "sessionId": "sess_abc123",
    "fromSeq": 0
  }
}

// Response
{
  "jsonrpc": "2.0",
  "id": 5,
  "result": {
    "entries": [
      {
        "seq": 1,
        "timestamp": "2026-04-07T10:30:00Z",
        "notification": {
          "method": "session/update",
          "params": { ... }
        }
      }
    ]
  }
}
```

历史条目存储的是完整的 notification（`method` + `params`），与实时推送的格式一致，agentd 可以用同一套处理逻辑消费。

### 4.6 `runtime/shutdown`（Runtime 扩展）

优雅关闭 agent 进程和 shim server。

```json
// Request
{
  "jsonrpc": "2.0",
  "id": 6,
  "method": "runtime/shutdown",
  "params": null
}

// Response（在进程退出前发出）
{
  "jsonrpc": "2.0",
  "id": 6,
  "result": null
}
```

---

## 5. 通知事件完整清单

订阅后，shim 通过两类 notification 推送事件：

### 5.1 `session/update`（透传 ACP）

直接透传 ACP 的 `session/update` notification，`params` 结构与 ACP 规范完全一致：

| sessionUpdate 值 | 来源 | 说明 |
|-----------------|------|------|
| `user_message_chunk` | ACP 透传 | 用户消息回显 |
| `agent_message_chunk` | ACP 透传 | Agent 回复文本块（流式） |
| `agent_thought_chunk` | ACP 透传 | Agent 推理块（流式） |
| `tool_call` | ACP 透传 | 工具调用创建 |
| `tool_call_update` | ACP 透传 | 工具调用状态/内容更新 |
| `plan` | ACP 透传 | 执行计划更新 |
| `available_commands_update` | ACP 透传 | 可用命令列表变更 |
| `config_option_update` | ACP 透传 | 配置选项变更 |
| `session_info_update` | ACP 透传 | 会话元数据更新 |
| `current_mode_update` | ACP 透传 | 当前模式变更（旧版） |

### 5.2 `runtime/update`（Runtime 扩展）

ACP 协议中没有的 runtime 级别事件：

| runtimeUpdate 值 | 说明 |
|-----------------|------|
| `turn_start` | Prompt turn 开始（agentd 收到 session/prompt 后） |
| `turn_end` | Prompt turn 结束，携带 `stopReason` |
| `agent_error` | Agent 进程异常或 ACP 通信失败 |

```json
// turn_start 示例
{
  "jsonrpc": "2.0",
  "method": "runtime/update",
  "params": {
    "sessionId": "sess_abc123",
    "update": {
      "runtimeUpdate": "turn_start"
    }
  }
}

// turn_end 示例
{
  "jsonrpc": "2.0",
  "method": "runtime/update",
  "params": {
    "sessionId": "sess_abc123",
    "update": {
      "runtimeUpdate": "turn_end",
      "stopReason": "end_turn"
    }
  }
}
```

---

## 6. 对 agent-shim.md 的更新点

以下内容需要在 `docs/design/runtime/agent-shim.md` 中更新：

### 6.1 设计原则更新

**删除**：
> "ACP 不穿透——agent-shim 内部使用 ACP 与 agent 通信，但 shim RPC 暴露的是翻译后的 typed events 和高层命令，消费方无需感知 ACP。"

**替换为**：
> "shim RPC 是 ACP 的超集——ACP 核心方法在 shim RPC 中透传，方法名和参数结构与 ACP 对齐；runtime 生命周期管理等扩展能力使用 `runtime/` 命名空间。agentd 作为消费方，可以直接使用 ACP 规范文档理解 `session/prompt`、`session/update` 等方法的语义。"

### 6.2 fs/terminal 能力立场（新增章节）

新增章节说明：

1. ACP `fs`/`terminal` 能力是 IDE/Editor 专属设计，用于读取 editor buffer 和在 IDE panel 展示终端输出
2. runtime 不是 IDE，不实现这两个 Client 能力
3. Agent 通过自身工具（MCP tools / 内置工具）直接操作磁盘和 shell，功能完整无缺失
4. `initialize` 握手时，`clientCapabilities` 中不携带 `fs` 和 `terminal` 字段

### 6.3 ACP → Typed Event 翻译示例更新

将翻译示例改为透传示例：

```
ACP 层（shim 内部）                  shim-rpc 对外推送
─────────────────────────────────    ─────────────────────────────────
ACP session/update:                  session/update notification（原样）
  sessionUpdate: agent_message_chunk   sessionUpdate: agent_message_chunk
  content: [{text: "..."}]             content: [{text: "..."}]

（session/prompt 调用开始时）        runtime/update notification
                                       runtimeUpdate: turn_start

ACP session/prompt response:         runtime/update notification
  stopReason: end_turn                 runtimeUpdate: turn_end
                                       stopReason: end_turn
```

---

## 7. 改进计划（分阶段）

### Phase 1：协议规范更新（文档先行）

- [ ] 更新 `docs/design/runtime/shim-rpc-spec.md`
  - 方法重命名（PascalCase → namespace/verb_noun）
  - 删除 `$/event` 包装层，改为 `session/update` + `runtime/update`
  - 删除 `file_read`/`file_write`/`command` 事件
  - 新增 `runtime/update` 定义
  - 更新历史记录格式（存储完整 notification）
  - 补充 sessionId 双 ID 说明（OAR agent ID vs ACP sessionId）
- [ ] 更新 `docs/design/runtime/agent-shim.md`
  - 更新设计原则（ACP 超集 vs 翻译层）
  - 新增 fs/terminal 能力立场章节
- [ ] 更新 `docs/design/runtime/runtime-spec.md`
  - State 中新增 `sessionId` 字段定义
  - 说明该字段在 `session/new` 成功后写入（`creating` → `created` 转换时）
  - 更新 state.json 示例，加入 `sessionId`
  - 说明崩溃恢复时 agentd 从 state.json 读取 `sessionId` 恢复正常调用

### Phase 2：代码实现更新

- [ ] `pkg/spec/state_types.go`：`State` 结构体新增 `SessionID string` 字段
- [ ] `pkg/runtime/runtime.go`：
  - `Create()` 的 `created` 状态写入加入 `SessionID: string(m.sessionID)`
  - `Kill()` 和后台 goroutine 的 `stopped` 状态写入同样保留 `SessionID`（从 Manager 字段取）
- [ ] `pkg/rpc/server.go`：方法名更新
- [ ] `pkg/events/translator.go`：改为透传模式（减少翻译代码）
- [ ] `pkg/events/types.go`：清理 `FileReadEvent`/`FileWriteEvent`/`CommandEvent`，新增 `RuntimeUpdate` 类型
- [ ] `pkg/runtime/client.go`：更新调用端方法名
- [ ] `pkg/runtime/client_test.go`：更新测试

---

## 8. 不变的部分

以下设计保持不变：

| 设计决策 | 理由 |
|---------|------|
| JSON-RPC 2.0 over Unix socket | 成熟方案，与 containerd-shim 对标 |
| 每个 session 一个 shim 进程 | 爆炸半径隔离 |
| Socket 路径约定 `/run/agentd/shim/<session-id>/agent-shim.sock` | agentd 重连逻辑依赖此约定 |
| `runtime/subscribe` 立即返回，事件异步推送 | 重连友好 |
| `runtime/get_history` 支持断点续传 | agentd 重启恢复 |
| shim 响应 agent 的权限请求（`session/request_permission`） | shim 仍是权限策略的执行者 |
