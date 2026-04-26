---
last_updated: 2026-04-18
---

# Orchestration Guide — 外部调用方如何使用 MASS

## 概述

MASS 是 Agent 基础设施层，不是编排引擎。它提供 Agent 生命周期管理、workspace 准备、进程监督和 agent 间通信原语。**编排逻辑不在 MASS 内部** — 由外部调用方（External Caller）决定。

```text
┌ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ┐
  External Caller（Orchestrator Agent / 用户程序 / 脚本）
│                                                                   │
   决策：创建哪些 agent、分配什么任务、何时路由、何时终止
│  工具：massctl CLI / ARI Go SDK                                   │
└ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ┬ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ┘
                          │  massctl / ARI
                          ▼
┌───────────────────────────────────────────────────────────────────┐
│                     MASS (agentd)                                 │
│  职责：realize workspace、bootstrap agent、route message          │
│  不做：编排、重试、条件分支、DAG、workflow                        │
└───────────────────────────────────────────────────────────────────┘
```

本文档描述外部调用方如何使用 MASS 提供的原语来构建多 Agent 协作工作流。

---

## 调用方模型

MASS 有两类调用方接口：

| 接口 | 适用场景 | 特点 |
|------|---------|------|
| `massctl` CLI | AI orchestrator agent、shell 脚本、手动操作 | 文本 / JSON 输出，适合 shell out |
| ARI Go SDK | Go 程序化集成 | 类型安全，controller-runtime 风格 |

两者能力等价，覆盖完整的 ARI 方法集。AI orchestrator agent 推荐直接通过 shell 调用 `massctl`。

---

## 基础工作流

任何多 Agent 工作流都遵循相同的 bootstrap 序列：

```text
1. 注册 Agent 定义（可复用模板）
2. 创建 Workspace（准备工作目录）
3. 创建 AgentRun（启动 agent 进程）
4. 发送 Prompt / 路由消息
5. 观测状态（轮询 / hooks）
6. 清理
```

### 第一步：注册 Agent 定义

Agent 定义是可复用的命名配置模板，描述 "如何启动一个 agent 进程"。

```bash
# 从 YAML 文件注册
massctl agent apply -f agents/claude.yaml
massctl agent apply -f agents/codex.yaml
```

Agent YAML 格式：

```yaml
meta
  name: claude
spec:
  command: bunx
  args:
    - "@agentclientprotocol/claude-agent-acp@v0.26.0"
  env:
    - name: ANTHROPIC_API_KEY
      value: "sk-..."
  startupTimeoutSeconds: 60
```

Agent 定义在 daemon 生命周期内持久化，跨 workspace 共享。一次注册，多次使用。

### 第二步：创建 Workspace

Workspace 准备 agent 的工作目录。创建是异步的，需要轮询等待就绪。

```bash
# 创建 workspace
massctl workspace create --name my-project --source-type local --source-path /path/to/code

# 轮询等待 ready
while true; do
  phase=$(massctl workspace get my-project -o json | jq -r '.status.phase')
  [ "$phase" = "ready" ] && break
  sleep 1
done
```

或使用 `massctl compose apply` 一次性创建 workspace + 所有 AgentRun：

```bash
massctl compose apply -f workspace-compose.yaml
```

或使用 `massctl compose run` 快速启动单个 Agent（当前目录作为 local workspace）：

```bash
massctl compose run -w my-project --agent claude
```

### 第三步：创建 AgentRun

AgentRun 是 Agent 定义的运行实例。创建是异步的，需要轮询等待 `idle`。

```bash
# 创建 AgentRun
massctl agentrun create \
  --workspace my-project \
  --name coder \
  --agent claude \
  --system-prompt "You are a coding agent..."

# 轮询等待 idle
while true; do
  state=$(massctl agentrun get coder -w my-project -o json | jq -r '.status.phase')
  [ "$state" = "idle" ] && break
  [ "$state" = "error" ] && { echo "bootstrap failed"; exit 1; }
  sleep 2
done
```

### 第四步：发送 Prompt

```bash
# 发送 prompt（异步，立即返回）
massctl agentrun prompt coder -w my-project --text "Implement function X"

# 轮询等待 agent 完成（idle = 完成当前 turn）
while true; do
  state=$(massctl agentrun get coder -w my-project -o json | jq -r '.status.phase')
  [ "$state" = "idle" ] && break
  [ "$state" = "error" ] && { echo "agent error"; exit 1; }
  sleep 2
done
```

### 第五步：Agent 间路由

```bash
# 从 coder 向 reviewer 发送消息
massctl workspace send \
  --name my-project \
  --from coder \
  --to reviewer \
  --text "[proposal] Here is the implementation..."
```

注意：`workspace/send` 要求目标 agent 处于 `idle` 状态。调用方需要在发送前确认目标就绪。

### 第六步：清理

```bash
massctl agentrun stop coder -w my-project
massctl agentrun stop reviewer -w my-project
massctl agentrun delete coder reviewer -w my-project
massctl workspace delete my-project
```

