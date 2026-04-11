# Workspace Communication — Agent 间通信设计

## 概述

本文档定义 workspace 内 agent 间的通信模型。
OAR 提供两种并存的通信原语：**消息（Message）** 和 **任务（Task）**。

- **消息** 是即发即忘的单向通知，适用于通知、提醒、简单问答。
- **任务** 是带状态追踪的委派工作单元，适用于需要结果回传的协作场景。

两者共享同一个投递通道（`workspace/send`），但 Task 在其上增加了
状态机、自动回传和生命周期管理。

### 设计原则

1. **agentd 只做路由和状态管理** — 不做编排、不做重试策略、不做依赖图。
2. **Agent 自主决策** — agent（LLM）通过 MCP 工具自行决定何时委派、委派给谁。
3. **外部目标不归 OAR 管** — orchestrator 的顶层任务追踪不在本设计范围内。
4. **简单优先** — 最小概念集合，不引入不需要的抽象。

### OCI 对标

| OCI 概念 | OAR 对应 |
|----------|---------|
| Container 间 IPC（shared PID/network namespace） | Workspace Message（`workspace/send`） |
| Pod 内 sidecar → main container 的健康检查/信号 | Workspace Task（带状态追踪的委派） |
| kubelet 不管 Pod 内 container 如何通信 | agentd 不管 agent 间协作策略 |

---

## 第一部分：消息（Message）

### 现有能力

`workspace/send` 提供 agent 间的即发即忘消息路由：

```
Agent A → workspace_send(to=B, message="...") → agentd → prompt 投递给 B
```

投递条件：目标 agent 处于 `idle` 状态。
投递语义：fire-and-forget，`delivered: true` 表示已分发到 shim，不保证处理。

### 增强：信封元数据

在现有 `workspace/send` 基础上，信封头增加以下可选字段：

| 字段 | 类型 | 含义 |
|------|------|------|
| `from` | string | 发送者 agent name（已有） |
| `needsReply` | bool | 期望接收方回复（已有） |
| `replyTo` | string | 回复目标 agent name（已有，默认等于 from） |
| `threadId` | string | 消息线程标识，关联同一话题的多条消息（新增） |

**信封格式**：

```
[workspace-message from=codex reply-to=codex reply-requested=true thread=review-20260412]

<消息正文>
```

### 增强：Auto-Reply 保底机制

当 `needsReply=true` 时，agentd 记录一个 `pendingReply` 记录：

```go
type PendingReply struct {
    ThreadID  string
    From      string // 原始发送者
    To        string // 消息接收者
    CreatedAt time.Time
}
```

**消解条件**（满足任一即消解）：
1. 接收方在处理 turn 的过程中调用了 `workspace_send(to=<from>)` — agent 主动回复
2. 接收方的 turn 结束（`running → idle` 状态变更） — 系统自动转发 turn 输出

**Auto-reply 行为**：
当条件 2 触发时，agentd 将接收方的最后一次 turn 输出（text event 汇总）
作为消息自动转发给原始发送者。信封标记：

```
[workspace-message from=claude-code reply-to=claude-code auto-reply=true thread=review-20260412]

<turn 输出文本>
```

**设计要点**：
- auto-reply 是保底机制，不是首选路径。理想情况下 agent 应主动调用 `workspace_send` 回复。
- auto-reply 只在 `needsReply=true` 且 agent 未在 turn 中主动回复时触发。
- auto-reply 的内容是 turn 输出的文本部分，不含工具调用详情。

---

## 第二部分：任务（Task）

### 动机

消息是单向的通知。当 agent A 需要 agent B 完成一项工作并返回结果时，
纯消息模型要求 A 发完消息后等待 B 主动回复——但 B 可能忘记回复、或不知道该回复。

Task 提供了一个轻量的工作委派模型：
- 有明确的状态机（pending → working → completed/failed）
- 结果自动回传给委派者
- 支持 task 内的多轮消息（讨论）

### Task 状态机

```
pending ──> working ──> completed
                    ├──> failed
                    └──> canceled
```

| 状态 | 含义 |
|------|------|
| `pending` | task 已创建，等待 assignee 接收 |
| `working` | assignee 已接收，正在处理 |
| `completed` | assignee 完成，结果已回传 |
| `failed` | assignee 报告失败 |
| `canceled` | 创建者取消 |

状态转换规则：
- `pending → working`：agentd 将 task 投递给 assignee 时自动转换
- `working → completed`：assignee 调用 `workspace_task_complete`
- `working → failed`：assignee 调用 `workspace_task_fail`，或 assignee 的 turn 异常结束
- `pending → canceled` / `working → canceled`：创建者调用 `workspace_task_cancel`
- 终态（completed/failed/canceled）不可回退

