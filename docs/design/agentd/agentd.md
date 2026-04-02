# agentd — Agent 运行时守护进程

## 架构参照

containerd 是高层容器运行时守护进程。它位于编排层（kubelet 通过 CRI）
和底层运行时（runc 通过 containerd-shim）之间。
containerd 将镜像、快照、容器和任务作为独立子系统管理。

agentd 在 agent 技术栈中占据相同位置。它管理 workspace、session、
room 和 agent 进程。它向上暴露 ARI 接口，向下通过 agent-shim 管理 agent 进程。

### containerd 子系统 → agentd 子系统

| containerd | agentd | 职责 |
|-----------|--------|------|
| Image Service | **Workspace Manager** | 从 spec 准备工作环境 |
| Snapshotter | — | 不需要（没有文件系统层叠） |
| Content Store | — | 不需要（没有内容寻址的 blob） |
| Container Service | **Session Manager** | Session 元数据的 CRUD |
| Task Service | **Process Manager** | 运行中 agent 进程的生命周期（通过 agent-shim） |
| Sandbox Controller | **Room Manager** | 追踪 room 成员关系，注入 room MCP tool |
| CRI Plugin | **ARI Service** | 通过 Unix socket 暴露管理接口 |

**关键结构决策**：containerd 将"Container"（静态元数据）和"Task"
（运行中进程）分离。一个 container 可以没有 task（镜像已拉取但未启动）。
agentd 做了相同的分离：Session（元数据、配置）独立于
agent 进程（可能正在运行，也可能没有）存在。

## 内部架构

```
┌──────────────────────────────────────────────────────────────────┐
│                            agentd                                 │
│                                                                   │
│  ┌────────────────────────────────────────────────────────────┐  │
│  │                      ARI Service                           │  │
│  │   JSON-RPC 2.0 over Unix Socket                           │  │
│  │   消费者：orchestrator、agent-shim-cli                           │  │
│  └─────────────────────────┬──────────────────────────────────┘  │
│                            │                                      │
│  ┌─────────────┬───────────┼───────────┬──────────────────────┐  │
│  │             │           │           │                      │  │
│  │  Workspace  │  Session  │  Process  │   Room               │  │
│  │  Manager    │  Manager  │  Manager  │   Manager            │  │
│  │             │           │           │                      │  │
│  │  Prepare    │  Create   │  Spawn    │   追踪成员关系       │  │
│  │  Cleanup    │  Get/List │  Kill     │   注入 MCP tool      │  │
│  │  Hooks      │  Update   │  State    │   路由消息           │  │
│  │             │  Delete   │  Monitor  │                      │  │
│  └─────────────┴───────────┴─────┬─────┴──────────────────────┘  │
│                                  │                                │
│  ┌───────────────────────────────▼────────────────────────────┐  │
│  │                    Runtime Layer                            │  │
│  │                                                            │  │
│  │   agent-shim（默认）  |  agent-shim-docker（未来）  |  ...     │  │
│  └────────────────────────────────────────────────────────────┘  │
│                                                                   │
│  ┌────────────────────────────────────────────────────────────┐  │
│  │                    Metadata Store                          │  │
│  │                                                            │  │
│  │   Sessions、workspaces、rooms、events                      │  │
│  └────────────────────────────────────────────────────────────┘  │
└──────────────────────────────────────────────────────────────────┘
```

## Workspace Manager

**参照**：containerd 的 Image Service + Snapshotter。

containerd 拉取镜像（Image Service），解压层（Snapshotter），产出 rootfs。
agentd 的 Workspace Manager 解析 workspace spec，产出准备就绪的目录。

### 职责

| 职责 | 说明 |
|------|------|
| **准备** | 克隆 git 仓库、创建空目录、执行 setup hooks |
| **追踪** | 维护 workspace 元数据（source、路径、创建时间、使用中的 session） |
| **共享** | 多个 session 可以共享同一个 workspace（如 room 内的 agent） |
| **清理** | 执行 teardown hooks，删除托管目录 |
| **引用计数** | 追踪哪些 session 引用了 workspace。只在引用计数为 0 时才清理 |

