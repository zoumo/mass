---
last_updated: 2026-04-18
---

# agent-run

## 定位

agent-run 是 MASS Runtime 的参考实现边界：
它读取 bundle、启动 agent 进程、持有 stdio、完成 ACP bootstrap，
并对外暴露 agent-run RPC。

它对标 containerd-shim，但在 MASS 里同时吸收了独立 `runc` 不再成立后留下的职责。
相关原因见 [why-no-runa.md](why-no-runa.md)。

每个 AgentRun（运行实例）对应一个独立的 agent-run 进程。

```text
mass daemon（可重启）
   │  agent-run RPC: session/* + runtime/*
   ▼
agent-run（每个 AgentRun 一个，独立存活）
   │  ACP over stdio
   ▼
agent 进程（claude-acp / pi-acp / gemini / ...）
```

## Agent-Run RPC 稳定性声明

**agent-run 保持现有 RPC 边界。**
agent-run 提供 `session/*` + `runtime/*` RPC surface（request/response，clean-break 实现已对齐）、bundle/state 共置和单 AgentRun 单 agent-run 进程设计。

mass daemon 的外部 ARI 使用 `agentrun/*` 管理运行实例生命周期，`agent/*` 管理 Agent CRUD。agent-run RPC 的 `session/*` + `runtime/*` 是内部协议，不暴露给外部调用方。
**统一 notification surface**：live notification 统一为 `runtime/event_update`，携带 `runId`、`sessionId`、`seq`、`type`、`turnId`（turn 内事件）、`payload` 顶层字段。事件类型包括核心流式事件和 `runtime_update`（合并进程状态与 session 元数据）。
详见 [run-rpc-spec.md](run-rpc-spec.md) 中的"Turn-Aware Event Ordering"章节。

## 与规范文档的分工

- [runtime-spec.md](runtime-spec.md) 定义 runtime 状态、bundle、state dir 与 socket 路径；
- [run-rpc-spec.md](run-rpc-spec.md) 定义对上的规范 surface、notification 名、回放 / 恢复语义；
- 本文档只解释 **agent-run 这个组件为什么存在、拥有哪些职责、边界在哪里**。

也就是说：

- socket 路径约定不是本文档的 authority；
- 方法名与 notification 名不是本文档的 authority；
- 本文档不再重复维护另一套 agent-run 协议说法。

## 架构参照

| containerd 生态 | MASS 生态 | 说明 |
|----------------|----------|------|
| containerd | mass daemon | 高层守护进程，可重启 |
| containerd-shim | agent-run | 中间层，独立进程，生命周期与单个 workload 绑定 |
| runc | — | agent 不需要内核隔离，fork/exec 责任吸收到 agent-run |
| 容器进程 | agent 进程 | 工作负载 |
| ttrpc over Unix socket | JSON-RPC 2.0 over Unix socket | agent-run 控制协议 |

**agent-run = fork/exec + stdio 持有 + ACP client + runtime truth exporter**

## 进程模型

mass daemon 为每个 AgentRun fork/exec 一个 agent-run：

```text
mass fork/exec agent-run --bundle <bundle-dir> --state-dir <state-dir> --permissions <policy> --id <id>
```

agent-run 内部的职责序列是：

1. 读取 bundle/config.json；
2. 解析并解析（resolve）`agentRoot.path`，得到 canonical `cwd`；
3. fork/exec agent 进程（`process.command + args + env`）；
4. 持有 agent 的 stdin/stdout，根据 `clientProtocol` 完成协议握手（如 ACP `initialize`）；
5. 使用 `clientProtocol` 指定的方式建立 bootstrap（resolved `cwd` + `session` 字段）；
6. 写入 runtime state 与事件日志；
7. 在 agent-run socket 上提供对外控制与恢复能力；
8. 持续监督 agent 进程，必要时执行 stop / cleanup 流程。

## 双重角色

### 角色 1：ACP Client（朝下）

agent-run 是 agent 进程唯一的 ACP client，负责：

- 完成 ACP `initialize` 握手；
- 用 resolved `cwd` 和 `session` 字段建立 bootstrap session；
- 将 `session.systemPrompt` 落实为创建期 bootstrap 语义，而不是外部工作 turn；
- 转发工作 turn；
- 处理 agent 发起的 `fs/*` / `terminal/*` 请求（按 permission posture）；
- 在需要时执行 `session/load` 或等价恢复步骤；
- 监控 agent 退出并记录 runtime-local failure 细节。

