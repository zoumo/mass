# agent-shim

## 定位

agent-shim 是 OAR Runtime 的参考实现边界：
它读取 bundle、启动 agent 进程、持有 stdio、完成 ACP bootstrap，
并对外暴露 shim RPC。

它对标 containerd-shim，但在 OAR 里同时吸收了独立 `runc` 不再成立后留下的职责。
相关原因见 [why-no-runa.md](why-no-runa.md)。

每个 AgentRun（运行实例）对应一个独立的 agent-shim 进程。

```text
agentd（可重启）
   │  shim RPC: session/* + runtime/*
   ▼
agent-shim（每个 AgentRun 一个，独立存活）
   │  ACP over stdio
   ▼
agent 进程（claude-acp / pi-acp / gemini / ...）
```

## shim RPC 稳定性声明

**agent-shim 保持现有 RPC 边界。**
shim 提供 `session/*` + `runtime/*` RPC surface（clean-break 实现已对齐）、bundle/state 共置和单 AgentRun 单 shim 进程设计。

agentd 的外部 ARI 使用 `agentrun/*` 管理运行实例生命周期，`agent/*` 管理 Agent CRUD。shim RPC 的 `session/*` + `runtime/*` 是内部协议，不暴露给外部调用方。
**事件排序增强**：`session/update` envelope 中增加 `turnId`、`streamSeq`、`phase` 三个字段，用于支持 turn 级精确回放。
详见 [shim-rpc-spec.md](shim-rpc-spec.md) 中的"Turn-Aware Event Ordering"章节。

## 与规范文档的分工

- [runtime-spec.md](runtime-spec.md) 定义 runtime 状态、bundle、state dir 与 socket 路径；
- [shim-rpc-spec.md](shim-rpc-spec.md) 定义对上的规范 surface、notification 名、回放 / 恢复语义；
- 本文档只解释 **agent-shim 这个组件为什么存在、拥有哪些职责、边界在哪里**。

也就是说：

- socket 路径约定不是本文档的 authority；
- 方法名与 notification 名不是本文档的 authority；
- 本文档不再重复维护另一套 shim 协议说法。

## 架构参照

| containerd 生态 | OAR 生态 | 说明 |
|----------------|----------|------|
| containerd | agentd | 高层守护进程，可重启 |
| containerd-shim | agent-shim | 中间层，独立进程，生命周期与单个 workload 绑定 |
| runc | — | agent 不需要内核隔离，fork/exec 责任吸收到 agent-shim |
| 容器进程 | agent 进程 | 工作负载 |
| ttrpc over Unix socket | JSON-RPC 2.0 over Unix socket | shim 控制协议 |

**agent-shim = fork/exec + stdio 持有 + ACP client + runtime truth exporter**

## 进程模型

agentd 为每个 AgentRun fork/exec 一个 agent-shim：

```text
agentd fork/exec agent-shim --bundle <bundle-dir> --socket <socket-path>
```

agent-shim 内部的职责序列是：

1. 读取 bundle/config.json；
2. 解析并解析（resolve）`agentRoot.path`，得到 canonical `cwd`；
3. fork/exec agent 进程（`acpAgent.process.command + args + env`）；
4. 持有 agent 的 stdin/stdout，并作为唯一 ACP client 完成 `initialize`；
5. 建立 ACP bootstrap（resolved `cwd`、`acpAgent.session`、`acpAgent.systemPrompt` 的兼容实现）；
6. 写入 runtime state 与事件日志；
7. 在 shim socket 上提供对外控制与恢复能力；
8. 持续监督 agent 进程，必要时执行 stop / cleanup 流程。

## 双重角色

### 角色 1：ACP Client（朝下）

agent-shim 是 agent 进程唯一的 ACP client，负责：

- 完成 ACP `initialize` 握手；
- 用 resolved `cwd` 和 `acpAgent.session` 建立 bootstrap session；
- 将 `acpAgent.systemPrompt` 落实为创建期 bootstrap 语义，而不是外部工作 turn；
- 转发工作 turn；
- 处理 agent 发起的 `fs/*` / `terminal/*` 请求（按 permission posture）；
- 在需要时执行 `session/load` 或等价恢复步骤；
- 监控 agent 退出并记录 runtime-local failure 细节。

### 角色 2：Runtime Session Server（朝上）

