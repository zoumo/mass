# 多 Agent 协作验证操作手册

## 验证场景

在同一个 Workspace 中拉起三个 AgentRun，协作完成 code review → 复查 → 修复：

| Agent 名称 | Runtime Class | 角色 | ACP Adapter |
|---|---|---|---|
| **codex** | `codex` | 深度 code review，产出问题列表 | `@zed-industries/codex-acp` (Node, via bunx) |
| **claude-code** | `claude` | 复查 review 结果，标注优先级 | `@agentclientprotocol/claude-agent-acp` (Node) |
| **gsd-pi** | `gsd-pi` | 按复查结果执行修复 | `gsd-pi-acp` (Node) |

## 前置条件

- 已编译 `agentd` 和 `agentdctl`（`make build`）
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

## 一键启动脚本

脚本位于 `bin/e2e/setup.sh`，使用 [cmux](https://github.com/manaflow-ai/cmux) 创建多窗格终端环境。

脚本执行流程：

1. 启动 `agentd server`，等待 ARI socket 就绪
2. 通过 `agentdctl agent apply` 注册三个 agent 模板
3. 通过 `agentdctl workspace create local` 创建本地 workspace
4. 通过 `agentdctl agentrun create` 启动三个 agent run，等待全部进入 `idle` 状态
5. 通过 `agentdctl agentrun attach` 获取各 agent 的 shim socket 路径
6. 使用 cmux CLI 创建一个 workspace，分裂为三个 pane，分别启动 `agentdctl shim chat` 连接三个 agent

```
┌──────────────────────┬──────────────────────┐
│                      │  claude-code (复查)   │
│  codex (code review) ├──────────────────────┤
│                      │  gsd-pi (修复执行)    │
└──────────────────────┴──────────────────────┘
```

> 使用方法：`chmod +x bin/e2e/setup.sh && ./bin/e2e/setup.sh`

脚本核心步骤（cmux 部分）：

```bash
# 创建 cmux workspace
cmux new-workspace --name "oar-e2e" --cwd "$PROJECT_ROOT"

# 初始 surface 连接 codex
cmux send "agentdctl shim --socket '$CODEX_SOCK' chat"
cmux send-key Enter

# 右侧分裂连接 claude-code
cmux new-split right
cmux send --surface "$SPLIT1" "agentdctl shim --socket '$CLAUDE_SOCK' chat"
cmux send-key --surface "$SPLIT1" Enter

# 右侧下方分裂连接 gsd-pi
cmux new-split down --surface "$SPLIT1"
cmux send --surface "$SPLIT2" "agentdctl shim --socket '$GSDPI_SOCK' chat"
cmux send-key --surface "$SPLIT2" Enter
```

## 验证操作手册

以下命令假设环境已通过 `setup.sh` 启动。在另一个终端中复制 `setup.sh` 输出的环境变量（fish shell）：

```fish
set -x SOCKET /tmp/oar-e2e-<PID>/agentd.sock
set -x WS agentd-e2e
alias ctl './bin/agentdctl --socket $SOCKET'
```

> `setup.sh` 会输出实际的 PID 值，直接复制粘贴即可。

### 场景一：Agent 自我介绍与互发现

分别给三个 agent 发送自我介绍的 prompt，并要求它们报告通过 workspace 工具看到的其他 agent：

```bash
# 1. codex 自我介绍
ctl agentrun prompt "$WS/codex" --text \
  "请自我介绍你的名称和角色。然后使用 workspace 工具列出你所在 workspace 中的所有 agent，报告你看到了哪些其他 agent。"

# 2. claude-code 自我介绍
ctl agentrun prompt "$WS/claude-code" --text \
  "请自我介绍你的名称和角色。然后使用 workspace 工具列出你所在 workspace 中的所有 agent，报告你看到了哪些其他 agent。"

# 3. gsd-pi 自我介绍
ctl agentrun prompt "$WS/gsd-pi" --text \
  "请自我介绍你的名称和角色。然后使用 workspace 工具列出你所在 workspace 中的所有 agent，报告你看到了哪些其他 agent。"
```

**预期结果**：每个 agent 应该能报告出 workspace 中的三个 agent（codex、claude-code、gsd-pi）。

也可以通过 workspace send 在 agent 间直接发消息测试互通：

```bash
# codex 给 claude-code 打招呼
ctl workspace send "$WS" --from codex --to claude-code --text "你好 claude-code，我是 codex，请回复确认收到。"

# claude-code 给 gsd-pi 打招呼
ctl workspace send "$WS" --from claude-code --to gsd-pi --text "你好 gsd-pi，我是 claude-code，请回复确认收到。"
```

### 场景二：Code Review → 复查 → 修复协作流程

此场景验证三个 agent 的完整协作链路。

**设计原则**：每个 prompt 只负责一个阶段——agent 完成任务后发一条 workspace 消息，然后立即停止，由对方收到消息后继续。不做轮询，不跨阶段驱动。

```
codex(阶段1) → [workspace msg] → claude-code(阶段2) → [workspace msg] →
codex(阶段3) → [workspace msg] → claude-code(阶段4) → [workspace msg] →
codex(阶段5) → [workspace msg] → gsd-pi(阶段6)
```

---

#### 阶段1：codex 初步审查（人工触发）

```bash
ctl agentrun prompt "$WS/codex" --text \
"请严格按以下步骤执行，完成每步后立即进行下一步，全部完成后停止：

步骤1：审查 docs/design/ 目录下的所有设计文档，找出与当前代码实现不一致的内容。
对每个问题记录：文件路径、过时内容描述、建议的修复方案。可以参考 docs/plan/2026-04-12-design-implementation-drift-audit 这是上一轮的审查结果

步骤2：将审查结果写入 docs/plan/design-review-<YYYYMMDD>.md，使用以下结构：
  ## 审查结论
  （你的发现，每项包含文件路径、问题描述、修复建议）

  ## 讨论记录
  （留空）

  ## 最终方案
  （留空）

步骤3：通过 workspace 消息发送给 claude-code，原文如下（勿修改）：
---
[round-1-review-request]
我已完成初步审查，报告路径：docs/plan/design-review-<实际文件名>.md

请完成以下任务后，通过 workspace 消息回复给 codex，标注 [round-1-reply]：
1. 阅读「审查结论」中的每一项，对照实际代码核实
2. 对每项注明：同意 / 不同意（不同意时说明理由）
3. 将意见追加写入该文档「讨论记录」章节，标注「claude-code 第1轮」
4. 回复消息只需包含简短摘要（哪些同意，哪些有异议）
回复后请立即停止，等待我的下一条消息。
---

步骤4：消息发送后，立即停止全部操作。不要轮询检查回复，不要联系 gsd-pi，等待被唤醒。"
```

---

#### 阶段2：claude-code 复查（收到 workspace 消息后触发，或人工触发）

> 如果 claude-code 不能自动被 workspace 消息唤醒，手动执行：

```bash
ctl agentrun prompt "$WS/claude-code" --text \
"你收到了 codex 的 [round-1-review-request]。请严格按以下步骤执行：

步骤1：找到 codex 消息中指定的审查文档（docs/plan/design-review-*.md），
      仔细阅读「审查结论」中的每一项，对照实际代码进行核实。

步骤2：将复查意见追加写入该文档「讨论记录」章节，标注「claude-code 第1轮」：
  - 对每项注明：同意 / 不同意
  - 不同意的项请说明理由

步骤3：通过 workspace 消息回复给 codex，标注 [round-1-reply]，内容包含：
  - 文档已更新
  - 简短说明：同意哪些项、对哪些项有异议

步骤4：消息发送后立即停止。不要联系 gsd-pi，等待 codex 的回复。"
```

---

#### 阶段3：codex 第2轮回应（收到 [round-1-reply] 后触发，或人工触发）

```bash
ctl agentrun prompt "$WS/codex" --text \
"你收到了 claude-code 的 [round-1-reply]。请严格按以下步骤执行：

步骤1：阅读 docs/plan/design-review-*.md「讨论记录」中「claude-code 第1轮」的意见。

步骤2：将你的回应追加写入「讨论记录」章节，标注「codex 第2轮」：
  - 接受的意见：说明修改方案
  - 坚持的意见：给出理由

步骤3：通过 workspace 消息发送给 claude-code，标注 [round-2-codex-response]，内容包含：
  - 已更新讨论记录
  - 对各分歧项的最终立场简述
  - 请求：这是第2轮，请给出你的最终立场并共同确认方案，然后通过 workspace 回复 [round-2-final]

步骤4：消息发送后立即停止，等待 [round-2-final]。"
```

---

#### 阶段4：claude-code 最终立场（收到 [round-2-codex-response] 后触发，或人工触发）

```bash
ctl agentrun prompt "$WS/claude-code" --text \
"你收到了 codex 的 [round-2-codex-response]，这是第2轮也是最后一轮。请严格按以下步骤执行：

步骤1：阅读 docs/plan/design-review-*.md「讨论记录」中「codex 第2轮」的回应。

步骤2：将最终立场追加写入「讨论记录」章节，标注「claude-code 第2轮（最终）」。

步骤3：在文档「最终方案」章节写入共识：
  - 每个确认修复项标注 APPROVED
  - 每项注明优先级：P0（必须）/ P1（应该）/ P2（可改善）
  - 写明具体修复要求

步骤4：通过 workspace 消息回复给 codex，标注 [round-2-final]，内容包含：
  - 讨论完成，最终方案已写入文档

步骤5：消息发送后立即停止，等待 codex 通知执行结果。"
```

---

#### 阶段5：codex 派发给 gsd-pi（收到 [round-2-final] 后触发，或人工触发）

```bash
ctl agentrun prompt "$WS/codex" --text \
"你收到了 claude-code 的 [round-2-final]，讨论已完成。请执行以下操作：

步骤1：阅读 docs/plan/design-review-*.md「最终方案」章节，确认 APPROVED 项完整。

步骤2：通过 workspace 消息发送给 gsd-pi，原文如下（勿修改，填入实际文件名）：
---
[execution-request]
请执行 docs/plan/design-review-<实际文件名>.md 「最终方案」章节所有标注 APPROVED 的修复项。

执行要求：
1. 按 P0 → P1 → P2 顺序执行
2. 每完成一项，在文档该项后标注 DONE
3. 全部完成后，通过 workspace 消息回复给 codex，附执行结果摘要
---

步骤3：消息发送后停止，等待 gsd-pi 的完成报告。"
```

---

#### 阶段6：gsd-pi 执行修复（收到 [execution-request] 后触发，或人工触发）

```bash
ctl agentrun prompt "$WS/gsd-pi" --text \
"你收到了 codex 的 [execution-request]。请严格按以下步骤执行：

步骤1：阅读 docs/plan/design-review-*.md「最终方案」章节所有标注 APPROVED 的修复项。

步骤2：按 P0 → P1 → P2 优先级顺序逐项执行修复。
      每完成一项，在文档该项后追加标注 DONE。

步骤3：全部完成后，通过 workspace 消息回复给 codex，内容包含执行结果摘要。

步骤4：发送完成报告后停止。"
```

### 状态检查与调试

```bash
# 查看所有 agent 状态
ctl agentrun list --workspace "$WS"

# 查看单个 agent 详细状态
ctl agentrun status "$WS/codex"
ctl agentrun status "$WS/claude-code"
ctl agentrun status "$WS/gsd-pi"

# 检查 workspace 状态
ctl workspace get "$WS"

# 取消正在执行的 prompt
ctl agentrun cancel "$WS/codex"

# 查看 shim 历史事件（需要 shim socket 路径）
# 先获取 socket 路径
ctl agentrun attach "$WS/codex"
# 然后用返回的 socketPath 查看历史
ctl shim --socket <socketPath> history
```

### 清理环境

```bash
# 停止所有 agent
ctl agentrun stop "$WS/codex"
ctl agentrun stop "$WS/claude-code"
ctl agentrun stop "$WS/gsd-pi"

# 等待停止后删除
ctl agentrun delete "$WS/codex"
ctl agentrun delete "$WS/claude-code"
ctl agentrun delete "$WS/gsd-pi"

# 删除 workspace
ctl workspace delete "$WS"

# 或直接 Ctrl-C setup.sh，会自动清理
```

## CLI 命令速查

| 操作 | 命令 |
|---|---|
| 注册 agent 模板 | `agentdctl agent apply -f <file.yaml>` |
| 查看 agent 模板 | `agentdctl agent list` |
| 创建本地 workspace | `agentdctl workspace create local <name> --path <abs-path>` |
| 查看 workspace 状态 | `agentdctl workspace get <name>` |
| 创建 agent run | `agentdctl agentrun create --workspace <ws> --name <n> --runtime-class <rc>` |
| 发送 prompt（阻塞等待） | `agentdctl agentrun prompt <ws/name> --text '...' --wait` |
| 发送 prompt（异步） | `agentdctl agentrun prompt <ws/name> --text '...'` |
| 查看 agent 状态 | `agentdctl agentrun status <ws/name>` |
| agent 间消息 | `agentdctl workspace send <ws> --from <a> --to <b> --text '...'` |
| 取消执行中的 prompt | `agentdctl agentrun cancel <ws/name>` |
| 停止 agent | `agentdctl agentrun stop <ws/name>` |
| 删除 agent | `agentdctl agentrun delete <ws/name>` |
| 交互式 chat | `agentdctl shim --socket <path> chat` |
| 查看 shim 状态 | `agentdctl shim --socket <path> state` |
| 查看事件历史 | `agentdctl shim --socket <path> history` |

> 全局参数：`--socket <path>` 指定 ARI socket 路径（默认 `/var/run/agentd/ari.sock`）