---

## 编排模式

以下是常见的多 Agent 编排模式。编排逻辑由外部调用方实现，MASS 只提供原语。

### Pipeline（流水线）

```text
task → Agent A → Agent B → Agent C → result
```

每个 agent 的输出作为下一个 agent 的输入。适用于分阶段处理。

```bash
# A: 生成方案
massctl agentrun prompt --workspace ws --name designer --text "Design auth system"
wait_for_idle ws designer

# A → B: 方案交给 reviewer
massctl workspace send --name ws --from designer --to reviewer \
  --text "[proposal] <designer's output>"
wait_for_idle ws reviewer

# B → C: 审批后交给 executor
massctl workspace send --name ws --from reviewer --to executor \
  --text "[approved] <reviewer's output>"
wait_for_idle ws executor
```

### Review Loop（审查循环）

```text
Designer ←→ Reviewer（最多 N 轮）
     ↓
  Executor
```

设计者和审查者之间迭代，直到审查通过或达到最大轮次。

```bash
MAX_ROUNDS=3
round=1

massctl agentrun prompt --workspace ws --name designer \
  --text "Design auth system, then send proposal to reviewer"
wait_for_idle ws designer

while [ $round -le $MAX_ROUNDS ]; do
  # Wait for reviewer to finish
  wait_for_idle ws reviewer

  # Check reviewer's response (vian agent-run events or agent output convention)
  # If approved, break
  # If rejected, designer will receive feedback via agentrun_send and auto-iterate
  
  round=$((round + 1))
done
```

在实际使用中，review loop 通常由 agent 自身的 system prompt 驱动 — agent 通过 `agentrun_send` MCP 工具互相发送消息，外部调用方只需观测最终状态。

### Fan-out / Fan-in（扇出/汇聚）

```text
         ┌→ Agent B ─┐
Task → A ├→ Agent C ─┤→ E → result
         └→ Agent D ─┘
```

并行分发到多个 agent，汇总结果。

```bash
# Fan-out: 并行发送任务
massctl workspace send --name ws --from coordinator --to researcher-1 \
  --text "[task] Research topic A"
massctl workspace send --name ws --from coordinator --to researcher-2 \
  --text "[task] Research topic B"
massctl workspace send --name ws --from coordinator --to researcher-3 \
  --text "[task] Research topic C"

# Fan-in: 等待所有 agent 完成
wait_for_idle ws researcher-1
wait_for_idle ws researcher-2
wait_for_idle ws researcher-3

# 汇总结果给 writer
massctl workspace send --name ws --from coordinator --to writer \
  --text "[aggregate] Combine research results..."
```

### Coordinator（协调者模式）

一个 coordinator agent 根据任务性质动态路由到不同的专家 agent。这是最灵活的模式，coordinator 本身就是一个 MASS 管理的 agent，通过 `agentrun_send` 工具与其他 agent 通信。

```text
User → Coordinator Agent
         ├→ agentrun_send → Expert A
         ├→ agentrun_send → Expert B
         └→ agentrun_send → Expert C
```

这个模式下，外部调用方只需 prompt coordinator，编排逻辑在 coordinator 的 system prompt 中：

```bash
massctl agentrun prompt --workspace ws --name coordinator \
  --text "Complete this feature request: <description>"
# coordinator 自主决定调用哪些 expert agents
wait_for_idle ws coordinator
```

---

## 状态观测

外部调用方需要知道 agent 何时完成工作。MASS 提供三种观测方式（按实现优先级排序）：

### 1. 轮询（当前已可用）

最简单的方式。调用方定期查询 agent 状态。

```bash
wait_for_idle() {
  local ws=$1 name=$2
  while true; do
    state=$(massctl agentrun get "$name" -w "$ws" -o json | jq -r '.status.phase')
    case "$state" in
      idle) return 0 ;;
      error) return 1 ;;
      stopped) return 2 ;;
    esac
    sleep 2
  done
}
```

适用场景：agent 运行时间较长（分钟级）、agent 数量较少。

### 2. Lifecycle Hooks（设计中）

Workspace 配置 lifecycle hooks，当 agent 状态变化时 agentd 自动执行注册的命令。

```yaml
# workspace-compose.yaml
spec:
  hooks:
    agentrun:
      onStateChange:
        - command: /path/to/notify.sh
          args: ["${MASS_AGENT_NAME}", "${MASS_AGENT_STATE}"]
```

详见 [Lifecycle Hooks 设计文档](./mass/lifecycle-hooks.md)。

### 3. ARI Watch（未来规划）

类似 K8s List-Watch 模式，ARI 层面提供事件流。

```bash
massctl agentrun watch --workspace ws
# NDJSON 事件流
# {"workspace":"ws","name":"coder","state":"running","previousState":"idle","ts":"..."}
# {"workspace":"ws","name":"coder","state":"idle","previousState":"running","ts":"..."}
```

当前未实现。优先级低于 lifecycle hooks。

---