### Task 数据模型

```go
type WorkspaceTask struct {
    ID          string            // agentd 生成的唯一标识
    Workspace   string            // 所属 workspace
    Creator     string            // 创建者 agent name
    Assignee    string            // 被委派者 agent name
    Description string            // 任务描述（投递给 assignee 的 prompt）
    Status      TaskStatus        // 当前状态
    Result      string            // 完成时的结果文本
    Error       string            // 失败时的错误信息
    ParentID    string            // 可选：父 task ID，用于追踪 task 链
    CreatedAt   time.Time
    UpdatedAt   time.Time
}
```

### MCP 工具（暴露给 Agent）

workspace-mcp-server 新增以下工具：

#### `workspace_task_create`

创建一个 task 并投递给 assignee。

```json
{
  "type": "object",
  "properties": {
    "assignee": {
      "type": "string",
      "description": "Name of the agent to assign this task to"
    },
    "description": {
      "type": "string",
      "description": "Task description - what the assignee should do"
    },
    "parentTaskId": {
      "type": "string",
      "description": "Optional parent task ID for tracking task chains"
    }
  },
  "required": ["assignee", "description"]
}
```

**行为**：
1. agentd 创建 task 记录，状态 `pending`
2. 检查 assignee 是否 idle
3. 如果 idle：转为 `working`，投递 prompt 给 assignee
4. 如果非 idle：保持 `pending`，加入 assignee 的待办队列（见 Inbox 章节）
5. 返回 `{taskId, status}` 给创建者

**投递给 assignee 的 prompt 格式**：

```
[workspace-task id=task-001 from=codex status=working]

<description 内容>

---
When you finish this task, call workspace_task_complete with your result.
If you cannot complete it, call workspace_task_fail with the reason.
```

#### `workspace_task_complete`

标记当前 task 为完成，并将结果回传给创建者。

```json
{
  "type": "object",
  "properties": {
    "taskId": {
      "type": "string",
      "description": "The task ID to complete"
    },
    "result": {
      "type": "string",
      "description": "Task result - what was accomplished"
    }
  },
  "required": ["taskId", "result"]
}
```

**行为**：
1. 验证调用者是 task 的 assignee
2. 更新 task 状态：`working → completed`
3. 将结果作为消息自动投递给 creator：

```
[workspace-task-result id=task-001 from=claude-code status=completed]

<result 内容>
```

4. 如果 creator 当前 idle，立即投递；否则加入 creator 的待办队列

#### `workspace_task_fail`

标记 task 失败。

```json
{
  "type": "object",
  "properties": {
    "taskId": {
      "type": "string",
      "description": "The task ID that failed"
    },
    "error": {
      "type": "string",
      "description": "Reason for failure"
    }
  },
  "required": ["taskId", "error"]
}
```

**行为**：同 `workspace_task_complete`，但状态为 `failed`，信封标记 `status=failed`。

#### `workspace_task_cancel`

创建者取消一个 task。

```json
{
  "type": "object",
  "properties": {
    "taskId": {
      "type": "string",
      "description": "The task ID to cancel"
    }
  },
  "required": ["taskId"]
}
```

**行为**：
1. 验证调用者是 task 的 creator
2. 如果 assignee 正在处理（state=working），调用 `agentrun/cancel` 取消其 turn
3. 更新状态：`canceled`

#### `workspace_task_list`

查询当前 workspace 的 task 列表。

```json
{
  "type": "object",
  "properties": {
    "status": {
      "type": "string",
      "description": "Filter by status: pending, working, completed, failed, canceled"
    },
    "assignee": {
      "type": "string",
      "description": "Filter by assignee agent name"
    },
    "creator": {
      "type": "string",
      "description": "Filter by creator agent name"
    }
  }
}
```

### ARI 方法

在 ARI 层面，增加以下 JSON-RPC 方法供 workspace-mcp-server 调用：

| 方法 | 功能 |
|------|------|
| `workspace/taskCreate` | 创建 task |
| `workspace/taskComplete` | 完成 task |
| `workspace/taskFail` | 标记 task 失败 |
| `workspace/taskCancel` | 取消 task |
| `workspace/taskList` | 查询 task |
| `workspace/taskGet` | 获取单个 task 详情 |

---

## 第三部分：Inbox（待办队列）

### 问题

当前 `workspace/send` 要求目标 agent 处于 `idle` 状态才能投递。
这导致：
- 同时有多条消息发给同一个 agent → 只有第一条成功
- task 结果回传时 creator 正在忙 → 丢失

### 设计

每个 agent 维护一个 **inbox**（内存队列 + 可选持久化）。

