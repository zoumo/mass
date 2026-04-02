# Room Spec

## 架构参照

Kubernetes Pod 是最小的调度单元 — 一组共享 network 和 IPC namespace 的容器，
被协同调度，并拥有耦合的生命周期。
Pod 定义在 Pod Spec 中，由 kubelet 消费，被拆解为
CRI 调用（RunPodSandbox + CreateContainer）发送给 containerd。

Room 是 Agent 世界的对应概念：一组共享 workspace、
可以互相通信、并具有协调生命周期的 Agent。

### 与 Pod 的映射

| Pod 概念 | Room 对应 | 机制 |
|----------|----------|------|
| 共享 network namespace（localhost） | 共享消息总线 | Room 内 agent 通过 agentd 路由的 MCP tool 通信 |
| 共享 IPC namespace | 共享消息总线 | 同一机制 — 同 room 内的 agent 可以交换消息 |
| 共享 volumes | 共享 workspace | Room 内所有 agent 共享同一个 workspace 目录 |
| Pod Spec 由 kubelet 消费 | Room Spec 由 orchestrator 消费 | Room Spec 不由 agentd 消费 |
| kubelet 将 Pod 拆解为 CRI 调用 | orchestrator 将 Room 拆解为 ARI 调用 | agentd 只看到独立的 session，不看到 room |
| pause 容器（生命周期锚点） | agentd 中的 Room 元数据 | agentd 追踪 room 成员关系；不需要"pause agent" |

### Room 不是什么

- **不是一个 agent** — Room 没有进程、没有协议、没有行为。它是一个分组构造。
- **不由 agentd 管理生命周期** — agentd 将 room 成员关系作为 session 元数据追踪。
  orchestrator 拥有 room 的生命周期（创建、完成检测、销毁）。
- **不是必需的** — 独立的 session（不属于任何 room 的 agent）仍然是默认模式。

## 规范定义

### 顶层结构

```json
{
  "oarVersion": "0.1.0",
  "kind": "Room",
  "metadata": { },
  "spec": { }
}
```

### `metadata`

```json
{
  "metadata": {
    "name": "backend-refactor",
    "labels": {
      "project": "auth-service",
      "team": "backend"
    },
    "annotations": {}
  }
}
```

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `name` | string | 是 | 唯一的 room 名称 |
| `labels` | map[string]string | 否 | 用于过滤和选择的标签 |
| `annotations` | map[string]string | 否 | 任意元数据 |

### `spec`

#### `spec.workspace`

引用一个 Workspace Spec。Room 内所有 agent 共享此 workspace。

```json
{
  "spec": {
    "workspace": {
      "source": {
        "type": "local",
        "path": "/home/user/project"
      }
    }
  }
}
```

workspace 对象遵循 Workspace Spec 格式（见 [workspace-spec.md](../workspace/workspace-spec.md)）。
它可以是内联定义或对外部文件的引用 — 这是 orchestrator 的关切。

#### `spec.agents`

组成此 room 的 agent。

```json
{
  "spec": {
    "agents": [
      {
        "name": "architect",
        "runtimeClass": "claude",
        "systemPrompt": "你是这次重构任务的首席架构师。"
      },
      {
        "name": "coder",
        "runtimeClass": "codex"
      },
      {
        "name": "reviewer",
        "runtimeClass": "gemini",
        "systemPrompt": "审查代码变更的正确性和风格。"
      }
    ]
  }
}
```

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `name` | string | 是 | Room 内的 agent 名称。在 room 内必须唯一。用于 agent 间通信的寻址 |
| `runtimeClass` | string | 是 | Agent 类型名称。agentd 查找对应的 handler 配置，生成 config.json 传给 agent-shim |
| `systemPrompt` | string | 否 | 在 session 创建时注入的系统提示 |

**设计说明**：每个 agent 条目通过 `runtimeClass` 引用 agent 类型，而不是内嵌启动配置。
这类似于 Pod 中的容器通过名称引用镜像，而不是内嵌镜像定义。
`runtimeClass` 由 agentd 解析为具体的启动参数（command、args、env），
然后生成 OAR config.json 传给 agent-shim。

#### `spec.communication`

Room 内 agent 如何互相发现和通信。

```json
{
  "spec": {
    "communication": {
      "mode": "mesh"
    }
  }
}
```

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `mode` | string | 是 | 通信拓扑 |

**通信模式**：

