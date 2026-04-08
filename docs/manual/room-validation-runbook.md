# Room 多 Agent 验证操作手册

## 场景

在同一个 Room 中拉起三个 agent，协作完成 code review → 复查 → 修复：

| Agent | RuntimeClass | 角色 | ACP Adapter |
|---|---|---|---|
| **codex** | `codex` | 深度 code review，产出问题列表 | `codex-acp` (Rust, zed-industries) |
| **claude** | `claude-code` | 复查 codex 的 review 结果，标注优先级 | `claude-agent-acp` (Node) |
| **gsd** | `gsd-pi` | 按复查结果执行修复 | `pi-acp` (Node) |

## 前置条件

### 1. 构建 OAR 二进制

```bash
cd /Users/jim/code/zoumo/open-agent-runtime

# 构建全部所需二进制
go build -o bin/agentd ./cmd/agentd
go build -o bin/agent-shim ./cmd/agent-shim
go build -o bin/agentdctl ./cmd/agentdctl
go build -o bin/room-mcp-server ./cmd/room-mcp-server
```

### 2. 安装 codex-acp

```bash
# 从 zed-industries/codex-acp 安装 Rust ACP adapter
cargo install --git https://github.com/zed-industries/codex-acp
# 验证
codex-acp --help
```

### 3. 验证其他 ACP adapter

```bash
# claude-code adapter
ls /Users/jim/.bun/install/cache/@GH@agentclientprotocol-claude-agent-acp-7506223@@@1/dist/index.js

# gsd-pi adapter
which bunx && bunx pi-acp --help
```

### 4. 环境变量

```bash
# Anthropic API Key (claude-code 和 gsd-pi 都需要)
export ANTHROPIC_API_KEY=$(python3 -c "import json; print(json.load(open('/Users/jim/.claude/config.json'))['primaryApiKey'])")

# OpenAI API Key (codex 需要)
# 如果 codex 已通过 codex --auth 登录过，可能不需要显式设置
export OPENAI_API_KEY="你的key"
```

### 5. 准备工作目录

```bash
# 创建验证用工作目录 — 放一个真实的小项目或本项目的子目录
mkdir -p /tmp/oar-room-validation
# 可以 clone 一个小项目，或者直接用本项目
ln -sf /Users/jim/code/zoumo/open-agent-runtime /tmp/oar-room-validation/workspace
```

## 操作步骤

### Step 0: 写 agentd 配置

```bash
cat > /tmp/oar-room-validation/config.yaml << 'EOF'
socket: /tmp/oar-room-validation/agentd.sock
workspaceRoot: /tmp/oar-room-validation/workspaces
metaDB: /tmp/oar-room-validation/agentd.db
bundleRoot: /tmp/oar-room-validation/bundles

runtimeClasses:
  codex:
    command: codex-acp
    env: {}

  claude-code:
    command: node
    args:
      - "/Users/jim/.bun/install/cache/@GH@agentclientprotocol-claude-agent-acp-7506223@@@1/dist/index.js"
    env: {}

  gsd-pi:
    command: bunx
    args:
      - "pi-acp"
    env:
      PI_ACP_PI_COMMAND: gsd
      PI_CODING_AGENT_DIR: /Users/jim/.gsd/agent
EOF
```

> **注意:** `env: {}` 表示继承父进程环境（包括 API keys）。如需隔离可显式列出。

### Step 1: 启动 agentd

```bash
# Terminal 1 — 启动 daemon
export OAR_SHIM_BINARY=$(pwd)/bin/agent-shim
./bin/agentd --config bin/room-validating/config.yaml
```

### Step 2: 准备 Workspace

```bash
# Terminal 2 — 操作终端
# 设置 socket 路径变量（后续所有命令都用这个）
SOCK=$(pwd)/bin/room-validating/agentd.sock

# 准备共享 workspace（用 local 模式指向项目目录）
./bin/agentdctl --socket $SOCK workspace prepare --spec '{
  "oarVersion": "0.1.0",
  "metadata": {"name": "oar-project"},
  "source": {"type": "local", "path": "/Users/jim/code/zoumo/open-agent-runtime"}
}'
# 记下返回的 workspaceId → 后续步骤引用为 $WS_ID
```

### Step 3: 创建 Room

```bash
# 创建 review Room
./bin/agentdctl --socket $SOCK room create --name "code-review" --mode mesh
```

### Step 4: 创建三个成员 Session

> **注意：** `session/new` 只创建 DB 记录（state=created），不会创建 bundle 或启动进程。
> Bundle 创建 + ACP 进程启动在首次 `session prompt` 时自动触发（auto-start）。