### Workspace 存储

```
/var/lib/agentd/
├── workspaces/
│   ├── ws-abc123/            ← git clone
│   ├── ws-def456/            ← emptyDir
│   └── ...
├── meta.db                   ← 元数据存储
└── ...
```

## Session Manager

**参照**：containerd 的 Container Service。

containerd 将 Container（静态元数据：image、labels、spec）和 Task（运行中进程）分离。
一个 Container 可以没有运行中的 Task。agentd 做了相同的分离。

### Session = 元数据，Process = 执行

```
containerd：
  Container: { id, image, labels, spec, snapshotKey }    ← 静态
  Task:      { pid, status, stdin/stdout/stderr }          ← 动态

agentd：
  Session:   { id, runtimeClass, workspace, room, labels }  ← 静态
  Process:   { pid, status, stdin/stdout }                    ← 动态（由 Process Manager 管理）
```

### 接口

```go
type SessionManager interface {
    // Create 注册一个新 session。不启动 agent 进程。
    Create(ctx context.Context, opts CreateSessionOpts) (*Session, error)

    // Get 返回 session 元数据。
    Get(ctx context.Context, id string) (*Session, error)

    // List 返回匹配过滤条件的 session。
    List(ctx context.Context, filter SessionFilter) ([]*Session, error)

    // Update 修改 session 元数据（labels、state）。
    Update(ctx context.Context, id string, opts UpdateSessionOpts) error

    // Delete 移除 session 元数据。进程仍在运行时失败。
    Delete(ctx context.Context, id string) error
}

type CreateSessionOpts struct {
    ID           string
    RuntimeClass string            // runtimeClass 名称，agentd 解析为具体启动配置
    Workspace    string            // workspace ID
    Room         string            // room 名称（独立 session 时为空）
    RoomAgent    string            // room 内的 agent 名称（独立 session 时为空）
    Labels       map[string]string
    SystemPrompt string
}

type Session struct {
    ID           string
    RuntimeClass string
    Workspace   string
    Room        string
    RoomAgent   string
    Labels      map[string]string
    State       SessionState
    CreatedAt   time.Time
    UpdatedAt   time.Time
}

type SessionState string

const (
    SessionCreated    SessionState = "created"
    SessionRunning    SessionState = "running"
    SessionPausedWarm SessionState = "paused:warm"
    SessionPausedCold SessionState = "paused:cold"
    SessionStopped    SessionState = "stopped"
)
```

## Process Manager

**参照**：containerd 的 Task Service。

containerd Task Service 管理运行中的容器进程，与 containerd-shim 集成。
agentd 的 Process Manager 管理运行中的 agent 进程，与 agent-shim 集成。

### 接口

```go
type ProcessManager interface {
    // Start 为一个 session 启动 shim + agent 进程。
    Start(ctx context.Context, sessionID string) error

    // Stop 通过 shim RPC 优雅关闭 agent。
    Stop(ctx context.Context, sessionID string, timeout time.Duration) error

    // State 通过 shim RPC 查询进程状态。
    State(ctx context.Context, sessionID string) (*ProcessState, error)

    // Connect 连接到已存在的 shim（agentd 重启后恢复）。
    Connect(ctx context.Context, sessionID string, shimSocket string) error
}

type ProcessState struct {
    PID       int
    Status    string  // "running" | "stopped" | "unknown"
    ExitCode  *int    // 仍在运行时为 nil
    StartedAt time.Time
}
```

### Start 的工作流程

