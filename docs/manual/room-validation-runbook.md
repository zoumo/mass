# Room 多 Agent 验证操作手册

## 场景

在同一个 Room 中拉起三个 Agent，协作完成 code review → 复查 → 修复：

| Agent 名称 | RuntimeClass | 角色 | ACP Adapter |
|---|---|---|---|
| **reviewer** | `codex` | 深度 code review，产出问题列表 | `@zed-industries/codex-acp` (Node, via bunx) |
| **checker** | `claude-code` | 复查 review 结果，标注优先级 | `@agentclientprotocol/claude-agent-acp` (Node) |
| **fixer** | `gsd-pi` | 按复查结果执行修复 | `pi-acp` (Node) |

## 架构概览

```
agentdctl (CLI)
    │
    ▼
agentd (daemon, Unix socket)
    ├── agent/create  ──► 创建 Agent + 关联 Session + 启动 shim（后台异步）
    ├── agent/prompt  ──► 查找关联 session，通过 shim 投递 prompt
    ├── room/send     ──► 按 agent 名称在 Room 内路由消息
    └── room/delete   ──► 级联清理：停止 agent → 删除 session → 删除 room
```

**核心概念：**
- **Agent** 是首要抽象。`agent/create` 自动在后台完成：创建关联 Session、获取 workspace 引用、启动 shim 进程。
- **Session** 是内部实现细节。Room 场景下不需要手动创建 session。
- **Agent 状态机**：`creating` → `created` → `running` → `stopped` / `error`
- **通信模式**：`mesh`（默认，全互联）、`star`（星型，中心辐射）、`isolated`（隔离，禁止跨 agent 通信）

## 前置条件

### 1. 构建 OAR 二进制

```bash
cd /Users/jim/code/zoumo/open-agent-runtime

# 一次性构建所有二进制
make build

# 验证产物（agentd, agent-shim, agent-shim-cli, agentdctl, room-mcp-server）
ls bin/
```

### 2. 验证 ACP Adapter

三个 adapter 均为 Node 包，通过 bunx 运行：

```bash
# codex adapter
bunx @zed-industries/codex-acp --help

# claude-code adapter
bunx @agentclientprotocol/claude-agent-acp --help

# gsd-pi adapter
bunx pi-acp --help
```

### 3. 环境变量

```bash
# Anthropic API Key（claude-code 和 gsd-pi 需要）
export ANTHROPIC_API_KEY="your-key"

# OpenAI API Key（codex 需要）
export OPENAI_API_KEY="your-key"

# Shim 二进制路径（agentd 用它来启动 shim 进程）
export OAR_SHIM_BINARY=$(pwd)/bin/agent-shim
```

### 4. 验证配置

预配置环境位于 `bin/room-validating/`：

```
bin/room-validating/
├── config.yaml        # agentd 配置：socket、DB、runtime class 定义
├── workspaces/        # workspace 根目录
├── setup-room.sh      # 一键创建 workspace + room + agent 并等待就绪
├── teardown-room.sh   # 一键停止 agent + 删除 room + 清理 workspace
└── agentd.db          # SQLite 元数据库（自动创建）
```

配置文件（`bin/room-validating/config.yaml`）：

```yaml
socket: /Users/jim/code/zoumo/open-agent-runtime/bin/room-validating/agentd.sock
workspaceRoot: /Users/jim/code/zoumo/open-agent-runtime/bin/room-validating/workspaces
metaDB: /Users/jim/code/zoumo/open-agent-runtime/bin/room-validating/agentd.db

runtimeClasses:
  codex:
    command: bunx
    args:
    - "@zed-industries/codex-acp"
    env: {}
  claude-code:
    command: bunx
    args:
     - "@agentclientprotocol/claude-agent-acp"
    env: {}
  gsd-pi:
    command: bunx
    args:
      - "pi-acp"
    env:
      PI_ACP_PI_COMMAND: gsd
      PI_CODING_AGENT_DIR: /Users/jim/.gsd/agent
```

> **注意：** `env: {}` 表示该 runtime class 继承父进程环境变量（包括 API Key）。

## 快速开始（脚本一键操作）

启动 agentd 后，可直接用脚本完成 setup 和 teardown：

