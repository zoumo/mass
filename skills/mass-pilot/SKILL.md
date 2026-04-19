---
name: mass-pilot
description: |
  通过 massctl CLI 使用 MASS（Multi-Agent Supervision System）管理 workspace、agent 生命周期，
  按任务复杂度编排多 agent 协作，解决实际问题。
  当用户提到 mass、massctl、agent 生命周期、workspace、多 agent 协作、或想启动/管理 AI agent 时触发。
version: 0.1.0
---

# MASS Usage Guide

通过 `massctl` 创建 workspace，启动 agent，解决问题，回收资源。

## 健康检查（每次操作前必须执行）

```bash
massctl daemon status
```

- `daemon: running` → 继续
- `daemon: not running` → **停止。告知用户 mass daemon 未运行，不要自行启动。**

> `--socket` 默认 `$HOME/.mass/mass.sock`，自定义时加 `--socket <path>`。以下示例省略该 flag。

### 查看可用 Agent

健康检查通过后，先确认当前可用的 agent 定义：

```bash
massctl agent get
```

虽然 daemon 启动时会内置 `claude`、`codex`、`gsd-pi`，但用户可能已自定义其他 agent。**始终以 `agent get` 的实际输出为准，不要假设只有内置 agent。**

## 核心概念

| 对象 | 含义 | 标识 |
|------|------|------|
| **Workspace** | agent 共享的工作目录（git clone / 本地路径 / 空目录） | `name` |
| **Agent** | 可复用的 agent 定义（command + args + env + disabled） | `name` |
| **AgentRun** | 绑定到 workspace 的运行中 agent 实例 | `(workspace, name)` |

## 内置 Agent

| 名称 | 特长 | 最佳角色 | 默认状态 |
|------|------|----------|----------|
| `claude` | 全能——设计、编码、规划、分析 | 规划者、主力 worker、协调者 | 启用 |
| `codex` | 严谨严格，善于发现边界问题 | 方案 reviewer、QA 关卡 | 启用 |
| `gsd-pi` | 长时间运行编码任务，按步骤逐项执行 | 执行者（用 `/gsd auto <计划>` 驱动） | **禁用** |

> `gsd-pi` 默认禁用（`disabled: true`）。启用方法：`massctl agent apply gsd-pi --disabled=false`

## 端到端流程

```
健康检查 → 创建 Workspace → 等待 Ready
  → 创建 AgentRun → 等待 Idle
  → Prompt / Agent 协作 → 拿到结果
  → Stop Agent → Delete Agent → Delete Workspace
```

---

## Part 1: Workspace 管理

### 创建 Workspace

```bash
# 挂载本地目录（mass 不会删除它）
massctl workspace create local --name my-ws --path /path/to/code

# 克隆 git 仓库（mass 管理该目录）
massctl workspace create git --name my-ws --url https://github.com/org/repo.git --ref main

# 空目录
massctl workspace create empty --name my-ws
```

创建是**异步**的，轮询等待 ready：

```bash
massctl workspace get my-ws
# 等待 status.phase == "ready"
# 如果 phase == "error" → 创建失败，检查 source 配置
```

### 查看 / 删除

```bash
massctl workspace get [NAME]     # 列出或查看 workspace
massctl workspace delete NAME    # 删除（需先清空所有 agentrun）
```

---

## Part 2: AgentRun 生命周期管理

AgentRun 属于某个 workspace，标识是 `(workspace, name)`。

### 状态机

```
creating ──┐
           ├──> idle ──> running ──> stopped
           |              │
    error <─┴─────────────┘
```

| 状态 | 含义 | 允许操作 |
|------|------|----------|
| `creating` | 正在启动 | 轮询等待 |
| `idle` | 就绪 | prompt, stop |
| `running` | 正在处理 prompt | cancel, stop |
| `stopped` | 已停止，可恢复 | restart, delete |
| `error` | 失败 | restart, delete |

### 创建

```bash
massctl agentrun create \
  -w my-ws --name worker --agent claude \
  --system-prompt "You are a senior engineer."
```

可选 flag：`--permissions approve_all|approve_reads|deny_all`、`--restart-policy try_reload|always_new`

启动是**异步**的：

```bash
massctl agentrun get worker -w my-ws   # 等待 state == "idle"
```

### 生命周期操作

```bash
massctl agentrun stop worker -w my-ws       # → stopped
massctl agentrun restart worker -w my-ws     # stopped/error → creating → idle
massctl agentrun cancel worker -w my-ws      # 取消当前 turn (running → idle)
massctl agentrun delete worker -w my-ws      # 删除记录（需 stopped/error）
```

### 查看

```bash
massctl agentrun get -w my-ws               # 列出 workspace 下所有 agentrun
massctl agentrun get -w my-ws --state idle   # 按状态过滤
massctl agentrun get worker -w my-ws         # 查看指定 agentrun
```

### Compose：声明式多 Agent 启动

```bash
massctl compose -f compose.yaml
```

自动完成：创建 workspace → 等待 ready → 创建所有 agent → 等待全部 idle。

格式见 [references/compose-format.md](references/compose-format.md)。

---

## Part 3: 与 Agent 交互

### 发送 Prompt

仅当 agent 状态为 `idle` 时可用。

```bash
# 发后即走
massctl agentrun prompt worker -w my-ws --text "Fix the auth bug"

# 等待结果（5 分钟超时）
massctl agentrun prompt worker -w my-ws --text "Fix the auth bug" --wait
```