```
ProcessManager.Start(sessionID):
  1. 解析 session → 获取 runtimeClass、workspace 路径、systemPrompt、prompt、mcpServers、permissions
  2. 查找 runtimeClass 配置 → 获取 command、args、env、capabilities
  3. 生成 OAR Runtime Spec（config.json）：
       acpAgent:
         systemPrompt = session.systemPrompt
         process:
           command = runtimeClass.command
           args    = runtimeClass.args
           env     = merge(runtimeClass.env, session.env)  // 解析 ${VAR}
         session:
           mcpServers = session.mcpServers
       agentRoot.path = "workspace"    // 固定的相对路径
       permissions    = session.permissions
  4. 创建 bundle 目录，写入 config.json
  5. 创建 workspace 符号链接：bundle/workspace → workspace.path
     （由 Workspace Manager 提供，agentd 在此处链接到 bundle）
  6. fork/exec shim 进程：
       agent-shim --bundle <bundle-dir> \
         --id <sessionID> --state-dir /run/agentd/shim
       # socket: /run/agentd/shim/<sessionID>/agent-shim.sock
  6. shim 内部执行：
       → 读取 config.json
       → 启动 agent 进程（acpAgent.process）
       → ACP initialize 握手
       → ACP session/new（systemPrompt + workspace + mcpServers）
       → 创建 RPC socket，等待 agentd 连接
  7. agentd 连接到 shim socket
  8. 调用 shim.Subscribe() 订阅 typed event stream
  9. 更新 session 状态 → Running
```

**对标 containerd**：containerd 的 Task Service 在启动容器时也要先生成 config.json，
写入 bundle 目录，然后启动 shim，shim 再启动 runc/容器。agentd 的 Process Manager
做完全相同的事情。

## Room Manager

**参照**：containerd 的 Sandbox Controller（CRI PodSandbox）。

当 kubelet 创建 Pod 时，containerd 创建 PodSandbox（pause 容器）来持有共享的 namespace。
业务容器加入这些 namespace。

agentd 的 Room Manager 追踪 room 成员关系并提供共享通信层。
和 PodSandbox 不同，这里没有"pause agent" — room 是纯粹的元数据 + 消息路由。

### 接口

```go
type RoomManager interface {
    // CreateRoom 注册一个 room。Session 单独添加。
    CreateRoom(ctx context.Context, name string, opts RoomOpts) (*Room, error)

    // GetRoom 返回 room 元数据。
    GetRoom(ctx context.Context, name string) (*Room, error)

    // ListRooms 返回所有 room。
    ListRooms(ctx context.Context) ([]*Room, error)

    // DeleteRoom 移除 room 元数据。所有成员 session 必须先停止。
    DeleteRoom(ctx context.Context, name string) error

    // RouteMessage 从一个 room agent 向另一个发送消息。
    // 这是 room_send MCP tool 的后端实现。
    RouteMessage(ctx context.Context, room string, from string, to string, message string) (*PromptResult, error)

    // BroadcastMessage 向 room 内除发送者外的所有 agent 发送消息。
    BroadcastMessage(ctx context.Context, room string, from string, message string) error
}

type RoomOpts struct {
    Labels        map[string]string
    Communication Communication
}

type Room struct {
    Name          string
    Labels        map[string]string
    Communication Communication
    Members       []RoomMember  // 从 session 元数据填充
    CreatedAt     time.Time
}

type RoomMember struct {
    AgentName string
    SessionID string
    State     SessionState
}
```

### MCP Tool 注入

当一个 session 属于某个 room 时，agentd 注入 room 级别的 MCP tool。
注入机制因 agent 而异，推迟到后续设计阶段。
MCP tool 的调用通过 agentd 的 Room Manager 路由：

```
Agent A 调用 room_send("coder", "写测试")
  → agentd 中的 MCP tool handler
  → RoomManager.RouteMessage(room, "architect", "coder", "写测试")
  → 解析 "coder" → session ID
  → ProcessManager.Attach(sessionID) → ACPChannel
  → ACPChannel.SendRequest("session/prompt", message)
  → 将结果返回给 Agent A 的 MCP tool 调用
```

## ACP Client 模型与 Attach 语义

### agent-shim 是 ACP Client