```bash
# Terminal 1：启动 daemon
export OAR_SHIM_BINARY=$(pwd)/bin/agent-shim
./bin/agentd --config bin/room-validating/config.yaml

# Terminal 2：一键创建 workspace + room + 3 个 agent
./bin/room-validating/setup-room.sh
# 可选：指定 workspace 源路径
# ./bin/room-validating/setup-room.sh /path/to/your/project

# 脚本会输出 export 命令，复制到 shell 中即可使用 $CTL、$REVIEWER_ID 等变量
# 然后就可以执行 prompt / room send 等操作（见 Step 7）

# 清理：一键停止所有 agent + 删除 room + 清理 workspace
./bin/room-validating/teardown-room.sh
```

> 如需了解每一步的细节，请继续阅读下方的分步操作。

## 分步操作

### Step 1：启动 agentd

```bash
# Terminal 1 — 启动 daemon
export OAR_SHIM_BINARY=$(pwd)/bin/agent-shim
./bin/agentd --config bin/room-validating/config.yaml
```

预期输出：
```
agentd: loaded config from bin/room-validating/config.yaml
agentd: socket=...agentd.sock workspaceRoot=...workspaces
agentd: metadata store initialized at ...agentd.db
agentd: session recovery complete
agentd: starting ARI server on ...agentd.sock
```

### Step 2：准备 Workspace

```bash
# Terminal 2 — 后续所有命令都在这个终端执行
SOCK=$(pwd)/bin/room-validating/agentd.sock
CTL="./bin/agentdctl --socket $SOCK"

# 准备共享 workspace（local 模式 — 软链接到项目目录）
$CTL workspace prepare \
  --name oar-project \
  --type local \
  --path /Users/jim/code/zoumo/open-agent-runtime
```

记录返回的 `workspaceId`：
```bash
# 示例输出：{"workspaceId":"abc-123","path":"...","status":"ready"}
WS_ID="<输出中的 workspaceId>"
```

### Step 3：创建 Room

```bash
$CTL room create --name code-review --mode mesh
```

预期输出：
```json
{"name":"code-review","communicationMode":"mesh","createdAt":"..."}
```

### Step 4：创建三个 Agent

> **重要：** `agent create` 会立即返回 `state: "creating"`。shim 进程在后台协程中启动（90 秒超时）。必须轮询 `agent status` 等到状态变为 `"created"` 后才能发送 prompt。

```bash
# 1. Codex — 深度 review agent
$CTL agent create \
  --room code-review \
  --name reviewer \
  --runtime-class codex \
  --workspace-id "$WS_ID" \
  --description "Deep code reviewer"
# 记录返回的 agentId → $REVIEWER_ID

# 2. Claude Code — 复查 agent
$CTL agent create \
  --room code-review \
  --name checker \
  --runtime-class claude-code \
  --workspace-id "$WS_ID" \
  --description "Review cross-checker"
# 记录返回的 agentId → $CHECKER_ID

# 3. GSD — 修复 agent
$CTL agent create \
  --room code-review \
  --name fixer \
  --runtime-class gsd-pi \
  --workspace-id "$WS_ID" \
  --description "Code fixer"
# 记录返回的 agentId → $FIXER_ID
```

### Step 5：等待 Agent 就绪

轮询每个 agent，直到状态从 `"creating"` 变为 `"created"`：

```bash
# 检查每个 agent 的状态
$CTL agent status $REVIEWER_ID
$CTL agent status $CHECKER_ID
$CTL agent status $FIXER_ID

# 所有 agent 都应显示："state": "created"
# 如果出现 "error"，请查看 agentd 日志了解启动失败原因。
```

### Step 6：验证 Room 成员

```bash
$CTL room status --name code-review
```

预期输出包含一个 `members` 数组，列出全部三个 agent：
```json
{
  "name": "code-review",
  "communicationMode": "mesh",
  "members": [
    {"agentName": "reviewer", "runtimeClass": "codex", "agentState": "created"},
    {"agentName": "checker", "runtimeClass": "claude-code", "agentState": "created"},
    {"agentName": "fixer", "runtimeClass": "gsd-pi", "agentState": "created"}
  ]
}
```

### Step 7：执行 Review 流程

