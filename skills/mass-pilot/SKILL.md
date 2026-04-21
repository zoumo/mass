---
name: mass-pilot
description: |
  用文件 Task 协议编排多 agent 协作。提供标准化的任务委派、角色 workflow、轮询脚本。
  Orchestrator 通过 Task 文件与 agent 交互，agent 间不直接通信。
  依赖 mass-guide skill 进行 workspace 和 agent 生命周期管理（massctl 操作）。
  当用户提到多 agent 协作、编排、task protocol、角色分工时触发。
version: 0.1.0
---

# MASS Pilot — Multi-Agent Collaboration

通过文件 Task 协议编排多 agent 协作。Orchestrator 创建 task 文件委派工作，agent 执行并回写结果，agent 间不直接通信。

> **前置依赖**：本 skill 依赖 **mass-guide** skill 进行 workspace 和 agent 生命周期管理。
> 使用前确保已通过 `massctl` 创建 workspace 和 agentrun。

## Task Protocol

Task 文件是 orchestrator 与 agent 之间的唯一通信方式。完整格式规范见 [references/task-protocol.md](references/task-protocol.md)。

## 内置角色 Workflow

| 角色 | 文件 | 职责 | 建议权限 |
|------|------|------|----------|
| **Planner** | [templates/planner.md](templates/planner.md) | 分析需求、制定方案、修复问题 | `approve_all` |
| **Reviewer** | [templates/reviewer.md](templates/reviewer.md) | 审查方案、评估风险 | `approve_reads` |
| **Worker** | [templates/worker.md](templates/worker.md) | 执行计划、诚实报告 | `approve_all` |
| **Verifier** | [templates/verifier.md](templates/verifier.md) | 独立验证 worker 报告 | `approve_reads` |

预设角色可自由组合，不要求全部使用。Orchestrator 也可定义自定义角色。

---

## 编排流程

```
Step 0: 确定角色 → 生成 orchestrator workflow → 用户确认
Step 1: 创建 Workspace + AgentRun + 初始化 workflow（用 mass-guide skill）
Step 2: 按 orchestrator workflow 循环执行（委派 task → prompt → 轮询 → 路由）
Step 3: 清理（用 mass-guide skill）
```

### Step 0: 确定角色分配 & 生成 Orchestrator Workflow

根据任务分析，决定：

1. **需要哪些角色** — 从内置角色中选择，或定义自定义角色
2. **每个角色用哪个 agent** — 如 planner 用 `claude`，reviewer 用 `codex`
3. **每个角色的 workflow** — 使用内置 template 或自定义
4. **编排流程** — 角色间的执行顺序、分支条件、重试策略

| 任务复杂度 | 建议角色 |
|-----------|---------|
| 需要方案设计 + 审查 | planner + reviewer |
| 需要设计 + 审查 + 执行 | planner + reviewer + worker |
| 需要独立验证 | 加 verifier |
| 可并行拆分 | planner + N workers |

确定后，**生成 orchestrator 自身的 workflow** 写入：

```
.mass/{workspace}/_orchestrator/workflow.md
```

Orchestrator workflow 应包含：
- 角色分配表（agent name → role → workflow template）
- 编排阶段（phase 1, 2, 3...）及每阶段的 task 描述
- 路由规则（每阶段 response.status → 下一步动作）
- 重试/修复策略
- 人类升级条件

**请用户确认 orchestrator workflow**。用户可修改角色分配、调整流程、增减阶段。确认后进入 Step 1。

### Step 1: 创建 Workspace + AgentRun + 初始化

用 **mass-guide** skill 创建 workspace 和所有 agentrun。然后为每个 agent 初始化工作目录：

```bash
mkdir -p .mass/{workspace}/{agent}/
cp skills/mass-pilot/templates/{role}.md .mass/{workspace}/{agent}/workflow.md
```

同时复制 orchestrator workflow：

```bash
mkdir -p .mass/{workspace}/_orchestrator/
# orchestrator workflow 已在 Step 0 生成
```

