# 多 Agent 协作验证操作手册

## 场景

在同一个 Workspace 中拉起三个 Agent，协作完成 code review → 复查 → 修复：

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
    ├── workspace/create  ──► 创建 workspace（后台异步准备）
    ├── workspace/send    ──► 在 workspace 内按 agent 名称路由消息
    ├── workspace/delete  ──► 删除 workspace（需先停止所有 agent）
    ├── agent/create      ──► 创建 Agent + 启动 shim（后台异步）
    ├── agent/prompt      ──► fire-and-forget 投递 prompt
    └── agent/stop        ──► 停止 agent
```

**核心概念：**
- **Workspace** 是共享执行环境（git 仓库、本地目录或空目录），多个 Agent 可以共用同一个 Workspace。
- **Agent** 的身份是 `workspace/name` 复合键（无 UUID）。所有命令参数格式均为 `<workspace>/<agent-name>`。
- **Agent 状态机**：`creating` → `idle` → `running` → `stopped` / `error`
- **跨 Agent 通信**：通过 `workspace send` 在同一 workspace 内的 agent 之间路由消息。
- **agent/prompt 是异步的**：命令立即返回 `{accepted: true}`，agent 在后台处理。加 `--wait` flag 可轮询至完成。

## 前置条件

### 1. 构建 OAR 二进制

```bash
cd /Users/jim/code/zoumo/open-agent-runtime

# 一次性构建所有二进制
make build

# 验证产物
ls bin/
# agentd  agent-shim  agent-shim-cli  agentdctl workspace-mcp-server
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
├── bundles/           # agent bundle 根目录
├── setup-room.sh      # 一键创建 workspace + 3 个 agent 并等待就绪
├── teardown-room.sh   # 一键停止 agent + 删除 workspace
└── agentd.db          # SQLite 元数据库（自动创建）
```

配置文件（`bin/room-validating/config.yaml`）：

```yaml
socket: /Users/jim/code/zoumo/open-agent-runtime/bin/room-validating/agentd.sock
workspaceRoot: /Users/jim/code/zoumo/open-agent-runtime/bin/room-validating/workspaces
metaDB: /Users/jim/code/zoumo/open-agent-runtime/bin/room-validating/agentd.db
bundleRoot: /Users/jim/code/zoumo/open-agent-runtime/bin/room-validating/bundles

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

---

## 快速开始（脚本一键操作）

启动 agentd 后，可直接用脚本完成 setup 和 teardown：

```bash
# Terminal 1：启动 daemon
export OAR_SHIM_BINARY=$(pwd)/bin/agent-shim
./bin/agentd --config bin/room-validating/config.yaml

# Terminal 2：一键创建 workspace + 3 个 agent
./bin/room-validating/setup-room.sh
# 可选：指定 workspace 源路径
# ./bin/room-validating/setup-room.sh /path/to/your/project

# 脚本会输出 export 命令，复制到 shell 中即可使用 $CTL 等变量
# 然后就可以执行 agent prompt / workspace send 等操作（见 Step 6）

# 清理：一键停止所有 agent + 删除 workspace
./bin/room-validating/teardown-room.sh
```

> 如需了解每一步的细节，请继续阅读下方的分步操作。

---

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

### Step 2：创建 Workspace

```bash
# Terminal 2 — 后续所有命令都在这个终端执行
SOCK=$(pwd)/bin/room-validating/agentd.sock
CTL="./bin/agentdctl --socket $SOCK"

# 创建共享 workspace（local 模式 — 软链接到项目目录）
$CTL workspace create \
  --name oar-project \
  --type local \
  --path /Users/jim/code/zoumo/open-agent-runtime
```

预期输出：
```json
{"name":"oar-project","phase":"pending"}
```

等待 workspace 就绪：
```bash
# 轮询直到 phase 变为 "ready"
$CTL workspace list
# 预期：{"workspaces":[{"name":"oar-project","phase":"ready","path":"..."}]}
```

### Step 3：创建三个 Agent

> **重要：** `agent create` 会立即返回 `state: "creating"`。shim 进程在后台协程中启动。必须轮询 `agent status` 等到状态变为 `"idle"` 后才能发送 prompt。

```bash
# 1. Codex — 深度 review agent
$CTL agent create \
  --workspace oar-project \
  --name reviewer \
  --runtime-class codex

# 2. Claude Code — 复查 agent
$CTL agent create \
  --workspace oar-project \
  --name checker \
  --runtime-class claude-code

# 3. GSD — 修复 agent
$CTL agent create \
  --workspace oar-project \
  --name fixer \
  --runtime-class gsd-pi
```

> Agent 的身份标识为 `oar-project/reviewer`、`oar-project/checker`、`oar-project/fixer`，即 `workspace/name` 格式。

### Step 4：等待 Agent 就绪

轮询每个 agent，直到状态从 `"creating"` 变为 `"idle"`：

```bash
$CTL agent status oar-project/reviewer
$CTL agent status oar-project/checker
$CTL agent status oar-project/fixer

# 所有 agent 都应显示："state": "idle"
# 如果出现 "error"，请查看 agentd 日志了解启动失败原因。
```