```bash
WS_ID="<Step 2 返回的 workspaceId>"

# Codex — 深度 review agent
./bin/agentdctl --socket $SOCK session new \
  --runtime-class codex \
  --workspace-id "$WS_ID" \
  --room code-review \
  --room-agent reviewer
# 记下返回 JSON 中的 sessionId → $CODEX_SID

# Claude Code — 复查 agent
./bin/agentdctl --socket $SOCK session new \
  --runtime-class claude-code \
  --workspace-id "$WS_ID" \
  --room code-review \
  --room-agent checker
# 记下 sessionId → $CLAUDE_SID

# GSD — 修复 agent
./bin/agentdctl --socket $SOCK session new \
  --runtime-class gsd-pi \
  --workspace-id "$WS_ID" \
  --room code-review \
  --room-agent fixer
# 记下 sessionId → $GSD_SID
```

### Step 5: 检查 Room 状态

```bash
./bin/agentdctl --socket $SOCK room status --name "code-review"
# 应该看到三个 member: reviewer(codex), checker(claude-code), fixer(gsd-pi)
# 所有 state=created（还没 prompt 过，进程未启动）
```

### Step 6: 执行 Review 流程

> **关键：`session/prompt` 是同步阻塞的。**
> 命令返回 = agent 已完成处理。返回值包含 `stopReason`（`end_turn` 表示正常完成）。
>
> **超时：** 默认 120 秒。深度 code review 可能超过这个时间。
> 如果超时，考虑缩小 review 范围（单个文件/单个函数），或修改 `deliverPrompt` 超时。
>
> **怎么知道完成了：**
> - CLI 命令返回 → 完成（同步）
> - 通过 `session/status` 也可查：`state=running` 表示正在处理，idle 后回到等待状态
> - agentd 日志会打印 `deliverPrompt completed for session X, stopReason=end_turn`

#### 6.1 让 Codex 做深度 code review

```bash
# 这个命令会阻塞直到 codex 完成 review 并返回结果
# 首次 prompt 会触发 auto-start：创建 bundle → 启动 shim → 连接 → 发送 prompt
./bin/agentdctl --socket $SOCK session prompt "$CODEX_SID" --text '
对 pkg/ari/server.go 做深度 code review。关注以下方面：
1. 错误处理是否完整
2. 并发安全问题
3. 资源泄漏风险
4. API 设计一致性
5. 边界情况处理

输出格式：每个问题用 [P0/P1/P2] 标注严重等级，附带具体行号和修复建议。
完成后，调用 room_send 工具把 review 结果发给 checker。
'
# ↑ 命令返回 = codex 完成。返回内容包含 review 结果。
```

> 如果 codex 不支持自动调用 room_send MCP tool，
> 你可以手动中继结果（见 Step 6.1b）。

#### 6.1b（备选）手动中继 — 如果 agent 不主动调用 room_send

如果 codex 完成 review 但没有调用 room_send，你可以把它的输出复制下来，
然后通过 ARI 手动路由：

```bash
# 通过 room/send 把 codex 的输出发给 claude
./bin/agentdctl --socket $SOCK room send \
  --room "code-review" \
  --from "reviewer" \
  --to "checker" \
  --text "<粘贴 codex 的 review 输出>"
```

#### 6.2 让 Claude Code 复查

```bash
# 如果消息通过 room_send 自动到达，claude 会自动收到 prompt
# 如果需要手动触发：
./bin/agentdctl --socket $SOCK session prompt "$CLAUDE_SID" --text '
你收到了 codex reviewer 的 code review 结果。请：
1. 验证每个发现是否属实（检查实际代码）
2. 标注是否同意 P0/P1/P2 评级
3. 过滤掉误报
4. 补充 codex 可能遗漏的问题
5. 产出最终的修复清单，按优先级排序

完成后，调用 room_send 把最终修复清单发给 fixer。
'
```

#### 6.3 让 GSD 执行修复

```bash
# 同上 — 消息可能自动到达或需要手动中继
./bin/agentdctl --socket $SOCK session prompt "$GSD_SID" --text '
你收到了经过复查确认的 code review 修复清单。请：
1. 按 P0 → P1 → P2 顺序逐个修复
2. 每个修复后运行相关测试确认不破坏现有功能
3. 完成后给出修复总结
'
```

### Step 7: 验证 + 清理