## Orchestrator Agent 使用指南

当外部调用方是一个 AI agent（如 Claude Code）时，它通过 shell out 调用 `massctl` 来操作 MASS。
以下是给 orchestrator agent 的 system prompt 参考模板：

```markdown
## MASS 多 Agent 编排工具

你可以通过 massctl CLI 管理多个 AI agent 协作完成任务。

### 可用命令

| 命令 | 用途 |
|------|------|
| `massctl agent apply -f <file>` | 注册 agent 定义 |
| `massctl workspace create --name <ws> --source-type local --source-path <path>` | 创建 workspace |
| `massctl workspace get <ws> -o json` | 查询 workspace 状态 |
| `massctl compose apply -f <file>` | 一次性创建 workspace + agents |
| `massctl compose run -w <ws> --agent <def>` | 快速启动单个 agent（当前目录作为 workspace） |
| `massctl agentrun create --workspace <ws> --name <name> --agent <def>` | 启动 agent |
| `massctl agentrun get <name> -w <ws> -o json` | 查询 agent 状态 |
| `massctl agentrun get -w <ws> -o json` | 列出所有 agent |
| `massctl agentrun prompt <name> -w <ws> --text <msg>` | 发送 prompt |
| `massctl workspace send --name <ws> --from <a> --to <b> --text <msg>` | agent 间通信 |
| `massctl agentrun stop <name> -w <ws>` | 停止 agent |
| `massctl agentrun delete <name> -w <ws>` | 删除 agent |

### 工作流程

1. 确保 mass daemon 已启动（`mass daemon status`）
2. 注册需要的 agent 定义（或确认已存在：`massctl agent get`）
3. 创建 workspace，轮询直到 phase=ready
4. 创建 AgentRun，轮询直到 phase=idle
5. 发送 prompt 给 agent，轮询直到 agent 回到 idle
6. 根据结果决定下一步：发送给其他 agent、返回给用户、或迭代
7. 完成后清理 agent 和 workspace

### 关键约束

- workspace/send 要求目标 agent 处于 idle 状态
- agentrun/prompt 要求 agent 处于 idle 状态
- 创建操作是异步的，必须轮询等待就绪
- agent 间通过 agentrun_send MCP 工具自主通信，不需要 orchestrator 中转每条消息
```

---

## 最佳实践

### 1. 让 agent 自主协作，orchestrator 只做宏观控制

**推荐**：orchestrator 给 agent 注入协作协议（system prompt），让 agent 通过 `agentrun_send` 自主交互。orchestrator 只在关键节点介入（启动、超时、最终结果收集）。

**不推荐**：orchestrator 中转每条消息（A 的输出 → orchestrator → B 的输入）。这增加了延迟和复杂度。

### 2. 使用 workspace-compose 简化 bootstrap

```yaml
kind: workspace-compose
meta
  name: my-project
spec:
  source:
    type: local
    path: /path/to/code
  runs:
    - name: coder
      agent: claude
      systemPrompt: |
        You are a coding agent. When you finish, send results to reviewer via agentrun_send.
    - name: reviewer
      agent: codex
      systemPrompt: |
        You are a code reviewer. Send feedback or approval via agentrun_send.
```

```bash
massctl compose apply -f workspace-compose.yaml
```

### 3. 在 system prompt 中定义协作协议

Agent 间的消息约定（如 `[round-N-proposal]`、`[final-approved]`）应在 system prompt 中定义。这比外部强制路由更稳定。

### 4. 设置最大轮次防止无限循环

Review loop 等模式必须有退出条件。在 system prompt 中限制最大轮次：

```
最多进行 3 轮审查。第 3 轮后无论如何发送 [final-approved]。
```

### 5. 发送前检查目标状态

`workspace/send` 在目标非 idle 时会返回错误。orchestrator 应在发送前检查：

```bash
target_state=$(massctl agentrun get reviewer -w ws -o json | jq -r '.status.phase')
if [ "$target_state" != "idle" ]; then
  echo "reviewer is $target_state, waiting..."
  wait_for_idle ws reviewer
fi
massctl workspace send --name ws --from coder --to reviewer --text "..."
```

### 6. 使用 JSON 输出模式便于程序处理

`massctl` 支持 `-o json` 输出，适合 orchestrator agent 解析：

```bash
massctl agentrun get -w ws -o json | jq '.items[] | {name: .metadata.name, phase: .status.phase}'
```

---

## 不做什么

以下能力 **不在 MASS 范围内**，由外部调用方自行实现：

1. **编排引擎** — 不做 DAG、依赖图、条件分支、workflow DSL
2. **重试策略** — 不自动重试失败的 prompt 或 agent
3. **超时机制** — 不为 prompt turn 设置全局超时
4. **结果聚合** — 不汇总多个 agent 的输出
5. **任务队列** — 不为 agent 维护 task/inbox 队列（见 [communication.md](./workspace/communication.md) future work 部分）
6. **持久化工作流状态** — 不保存编排进度，调用方自行管理
