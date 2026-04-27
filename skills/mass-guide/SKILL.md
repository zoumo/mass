---
name: mass-guide
description: |
  通过 massctl CLI 使用 MASS 管理 workspace、agent 生命周期、task 委派。
  当用户提到 mass、massctl、agent 生命周期、workspace、task、或想启动/管理 AI agent 时触发。
  多 agent 协作编排见 mass-pilot skill。
version: 0.3.0
---

# MASS Usage Guide

通过 `massctl` 创建 workspace，启动 agent，管理生命周期。

## 健康检查（每次操作前必须执行）

```bash
mass daemon status
```

- `daemon: running (pid: N)` → 继续
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
| **Task** | 结构化任务委派（request → agent → response） | `(workspace, agent, task-id)` |

## 内置 Agent

| 名称 | 特长 | 最佳角色 | 默认状态 |
|------|------|----------|----------|
| `claude` | 全能——设计、编码、规划、分析 | 规划者、主力 worker、协调者 | 启用 |
| `codex` | 严谨严格，善于发现边界问题 | 方案 reviewer、QA 关卡 | 启用 |
| `gsd-pi` | 长时间运行编码任务，按步骤逐项执行 | 执行者（用 `/gsd auto <计划>` 驱动） | **禁用** |

> `gsd-pi` 默认禁用（`disabled: true`）。启用方法：`massctl agent apply gsd-pi --disabled=false`

## 端到端流程

```
健康检查 → compose apply → 所有 agent idle
  → task do → 轮询 done → 读取 reason
  → compose down (stop + delete agents + delete workspace)
```

> 多 agent 协作编排见 [mass-pilot](../mass-pilot/SKILL.md) skill。

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
massctl workspace delete NAME --force   # 自动 stop + delete 所有 agentrun 后删除
```

### Workspace Send：Agent 间消息传递

```bash
massctl workspace send -w my-ws --from agent-a --to agent-b --text "task complete"
```

向 workspace 内其他 agent 发送消息，用于 agent 间协作。

---

## Part 2: Agent 启动（推荐：Compose）

### Compose Apply：声明式多 Agent 启动（推荐）

```bash
massctl compose apply -f compose.yaml

# 覆盖 compose 文件中的 workspace 名称
massctl compose apply -f compose.yaml --workspace my-custom-ws
```

自动完成：创建 workspace → 等待 ready → 创建所有 agent → 等待全部 idle → 打印每个 agent 的 socket 路径。

**这是启动 agent 的推荐方式。** 单次命令替代手动创建 workspace、逐个创建 agentrun、分别轮询的全部步骤。

Compose 文件格式见 [../mass-pipeline/references/compose-schema.md](../mass-pipeline/references/compose-schema.md)。

```yaml
# compose.yaml 最小示例
kind: workspace-compose
metadata:
  name: my-ws
spec:
  source:
    type: local
    path: /path/to/code
  runs:
    - name: worker
      agent: claude
      systemPrompt: "You are a senior engineer."
```

### Compose Run：快速启动单个 Agent

```bash
# 使用当前目录，快速启动一个 agent
massctl compose run -w my-ws --agent claude

# 指定名称和 system prompt
massctl compose run -w my-ws --agent claude --name reviewer \
  --system-prompt "You are a code reviewer."

# 不等待 agent idle，立即返回
massctl compose run -w my-ws --agent claude --no-wait

# 使用 workflow 文件
massctl compose run -w my-ws --agent claude --workflow workflow.yaml
```

workspace 已存在且 ready 时自动复用；否则以当前目录创建新的 local workspace。

---

## Part 3: AgentRun 手动管理（备用）

手动管理适用于需要对单个 agentrun 进行细粒度控制的场景。批量启动优先用 `compose apply`。

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
| `idle` | 就绪 | prompt, task do, stop |
| `running` | 正在处理 prompt | cancel, stop |
| `stopped` | 已停止，可恢复 | restart, delete |
| `error` | 失败 | restart, delete |

### 创建

```bash
massctl agentrun create \
  -w my-ws --name worker --agent claude \
  --system-prompt "You are a senior engineer."