```go
type Inbox struct {
    Items []InboxItem
}

type InboxItem struct {
    Type      string    // "message" | "task" | "task-result"
    Priority  int       // 0=normal, 1=high (task results)
    Payload   string    // 投递给 agent 的完整 prompt（含信封头）
    CreatedAt time.Time
    TaskID    string    // 关联的 task ID（如有）
}
```

**投递策略**：
1. `workspace/send` 和 `workspace/taskCreate` 先尝试直接投递（agent idle 时）
2. 如果 agent busy（running 状态）→ 消息入 inbox
3. agent turn 结束回到 idle → agentd 自动从 inbox 取出下一条投递
4. 取出顺序：FIFO，task-result 优先于普通 message

**Inbox 消费触发点**：
在 agentd 的 `runtime/stateChange` notification handler 中，
当 agent 从 `running → idle` 时，检查其 inbox 是否非空：

```go
// In notification handler
case events.MethodRuntimeStateChange:
    p := parseStateChange(params)
    if p.Status == spec.StatusIdle {
        // Check inbox
        if item := inbox.Dequeue(workspace, name); item != nil {
            // Dispatch next item as prompt
            dispatchPrompt(workspace, name, item.Payload)
        }
    }
```

**上限与反压**：
- Inbox 上限：每个 agent 最多 100 条待处理消息
- 超过上限：返回错误 `inbox full`
- 不做重试：发送方 agent 收到错误后自行决策

---

## 第四部分：Agent 能力发现

### 问题

agent 需要知道 workspace 里有哪些可用的协作者，以及它们各自擅长什么。
如果在 system prompt 中硬编码成员名单：
- agent 挂了 → 调用者不知道
- 新增 agent → 调用者不知道

### 设计

#### Agent 描述字段

在 `agent/create` 的参数中增加可选字段：

| 字段 | 类型 | 含义 |
|------|------|------|
| `description` | string | agent 的角色描述（人类可读） |
| `capabilities` | []string | agent 的能力标签（机器可读） |

这些字段存储在 agent 元数据中，通过 `workspace/status` 返回。

#### 增强 `workspace/status` 返回

```json
{
  "name": "my-project",
  "phase": "ready",
  "path": "/home/user/project",
  "members": [
    {
      "workspace": "my-project",
      "name": "codex",
      "runtimeClass": "codex",
      "state": "idle",
      "description": "Deep code review expert",
      "capabilities": ["code-review", "architecture-analysis"]
    },
    {
      "workspace": "my-project",
      "name": "claude-code",
      "runtimeClass": "claude",
      "state": "running",
      "description": "Code verification and review",
      "capabilities": ["code-review", "verification"]
    }
  ]
}
```

#### System Prompt 指导

agent 的 system prompt 不列出具体成员，而是指导 agent 如何发现：

```
你可以使用 workspace_status 工具查看当前 workspace 中的其他 agent。
每个 agent 有 description 和 capabilities 字段描述其能力。
当你需要协作时，先查看可用 agent，根据能力选择合适的目标。
如果目标 agent 不可用（state 不是 idle），可以通过 task 委派（会自动排队）。
```

---

## 第五部分：完整协作流程示例

以验证手册中的 code review 场景为例：

### 准备

```bash
# Orchestrator 创建 workspace 和三个 agent
agentdctl workspace create local my-project --path /path/to/project
agentdctl agentrun create --workspace my-project --name codex --runtime-class codex \
  --description "Deep code review expert" \
  --capabilities "code-review,architecture-analysis"
agentdctl agentrun create --workspace my-project --name claude-code --runtime-class claude \
  --description "Code verification and review" \
  --capabilities "code-review,verification"
agentdctl agentrun create --workspace my-project --name gsd-pi --runtime-class gsd-pi \
  --description "Code fix implementation" \
  --capabilities "code-execution,fix-implementation"
```

### 触发（唯一的人工操作）

```bash
agentdctl agentrun prompt my-project/codex --text \
  "请审查 docs/design/ 下的设计文档与代码的一致性。
   完成后，创建 task 委派给具备 verification 能力的 agent 进行复查。
   复查完成后，创建 task 委派给具备 fix-implementation 能力的 agent 执行修复。"
```

### 自动流转

```
1. codex 收到 prompt → 开始审查 → 完成
2. codex 调用 workspace_status → 发现 claude-code 有 verification 能力
3. codex 调用 workspace_task_create(assignee="claude-code", description="请复查...")
   → agentd 创建 task-001，投递给 claude-code
4. codex 的 turn 结束 → idle（等待 task 结果）

5. claude-code 收到 [workspace-task id=task-001 ...] prompt → 开始复查
6. claude-code 完成 → 调用 workspace_task_complete(taskId="task-001", result="...")
   → agentd 更新 task-001 为 completed
   → 结果自动投递给 codex（如果 idle 直接投递，否则入 inbox）

7. codex 收到 [workspace-task-result id=task-001 status=completed] → 继续处理
8. codex 调用 workspace_task_create(assignee="gsd-pi", description="请执行修复...", parentTaskId="task-001")
   → task-002 投递给 gsd-pi

9. gsd-pi 收到 task → 执行修复 → workspace_task_complete
   → 结果回传 codex
10. codex 收到最终结果 → 整个流程完成
```

