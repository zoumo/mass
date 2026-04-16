# AgentRun Lifecycle Hooks — 设计提案

## 状态

**Design Proposal** — 尚未实现。

## 动机

外部调用方（orchestrator agent 或用户程序）需要知道 agent 何时完成工作。
当前唯一的方式是轮询 `agentrun/get`，存在以下问题：

- **效率低**：高频轮询浪费资源，低频轮询增加延迟
- **无法被动触发**：调用方必须主动查询，无法"被通知"
- **不适合集成**：外部系统（CI/CD、webhook、消息队列）无法直接接收 agent 状态变化

Workspace Spec 已有 `setup` / `teardown` hooks 用于 workspace 生命周期。
本提案扩展 hooks 机制到 **AgentRun 状态变化**，让 agentd 在 agent 状态转换时自动执行注册的外部命令。

---

## 现有 Hooks 回顾

Workspace Spec 当前支持两类 hooks（见 [workspace-spec.md](../workspace/workspace-spec.md)）：

| Hook | 触发时机 | 用途 |
|------|---------|------|
| `setup` | workspace 准备阶段 | 安装依赖、启动服务 |
| `teardown` | workspace 清理阶段 | 停止服务、清理资源 |

Hook 执行模型：

- 在 agentd 主机进程环境中执行
- 以 workspace 目录为 `cwd`
- 数组顺序依次执行
- `setup` hook 失败会中止 workspace 准备

本提案在同一框架下增加 AgentRun 生命周期 hooks。

---

## 提案：AgentRun Lifecycle Hooks

### 配置位置

AgentRun lifecycle hooks 配置在 **Workspace 层级**，因为：

1. hooks 是 workspace 运维关切，不是 agent 运行时逻辑
2. 同一 workspace 内的所有 agent 共享相同的通知策略
3. 与现有 `setup`/`teardown` hooks 保持一致的配置层级
4. 外部调用方在创建 workspace 时就能声明完整的通知策略

### 配置格式

扩展 Workspace Spec 的 `hooks` 字段：

```json
{
  "hooks": {
    "setup": [...],
    "teardown": [...],
    "agentrun": {
      "onStateChange": [
        {
          "command": "/path/to/notify.sh",
          "args": ["${MASS_AGENT_NAME}", "${MASS_AGENT_STATE}"],
          "description": "Notify orchestrator of agent state change"
        }
      ]
    }
  }
}
```

对应 workspace-compose YAML 格式：

```yaml
kind: workspace-compose
metadata:
  name: my-project
spec:
  source:
    type: local
    path: /path/to/code
  hooks:
    agentrun:
      onStateChange:
        - command: /path/to/notify.sh
          args: ["${MASS_AGENT_NAME}", "${MASS_AGENT_STATE}"]
          description: Notify orchestrator of agent state change
  agents:
    - metadata:
        name: coder
      spec:
        agent: claude
```

### Hook 类型

| Hook | 触发时机 | 用途 |
|------|---------|------|
| `onStateChange` | AgentRun 状态发生任何转换时 | 通用状态通知 |

`onStateChange` 覆盖所有状态转换：

```
creating → idle       (bootstrap 完成)
creating → error      (bootstrap 失败)
idle → running        (prompt 开始处理)
running → idle        (prompt 完成)
running → error       (运行时错误)
idle → stopped        (主动停止)
running → stopped     (主动停止)
```

> **设计选择**：只提供 `onStateChange` 一个 hook 类型，通过环境变量区分具体状态。
> 不提供 `onIdle`、`onRunning`、`onError` 等细分 hook —— 外部命令可以通过 `$MASS_AGENT_STATE` 自行过滤。
> 如果未来有强需求，可以增加细分 hook 而不破坏现有格式。

### 环境变量

Hook 命令执行时，agentd 注入以下环境变量：

| 变量 | 类型 | 含义 |
|------|------|------|
| `MASS_WORKSPACE_NAME` | string | workspace 名称 |
| `MASS_AGENT_NAME` | string | AgentRun 名称 |
| `MASS_AGENT_STATE` | string | 转换后的状态（`idle`, `running`, `stopped`, `error`, `creating`） |
| `MASS_AGENT_PREVIOUS_STATE` | string | 转换前的状态 |
| `MASS_SOCKET` | string | agentd unix socket 路径 |

环境变量支持在 `args` 中作为 `${VAR}` 模板引用。agentd 在执行前展开这些变量。

### 执行模型

| 维度 | 行为 |
|------|------|
| 执行时机 | 状态转换完成后，异步执行 |
| 阻塞性 | **不阻塞**状态转换。hook 在独立 goroutine 中执行 |
| 工作目录 | workspace 目录 |
| 执行环境 | agentd 主机进程环境 + 注入的环境变量 |
| 超时 | 单个 hook 执行超时 30 秒（可配置） |
| 数组顺序 | 依次执行，前一个完成后执行下一个 |
| 并发控制 | 同一 workspace 的 hook 执行串行化，防止并发竞争 |

### 错误处理

| 情况 | 行为 |
|------|------|
| hook 执行失败（非零退出码） | 记录日志，不影响状态转换，不重试 |
| hook 超时 | kill 进程，记录日志 |
| hook 命令不存在 | 记录日志，跳过 |
| agentd 重启 | 不重放历史事件，只触发重启后的新事件 |