预期输出示例：
```json
{
  "agent": {
    "workspace": "oar-project",
    "name": "reviewer",
    "runtimeClass": "codex",
    "state": "idle"
  }
}
```

### Step 5：验证 Workspace 中的 Agent 列表

```bash
$CTL agent list --workspace oar-project
```

预期输出包含三个 agent：
```json
{
  "agents": [
    {"workspace": "oar-project", "name": "reviewer", "runtimeClass": "codex", "state": "idle"},
    {"workspace": "oar-project", "name": "checker", "runtimeClass": "claude-code", "state": "idle"},
    {"workspace": "oar-project", "name": "fixer", "runtimeClass": "gsd-pi", "state": "idle"}
  ]
}
```

### Step 6：执行 Review 流程

> **关键行为：**
> - `agent prompt` 是**异步 fire-and-forget** — 命令立即返回 `{accepted: true}`，agent 在后台处理。
> - 加 `--wait` flag 可以轮询 `agent/status` 直到 state 不再为 `"running"`。
> - Agent 状态转换：`idle` → `running`（处理中）→ `idle`（完成后回到空闲）。

#### 6.1 让 Codex 做深度 Code Review

```bash
$CTL agent prompt oar-project/reviewer --wait --text '
对 pkg/ari/server.go 做深度 code review。关注以下方面：
1. 错误处理是否完整
2. 并发安全问题
3. 资源泄漏风险
4. API 设计一致性
5. 边界情况处理

输出格式：每个问题用 [P0/P1/P2] 标注严重等级，附带具体行号和修复建议。
完成后，调用 workspace_send 工具把 review 结果发给 checker。
'
```

#### 6.2（备选）手动中继消息

如果 agent 没有自动调用 `workspace_send`（取决于 ACP adapter 是否支持 MCP tool 调用），需要手动中继结果：

```bash
$CTL workspace send \
  --workspace oar-project \
  --from reviewer \
  --to checker \
  --text "<粘贴 codex 的 review 输出>"
```

> **workspace/send 工作原理：** 消息会被添加归因前缀 `[workspace:oar-project from:reviewer]`，然后通过 `agent/prompt` 投递到 checker agent 的关联 shim。这是异步的 — 命令在投递后返回 `{delivered: true/false}`。

#### 6.3 让 Claude Code 复查

```bash
$CTL agent prompt oar-project/checker --wait --text '
你收到了 reviewer agent 的 code review 结果。请：
1. 验证每个发现是否属实（检查实际代码）
2. 标注是否同意 P0/P1/P2 评级
3. 过滤掉误报
4. 补充可能遗漏的问题
5. 产出最终的修复清单，按优先级排序

完成后，调用 workspace_send 把最终修复清单发给 fixer。
'
```

#### 6.4 让 GSD 执行修复

```bash
$CTL agent prompt oar-project/fixer --wait --text '
你收到了经过复查确认的 code review 修复清单。请：
1. 按 P0 → P1 → P2 顺序逐个修复
2. 每个修复后运行相关测试确认不破坏现有功能
3. 完成后给出修复总结
'
```

### Step 7：清理

清理遵循严格顺序：**停止 agent → 删除 agent → 删除 workspace**。

```bash
# 停止所有 agent
$CTL agent stop oar-project/reviewer
$CTL agent stop oar-project/checker
$CTL agent stop oar-project/fixer

# 删除已停止的 agent（必须先 stop）
$CTL agent delete oar-project/reviewer
$CTL agent delete oar-project/checker
$CTL agent delete oar-project/fixer

# 删除 workspace（需确保所有 agent 已删除）
$CTL workspace delete oar-project
```

> **注意：** `workspace/delete` 会在 store 层校验没有活跃 agent，如有活跃 agent 会拒绝删除。

---

## CLI 参考

### agentdctl 命令一览

| 命令 | 说明 |
|---|---|
| `agentdctl workspace create` | 创建 workspace（--name, --type, --path/--url/--ref/--depth） |
| `agentdctl workspace list` | 列出所有 workspace（仅 ready 状态） |
| `agentdctl workspace delete <name>` | 删除 workspace（需先删除所有 agent） |
| `agentdctl workspace send` | 在 agent 之间路由消息（--workspace, --from, --to, --text） |
| `agentdctl agent create` | 创建 agent（--workspace, --name, --runtime-class） |
| `agentdctl agent list` | 列出 agent（--workspace, --state） |
| `agentdctl agent status <ws/name>` | 查看 agent 状态及 shim 运行信息 |
| `agentdctl agent prompt <ws/name>` | 向 agent 发送 prompt（--text，--wait 可阻塞等待完成） |
| `agentdctl agent cancel <ws/name>` | 取消 agent 当前 turn |
| `agentdctl agent stop <ws/name>` | 停止 agent |
| `agentdctl agent restart <ws/name>` | 重启已停止/出错的 agent |
| `agentdctl agent delete <ws/name>` | 删除 agent（必须先停止） |
| `agentdctl agent attach <ws/name>` | 获取 shim socket 路径 |
| `agentdctl daemon status` | 检查 daemon 健康状态 |

