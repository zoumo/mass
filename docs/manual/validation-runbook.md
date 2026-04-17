# 多 Agent 协作验证操作手册

## 验证场景

在同一个 Workspace 中拉起三个 AgentRun，协作完成 **方案设计 → 严格审查 → 执行** 的通用任务流：

| Agent 名称 | Runtime Class | 角色 | ACP Adapter |
|---|---|---|---|
| **claude-code** | `claude` | 方案设计者：理解需求，产出具体可执行的方案 | `@agentclientprotocol/claude-agent-acp` (Node) |
| **codex** | `codex` | 严格审查者：质疑假设，找漏洞，最多 3 轮达成一致 | `@zed-industries/codex-acp` (Node, via bunx) |
| **gsd-pi** | `gsd-pi` | 执行者：严格按最终方案操作，不做额外设计决策 | `gsd-pi-acp` (Node) |

**协作流**：

```
human → claude-code [round-1-proposal]
           ↓ ↑  (最多 3 轮)
         codex [round-N-feedback / final-approved]
           ↓
       claude-code [execution-request]
           ↓
         gsd-pi [execution-done]
```

## 前置条件

- 已编译 `mass` 和 `massctl`（`make build`）
- 已安装 [cmux](https://github.com/manaflow-ai/cmux)（`brew tap manaflow-ai/cmux && brew install --cask cmux`）
- 已安装 `bunx`（Node.js 环境）
- 所需的 ACP adapter npm 包可被 `bunx` 访问
- 当前工作目录为项目根目录

## 配置文件

### Agent 模板定义

三个 agent 模板分别保存为 YAML 文件，存放在 `bin/e2e/agents/` 下：

**`bin/e2e/agents/codex.yaml`**
```yaml
metadata:
  name: codex
spec:
  command: bunx
  args:
    - "@zed-industries/codex-acp"
```

**`bin/e2e/agents/claude.yaml`**
```yaml
metadata:
  name: claude
spec:
  command: bunx
  args:
    - "@agentclientprotocol/claude-agent-acp@v0.26.0"
```

**`bin/e2e/agents/gsd-pi.yaml`**
```yaml
metadata:
  name: gsd-pi
spec:
  command: bunx
  args:
    - "gsd-pi-acp"
```

### Workspace 定义

**`bin/e2e/workspace.yaml`**
```yaml
name: agentd-e2e
source:
  type: local
  path: /Users/jim/code/zoumo/open-agent-runtime
```

> 注意：`source.path` 必须为绝对路径，请根据实际项目位置修改。

## 快速启动（massctl up）

`massctl up` 从一个 YAML 配置文件中读取 workspace + agent 描述，一次性调用 ARI RPC 创建全部资源并等待就绪：

**`bin/e2e/up.yaml`**
```yaml
kind: workspace-up
metadata:
  name: agentd-e2e
spec:
  source:
    type: local
    path: /Users/jim/code/zoumo/open-agent-runtime
  agents:
    - metadata:
        name: codex
      spec:
        agent: codex
    - metadata:
        name: claude-code
      spec:
        agent: claude
    - metadata:
        name: gsd-pi
      spec:
        agent: gsd-pi
```

> `source.path` 必须为绝对路径，请根据实际项目位置修改。

前置步骤（需手动执行一次）：

```bash
# 1. 启动 mass
./bin/mass server &
# 等待 ARI socket 就绪

# 2. 注册 agent 模板
./bin/massctl agent apply -f bin/e2e/agents/codex.yaml
./bin/massctl agent apply -f bin/e2e/agents/claude.yaml
./bin/massctl agent apply -f bin/e2e/agents/gsd-pi.yaml
```

然后执行 `up`，会自动创建 workspace、所有 agent run，并等待全部 idle 后输出 shim socket 路径：

```bash
./bin/massctl --socket /var/run/mass/mass.sock up -f bin/e2e/up.yaml
```

示例输出：
```
Workspace "agentd-e2e" created (phase: pending)
Waiting for workspace "agentd-e2e" to be ready...
Workspace "agentd-e2e" is ready (path: /Users/jim/code/zoumo/open-agent-runtime)
Agent run "agentd-e2e"/"codex" created (state: creating)
Agent run "agentd-e2e"/"claude-code" created (state: creating)
Agent run "agentd-e2e"/"gsd-pi" created (state: creating)
Waiting for agent "agentd-e2e"/"codex" to be idle...
Agent "agentd-e2e"/"codex" is idle
Waiting for agent "agentd-e2e"/"claude-code" to be idle...
Agent "agentd-e2e"/"claude-code" is idle
Waiting for agent "agentd-e2e"/"gsd-pi" to be idle...
Agent "agentd-e2e"/"gsd-pi" is idle

All agents are ready. Shim sockets:
  agentd-e2e/codex: /tmp/mass-<PID>/codex.sock
  agentd-e2e/claude-code: /tmp/mass-<PID>/claude-code.sock
  agentd-e2e/gsd-pi: /tmp/mass-<PID>/gsd-pi.sock
```

## 一键启动脚本

脚本位于 `bin/e2e/setup.sh`，使用 [cmux](https://github.com/manaflow-ai/cmux) 创建多窗格终端环境。

脚本执行流程：

1. 启动 `mass server`，等待 ARI socket 就绪
2. 通过 `massctl agent apply` 注册三个 agent 模板
3. 通过 `massctl up -f bin/e2e/up.yaml` 创建 workspace + 所有 agent run，等待全部 idle
4. 通过 `massctl agentrun get` 获取各 agent 的 shim socket 路径（`.status.shim.socketPath`）
5. 使用 cmux CLI 创建一个 workspace，分裂为三个 pane，分别启动 `massctl shim chat` 连接三个 agent

```
┌──────────────────────┬──────────────────────┐
│                      │  codex (严格审查)     │
│  claude-code (方案)  ├──────────────────────┤
│                      │  gsd-pi (执行)        │
└──────────────────────┴──────────────────────┘
```

> 使用方法：`chmod +x bin/e2e/setup.sh && ./bin/e2e/setup.sh`

脚本核心步骤（cmux 部分）：

```bash
# 创建 cmux workspace
cmux new-workspace --name "mass-e2e" --cwd "$PROJECT_ROOT"

# 初始 surface 连接 codex
cmux send "massctl shim --socket '$CODEX_SOCK' chat"
cmux send-key Enter

# 右侧分裂连接 claude-code
cmux new-split right
cmux send --surface "$SPLIT1" "massctl shim --socket '$CLAUDE_SOCK' chat"
cmux send-key --surface "$SPLIT1" Enter

# 右侧下方分裂连接 gsd-pi
cmux new-split down --surface "$SPLIT1"
cmux send --surface "$SPLIT2" "massctl shim --socket '$GSDPI_SOCK' chat"
cmux send-key --surface "$SPLIT2" Enter
```

## 验证操作手册

以下命令假设环境已通过 `setup.sh` 启动。在另一个终端中复制 `setup.sh` 输出的环境变量（fish shell）：

```fish
set -x SOCKET /tmp/mass-e2e-<PID>/mass.sock
set -x WS agentd-e2e
alias ctl './bin/massctl --socket $SOCKET'
```

> `setup.sh` 会输出实际的 PID 值，直接复制粘贴即可。

### 场景一：Agent 自我介绍与互发现

分别给三个 agent 发送自我介绍的 prompt，验证 workspace 互发现：

```bash
for agent in claude-code codex gsd-pi; do
  ctl agentrun prompt --workspace "$WS" --name "$agent" --text \
    "请自我介绍你的名称和角色。然后使用 workspace 工具列出你所在 workspace 中的所有 agent，报告你看到了哪些其他 agent。"
done
```

**预期结果**：每个 agent 能报告出三个 agent（claude-code、codex、gsd-pi）。

也可以通过 workspace send 测试直接互通：

```bash
ctl workspace send --name "$WS" --from claude-code --to codex --text "你好 codex，我是 claude-code，请回复确认收到。"
ctl workspace send --name "$WS" --from codex --to gsd-pi --text "你好 gsd-pi，我是 codex，请回复确认收到。"
```

### 场景二：方案设计 → 审查 → 执行 协作流程

此场景验证三个 agent 的完整协作链路。

**设计原则**：每个 agent 完成阶段任务后发一条 workspace 消息然后停止，由对方消息驱动下一阶段。不做轮询，不跨阶段驱动。

---

#### 阶段1：触发 claude-code 出方案（人工触发）

```bash
ctl agentrun prompt --workspace "$WS" --name claude-code --text \
"任务：<在这里描述具体任务，例如：重构 pkg/ari/server.go 的错误处理，使其统一返回结构化错误>"
```

> claude-code 的 system prompt 已固化后续流程：写方案文档 → 发 [round-1-proposal] 给 codex → 根据反馈修订 → 收到 [final-approved] 后派发给 gsd-pi。

---

#### 阶段2：codex 审查（收到 [round-1-proposal] 后自动触发，或人工触发）

```bash
ctl agentrun prompt --workspace "$WS" --name codex --text \
"你收到了 claude-code 的 [round-1-proposal]，请按协作协议审查。"
```

---

#### 阶段3：claude-code 修订（收到 [round-1-feedback] 后自动触发，或人工触发）

```bash
ctl agentrun prompt --workspace "$WS" --name claude-code --text \
"你收到了 codex 的 [round-1-feedback]，请按协作协议修订方案。"
```

---

#### 阶段4+：后续轮次（最多到 round-3）

codex 每轮发 `[round-N-feedback]`，claude-code 回复 `[round-N-revised-proposal]`，直到 codex 发出 `[final-approved]`。

手动触发时替换消息标注即可：

```bash
# codex 第N轮审查
ctl agentrun prompt --workspace "$WS" --name codex --text \
"你收到了 claude-code 的 [round-N-revised-proposal]，请按协作协议审查。"

# claude-code 第N轮修订
ctl agentrun prompt --workspace "$WS" --name claude-code --text \
"你收到了 codex 的 [round-N-feedback]，请按协作协议修订方案。"
```

---

#### 最终阶段：gsd-pi 执行（收到 [execution-request] 后自动触发，或人工触发）

```bash
ctl agentrun prompt --workspace "$WS" --name gsd-pi --text \
"你收到了 claude-code 的 [execution-request]，请按协作协议执行。"
```

### 状态检查与调试

```bash
# 查看所有 agent 状态
ctl agentrun list --workspace "$WS"

# 查看单个 agent 详细状态（包含 shim socket 路径）
ctl agentrun get --workspace "$WS" --name codex
ctl agentrun get --workspace "$WS" --name claude-code
ctl agentrun get --workspace "$WS" --name gsd-pi

# 检查 workspace 状态
ctl workspace get --name "$WS"

# 取消正在执行的 prompt
ctl agentrun cancel --workspace "$WS" --name codex

# 获取 shim socket 路径（用于 shim 子命令）
ctl agentrun get --workspace "$WS" --name codex | jq -r '.status.shim.socketPath'
# 然后用返回的 socketPath 查看状态
massctl shim --socket <socketPath> state
```

### 清理环境

```bash
# 停止所有 agent
ctl agentrun stop --workspace "$WS" --name codex
ctl agentrun stop --workspace "$WS" --name claude-code
ctl agentrun stop --workspace "$WS" --name gsd-pi

# 等待停止后删除
ctl agentrun delete --workspace "$WS" --name codex
ctl agentrun delete --workspace "$WS" --name claude-code
ctl agentrun delete --workspace "$WS" --name gsd-pi

# 删除 workspace
ctl workspace delete --name "$WS"

# 或直接 Ctrl-C setup.sh，会自动清理
```

## CLI 命令速查

| 操作 | 命令 |
|---|---|
| **一键创建 workspace+agents** | `massctl up -f <config.yaml>` |
| 注册 agent 模板 | `massctl agent apply -f <file.yaml>` |
| 查看 agent 模板 | `massctl agent list` |
| 创建本地 workspace | `massctl workspace create local --name <name> --path <abs-path>` |
| 查看 workspace 状态 | `massctl workspace get --name <name>` |
| 创建 agent run | `massctl agentrun create --workspace <ws> --name <n> --runtime-class <rc>` |
| 发送 prompt（阻塞等待） | `massctl agentrun prompt --workspace <ws> --name <name> --text '...' --wait` |
| 发送 prompt（异步） | `massctl agentrun prompt --workspace <ws> --name <name> --text '...'` |
| 查看 agent run 详情 | `massctl agentrun get --workspace <ws> --name <name>` |
| agent 间消息 | `massctl workspace send --name <ws> --from <a> --to <b> --text '...'` |
| 取消执行中的 prompt | `massctl agentrun cancel --workspace <ws> --name <name>` |
| 停止 agent | `massctl agentrun stop --workspace <ws> --name <name>` |
| 删除 agent | `massctl agentrun delete --workspace <ws> --name <name>` |
| 交互式 chat | `massctl shim --socket <path> chat` |
| 查看 shim 状态 | `massctl shim --socket <path> state` |

> 全局参数：`--socket <path>` 指定 ARI socket 路径（默认 `/var/run/mass/mass.sock`）