### 交互式聊天

```bash
massctl agentrun chat worker -w my-ws
```

### Agent 间消息

同一 workspace 内的 agent 通过 workspace 消息通信：

```bash
massctl workspace send -w my-ws --from planner --to reviewer \
  --text "[round-1-proposal] Here is my design..."
```

Agent 内部有两个 MCP 工具：
- **`workspace_status`** — 获取 workspace 信息和所有 agentrun 列表
- **`workspace_send`** — 向同 workspace 另一个 agent 发消息（参数：targetAgent, message, needsReply）

### 消息协议（Tag 规范）

| Tag | 方向 | 含义 |
|-----|------|------|
| `[round-N-proposal]` | planner → reviewer | 提交方案 |
| `[round-N-revised-proposal]` | planner → reviewer | 修订后重提 |
| `[round-N-feedback]` | reviewer → planner | 审查问题列表 |
| `[final-approved]` | reviewer → planner | 方案通过 |
| `[execution-request]` | planner → executor | 下发执行 |
| `[clarification-needed]` | executor → planner | 执行卡住，需指引 |
| `[clarification-reply]` | planner → executor | 回答澄清 |
| `[execution-done]` | executor → planner | 执行完成 |

---

## Part 4: 按任务复杂度选择模式

### Level 1：简单任务 — 单 Agent

**场景：** Bug 修复、小功能、代码审查。步骤 < 3。

```bash
massctl workspace create local --name task-ws --path /path/to/code
# 等 ready
massctl agentrun create -w task-ws --name worker --agent claude \
  --system-prompt "You are a senior engineer working on this codebase."
# 等 idle
massctl agentrun prompt worker -w task-ws \
  --text "Fix the nil pointer in pkg/auth/handler.go:42" --wait
# 拿到结果后清理
massctl agentrun stop worker -w task-ws
massctl agentrun delete worker -w task-ws
massctl workspace delete task-ws
```

### Level 2：中等任务 — Worker + Reviewer

**场景：** 功能实现、设计决策、中等重构。3-5 步，需要 review。

**Agent：** `claude`（worker）+ `codex`（reviewer）

**模式：** Worker 出方案 → Reviewer 审查 → 最多 3 轮迭代达成一致 → Worker 执行。

详细 compose 配置见 [references/level2-compose.md](references/level2-compose.md)。

```bash
massctl compose -f compose.yaml
massctl agentrun prompt worker -w feature-ws \
  --text "Implement rate limiting for /api/v1/*. Max 100 req/min per API key."
# agent 通过 workspace_send 自主协作
# 完成后清理
```

### Level 3：复杂任务 — Planner + Reviewer + Executor

**场景：** 大规模重构、多文件改动、5+ 步骤。需要设计审查 + 专人执行。

**Agent：** `claude`（planner）+ `codex`（reviewer）+ `gsd-pi`（executor）

**模式：** Planner 设计 → Reviewer 审查 → 迭代 → Executor 用 `/gsd auto <方案>` 执行。

详细 compose 配置见 [references/level3-compose.md](references/level3-compose.md)。

```bash
massctl compose -f compose.yaml
massctl agentrun prompt planner -w refactor-ws \
  --text "Refactor auth system: extract middleware, add JWT, migrate to Redis, update handlers, add tests."
# planner→reviewer 多轮审查 → executor /gsd auto 执行 → planner 验证
# 完成后清理
```

### Level 4：高级 — 自定义编排

**场景：** 跨仓库、并行工作流、特殊协作模式。

**设计原则：**
1. 按能力拆分——每个 agent 职责清晰不重叠
2. `claude` 做协调者——理解上下文最好，路由工作给专家
3. `codex` 守所有审查关卡——它能发现别人遗漏的问题
4. `gsd-pi` 做所有执行——用 `/gsd auto <计划>` 驱动
5. 审查最多 3 轮——第 3 轮强制收敛，标记 RISK
6. 每条消息加 Tag——agent 据此判断下一步动作
7. 计划写文件——消息中传文件路径，不传全文
8. 一个 workspace 对应一个完整任务——不混杂无关工作

**常见模式：** 并行执行、流水线、多仓库协调。详见 [references/advanced-patterns.md](references/advanced-patterns.md)。

---

## Part 5: 错误处理

详细的错误诊断、恢复方案和决策树见 [references/error-handling.md](references/error-handling.md)。

### Agent 禁用诊断

如果 `agentrun/create` 返回 `agent <name> is disabled` 错误：

```bash
# 检查 agent 是否禁用
massctl agent get

# 启用指定 agent
massctl agent apply <name> --disabled=false
```

### 快速恢复

```bash
# 查看状态
massctl agentrun get -w my-ws

# error 状态 → restart
massctl agentrun restart <name> -w my-ws

# running 卡住 → cancel → 重新 prompt
massctl agentrun cancel <name> -w my-ws

# 全部重建
for agent in $(massctl agentrun get -w my-ws -o json | jq -r '.[].metadata.name'); do
  massctl agentrun stop $agent -w my-ws 2>/dev/null
  massctl agentrun delete $agent -w my-ws 2>/dev/null
done
massctl workspace delete my-ws
```

### 清理顺序

**stop agent → delete agent → delete workspace**，顺序不可颠倒。