| 模式 | 说明 |
|------|------|
| `mesh` | 任意 agent 可以向 room 内任意其他 agent 发消息 |
| `star` | 只有 leader agent 可以向其他 agent 发消息；其他 agent 只能回复 leader |
| `isolated` | 无 agent 间通信。Agent 共享 workspace 但不互相对话 |

## Agent 间通信

### Room 内 Agent 如何对话

Pod 内容器通过共享的 network namespace 通信 — 它们可以通过 localhost
互相访问，而不需要知道 Pod 这个抽象。

Room 内 agent 通过 agentd 注入的 MCP tool 通信。Agent 不知道自己在一个 room 里 —
它只是有一些可用的 tool，这些 tool 恰好将消息路由到其他 agent。

```
Pod：
  nginx → curl http://localhost:8080 → sidecar
  机制：共享 network namespace，同一个 IP 栈

Room：
  architect → room_send("coder", "实现 auth 模块") → coder
  机制：MCP tool → agentd 消息路由 → 目标 session 的 ACP prompt
```

### 注入的 MCP Tool

当 agentd 创建一个属于 room 的 session 时，它注入 room 级别的 MCP tool：

```
room_send(agent_name, message)
  向 room 内的另一个 agent 发送消息（通过名称寻址）。
  内部转换为：agentd 查找目标 session，作为 ACP session/prompt 转发。

room_broadcast(message)
  向 room 内所有其他 agent 发送消息。

room_status()
  获取 room 内所有 agent 的状态。
  返回：[{name, state, hasActivePrompt}]
```

Agent 通过**名称**互相寻址（而非 session ID），就像 Pod 内容器通过
**localhost** 互相寻址（而非 IP 地址）。Room 抽象在底层 session 基础设施之上
提供了一个稳定的命名层。

### Busy Session 处理

ACP 是每个 session 串行的 — 同一时间只能处理一个 prompt。
当 agent A 向正忙的 agent B 发送消息时：

```
room_send("coder", message):
  coder 空闲   → 作为 session/prompt 转发，返回结果
  coder 忙碌   → 返回错误：agent busy

  调用方（发送消息的 agent）自行决定：
  - 稍后重试
  - 自己做这个工作
  - 请求另一个 agent
```

默认不排队、不打断。发送方 agent 在自己的推理过程中处理 busy 错误，
这对 LLM agent 来说是最自然的模式。

## Room 生命周期

由 orchestrator 管理（不是 agentd）：

```
1. Orchestrator 读取 Room Spec

2. 准备 workspace
   orchestrator → agentd: workspace/prepare(spec.workspace)
   ← workspacePath

3. 创建 session（每个 agent 一个）
   对 spec.agents 中的每个 agent：
     orchestrator → agentd: session/new {
       runtimeClass: agent.runtimeClass,
       workspace: workspacePath,
       room: metadata.name,        ← room 成员关系
       name: agent.name,           ← room 内的名称
       systemPrompt: agent.systemPrompt
     }
   agentd：创建 session，标记 room 元数据，注入 room MCP tool

4. 向 leader（或全部，取决于编排逻辑）发送初始 prompt
   orchestrator → agentd: session/prompt { sessionId, prompt }

5. Agent 工作，通过 room MCP tool 互相通信

6. 完成判定
   由 orchestrator 自行决定 room 何时算"完成"。
   Room Spec 不定义 completionPolicy — 不同场景的完成条件差异很大，
   交给 orchestrator 在业务逻辑中实现。

7. 销毁
   orchestrator → agentd: session/stop（对每个 session）
   orchestrator → agentd: workspace/cleanup(workspacePath)
```

## 完整示例

```json
{
  "oarVersion": "0.1.0",
  "kind": "Room",

  "metadata": {
    "name": "backend-refactor",
    "labels": {
      "project": "auth-service"
    }
  },

  "spec": {
    "workspace": {
      "source": {
        "type": "local",
        "path": "/home/user/project"
      }
    },

    "agents": [
      {
        "name": "architect",
        "runtimeClass": "claude",
        "systemPrompt": "你是首席架构师。将 auth 模块重构拆解为任务，将编码任务委派给 'coder'，并请 'reviewer' 进行代码审查。"
      },
      {
        "name": "coder",
        "runtimeClass": "codex"
      },
      {
        "name": "reviewer",
        "runtimeClass": "gemini",
        "systemPrompt": "审查代码变更的正确性、安全性和风格。将发现报告给请求审查的 agent。"
      }
    ],

    "communication": {
      "mode": "mesh"
    }
  }
}
```