> **关键决策**：lifecycle hooks 的失败**不影响** agent 状态转换。
> 这与 `setup` hooks 不同 — `setup` hook 失败会中止 workspace 准备。
> lifecycle hooks 是通知性质的，不应该成为 agent 运行的阻塞点。

---

## 使用示例

### 示例 1：写文件通知

最简单的方式 — hook 写一个文件，orchestrator 监听文件变化：

```yaml
hooks:
  agentrun:
    onStateChange:
      - command: sh
        args: ["-c", "echo '${MASS_AGENT_NAME}:${MASS_AGENT_STATE}' >> /tmp/mass-events.log"]
        description: Append state change to log file
```

### 示例 2：HTTP Webhook

通知外部系统：

```yaml
hooks:
  agentrun:
    onStateChange:
      - command: curl
        args:
          - "-s"
          - "-X"
          - "POST"
          - "http://localhost:8080/hooks/agent-state"
          - "-H"
          - "Content-Type: application/json"
          - "-d"
          - '{"workspace":"${MASS_WORKSPACE_NAME}","agent":"${MASS_AGENT_NAME}","state":"${MASS_AGENT_STATE}","previousState":"${MASS_AGENT_PREVIOUS_STATE}"}'
        description: Notify webhook endpoint
```

### 示例 3：触发下游 agent

当 coder 完成时自动 prompt reviewer（通过 massctl）：

```yaml
hooks:
  agentrun:
    onStateChange:
      - command: sh
        args:
          - "-c"
          - |
            if [ "${MASS_AGENT_NAME}" = "coder" ] && [ "${MASS_AGENT_STATE}" = "idle" ] && [ "${MASS_AGENT_PREVIOUS_STATE}" = "running" ]; then
              massctl workspace send --socket "${MASS_SOCKET}" --name "${MASS_WORKSPACE_NAME}" --from coder --to reviewer --text "[review-request] coder has completed"
            fi
        description: Auto-trigger reviewer when coder finishes
```

### 示例 4：只关心特定转换

Hook 脚本内部过滤关心的状态转换：

```bash
#!/bin/bash
# notify.sh — only notify on running→idle (turn completed)
if [ "$MASS_AGENT_PREVIOUS_STATE" = "running" ] && [ "$MASS_AGENT_STATE" = "idle" ]; then
  echo "Agent $MASS_AGENT_NAME completed turn in workspace $MASS_WORKSPACE_NAME"
  # do actual notification...
fi
```

---

## 实现要点

### 在 agentd 中的集成点

agentd 的 Process Manager 已经在 agent 状态转换时发出内部事件。
lifecycle hooks 只需在状态转换路径上增加一个 hook 执行步骤：

```text
状态转换发生
  ├→ 更新 store（bbolt）          ← 已有
  ├→ 更新内存状态                   ← 已有
  └→ 异步执行 onStateChange hooks  ← 新增
```

### 配置存储

Workspace hooks 当前存储在 Workspace Spec 中。扩展现有的 `hooks` 字段即可，不需要新的存储结构。

### Workspace Spec 变更

当前 Workspace Spec（[workspace-spec.md](../workspace/workspace-spec.md)）hooks 结构：

```json
{
  "hooks": {
    "setup": [HookEntry],
    "teardown": [HookEntry]
  }
}
```

扩展为：

```json
{
  "hooks": {
    "setup": [HookEntry],
    "teardown": [HookEntry],
    "agentrun": {
      "onStateChange": [HookEntry]
    }
  }
}
```

`HookEntry` 结构不变：

```json
{
  "command": "string",
  "args": ["string"],
  "description": "string"
}
```

新增的部分是 `agentrun` 子对象和变量展开能力。

---

## 与其他方案的关系

### 轮询

轮询是零成本方案，lifecycle hooks 不替代轮询。两者可以共存：

- 简单场景：直接轮询 `agentrun/get`
- 需要被动通知：配置 lifecycle hooks
- 混合使用：hooks 通知"有变化"，调用方再通过 `agentrun/get` 获取详细状态

### ARI Watch

ARI Watch（类似 K8s List-Watch 模式）是程序化客户端的实时事件流方案。

| 维度 | Lifecycle Hooks | ARI Watch |
|------|----------------|-----------|
| 触发方式 | 命令执行 | 事件流推送 |
| 适用场景 | Shell 集成、webhook、CI/CD | 程序化客户端、dashboard |
| 实时性 | 毫秒级（命令启动有开销） | 毫秒级（流式推送） |
| 可靠性 | 无状态，不需要持久连接 | 需要连接管理和重连 |
| 灵活性 | 高（任意命令） | 固定格式事件 |
| 实现复杂度 | 低 | 高（需要事件广播器 + 连接管理） |

lifecycle hooks 优先实现，ARI Watch 作为未来规划。两者不冲突。

---

## 不做什么

1. **不阻塞状态转换** — hook 失败不影响 agent 运行
2. **不重试** — hook 执行失败不自动重试
3. **不保证 exactly-once** — 极端情况（agentd crash）可能丢失事件
4. **不提供回调结果** — hook 的 stdout/stderr 只记录日志，不回传给调用方
5. **不做消息路由** — hooks 是通知机制，不是消息总线
6. **不重放历史** — agentd 重启后不重放之前的状态变化