```bash
# 查看各 session 状态
./bin/agentdctl --socket $SOCK session list

# 停止所有 session
./bin/agentdctl --socket $SOCK session stop "$CODEX_SID"
./bin/agentdctl --socket $SOCK session stop "$CLAUDE_SID"
./bin/agentdctl --socket $SOCK session stop "$GSD_SID"

# 删除 session
./bin/agentdctl --socket $SOCK session remove "$CODEX_SID"
./bin/agentdctl --socket $SOCK session remove "$CLAUDE_SID"
./bin/agentdctl --socket $SOCK session remove "$GSD_SID"

# 删除 Room（member session 必须先 stop/remove）
./bin/agentdctl --socket $SOCK room delete --name "code-review"

# 清理 workspace
./bin/agentdctl --socket $SOCK workspace cleanup --workspace-id "$WS_ID"
```

---

## 直接 JSON-RPC 操作（如果 agentdctl 缺少某些子命令）

agentdctl 可能还没有 `room` 子命令。可以用 `socat` 直接发 JSON-RPC：

```bash
SOCK=/tmp/oar-room-validation/agentd.sock

# room/create
echo '{"jsonrpc":"2.0","id":1,"method":"room/create","params":{"name":"code-review","labels":{},"communication":{"mode":"mesh"}}}' | socat - UNIX-CONNECT:$SOCK

# room/status
echo '{"jsonrpc":"2.0","id":2,"method":"room/status","params":{"name":"code-review"}}' | socat - UNIX-CONNECT:$SOCK

# room/send (点对点消息)
echo '{"jsonrpc":"2.0","id":3,"method":"room/send","params":{"room":"code-review","from":"reviewer","to":"checker","text":"这是review结果..."}}' | socat - UNIX-CONNECT:$SOCK

# room/delete
echo '{"jsonrpc":"2.0","id":4,"method":"room/delete","params":{"name":"code-review"}}' | socat - UNIX-CONNECT:$SOCK
```

或者用 Python 脚本：

```python
#!/usr/bin/env python3
"""ARI JSON-RPC client for manual Room validation."""
import socket, json, sys

SOCK = "/tmp/oar-room-validation/agentd.sock"
_id = 0

def call(method, params=None):
    global _id
    _id += 1
    msg = {"jsonrpc": "2.0", "id": _id, "method": method}
    if params:
        msg["params"] = params
    s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    s.connect(SOCK)
    s.sendall((json.dumps(msg) + "\n").encode())
    s.settimeout(120)  # prompts can take a while
    data = b""
    while True:
        chunk = s.recv(4096)
        if not chunk:
            break
        data += chunk
        # JSON-RPC response is a single line
        if b"\n" in data:
            break
    s.close()
    return json.loads(data.decode().strip())

if __name__ == "__main__":
    # Example usage
    print(json.dumps(call("room/status", {"name": "code-review"}), indent=2))
```

---

## 简化验证（如果三 agent 环境搭建困难）

如果 codex-acp 安装不顺利，可以先用两个 agent 验证核心流程：

| Agent | RuntimeClass | 角色 |
|---|---|---|
| **claude** | `claude-code` | code review + 复查 |
| **gsd** | `gsd-pi` | 执行修复 |

流程简化为：
1. Claude 做 review → room_send 给 gsd
2. GSD 执行修复

这仍然证明了 Room 路由的核心能力。

---

## 预期观察点

验证时重点关注：

| 观察项 | 正常表现 | 异常信号 |
|---|---|---|
| session/new 速度 | 几秒内返回 created | 超过 30s → shim 启动问题 |
| 首次 prompt 响应 | 1-2 分钟（LLM 调用） | 超过 5 分钟 → API key 或网络问题 |
| room_send 投递 | 目标 agent 收到并开始处理 | "session not running" → 需先 prompt 激活 |
| room/status 成员列表 | 三个 member 全部显示 | 缺少 member → session/new 时 room 不匹配 |
| 清理顺序 | stop → remove → room/delete → workspace/cleanup | room/delete 失败 → 还有 active session |

## 已知限制

- **session/prompt 是同步阻塞的** — 调用方阻塞直到 agent 完成处理（返回 `stopReason`）。默认超时 120 秒。
- **room_send 也是同步的** — 底层调用 `deliverPrompt`，发送方阻塞直到目标 agent 完成处理。
- **只有点对点** — 没有 room_broadcast，每次只能发给一个 agent。
- **归因是文本前缀** — `[room:code-review from:reviewer]` 是 prepend 在 prompt 前面的，不是结构化元数据。
- **MCP 工具可能不被所有 agent 识别** — codex-acp 是否支持 MCP tool 调用取决于 adapter 实现。如果不支持，只能走手动中继路径。