所有命令都支持 `--socket <path>` 来指定 ARI Unix socket 路径。

### Agent 身份格式

Agent 的身份是 `workspace/name` 复合键，所有操作类命令均使用此格式：

```bash
# 正确
$CTL agent status oar-project/reviewer
$CTL agent prompt oar-project/checker --text "..."
$CTL agent stop oar-project/fixer

# 错误（不支持 agentId UUID）
$CTL agent status abc-123-def
```

### ARI JSON-RPC 方法（直接调用）

如需绕过 CLI 直接发送 JSON-RPC（例如通过 `socat`）：

```bash
SOCK=$(pwd)/bin/room-validating/agentd.sock

# workspace/create
echo '{"jsonrpc":"2.0","id":1,"method":"workspace/create","params":{"name":"oar-project","source":{"type":"local","local":{"path":"/Users/jim/code/zoumo/open-agent-runtime"}}}}' \
  | socat - UNIX-CONNECT:$SOCK

# workspace/list
echo '{"jsonrpc":"2.0","id":2,"method":"workspace/list","params":{}}' \
  | socat - UNIX-CONNECT:$SOCK

# agent/create
echo '{"jsonrpc":"2.0","id":3,"method":"agent/create","params":{"workspace":"oar-project","name":"reviewer","runtimeClass":"codex"}}' \
  | socat - UNIX-CONNECT:$SOCK

# agent/prompt
echo '{"jsonrpc":"2.0","id":4,"method":"agent/prompt","params":{"workspace":"oar-project","name":"reviewer","prompt":"review the code"}}' \
  | socat - UNIX-CONNECT:$SOCK

# workspace/send
echo '{"jsonrpc":"2.0","id":5,"method":"workspace/send","params":{"workspace":"oar-project","from":"reviewer","to":"checker","message":"review results..."}}' \
  | socat - UNIX-CONNECT:$SOCK

# agent/status
echo '{"jsonrpc":"2.0","id":6,"method":"agent/status","params":{"workspace":"oar-project","name":"reviewer"}}' \
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
1. Claude 做 review → `workspace send` 给 fixer
2. GSD 执行修复

这仍然能验证 Workspace 路由、Agent 生命周期和跨 Agent 通信的核心能力。

---

## 诊断检查表

| 检查项 | 正常表现 | 异常信号 |
|---|---|---|
| `workspace create` 返回 | `phase: "pending"`，< 1 秒 | 超时 → ARI socket 问题 |
| `workspace list` | `phase: "ready"` | 不出现 → 还在准备中，用 workspace/status 查 |
| `agent create` 返回 | `state: "creating"`，< 1 秒 | 超时 → ARI socket 问题 |
| 创建后 ~10 秒 `agent status` | `state: "idle"` | `state: "error"` → 查看 agentd 日志 |
| `agent prompt --wait` 响应 | 1-2 分钟内完成 | > 5 分钟 → API Key 或网络问题 |
| `workspace send` 投递 | `delivered: true` | `false` → target agent 不存在或不在 idle/running 状态 |
| `agent list --workspace` | 三个 agent 全部显示 | 缺少 agent → `agent create` 失败 |
| `workspace delete` | 成功 | 失败 → 还有活跃 agent，先 stop + delete |
| `daemon status` | `daemon: running` | `not running` → 检查 socket 路径 |

## Agent 异常恢复

如果 agent 进入 `"error"` 状态：

```bash
# 查看错误原因
$CTL agent status oar-project/AGENT_NAME

# 重启 agent（后台重新启动 shim）
$CTL agent restart oar-project/AGENT_NAME

# 轮询直到重新就绪
$CTL agent status oar-project/AGENT_NAME
# 等待 state: "idle"
```

---

## 已知限制

- **`agent/prompt` 是异步的** — 命令立即返回 `{accepted: true}`，agent 在后台处理。用 `--wait` flag 或轮询 `agent/status` 检测完成。
- **`workspace/send` 是异步的** — 消息投递后立即返回 `{delivered: true/false}`，不等待目标 agent 处理完毕。
- **仅支持点对点** — `workspace send` 每次只能发给一个 agent，没有广播功能。
- **消息归因是文本前缀** — `[workspace:oar-project from:reviewer]` 是 prepend 在 prompt 前面的，不是结构化元数据。
- **MCP tool 支持因 adapter 而异** — agent 能否自主调用 `workspace_send` 取决于 ACP adapter 实现，不支持时走手动中继路径。
- **启动超时** — `agent/create` 后台协程有固定超时。如果 shim 启动超时，agent 进入 `"error"` 状态。
- **恢复保护** — daemon 启动恢复期间，操作类请求（`agent/prompt`、`workspace/send`）会被阻塞（返回 -32001），直到恢复完成。
- **workspace/list 仅返回 ready** — pending/error 状态的 workspace 不出现在列表中；用 `workspace/status` RPC 直接查询。
