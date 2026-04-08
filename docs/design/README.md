# Open Agent Runtime (OAR) — 设计规范 v2

## 什么是 OAR

OAR（Open Agent Runtime）是一套用于管理 AI 编码 Agent 的标准和组件。
它直接借鉴了 OCI（Open Container Initiative）生态的架构思想，
将相同的关注点分离原则应用到 Agent 领域。

```text
OCI（容器世界）                       OAR（Agent 世界）
─────────────────────                ─────────────────────
Open Container Initiative       →    Open Agent Runtime
OCI Runtime Spec                →    OAR Runtime Spec
OCI Image Spec                  →    OAR Workspace Spec
runc + containerd-shim          →    agent-shim（合并，无独立 runa）
containerd                      →    agentd
CRI (Container Runtime Interface) →  ARI (Agent Runtime Interface)
Pod                             →    Room
Container                       →    Agent（外部对象）/ Session（内部运行时实例）
Image / rootfs                  →    Workspace
crictl                          →    agent-shim-cli
```

## 为什么对标 OCI

容器生态解决了一个在结构上完全同构的问题：如何标准化地描述、准备和执行隔离的工作负载。
Agent 面临着同样的分层关切：

| 关切 | 容器方案 | Agent 方案 |
|------|---------|-----------|
| "运行什么" | OCI Runtime Spec (config.json) | OAR Runtime Spec (config.json) |
| "准备什么环境" | OCI Image Spec (layers → rootfs) | OAR Workspace Spec (source + hooks → workdir) |
| "底层执行 + 协议适配" | runc + containerd-shim | agent-shim（合并，无独立 runa） |
| "高层管理" | containerd (images, snapshots, tasks) | agentd（Agent Manager 管理外部 agent 生命周期；Session Manager 为内部实现） |
| "管理接口" | CRI (kubelet → containerd) | ARI (orchestrator → agentd)，外部对象为 agent |
| "协同调度组" | Pod（共享 network/IPC namespace） | Room（共享 workspace、消息总线） |

通过遵循这套经过验证的分层架构，每个组件都有清晰、有界的职责。
规范是契约；组件是可替换的实现。

## 设计原则

1. **规范先于实现** — 先定义接口和格式，组件随后跟进。
2. **不背容器包袱** — 我们借鉴 OCI 的架构，不搬运内核隔离。
   不涉及 namespace、cgroups、seccomp、pivot_root。Agent 是进程，不是沙箱。
3. **面向 Agent 的原生关切** — 聚焦 Agent 真正需要的：workspace 准备、
   协议通信（ACP）、技能/知识注入、Agent 间通信。
4. **分层分离** — 每层只做一件事。agent-shim 运行进程并持有 ACP。agentd 管理生命周期。
   Orchestrator 决定运行什么。Spec 是各层之间的粘合剂。
5. **简单优先** — 为当前需求设计。扩展点存在但保持空白，直到真实需求出现。

## 架构概览

```text
┌────────────────────────────────────────────────────────────┐
│                       Orchestrator                          │
│                                                             │
│   消费: Room Spec, Workspace Spec, Runtime Spec             │
│   决策: 运行哪些 agent、准备什么 workspace                    │
│   调用: ARI                                                 │
└──────────────────────────┬─────────────────────────────────┘
                           │ ARI（JSON-RPC over Unix Socket）
                           ▼
┌────────────────────────────────────────────────────────────┐
│                        agentd                               │
│                                                             │
│   ┌──────────────┐  ┌──────────────┐  ┌────────────────┐  │
│   ┌─────────────────────────────────────────────────────┐   │
│   │  Agent Manager (external API object: agent/*)       │   │
│   └──────┬──────────────────────────────────────────────┘   │
│          │ [internal: Session Manager + Workspace Manager +  │
│          │  Room Manager]                                     │
└──────────────────────────────────────────────────────────────┘
             │            │            │
             │ shim RPC over Unix socket │
             ▼            ▼            ▼
        ┌────────────┐ ┌────────────┐ ┌────────────┐
        │ agent-shim │ │ agent-shim │ │ agent-shim │
        └────┬───────┘ └────┬───────┘ └────┬───────┘
             │              │              │
             │ ACP over stdio              │
             ▼              ▼              ▼
          Agent A        Agent B        Agent C
```