### 角色 2：Runtime Session Server（朝上）

agent-run 对上暴露的是 **runtime/session 语义**，不是 raw ACP。
规范 surface 由 [run-rpc-spec.md](run-rpc-spec.md) 定义：

```text
session/prompt       发送一个工作 turn
session/cancel       取消当前 turn
runtime/watch_event  K8s List-Watch 风格事件订阅（replay + live）
session/load         恢复已有 ACP session（best-effort，recovery 时始终尝试）
session/set_model    切换当前模型
runtime/status       查询 runtime truth 与恢复边界
runtime/stop         优雅停止 runtime
```

对上暴露的 live notification 也是 agent-run 自己的 surface：

- `runtime/event_update`（统一 notification，包含核心流式事件和 `runtime_update` 事件）

这层 API 让 agentd 只关心：

- 这个 runtime 现在是什么状态；
- 当前 turn 的输入 / 输出和副作用；
- 断线后如何补历史并恢复 live 流；
- 何时需要停止或清理。

## 权威边界

agent-run 处在 MASS runtime design set 的 authority split 中间：

| 关切 | authority | agent-run 的角色 |
|------|-----------|-------------------|
| AgentRun identity `(workspace, name)` | agentd / ARI | 读取外部分配结果，不重新定义 |
| process truth、runtime status、runtime-local failure | runtime / agent-run | 直接拥有并对上暴露 |
| ACP `sessionId` 与 ACP 协议细节 | ACP peer + agent-run | 内部维护，不让上层越过 agent-run 边界 |
| desired scheduling intent | external caller | 不拥有 |

因此，agent-run 负责的是 **runtime-local truth**，不是外部调度策略。

## ACP 是实现细节，typed notifications 才是契约

agent-run 的核心价值不是“把 ACP 暴露给 agentd”，而是“把 ACP 封装掉”。

```text
mass ↔ agent-run:  session/* + runtime/* + typed notifications
agent-run ↔ agent:   ACP over stdio
```

这意味着：

- agentd 不需要理解 ACP 事件名、握手细节或客户端职责；
- agent-run 把底层协议翻译成上层能消费的 `runtime/event_update` notification；
- 若未来某个 agent 不走 ACP，只要 agent-run 继续维持相同的对上 surface，
  上层 contract 仍然成立。

## 恢复与爆炸半径隔离

独立 agent-run 进程的价值仍然是爆炸半径隔离：

```text
mass daemon 重启
    │
    ├── agent-run 1（my-project-architect）→ 继续存活
    ├── agent-run 2（my-project-coder）→ 继续存活
    └── agent-run 3（my-project-reviewer）→ 继续存活
```

mass daemon 恢复后，不需要重新理解 ACP，只需要：

1. 发现 agent-run socket；
2. 连接；
3. `runtime/status` 获取当前 runtime truth 与 `lastSeq`；
4. `runtime/watch_event` 一步完成历史补齐 + live 流恢复。

**重要**：socket 路径、state dir 布局、`events.jsonl` 的存在本身，由 runtime-spec authority 定义；
恢复方法名与顺序，由 run-rpc-spec authority 定义；
本文档只是解释为什么 agent-run 必须提供这类能力。

## 为什么需要独立 agent-run

独立 agent-run 解决的是 agent 场景下几个无法回避的问题：

1. **stdio 必须被长期持有** —— ACP over stdio 决定了 agent 不能在 client 退出后继续”自己活着”；
2. **process truth 需要独立于 agentd 生存** —— agentd 重启不应直接杀掉所有 AgentRun；
3. **协议兼容性需要集中处理** —— ACP bootstrap、权限处理、协议翻译不能散落在 agentd 各处；
4. **恢复面需要稳定边界** —— socket、state、history、typed notification 都要由一个长期存在的进程维护。

## 实现状态

当前实现已对齐 clean-break contract：

- request/response surface 是 `session/*` + `runtime/*`（已实现）；
- notification surface 是 `runtime/event_update`（统一替代原 `session/update` + `runtime/state_change`）；
- recovery story 通过 `runtime/status` / `runtime/watch_event` 闭合；
- ACP 继续留在 agent-run 内部；
- `session/load` 在 recovery 时始终尝试，agent-run 内部检查 ACP `loadSession` 能力并自动 fallback；调用方无需关心恢复策略。