> **关键行为：**
> - `agent prompt` 是**同步阻塞**的 — 命令会阻塞直到 agent 完成处理（返回 `stopReason`）。
> - 默认 prompt 超时 **120 秒**。
> - Agent 状态转换：`created` → `running`（处理中）→ `created`（完成后回到空闲）。

#### 7.1 让 Codex 做深度 Code Review

```bash
$CTL agent prompt $REVIEWER_ID --text '
对 pkg/ari/server.go 做深度 code review。关注以下方面：
1. 错误处理是否完整
2. 并发安全问题
3. 资源泄漏风险
4. API 设计一致性
5. 边界情况处理

输出格式：每个问题用 [P0/P1/P2] 标注严重等级，附带具体行号和修复建议。
完成后，调用 room_send 工具把 review 结果发给 checker。
'
```

#### 7.2（备选）手动中继消息

如果 agent 没有自动调用 `room_send`（取决于 ACP adapter 是否支持 MCP tool 调用），需要手动中继结果：

```bash
$CTL room send \
  --room code-review \
  --from reviewer \
  --to checker \
  --text "<粘贴 codex 的 review 输出>"
```

> **room/send 工作原理：** 消息会被添加归因前缀 `[room:code-review from:reviewer]`，然后通过 `deliverPrompt` 投递到 checker agent 的关联 session。这是同步的 — 命令会阻塞直到 checker 处理完毕。

#### 7.3 让 Claude Code 复查

```bash
$CTL agent prompt $CHECKER_ID --text '
你收到了 reviewer agent 的 code review 结果。请：
1. 验证每个发现是否属实（检查实际代码）
2. 标注是否同意 P0/P1/P2 评级
3. 过滤掉误报
4. 补充可能遗漏的问题
5. 产出最终的修复清单，按优先级排序

完成后，调用 room_send 把最终修复清单发给 fixer。
'
```

#### 7.4 让 GSD 执行修复

```bash
$CTL agent prompt $FIXER_ID --text '
你收到了经过复查确认的 code review 修复清单。请：
1. 按 P0 → P1 → P2 顺序逐个修复
2. 每个修复后运行相关测试确认不破坏现有功能
3. 完成后给出修复总结
'
```

### Step 8：清理

清理遵循严格顺序：**停止 agent → 删除 room**（room/delete 会级联删除 agent 和 session）。

```bash
# 停止所有 agent
$CTL agent stop $REVIEWER_ID
$CTL agent stop $CHECKER_ID
$CTL agent stop $FIXER_ID

# 删除 room（级联：删除已停止的 agent 及其关联 session）
$CTL room delete --name code-review

# 清理 workspace（仅在 ref_count == 0 时可执行）
$CTL workspace cleanup "$WS_ID"
```

> **注意：** 如果有 agent 处于非 stopped/error 状态，`room/delete` 会拒绝删除。它会自动删除所有已停止或出错的 agent 及其关联 session，然后才删除 room 本身。

---

## CLI 参考

### agentdctl 命令一览

| 命令 | 说明 |
|---|---|
| `agentdctl workspace prepare` | 准备 workspace（--name, --type, --path/--url） |
| `agentdctl workspace list` | 列出所有 workspace |
| `agentdctl workspace cleanup <id>` | 清理 workspace |
| `agentdctl room create` | 创建 room（--name, --mode） |
| `agentdctl room status` | 查看 room 状态和成员（--name） |
| `agentdctl room send` | 在 agent 之间发送消息（--room, --from, --to, --text） |
| `agentdctl room delete` | 删除 room（--name） |
| `agentdctl agent create` | 创建 agent（--room, --name, --runtime-class, --workspace-id） |
| `agentdctl agent list` | 列出 agent（--room, --state） |
| `agentdctl agent status <id>` | 查看 agent 状态及 shim 运行信息 |
| `agentdctl agent prompt <id>` | 向 agent 发送 prompt（--text） |
| `agentdctl agent cancel <id>` | 取消 agent 当前 turn |
| `agentdctl agent stop <id>` | 停止 agent |
| `agentdctl agent restart <id>` | 重启已停止/出错的 agent |
| `agentdctl agent delete <id>` | 删除 agent（必须先停止） |
| `agentdctl agent attach <id>` | 获取 shim socket 路径 |
| `agentdctl daemon status` | 检查 daemon 健康状态 |