agent-shim 对上暴露的是 **runtime/session 语义**，不是 raw ACP。
规范 surface 由 [shim-rpc-spec.md](shim-rpc-spec.md) 定义：

```text
session/prompt       发送一个工作 turn
session/cancel       取消当前 turn
session/subscribe    恢复 / 建立 live notification 流
runtime/status       查询 runtime truth 与恢复边界
runtime/history      回放历史 notification
runtime/stop         优雅停止 runtime
```

对上暴露的 live notification 也是 shim 自己的 surface：

- `session/update`
- `runtime/stateChange`

这层 API 让 agentd 只关心：

- 这个 runtime 现在是什么状态；
- 当前 turn 的输入 / 输出和副作用；
- 断线后如何补历史并恢复 live 流；
- 何时需要停止或清理。

## 权威边界

agent-shim 处在 OAR runtime design set 的 authority split 中间：

| 关切 | authority | agent-shim 的角色 |
|------|-----------|-------------------|
| AgentRun identity `(workspace, name)` | agentd / ARI | 读取外部分配结果，不重新定义 |
| process truth、runtime status、runtime-local failure | runtime / shim | 直接拥有并对上暴露 |
| ACP `sessionId` 与 ACP 协议细节 | ACP peer + shim | 内部维护，不让上层越过 shim 边界 |
| desired orchestration intent | orchestrator | 不拥有 |

因此，agent-shim 负责的是 **runtime-local truth**，不是 orchestrator policy。

## ACP 是实现细节，typed notifications 才是契约

agent-shim 的核心价值不是“把 ACP 暴露给 agentd”，而是“把 ACP 封装掉”。

```text
agentd ↔ agent-shim:  session/* + runtime/* + typed notifications
agent-shim ↔ agent:   ACP over stdio
```

这意味着：

- agentd 不需要理解 ACP 事件名、握手细节或客户端职责；
- shim 把底层协议翻译成上层能消费的 `session/update` / `runtime/stateChange`；
- 若未来某个 agent 不走 ACP，只要 shim 继续维持相同的对上 surface，
  上层 contract 仍然成立。

## 恢复与爆炸半径隔离

独立 shim 进程的价值仍然是爆炸半径隔离：

```text
agentd 重启
    │
    ├── agent-shim 1（my-project-architect）→ 继续存活
    ├── agent-shim 2（my-project-coder）→ 继续存活
    └── agent-shim 3（my-project-reviewer）→ 继续存活
```

agentd 恢复后，不需要重新理解 ACP，只需要：

1. 发现 shim socket；
2. 连接；
3. `runtime/status` 获取当前 runtime truth 与 `lastSeq`；
4. `runtime/history` 补齐断线期间错过的 notification；
5. `session/subscribe` 恢复 live 流。

**重要**：socket 路径、state dir 布局、`events.jsonl` 的存在本身，由 runtime-spec authority 定义；
恢复方法名与顺序，由 shim-rpc-spec authority 定义；
本文档只是解释为什么 agent-shim 必须提供这类能力。

## 为什么需要独立 shim

独立 shim 解决的是 agent 场景下几个无法回避的问题：

1. **stdio 必须被长期持有** —— ACP over stdio 决定了 agent 不能在 client 退出后继续”自己活着”；
2. **process truth 需要独立于 agentd 生存** —— agentd 重启不应直接杀掉所有 AgentRun；
3. **协议兼容性需要集中处理** —— ACP bootstrap、权限处理、协议翻译不能散落在 agentd 各处；
4. **恢复面需要稳定边界** —— socket、state、history、typed notification 都要由一个长期存在的进程维护。

## 实现状态

当前实现已对齐 clean-break contract：

- 规范 surface 是 `session/*` + `runtime/*`（`pkg/rpc/server.go`、`pkg/agentd/shim_client.go` 已实现）；
- recovery story 通过 `runtime/status` / `runtime/history` / `session/subscribe` 闭合；
- ACP 继续留在 shim 内部；
- `session/load` 在 `ShimClient` 中作为可失败的恢复尝试（用于 `tryReload` restart policy）；该调用允许失败并 fallback。当前生产 shim RPC server（`pkg/rpc/server.go`）尚未注册 `session/load`，调用会返回 `MethodNotFound`；生产 shim 实现此方法是 future work。