ACP 基于 stdio JSON-RPC，天然是 1:1 的——一个 agent 进程，一个 client，一条 pipe。
每个 agent 有独立的 shim 进程持有其 stdio，作为唯一的 ACP client。

```
agentd（可重启，不影响 agent）
   │
   │  RPC over Unix socket
   ▼
agent-shim 进程（独立，持有 agent stdio）
   │  ACP over stdio
   ▼
agent 进程
```

这与 containerd-shim 的模型完全一致：

| | containerd | agentd |
|---|---|---|
| 守护进程 | containerd | agentd |
| 中间层 | containerd-shim | agent-shim |
| 工作负载 | 容器进程 | agent 进程 |
| 守护进程重启 | shim 和容器存活 | shim 和 agent 存活 |
| 通信协议 | containerd ↔ shim: ttrpc | agentd ↔ shim: RPC |
| | shim ↔ 容器: stdio | shim ↔ agent: ACP stdio |

### 爆炸半径隔离

每个 agent 有独立的 shim 进程。agentd 重启时：

1. 所有 shim 进程和 agent 进程不受影响
2. agentd 扫描 `/run/agentd/shim/*/agent-shim.sock`
3. 重新连接到每个 shim 的 RPC socket
4. 调用 `GetState()` 恢复 session 元数据
5. 重新订阅 typed event stream

这与 containerd 重启后通过 shim socket 重新连接的机制完全一致。

### fs / terminal 由 agent-shim 处理

ACP 是双向协议。agent 不只接收 prompt，也会主动向 client 发请求：

| Agent 发出的请求 | 说明 |
|---|---|
| `fs/read_text_file` | 读取文件内容 |
| `fs/write_text_file` | 写入文件 |
| `terminal/execute` | 执行 shell 命令 |
| `session/update` | 流式推送 thinking / content / tool_call |

agent-shim 作为 ACP client，**始终处理**这些请求。权限策略在 session 创建时确定：

```yaml
# session/new 参数
permissions: approve-all   # 所有 fs/terminal 操作自动批准
# 或
permissions: approve-reads  # 只读操作自动批准，写操作 deny
# 或
permissions: deny-all       # 所有操作 deny（只允许 session/update 流出）
```

**不支持运行时交互式审批**。agent-shim 按策略执行，不等待外部确认。
需要交互式权限审批的场景（如手动审批每个文件写入），应使用 toad 等工具直接
启动 agent，由工具本身作为 ACP client。

### Attach 的正确模型

Attach 不是 IO 接管，而是通过 agentd 订阅 shim 的事件流并注入 prompt：

```
agent process
      │  ACP stdio
      ▼
agent-shim（ACP client，持有 stdio）
      │  RPC
      ▼
agentd（RPC 客户端，连接多个 shim）
      │  ARI
      ▼
orchestrator / agent-shim-cli / TUI
```

当 TUI attach 到一个 session 时：

1. agentd 通过 shim RPC 调用 `Subscribe()` 订阅该 session 的 **typed event stream**
2. agentd 将 typed events（ThinkingEvent、TextEvent、ToolCallEvent 等）通过 ARI 转发给 TUI
3. TUI 发送 `session/prompt` 时，agentd 通过 shim RPC 调用 `Prompt()` 注入

多个 TUI 可以同时 attach 同一个 session（agentd fan-out），均可收到 typed events。
`session/prompt` 注入会进入 shim 内部队列，串行转发给 agent，不存在竞争。