所有命令都支持 `--socket <path>` 来指定 ARI Unix socket 路径。

### ARI JSON-RPC 方法（直接调用）

如需绕过 CLI 直接发送 JSON-RPC（例如通过 `socat`）：

```bash
SOCK=$(pwd)/bin/room-validating/agentd.sock

# room/create
echo '{"jsonrpc":"2.0","id":1,"method":"room/create","params":{"name":"code-review","communication":{"mode":"mesh"}}}' \
  | socat - UNIX-CONNECT:$SOCK

# agent/create
echo '{"jsonrpc":"2.0","id":2,"method":"agent/create","params":{"room":"code-review","name":"reviewer","runtimeClass":"codex","workspaceId":"...","description":"reviewer"}}' \
  | socat - UNIX-CONNECT:$SOCK

# agent/prompt
echo '{"jsonrpc":"2.0","id":3,"method":"agent/prompt","params":{"agentId":"...","prompt":"review the code"}}' \
  | socat - UNIX-CONNECT:$SOCK

# room/send
echo '{"jsonrpc":"2.0","id":4,"method":"room/send","params":{"room":"code-review","senderAgent":"reviewer","targetAgent":"checker","message":"review results..."}}' \
  | socat - UNIX-CONNECT:$SOCK

# room/status
echo '{"jsonrpc":"2.0","id":5,"method":"room/status","params":{"name":"code-review"}}' \
  | socat - UNIX-CONNECT:$SOCK
```

---

## 简化验证（两个 Agent）

如果 codex-acp 不可用，可以用两个 agent 验证核心流程：

| Agent | RuntimeClass | 角色 |
|---|---|---|
| **reviewer** | `claude-code` | Code review + 复查 |
| **fixer** | `gsd-pi` | 执行修复 |

流程：
1. Claude 做 review → `room send` 给 fixer
2. GSD 执行修复

这仍然能验证 Room 路由、Agent 生命周期和跨 Agent 通信的核心能力。

---

## 诊断检查表

| 检查项 | 正常表现 | 异常信号 |
|---|---|---|
| `agent create` 返回 | `state: "creating"`，< 1 秒 | 超时 → ARI socket 问题 |
| 创建后 ~10 秒 `agent status` | `state: "created"` | `state: "error"` → 查看 agentd 日志 |
| `agent prompt` 响应 | 1-2 分钟内返回 | > 5 分钟 → API Key 或网络问题 |
| `room send` 投递 | `delivered: true` + `stopReason` | `target agent not found` → agent 不在 room 中 |
| `room status` 成员列表 | 三个 agent 全部显示 | 缺少 agent → `agent create` 失败 |
| `room delete` | 成功 | `has active members` → 先停止 agent |
| `daemon status` | `daemon: running` | `not running` → 检查 socket 路径 |

## Agent 异常恢复

如果 agent 进入 `"error"` 状态：

```bash
# 查看错误原因
$CTL agent status $AGENT_ID

# 重启 agent（后台重新创建 session 和 shim）
$CTL agent restart $AGENT_ID

# 轮询直到重新就绪
$CTL agent status $AGENT_ID
# 等待 state: "created"
```

## 已知限制

- **`agent/prompt` 同步阻塞** — 调用方阻塞直到 agent 完成当前 turn（默认 120 秒超时）。
- **`room/send` 同步阻塞** — 发送方阻塞直到目标 agent 处理完消息。
- **仅支持点对点** — 没有广播功能，每次 `room send` 只能发给一个 agent。
- **消息归因是文本前缀** — `[room:code-review from:reviewer]` 是 prepend 在 prompt 前面的，不是结构化元数据。
- **MCP tool 支持因 adapter 而异** — agent 能否自主调用 `room_send` 取决于 ACP adapter 实现。不支持时需要走手动中继路径。
- **启动超时** — `agent/create` 后台协程有 90 秒超时。如果 shim 启动超时，agent 进入 `"error"` 状态。
- **恢复保护** — daemon 启动恢复期间，操作类请求（`agent/prompt`、`room/send`）会被阻塞，直到恢复完成。
