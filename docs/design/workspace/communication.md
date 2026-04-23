---
last_updated: 2026-04-17
---

# Workspace Communication — Agent 间通信设计

## 概述

本文档分为两部分：

1. **已实现（Message v0）** — 当前 `workspace/send` + envelope 投递约束的规范描述。
2. **设计提案：Task/Inbox** — 尚未实现的结构化任务委派和排队模型，标注为 **future work / design proposal**。

---

## 通信协议 — MCP 路由架构

Agent 间通信通过 workspace-mcp-server 实现。agentd 为每个 agent-run 进程注入一个 workspace-mcp 实例，作为 MCP stdio server 运行在 agent 的进程树中。

### MCP 工具

workspace-mcp-server 提供两个 MCP tool：

| MCP Tool | 对应 ARI 方法 | 功能 |
|----------|--------------|------|
| `workspace_send` | `workspace/send` | 向 workspace 内另一个 agent 发送消息 |
| `workspace_status` | `workspace/get` | 查询 workspace 成员与状态 |

### 注入方式

ProcessManager 在生成 `config.json` 时将 workspace-mcp 写入 `acpAgent.session.mcpServers`：

```json
{
  "type": "stdio",
  "name": "workspace",
  "command": "mass",
  "args": ["workspace-mcp", "--socket", "<mass unix socket path>", "--workspace", "<workspace name>", "--agent", "<agent name>"]
}
```

agent-run 启动时读取 `config.json`，fork/exec workspace-mcp 子进程。workspace-mcp 通过 `--socket` 指定的 Unix socket 连接回 mass 发起 ARI 调用。

### 消息路由数据流

以 3-agent 协作场景（claude-code / codex / gsd-pi）为例，claude-code 向 codex 发送 proposal 的完整路径：

```text
claude-code
  │  calls MCP tool: workspace_send(targetAgent="codex", message="[round-1-proposal] ...")
  ▼
workspace-mcp (claude-code 的 MCP server)
  │  ARI call: workspace/send(workspace="agentd-e2e", from="claude-code", to="codex", message=...)
  ▼
agentd
  │  lookup target agent-run → codex 的 agent-run
  │  append envelope: <workspace-message from="claude-code" reply-requested="true" />
  ▼
agent-run (codex)
  │  deliver as prompt to codex agent process
  ▼
codex (receives message, processes, replies via same path in reverse)
```

---

## 第一部分：已实现 — Message v0

### workspace/send

`workspace/send` 提供 workspace 内 agent 间的即发即忘消息路由。

**投递语义**：fire-and-forget。`delivered: true` 表示消息已分发到目标 agent 的 agent-run，不保证处理完成。

**投递约束**：目标 agent 必须处于 `idle` 状态才能接收消息。若目标 agent 不在 `idle` 状态（`creating`、`running`、`stopped`、`error`），调用返回错误。

**参数**：

| 字段 | 类型 | 必须 | 含义 |
|------|------|------|------|
| `workspace` | string | 是 | workspace 名称 |
| `from` | string | 是 | 发送者 agent name |
| `to` | string | 是 | 接收者 agent name |
| `message` | ContentBlock[] | 是 | 消息内容（ACP ContentBlock 数组 — text, image, audio 等） |
| `needsReply` | bool | 否 | 信封提示，表示期望接收方回复 |

**结果**：`{delivered: true}`

### 信封格式（Envelope）

mass 在投递消息时会在消息文本**尾部**追加 XML 格式的信封标签：

```
<消息正文>

<workspace-message from="<sender>" reply-to="<sender>" reply-requested="true" />
```

- 自闭合 XML 标签，属性值用双引号；
- 消息正文与 XML 标签间用 `\n\n` 分隔；
- `reply-to` 和 `reply-requested` 仅在 `needsReply=true` 时出现；
- `reply-to` 当前硬编码等于 `from`；
- 当前不支持 `threadId` 参数。

### 拒绝条件

| 条件 | 错误码 |
|------|-------|
| daemon 处于 recovery 模式 | `-32001` |
| 目标 agent 未找到 | `-32602` |
| 目标 agent 处于 `error` 状态 | `-32001` |
| 目标 agent 的 agent-run 未运行 | `-32001` |

---

## 第二部分：设计提案 — Task/Inbox（Future Work）

> **注意**：本部分描述尚未实现的功能。以下内容是设计提案，不是当前能力。
> 当前 ARI 不包含 `workspace/taskCreate` 等方法；workspace-mcp-server 不包含 `workspace_task_*` 工具；agentd 不维护 Inbox 队列或 PendingReply 记录。

### 动机

当前 `workspace/send` 要求目标 agent 处于 `idle` 状态，无法处理：

- 同时有多条消息发给同一个 agent
- task 结果回传时 creator 正在忙
- 需要明确状态追踪的委派工作

### 设计提案：Task 状态机

```
pending ──> working ──> completed
                    ├──> failed
                    └──> canceled
```

### 设计提案：Inbox 排队

每个 agent 维护一个 inbox 队列：
- `workspace/send` 和 task 投递先尝试直接投递（agent idle 时）
- 若 agent 忙（running 状态），消息入 inbox
- agent turn 结束回到 idle → agentd 自动从 inbox 取出下一条投递

### 设计提案：ARI 新增方法（未实现）

| 方法 | 功能 |
|------|------|
| `workspace/taskCreate` | 创建 task |
| `workspace/taskComplete` | 完成 task |
| `workspace/taskFail` | 标记 task 失败 |
| `workspace/taskCancel` | 取消 task |
| `workspace/taskList` | 查询 task |
| `workspace/taskGet` | 获取单个 task 详情 |

### 设计提案：workspace/send 参数扩展（未实现）

| 字段 | 类型 | 含义 |
|------|------|------|
| `threadId` | string | 消息线程标识，关联同一话题的多条消息 |

### 设计提案：MCP 工具（未实现）

workspace-mcp-server 拟新增以下工具：

- `workspace_task_create` — 创建 task 并委派给 assignee
- `workspace_task_complete` — 标记 task 完成并回传结果
- `workspace_task_fail` — 标记 task 失败
- `workspace_task_cancel` — 创建者取消 task
- `workspace_task_list` — 查询 task 列表

### 设计提案：agentrun/create 扩展（未实现）

拟为 `agentrun/create` 增加可选字段，用于 agent 能力发现：

| 字段 | 类型 | 含义 |
|------|------|------|
| `description` | string | agent 的角色描述（人类可读） |
| `capabilities` | []string | agent 的能力标签（机器可读） |

当前 `agentrun/create` 不包含这些字段。

---

## 不做什么（当前范围）

1. **编排引擎** — 不做 DAG、依赖图、条件分支。
2. **重试策略** — 不自动重试。
3. **超时机制** — 不为 task 设置全局超时。
4. **消息加密/ACL** — workspace 内 agent 完全信任，无权限控制。
5. **消息持久化** — inbox 暂时只在内存中（future work）。

---

## 附录：与 A2A 协议的对比

| A2A 概念 | MASS 对应 | 差异 |
|----------|---------|------|
| Task（8 个状态） | WorkspaceTask（5 个状态，提案） | MASS 不需要 INPUT_REQUIRED / AUTH_REQUIRED / REJECTED |
| contextId | threadId（提案） | MASS 的 thread 更轻量，只是字符串标记 |
| Agent Card | workspace_status（已实现） | MASS 在 workspace 范围内发现，不需要独立的 well-known URL |
| Push Notification | auto-reply + inbox delivery（提案） | MASS 使用 prompt 投递而非 HTTP webhook |
| Artifact | task result 纯文本（提案） | MASS 暂不需要结构化产物 |