**重要**：agentd 消费的是 agent-shim 翻译后的 typed events，不是 raw ACP 消息。
ACP 是 agent-shim 的实现细节，agentd 不感知也不依赖 ACP 协议。
详见 [agent-shim: Typed Events](../runtime/agent-shim.md#acp-是实现细节typed-events-是核心协议)。

### 适用边界

agentd 管理的是 **headless / orchestrated** 场景：

- 无人值守的 agent 任务
- orchestrator 驱动的多 agent 协作（Room）
- 外部系统通过 ARI 注入 prompt 并订阅输出

需要以下能力的场景**不属于 agentd 的范围**，应使用 toad / acpx 等工具：

- 运行时交互式权限审批
- 直接操作 agent 的 ACP 消息（如自定义 `fs/*` 响应逻辑）
- agent 需要与本地编辑器深度集成（文件树、diff 视图等）

两个模式不冲突：toad 直接启动 agent 自己作为 ACP client；agentd 管理的 session
是 headless 的，attach 只是观察和注入，不接管控制权。

## Metadata Store

**参照**：containerd 使用 BoltDB 存储元数据（containers、images、snapshots、leases）。

agentd 持久化存储 session、workspace 和 room 的元数据。
存储引擎的选择是实现细节。候选方案：SQLite、BoltDB、平面文件。

### 存储内容

| 数据 | 是否持久化 | 存储位置 |
|------|-----------|---------|
| Session 元数据（id、runtimeClass、workspace、labels、state） | 是 | Meta DB |
| Workspace 元数据（id、path、source、refs） | 是 | Meta DB |
| Room 元数据（name、members、communication mode） | 是 | Meta DB |
| 进程状态（pid、status） | 否 | agent-shim state.json 在 /run/（tmpfs） |
| ACP 事件流 | 可选 | Event log（追加写文件） |

### 与 containerd 存储的对比

```
containerd:                            agentd:
/var/lib/containerd/                   /var/lib/agentd/
├── io.containerd.metadata.v1.bolt/    ├── meta.db
│   └── meta.db (BoltDB)              │
├── io.containerd.content.v1.content/  │   （无 content store — 无镜像）
├── io.containerd.snapshotter.v1.*/    │   （无 snapshotter）
├── io.containerd.runtime.v2.task/     ├── bundles/
│   └── <id>/                          │   └── <session-id>/
│       └── config.json (OCI)          │       └── config.json (OAR)
└── ...                                ├── workspaces/
                                       │   ├── ws-abc123/
                                       │   └── ...
/run/containerd/                       /run/agentd/
├── containerd.sock                    ├── agentd.sock
└── ...                                └── ...

/run/runc/                             /run/agentd/shim/
└── <id>/state.json                    └── <id>/state.json
```

## RuntimeClass — Agent 类型注册

**参照**：K8s RuntimeClass + containerd runtime handler 注册。

在 K8s/containerd 生态中，"用什么 runtime 跑容器"不是写在 OCI config.json 里的，
而是在 Pod Spec 中声明，由 containerd 解析后生成具体的 config.json：

```
K8s 流程：
  1. 集群管理员创建 RuntimeClass 资源：
       name: kata, handler: kata

  2. containerd config.toml 注册 handler：
       [plugins.cri.containerd.runtimes.kata]
         runtime_type = "io.containerd.kata.v2"

  3. Pod Spec 引用：
       runtimeClassName: kata

  4. kubelet → containerd → 查找 kata handler
     → 生成 config.json（具体的启动参数）
     → runc 读取 config.json 并执行
```

agentd 使用相同的模式。`runtimeClass` 是 orchestrator 传给 agentd 的参数
（通过 ARI 请求），不在 OAR Runtime Spec（config.json）中。
agentd 解析 runtimeClass 后生成具体的 config.json 传给 agent-shim：

```
agentd 流程：
  1. agentd config.yaml 注册 runtimeClass：
       runtimeClasses:
         claude:
           command: "npx"
           args: ["-y", "@anthropic-ai/claude-code-acp"]

  2. orchestrator → ARI session/new { runtimeClass: "claude", systemPrompt: "..." }

  3. agentd 查找 claude handler
     → 生成 config.json：
         acpAgent.process.command = "npx"
         acpAgent.process.args = ["-y", "@anthropic-ai/claude-code-acp"]
         acpAgent.systemPrompt = "..."
         workspace = workspace path
     → agent-shim 读取 config.json 并执行
```

**关键设计**：`runtimeClass` 不出现在 Runtime Spec 中。
Runtime Spec 中是**已解析的具体值**（`acpAgent.process.command`、`acpAgent.process.args`）。
这和 OCI config.json 不包含 `runtimeClassName` 而是包含具体的
`process.args` 是完全一致的。

### 配置

```yaml
# /etc/agentd/config.yaml

runtimeClasses:

  # 原生 ACP agent — 直接启动
  gemini:
    command: "gemini"
    capabilities:
      streaming: true
      sessionLoad: false
      concurrentSessions: 1

  # ACP wrapper (shim) — 通过 wrapper 启动
  claude:
    command: "npx"
    args: ["-y", "@anthropic-ai/claude-code-acp"]
    env:
      ANTHROPIC_API_KEY: "${ANTHROPIC_API_KEY}"
    capabilities:
      streaming: true
      sessionLoad: true
      concurrentSessions: 1

  codex:
    command: "npx"
    args: ["-y", "codex-acp"]
    capabilities:
      streaming: true
      sessionLoad: false
      concurrentSessions: 1

  # 通过 pi-acp shim 接入的 GSD
  gsd:
    command: "npx"
    args: ["-y", "pi-acp"]
    env:
      PI_ACP_PI_COMMAND: "gsd"
    capabilities:
      streaming: true
      sessionLoad: false
      concurrentSessions: 1
```

每个 runtimeClass 条目：

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `command` | string | 是 | ACP agent 的可执行文件 |
| `args` | []string | 否 | 启动参数 |
| `env` | map[string]string | 否 | 环境变量。`${VAR}` 引用宿主环境 |
| `capabilities` | object | 否 | 此 ACP agent 的能力声明 |

**`capabilities` 字段**：

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `streaming` | bool | true | 支持 `session/update` 流式推送 |
| `sessionLoad` | bool | false | 支持 `session/load` 恢复 |
| `concurrentSessions` | int | 1 | 单进程支持的最大并发 session 数 |

### 对比

| | K8s/containerd | agentd |
|---|---|---|
| **声明位置** | Pod Spec: `runtimeClassName` | ARI 请求: `runtimeClass` |
| **是否在底层 spec 中** | 不在 OCI config.json 中 | 不在 OAR config.json 中 |
| **注册位置** | containerd config.toml | agentd config.yaml |
| **Handler 内容** | runtime_type + options | command + args + env + capabilities |
| **解析结果** | 生成 OCI config.json 传给 runc | 生成 OAR config.json（acpAgent）传给 agent-shim |
| **默认值** | runc（不指定时） | 必须显式指定 |
| **解析流程** | kubelet → containerd → handler → config.json | orchestrator → agentd → runtimeClass → config.json |

### 接口

```go
type RuntimeClassRegistry interface {
    // Get 返回指定名称的 RuntimeClass 配置。
    Get(ctx context.Context, name string) (*RuntimeClass, error)

    // List 返回所有已注册的 RuntimeClass。
    List(ctx context.Context) ([]*RuntimeClass, error)
}

type RuntimeClass struct {
    Name         string            `yaml:"name"`
    Command      string            `yaml:"command"`
    Args         []string          `yaml:"args,omitempty"`
    Env          map[string]string `yaml:"env,omitempty"`
    Capabilities Capabilities      `yaml:"capabilities,omitempty"`
}

type Capabilities struct {
    Streaming          bool `yaml:"streaming"`
    SessionLoad        bool `yaml:"sessionLoad"`
    ConcurrentSessions int  `yaml:"concurrentSessions"`
}
```

### Session 创建流程

```
ARI session/new {
  runtimeClass: "claude",
  workspaceId: "ws-abc123",
  systemPrompt: "你是后端工程师",
  prompt: "重构 auth 模块",
  env: ["GITHUB_TOKEN=${GITHUB_TOKEN}"]
}:

  1. 在 runtimeClasses 注册表中查找 "claude"
     → { command: "npx", args: ["-y", "@anthropic-ai/claude-code-acp"], env: {...} }
  2. 未找到 → 返回错误：unknown runtimeClass "claude"
  3. 创建 session 元数据（SessionManager.Create）
  4. 生成 OAR Runtime Spec（config.json）并写入 bundle 目录：
       acpAgent.systemPrompt = "..."                 ← 来自请求
       acpAgent.process.command = "npx"             ← 来自 runtimeClass 配置
       acpAgent.process.args = ["-y", "..."]        ← 来自 runtimeClass 配置
       acpAgent.process.env = 合并后的 env          ← runtimeClass.env + 请求中的 env
       acpAgent.session.mcpServers = [...]           ← 来自请求
       agentRoot.path = "workspace"                  ← 固定的相对路径
  5. 创建 workspace 符号链接：bundle/workspace → workspace 实际路径
  6. fork/exec agent-shim --bundle <dir>
     agent-shim 内部完成：进程启动 → ACP 握手 → session/new
  7. 返回 session ID
```

**对标 containerd**：

```
containerd 处理 CRI CreateContainer：
  1. 解析 runtimeClassName → handler 配置
  2. 从 image config 提取 entrypoint、env、labels
  3. 合并 Pod Spec overrides（env、mounts 等）
  4. 生成 OCI config.json
  5. 调用 runc create --bundle <dir>

agentd 处理 ARI session/new：
  1. 解析 runtimeClass → handler 配置（command、args、env）
  2. 合并请求参数（env、mcpServers、systemPrompt）
  3. 生成 OAR config.json（agentRoot + acpAgent）
  4. 创建 bundle 目录，创建 workspace 符号链接
  5. fork/exec agent-shim --bundle <dir>
```

## Session 生命周期

### 状态机

```
                 ┌──────────┐
                 │ Created  │
                 └────┬─────┘
                      │ session/prompt（首次 turn）
                      ▼
                 ┌──────────┐
       ┌─────── │ Running  │ ◄───────┐
       │        └────┬─────┘         │
       │             │               │
       │             │ turn 完成     │ session/prompt（下一个 turn）
       │             ▼               │
       │      ┌─────────────┐        │
       │      │ Paused:Warm │ ───────┘
       │      │（进程常驻） │
       │      └──────┬──────┘
       │             │ 空闲超时
       │             ▼
       │      ┌─────────────┐
       │      │ Paused:Cold │ ───────┐
       │      │（进程已退出）│        │ session/load + session/prompt
       │      └─────────────┘        │（重启进程，恢复 session）
       │                             ▼
       │                       ┌──────────┐
       │                       │ Running  │
       │                       └──────────┘
       │
       │ stop / error / timeout
       ▼
  ┌──────────┐
  │ Stopped  │
  └──────────┘
```

状态转换由 Session Manager + Process Manager 协作驱动。

## 配置

```yaml
# /etc/agentd/config.yaml

# Unix socket 路径
socket: /run/agentd/agentd.sock

# Workspace 存储根目录
workspaceRoot: /var/lib/agentd/workspaces

# 元数据存储路径
metaDB: /var/lib/agentd/meta.db

# 底层运行时
runtime: agent-shim

# Session 策略
sessionPolicy:
  warmIdleTimeout: 300s
  maxWarmSessions: 5
  coldRetentionTimeout: 24h

# RuntimeClass 注册（核心配置）
runtimeClasses:
  claude:
    command: "npx"
    args: ["-y", "@anthropic-ai/claude-code-acp"]
    env:
      ANTHROPIC_API_KEY: "${ANTHROPIC_API_KEY}"
    capabilities:
      streaming: true
      sessionLoad: true
      concurrentSessions: 1

  gemini:
    command: "gemini"
    capabilities:
      streaming: true

  gsd:
    command: "npx"
    args: ["-y", "pi-acp"]
    env:
      PI_ACP_PI_COMMAND: "gsd"
    capabilities:
      streaming: true
```