### Step 2: 按 Orchestrator Workflow 执行

按 `.mass/{workspace}/_orchestrator/workflow.md` 中定义的阶段循环执行。每个阶段包含：

**a) 创建 Task**

```bash
cat > .mass/{workspace}/{agent}/task-{NNNN}.json << 'EOF'
{
  "id": "task-{NNNN}",
  "assignee": "{agent}",
  "created_at": "{ISO8601_NOW}",
  "request": {
    "description": "{task_description}",
    "file_paths": ["{input_file_1}", "{input_file_2}"]
  }
}
EOF
```

**b) Prompt Agent**

```bash
massctl agentrun prompt {agent} -w {workspace} \
  --text "Your task is at: .mass/{workspace}/{agent}/task-{NNNN}.json
Read your workflow at .mass/{workspace}/{agent}/workflow.md, then read the task file.
Complete the task described in request.description. Read files listed in request.file_paths if present.
When done, set completed=true and add a response object with: status, description, file_paths (if you produced files), updated_at.
Task file update is ALWAYS your last write."
```

**c) 轮询等待**

```bash
skills/mass-pilot/scripts/poll-task.sh {workspace} {agent} .mass/{workspace}/{agent}/task-{NNNN}.json
```

详见 [references/poll-task.md](references/poll-task.md)。

| Exit Code | 含义 | 建议操作 |
|-----------|------|----------|
| 0 | completed==true | 读 response.status 做路由 |
| 1 | Agent idle, 重试用尽 | 手动检查 |
| 2 | Agent error/stopped | restart 或人类介入 |
| 3 | 超时 | 人类介入 |

**d) 路由决策**

```bash
status=$(jq -r '.response.status' .mass/{workspace}/{agent}/task-{NNNN}.json)
```

| response.status | 操作 |
|-----------------|------|
| `success` | 按 orchestrator workflow 进入下一阶段 |
| `failed` | 按 orchestrator workflow 的修复策略处理（如创建 fix task） |
| `needs_human` | 升级给人类 |

重复 a-d 直到 orchestrator workflow 中所有阶段完成。

### Step 3: 清理

用 **mass-guide** skill 清理：stop agent → delete agent → delete workspace。

---

## 设计原则

1. **按能力拆分** — 每个 agent 职责清晰不重叠
2. **Producer ≠ Verifier** — 生产者和验证者不能是同一个 agent
3. **审查最多 3 轮** — 第 3 轮强制收敛
4. **修复闭环** — 失败 → planner fix → 重试，最多 3 次 → 人类兜底
5. **一个 workspace 对应一个完整任务** — 不混杂无关工作
6. **Agent 间不直接通信** — 所有协调经 orchestrator 的 task 文件

## 常见编排模式

| 模式 | 角色 | 流程 |
|------|------|------|
| Plan-Review | planner + reviewer | plan → review → (fix loop) → execute |
| Plan-Review-Execute | planner + reviewer + worker | plan → review → execute → report |
| Full Pipeline | planner + reviewer + worker + verifier | plan → review → execute → verify |
| Parallel Workers | planner + N workers | plan → split → parallel execute → merge |

Orchestrator 不受限于以上模式，可根据实际需求自由组合。

---

## 错误处理

| 错误 | 原因 | 处理 |
|------|------|------|
| Task 文件不存在 | 路径错误或未创建 | 检查 `.mass/{workspace}/{agent}/` 目录 |
| Agent 完成但 task 未更新 | Agent 没按协议写回 response | 检查 prompt 是否包含 task protocol 指令 |
| poll-task.sh exit 1 | Agent idle 但 task 未完成，重试用尽 | 手动检查 agent 日志 |
| poll-task.sh exit 2 | Agent error/stopped | `massctl agentrun restart` → 等 idle → 重新 prompt |
| poll-task.sh exit 3 | 超时 | 人类介入 |
| response.status == needs_human | Agent 判断需要人类介入 | 读 response.description 了解原因 |

Agent 生命周期相关错误（创建、启动、停止）见 **mass-guide** skill。