```

可选 flag：
- `--permissions approve_all|approve_reads|deny_all`
- `--wait` — 等待 agentrun 进入 idle 状态（省去手动轮询）
- `--workflow <path>` — 工作流文件路径

启动是**异步**的：

```bash
massctl agentrun get worker -w my-ws   # 轮询直到 status.phase == "idle"
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
massctl agentrun get -w my-ws --phase idle   # 按状态过滤
massctl agentrun get worker -w my-ws         # 查看指定 agentrun
```

---

## Part 4: 与 Agent 交互

### 发送 Prompt

仅当 agent 状态为 `idle` 时可用。

```bash
# 发后即走
massctl agentrun prompt worker -w my-ws --text "Fix the auth bug"

# 等待结果（5 分钟超时）
massctl agentrun prompt worker -w my-ws --text "Fix the auth bug" --wait
```

### Task 生命周期

Task 是结构化的任务委派方式，自动处理 prompt、文件传递和结果收集。

#### Task 状态机

```
[do] → agent running → [agent calls task done] → done=true
                                                    │
                                          reason + updatedAt 填入
```

agent 调用 `massctl agentrun task done` 完成 task，`done` 字段由 Go 代码写为 bool `true`，类型安全。

#### 创建 Task（自动 prompt agent）

```bash
massctl agentrun task do -w {workspace} --run {agent} \
  --prompt "{task_prompt}" \
  --files {input_file_1} --files {input_file_2}
```

| Flag | Required | Description |
|------|----------|-------------|
| `-w, --workspace` | yes | Workspace name |
| `--run` | yes | AgentRun name |
| `--prompt` | yes | Task prompt / description |
| `--files` | no | Input file paths（可多次指定） |

`task do` 会：
1. 检查 agent 是否 idle（否则返回错误）
2. 创建 task 文件（系统生成 ID）
3. 自动 prompt agent（内置 task protocol）
4. Agent 状态 idle → running

返回包含 `task.id` 和 `taskPath`，用于后续查询。

#### Task 完成（由 Agent 调用）

Agent 完成工作后调用：

```bash
massctl agentrun task done \
  --task-file {task-path} \
  --reason {reason} \
  --response '{"description":"...","filePaths":["..."]}'
```

| Flag | Required | Description |
|------|----------|-------------|
| `--task-file` | yes | task JSON 文件路径（在 task do 的 request prompt 里） |
| `--reason` | yes | 结果描述字符串，如 success / failed / needs_human |
| `--response` | yes | JSON object，至少含 `description`，可含 `filePaths` |

CLI 负责写入：`done=true`（bool）、`reason`、`updatedAt=now()`，原子替换文件。

#### 查询 Task 状态

```bash
massctl agentrun task get -w {workspace} --run {agent} {task-id} [-o json|table]
```

Task JSON 结构（`AgentTask`）：

```json
{
  "id": "task-0001",
  "assignee": "worker",
  "attempt": 1,
  "createdAt": "2026-04-27T00:00:00Z",
  "request": {
    "prompt": "..."
  },
  "done": false,            // ← bool，由 task done CLI 写入
  "reason": "",             // ← agent 设置的结果字符串（done=true 后有值）
  "updatedAt": null,        // ← task done 时自动设为当前时间
  "response": { ... }       // ← 额外 JSON（description、filePaths 等）
}
```

轮询示例：

```bash
# 轮询直到 done == true
while true; do
  done=$(cat /path/to/task.json | jq -r '.done')
  [[ "$done" == "true" ]] && break
  sleep 5
done
reason=$(cat /path/to/task.json | jq -r '.reason')
echo "Task finished with reason: $reason"
```

#### 列出 Task

```bash
massctl agentrun task get -w {workspace} --run {agent}
```

#### 重试 Task

```bash
massctl agentrun task retry -w {workspace} --run {agent} {task-id}
```

增加 `attempt` 计数，清除旧 response / reason / done，自动重新 prompt agent。

> 多 agent 协作编排（task-based workflow）见 [mass-pilot](../mass-pilot/SKILL.md) skill。

### 交互式聊天

```bash
massctl agentrun chat worker -w my-ws
```

### 端到端示例（compose + task）

```bash
# 1. 启动
massctl compose apply -f compose.yaml   # workspace + agents，一次搞定

# 2. 委派任务
massctl agentrun task do -w my-ws --run worker \
  --prompt "Fix nil pointer in pkg/auth/handler.go:42"
# → 返回 task-id 和 taskPath

# 3. 轮询完成（或用 poll-task.sh）
# done=true → 读取 reason

# 4. 清理
massctl agentrun stop worker -w my-ws
massctl agentrun delete worker -w my-ws
massctl workspace delete my-ws
```

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