### 多轮讨论

如果 codex 和 claude-code 需要多轮讨论（如审查意见分歧）：

```
1. codex 创建 task-001 给 claude-code
2. claude-code 处理中需要追问 → 调用 workspace_send(to=codex, threadId=task-001, needsReply=true, message="这个问题我不太确定...")
   → 消息入 codex 的 inbox（因为 codex 还在等 task 结果，但此时是 idle 的）
3. codex 收到消息 → 回复 → workspace_send(to=claude-code, ...)
   → claude-code 正在 running → 消息入 inbox
4. claude-code 的 turn 结束回到 idle → 从 inbox 取出 codex 的回复 → 继续处理
5. 最终 claude-code 调用 workspace_task_complete 结束 task
```

---

## 第六部分：ARI 变更汇总

### 新增 ARI 方法

| 方法 | 功能 | Params |
|------|------|--------|
| `workspace/taskCreate` | 创建 task | `{workspace, creator, assignee, description, parentTaskId?}` |
| `workspace/taskComplete` | 完成 task | `{workspace, taskId, agent, result}` |
| `workspace/taskFail` | task 失败 | `{workspace, taskId, agent, error}` |
| `workspace/taskCancel` | 取消 task | `{workspace, taskId, agent}` |
| `workspace/taskList` | 列出 task | `{workspace, status?, assignee?, creator?}` |
| `workspace/taskGet` | 获取 task | `{workspace, taskId}` |

### 修改 ARI 方法

| 方法 | 变更 |
|------|------|
| `workspace/send` | 增加 `threadId` 可选参数 |
| `workspace/status` | members 返回增加 `description`, `capabilities` 字段 |
| `agent/create` | 增加 `description`, `capabilities` 可选参数 |

### 新增 MCP 工具

| 工具 | 功能 |
|------|------|
| `workspace_task_create` | 创建 task |
| `workspace_task_complete` | 完成 task |
| `workspace_task_fail` | task 失败 |
| `workspace_task_cancel` | 取消 task |
| `workspace_task_list` | 查询 task |

### agentd 内部变更

| 组件 | 变更 |
|------|------|
| Metadata Store | 增加 `workspace_tasks` 表 |
| Notification Handler | idle 时检查 inbox 并自动分发 |
| Process Manager | 维护每个 agent 的 inbox 队列 |

---

## 第七部分：不做什么

以下明确不在本设计范围内：

1. **编排引擎** — 不做 DAG、依赖图、条件分支。编排由 orchestrator 或 agent 自己决定。
2. **重试策略** — task 失败后不自动重试。创建者 agent 自行决定是否重新委派。
3. **超时机制** — 不为 task 设置全局超时。如果需要，可由创建者 agent 通过 `workspace_task_cancel` 主动取消。
4. **能力匹配路由** — 不做 `capability="X"` 的自动匹配分发。agent 自行通过 `workspace_status` 发现并选择。
5. **外部目标追踪** — orchestrator 的顶层任务生命周期不归 agentd 管。
6. **消息持久化** — inbox 暂时只在内存中，daemon 重启后清空。持久化是后续优化。
7. **消息加密/ACL** — workspace 内 agent 完全信任，无权限控制。

---

## 附录：与 A2A 协议的对比

| A2A 概念 | OAR 对应 | 差异 |
|----------|---------|------|
| Task（8 个状态） | WorkspaceTask（5 个状态） | OAR 不需要 INPUT_REQUIRED / AUTH_REQUIRED / REJECTED |
| contextId | threadId | OAR 的 thread 更轻量，只是字符串标记 |
| Agent Card | workspace_status + capabilities | OAR 在 workspace 范围内发现，不需要独立的 well-known URL |
| Push Notification | auto-reply + inbox delivery | OAR 使用 prompt 投递而非 HTTP webhook |
| Artifact | task result（纯文本） | OAR 暂不需要结构化产物 |
| Streaming | 无 | OAR task 是同步委派，不需要流式更新 |

OAR 的 Task 设计从 A2A 获得启发，但有意保持更轻量：
- 不需要 HTTP 传输层（workspace 内 Unix socket 通信）
- 不需要 Agent Card 发现协议（workspace_status 即可）
- 不需要结构化产物（文本足够，代码修改通过 workspace 文件系统共享）