## 先看哪里

在修改设计文档前，先看这两个 authority 入口：

1. [contract-convergence.md](./contract-convergence.md) — 当前跨文档 authority map 与关键不变量；
2. `*-spec.md` 文档 — 规范契约；无 `-spec` 后缀的文档只负责解释组件或设计理由。

对于 shim 边界，当前 authoritative 读取顺序是：

1. [runtime/runtime-spec.md](./runtime/runtime-spec.md) — runtime 状态模型、bundle、state dir、socket 路径；
2. [runtime/shim-rpc-spec.md](./runtime/shim-rpc-spec.md) — clean-break `session/*` + `runtime/*` surface、notification、recovery / replay；
3. [runtime/agent-shim.md](./runtime/agent-shim.md) — 组件职责与 ACP 边界；
4. [contract-convergence.md](./contract-convergence.md) — 跨层不变量与实现滞后说明。

## 文档索引

规范文档（`*-spec.md`）是接口契约，定义格式、状态模型和行为约束。
架构文档（无后缀）是实现说明、设计决策和组件描述。

### 根文档 / Authority Map

| 文档 | 类型 | 内容 |
|------|------|------|
| [contract-convergence.md](./contract-convergence.md) | authority map | 跨文档 authority、收敛不变量、实现滞后说明 |
| [roadmap.md](./roadmap.md) | 规划 | Development Roadmap — 分阶段实现计划（可能记录当前实现滞后，不替代规范） |

### runtime/ — Layer 1: 单 agent 进程（对标 runc + containerd-shim）

| 文档 | 类型 | 内容 |
|------|------|------|
| [runtime/runtime-spec.md](./runtime/runtime-spec.md) | 规范 | OAR Runtime Spec — state、bundle、lifecycle、operations、typed events |
| [runtime/config-spec.md](./runtime/config-spec.md) | 规范 | Config Spec — config.json schema（对标 OCI config.md） |
| [runtime/shim-rpc-spec.md](./runtime/shim-rpc-spec.md) | 规范 | Shim RPC Spec — clean-break `session/*` + `runtime/*` surface、`session/update` / `runtime/stateChange`、回放与重连语义 |
| [runtime/agent-shim.md](./runtime/agent-shim.md) | 组件 | agent-shim — 组件职责、ACP 边界、runtime truth、实现滞后说明（描述性，不重新定义协议） |
| [runtime/design.md](./runtime/design.md) | 设计 | 设计思路 — OCI 对标分析、架构决策、config.json 生成流程 |
| [runtime/why-no-runa.md](./runtime/why-no-runa.md) | 设计 | 为什么 OAR 没有 runa — agent 场景下独立运行时 CLI 不成立的原因 |

### workspace/ — Workspace Spec（对标 OCI Image Spec）

| 文档 | 类型 | 内容 |
|------|------|------|
| [workspace/workspace-spec.md](./workspace/workspace-spec.md) | 规范 | OAR Workspace Spec — 如何准备 agent 的工作环境（对标 OCI Image Spec） |

### agentd/ — Layer 2: 多 agent 管理（对标 containerd + CRI）

Agent 是外部 API 对象（orchestrator 通过 ARI `agent/*` 方法管理）；Session 是 agentd 内部的运行时实例，不暴露给外部调用方。

| 文档 | 类型 | 内容 |
|------|------|------|
| [agentd/ari-spec.md](./agentd/ari-spec.md) | 规范 | ARI — Agent Runtime Interface（对标 CRI）；外部对象为 agent，使用 `agent/*` 方法 |
| [agentd/agentd.md](./agentd/agentd.md) | 组件 | agentd — agent 运行时守护进程（对标 containerd）；包含 Agent Manager（外部）和 Session Manager（内部） |

### orchestrator/ — Layer 3: 多 agent 协同调度（对标 kubelet + Pod）

| 文档 | 类型 | 内容 |
|------|------|------|
| [orchestrator/room-spec.md](./orchestrator/room-spec.md) | 规范 | Room Spec — 协同调度的 agent 组（对标 Pod Spec） |
