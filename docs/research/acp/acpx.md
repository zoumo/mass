# acpx - 3W2H 深度调研

> 调研日期: 2026-04-01
> 当前版本: v0.4.0 (2026-03-29 发布)
> GitHub: https://github.com/openclaw/acpx
> npm: https://www.npmjs.com/package/acpx
> 许可证: MIT
> 语言: TypeScript
> 运行时: Node.js >= 22.12.0

---

## 目录

- [What - 是什么](#what---是什么)
- [Why - 为什么](#why---为什么)
- [Who - 谁在做](#who---谁在做)
- [How - 怎么做](#how---怎么做)
- [How Well - 做得怎么样](#how-well---做得怎么样)
- [对 MASS 的启示](#对-oai-的启示)
- [参考链接](#参考链接)

---

## What - 是什么

### 一句话定义

acpx 是一个无头（headless）CLI 客户端，实现了 [Agent Client Protocol (ACP)](https://agentclientprotocol.com)，让 AI Agent 和编排器可以通过结构化协议与编程 Agent 通信，而不是通过 PTY 抓取终端字符。

### 核心思想

acpx 解决的是 **Agent 间通信的标准化接入问题**：

```
没有 acpx:                              有了 acpx:
  Orchestrator ─┬─ PTY 抓取 Codex        Orchestrator ─┐
  Orchestrator ─┼─ PTY 抓取 Claude                     │
  Script       ─┼─ 解析 ANSI 转义         Script       ─┼── acpx ── ACP ──┬─ Codex
  CI Pipeline  ─┼─ 各自适配              CI Pipeline  ─┤                  ├─ Claude
  Agent A      ─┴─ 屏幕刮取 Agent B      Agent A      ─┘                  ├─ Pi
  (每个都要处理 PTY/ANSI)                (统一 ACP 接口)                  └─ OpenClaw
```

### 关键特征

| 特征 | 描述 |
|---|---|
| **ACP 原生** | 通过 JSON-RPC 2.0 / ndjson stdio 直接与 ACP 适配器通信，不抓取终端 |
| **多 Agent 统一** | 一套命令面支持 Pi、OpenClaw、Codex、Claude、Gemini、Cursor 等 15+ 编程 Agent |
| **持久会话** | 多轮对话在调用间持久化，按仓库目录作用域隔离 |
| **命名会话** | 在同一仓库内运行并行工作流（`-s backend`、`-s frontend`） |
| **提示词队列** | 在当前提示词运行时提交新提示词，按序执行 |
| **崩溃恢复** | 检测死亡的 Agent 进程并自动重载会话 |
| **结构化输出** | 类型化的 ACP 消息（thinking、tool_call、diff）替代 ANSI 抓取 |
| **工作流引擎** | 实验性的 `flow run` 命令，支持多步骤 TypeScript 工作流模块 |
| **Agent 优先** | 主要用户是另一个 Agent/编排器/工具链，人类可用性是次要约束 |

### 定位

acpx 不是一个编排框架，也不是要替代各 Agent 的原生工具。它的定位是：

- **最小可用的 ACP 客户端**：让 ACP 变得实用、健壮、易于组合
- **可复用的后端层**：为不想重新实现会话管理、队列、生命周期的工具提供基础设施
- **互操作性桥梁**：在不同 ACP 适配器之间抹平差异

---

## Why - 为什么

### 解决的核心问题

#### 1. PTY 抓取的脆弱性

在 acpx 出现之前，Agent-to-Agent 通信主要依赖 PTY（伪终端）抓取：

- 需要解析 ANSI 转义序列、颜色代码、光标移动
- 不同终端、不同 Agent 的输出格式各异
- Agent 的 UI 更新（进度条、spinner）会干扰抓取
- 结构化数据（diff、工具调用）被压平为纯文本
- 极难可靠地判断一个操作何时完成

acpx 通过 ACP 协议将这些问题从根本上消除——所有通信都是结构化 JSON。

#### 2. N x M 适配器问题

每个编排工具/CI 系统想对接每个编程 Agent，都需要单独适配：

- Codex 有自己的 CLI 接口
- Claude Code 有自己的 CLI 接口
- Pi 有自己的 CLI 接口
- 每个工具都有不同的会话管理方式

acpx 作为统一的 ACP 客户端，将 N 个消费者 x M 个 Agent 的问题降维为 N + M。

#### 3. Agent 间协作的刚需

随着 AI 编程 Agent 的成熟，出现了越来越多的 Agent 间协作场景：

- Agent A 需要委托子任务给 Agent B
- CI 流水线需要自动化调用编程 Agent
- 编排器需要在多个 Agent 间调度工作
- 需要在不同 Agent 间保持会话上下文

这些场景都需要一个可靠的、结构化的通信层。

#### 4. 会话管理的复杂性

生产级别的 Agent 交互需要：

- 持久化多轮对话上下文
- 处理 Agent 进程崩溃和重连
- 支持并行工作流
- 管理提示词排队和取消
- 处理权限控制和安全沙箱

这些基础设施如果每个工具各自实现，既浪费又容易出错。

### 为什么是 CLI 而非库/SDK

- **最大兼容性**：任何能执行 shell 命令的工具都能使用 acpx
- **语言无关**：不限于 TypeScript/JavaScript 生态
- **进程隔离**：Agent 进程管理天然适合独立进程
- **组合性**：Unix 哲学——可以与管道、脚本、CI 工具自由组合

---

## Who - 谁在做

### 组织

acpx 隶属于 [OpenClaw](https://github.com/openclaw) 组织，这是一个开源 AI 编程 Agent 项目。

- **主项目**: [openclaw/openclaw](https://github.com/openclaw/openclaw) — 开源编程 Agent
- **acpx**: openclaw 生态中的 ACP 客户端工具
- **团队邮箱**: dev@openclaw.ai
- **Discord**: https://discord.gg/qkhbAGHRBT
- **X/Twitter**: @openclaw, @steipete

### 核心贡献者

| 贡献者 | 角色/贡献领域 |
|---|---|
| **@dutifulbob (Bob)** | 架构师，实现了初始 CLI、ACP 会话模型、队列所有者架构、核心运行时 |
| **@osolmaz** | 核心维护者，实现了 Flows 引擎、trace/replay 系统、PR triage 示例、多项 bug 修复 |
| **@onutc** | 发布管理，贡献者文档同步 |
| **@vincentkoc** | 大量 Agent 集成（Copilot、Droid）、性能优化、测试覆盖 |
| **@frankekn** | Claude 会话选项、mcpServers 透传、sessions read 命令 |
| **@lynnzc** | ACP 一致性测试套件、Windows 兼容性、权限统计 |
| **@gandli** | iFlow/Kimi/Qwen Agent 集成 |

### 社区规模

- npm 周下载量有徽章但未公开具体数字
- GitHub 上持续活跃的贡献者约 20+
- 从 v0.1.3（2026-02-18）到 v0.4.0（2026-03-29），6 周内发布了 ~20 个版本
- 项目处于 **alpha** 阶段，接口仍在快速演进

---

## How - 怎么做

### 整体架构

acpx 的数据路径非常清晰：

```
CLI 命令 → AcpClient → ndjson/stdio → ACP 适配器 → 编程 Agent

具体来说：
┌──────────┐    ┌───────────┐    ┌─────────────┐    ┌──────────┐
│  acpx    │───>│ AcpClient │───>│ ACP Adapter │───>│ Coding   │
│  CLI     │<───│ (JSON-RPC)│<───│ (子进程)     │<───│ Agent    │
└──────────┘    └───────────┘    └─────────────┘    └──────────┘
                 ndjson/stdio     e.g. codex-acp      e.g. Codex
```

CLI 从不抓取终端文本，所有通信都是结构化的 ACP JSON-RPC 消息。

### 源码结构

```
src/
├── cli.ts                        # 入口文件
├── cli-core.ts                   # 命令处理和顶层 CLI 流程 (46KB，核心逻辑)
├── cli-public.ts                 # 公共 CLI 接口
├── cli/                          # CLI 子命令模块
├── client.ts                     # ACP 客户端：传输层和协议方法 (54KB)
├── config.ts                     # 配置加载和默认值
├── agent-registry.ts             # 内置 Agent 名称和启动命令映射
├── session-runtime.ts            # 会话生命周期和运行时行为 (52KB)
├── session-runtime/              # 会话运行时子模块
├── session-persistence.ts        # 会话持久化接口
├── session-persistence/          # 会话持久化实现
├── session-conversation-model.ts # 会话对话模型
├── session-events.ts             # 会话事件系统
├── queue-ipc.ts                  # 队列 IPC 客户端 (21KB)
├── queue-ipc-server.ts           # 队列 IPC 服务端 (13KB)
├── queue-messages.ts             # 队列消息类型
├── queue-lease-store.ts          # 队列租约存储
├── queue-owner-turn-controller.ts # 队列所有者轮次控制
├── permissions.ts                # 权限策略
├── filesystem.ts                 # 文件系统客户端方法 (fs/*)
├── terminal.ts                   # 终端客户端方法 (terminal/*)
├── output.ts                     # 流式文本输出格式化
├── output-json-formatter.ts      # JSON 输出格式化
├── flows.ts                      # Flows 运行时入口
├── flows/                        # Flows 引擎实现
├── types.ts                      # 核心类型定义
├── errors.ts                     # 错误类型
├── error-normalization.ts        # 错误规范化处理
├── acp-error-shapes.ts           # ACP 错误形状匹配
├── acp-jsonrpc.ts                # ACP JSON-RPC 工具
├── codex-compat.ts               # Codex 兼容性层
└── version.ts                    # 版本解析
```

### ACP 协议流程

acpx 实现的典型提示词流程：

```
1. initialize        — 握手，协商客户端能力
2. session/new        — 创建新会话（或 session/load 恢复已有会话）
3. session/prompt     — 发送提示词
4. session/update     — 流式接收更新通知直到完成
   ├── thinking       — Agent 思考过程
   ├── tool_call      — 工具调用（读文件、写文件、执行命令等）
   ├── text           — Agent 文本输出
   └── diff           — 代码变更
5. end_turn           — 本轮完成
```

客户端能力声明（initialize 阶段）：

```json
{
  "capabilities": {
    "fs": {
      "readTextFile": true,    // 允许 Agent 读取文件
      "writeTextFile": true    // 允许 Agent 写入文件
    },
    "terminal": {
      "create": true,          // 允许 Agent 创建终端
      "output": true,          // 终端输出回传
      "waitForExit": true,     // 等待进程退出
      "kill": true,            // 终止进程
      "release": true          // 释放终端
    }
  }
}
```

### ACP 覆盖范围

acpx 当前已实现的 ACP 方法：

| ACP 方法 | acpx 命令 | 状态 |
|---|---|---|
| `initialize` | 自动握手 | 已支持 |
| `session/new` | `sessions new` | 已支持 |
| `session/load` | 崩溃恢复/重连 | 已支持 |
| `session/prompt` | `prompt`、`exec`、隐式提示 | 已支持 |
| `session/update` | 流式输出 | 已支持 |
| `session/cancel` | `cancel` | 已支持 |
| `session/set_mode` | `set-mode` | 已支持 |
| `session/set_config_option` | `set <key> <value>` | 已支持 |
| `session/request_permission` | `--approve-all/reads/deny-all` | 已支持 |
| `authenticate` | 认证握手 | 已支持 |
| `fs/read_text_file` | 文件读取处理器 | 已支持 |
| `fs/write_text_file` | 文件写入处理器 | 已支持 |
| `terminal/*` | 完整终端生命周期 | 已支持 |
| `session/fork` | 分支会话 | 未支持 (unstable) |
| `session/list` | 服务端会话列表 | 未支持 (unstable) |
| `session/resume` | 恢复暂停的会话 | 未支持 (unstable) |

### Agent 注册表

acpx 内置了 15+ 编程 Agent 的适配器注册：

| Agent 名称 | 适配器 | 底层 Agent |
|---|---|---|
| `pi` | pi-acp (npm) | Pi Coding Agent |
| `openclaw` | 原生 (`openclaw acp`) | OpenClaw ACP bridge |
| `codex` | codex-acp (npm) | Codex CLI (OpenAI) |
| `claude` | claude-agent-acp (npm) | Claude Code (Anthropic) |
| `gemini` | 原生 (`gemini --acp`) | Gemini CLI (Google) |
| `cursor` | 原生 (`cursor-agent acp`) | Cursor CLI |
| `copilot` | 原生 (`copilot --acp --stdio`) | GitHub Copilot CLI |
| `droid` | 原生 (`droid exec --output-format acp`) | Factory Droid |
| `iflow` | 原生 (`iflow --experimental-acp`) | iFlow CLI |
| `kilocode` | `npx @kilocode/cli acp` | Kilocode |
| `kimi` | 原生 (`kimi acp`) | Kimi CLI (MoonshotAI) |
| `kiro` | 原生 (`kiro-cli-chat acp`) | Kiro CLI (AWS) |
| `opencode` | `npx opencode-ai acp` | OpenCode |
| `qoder` | 原生 (`qodercli --acp`) | Qoder CLI |
| `qwen` | 原生 (`qwen --acp`) | Qwen Code (Alibaba) |
| `trae` | 原生 (`traecli acp serve`) | Trae CLI (ByteDance) |

适配器分为两种模式：
- **npm 适配器**：通过 `npx` 自动下载，无需手动安装（如 pi-acp、codex-acp）
- **原生支持**：Agent 自身内置 ACP 支持，直接启动即可（如 gemini --acp）

对于自定义 Agent，可使用 `--agent` 逃生舱：

```bash
acpx --agent ./my-custom-acp-server 'do something'
```

### 会话管理

会话是 acpx 的核心概念，元数据存储在 `~/.acpx/sessions/*.json`：

```json
{
  "id": "session-uuid",
  "acpSessionId": "acp-session-id",
  "agentCommand": "codex",
  "cwd": "/path/to/repo",
  "name": "api",                    // 可选命名会话
  "createdAt": "2026-03-29T...",
  "lastUsedAt": "2026-03-29T...",
  "closedAt": null,                 // 软关闭时间戳
  "closed": false,
  "pid": 12345,                     // 适配器进程 PID
  "capabilities": { ... }           // 协议/版本能力快照
}
```

**会话路由机制**：

```
提示词路由：
  1. 从 cwd 或 --cwd 开始
  2. 向上遍历目录树到最近的 git root
  3. 匹配 (agent command, dir, optional name) 三元组
  4. 如果没找到 git root，仅精确匹配 cwd

命名会话：
  acpx codex -s api 'implement token pagination'     # api 会话
  acpx codex -s docs 'rewrite API docs'              # docs 会话
  → 同一仓库内的并行工作流
```

**会话生命周期**：

```
sessions new    → 创建新会话（软关闭之前的会话）
sessions ensure → 幂等：返回已有会话或创建新会话
sessions close  → 软关闭：终止进程，保留记录
sessions show   → 查看会话元数据
sessions history → 查看最近对话历史
```

**崩溃恢复**：当下次提示词发现保存的 PID 已死亡时：

```
1. 检测到 PID 死亡
2. 重新启动 Agent 进程
3. 尝试 session/load 恢复会话
4. 如果 load 失败，透明降级到 session/new
```

### 队列所有者架构

acpx 的提示词提交是队列感知的，这是其核心设计之一：

```
                    ┌──────────────────────────┐
                    │    Queue Owner Process    │
                    │  (一个 acpx 进程成为所有者)  │
                    │                          │
  acpx prompt ─────>│  ┌─────┐  ┌─────┐       │──── ACP ──── Agent
  acpx prompt ─────>│  │ P1  │  │ P2  │ Queue │
  acpx prompt ─────>│  └─────┘  └─────┘       │
                    │                          │
                    │  TTL: 300s (可配置)       │
                    └──────────────────────────┘
                    Unix Domain Socket / IPC

工作方式：
  1. 第一个 acpx 进程成为 "队列所有者"
  2. 后续调用通过 IPC 提交提示词到队列
  3. 队列所有者按序执行提示词
  4. 空闲 TTL 到期后所有者退出（默认 300 秒）
  5. --no-wait 提交后立即返回
  6. cancel 通过 IPC 发送 session/cancel 而不拆除会话
```

这种设计解决了：
- 并发提示词的序列化
- Agent 进程的生命周期管理
- 热重用（warm reuse）减少启动延迟
- `--ttl` 控制所有者存活时间

### 权限系统

acpx 处理 ACP `requestPermission` 回调：

```
模式层次：
  --approve-all          全部自动批准
  --approve-reads        读取/搜索自动批准，写入需要交互确认
  --deny-all             全部拒绝

路径沙箱：
  所有文件操作默认限制在 cwd 范围内

非交互模式：
  --non-interactive-permissions fail    非 TTY 环境下失败而非拒绝

权限统计：
  跟踪 requested/approved/denied/cancelled 用于退出码行为
```

### Flows 工作流引擎

acpx v0.4.0 引入了实验性的 Flows 系统，用于多步骤 ACP 工作流：

```typescript
// 示例：TypeScript 流程定义
import { defineFlow, acp, action, compute, checkpoint } from "acpx/flows";

export default defineFlow({
  name: "pr-triage",
  nodes: {
    prepare:    action({ ... }),      // 准备隔离工作区
    analyze:    acp({ ... }),         // 提取意图（模型推理）
    classify:   acp({ ... }),         // 分类 bug vs feature
    run_tests:  action({ ... }),      // 执行测试（确定性）
    decide:     compute({ ... }),     // 本地路由决策
    wait:       checkpoint(),         // 等待外部事件
  },
  edges: [
    { from: "prepare", to: "analyze" },
    { from: "analyze", to: "classify" },
    {
      from: "decide",
      switch: {
        on: "$.next",
        cases: {
          run_tests: "run_tests",
          wait: "wait",
        },
      },
    },
  ],
});
```

**四种步骤类型**：

| 类型 | 用途 | 示例 |
|---|---|---|
| `acp` | 模型推理工作 | 提取意图、判断方案、分类、代码变更 |
| `action` | 确定性的运行时动作 | shell 命令、git 操作、GitHub API、测试执行 |
| `compute` | 纯本地转换 | 数据归一化、路由决策、信号聚合 |
| `checkpoint` | 暂停等待外部事件 | 人类决策、外部回调 |

**关键设计决策**：

- 路由必须在运行时侧确定性完成，不在 Agent 内部
- 每个流程运行默认获得一个主 ACP 会话
- 节点产出结构化输出，运行时根据输出决定下一步
- 节点结果有四种 outcome：`ok`、`timed_out`、`failed`、`cancelled`
- 每个节点可以绑定不同的 cwd（工作目录隔离）

**流程状态持久化**：

```
~/.acpx/flows/runs/
├── <run-id>/
│   ├── manifest.json      # 运行元数据
│   ├── flow.json          # 流程定义快照
│   ├── trace.ndjson       # 追踪事件流
│   ├── projections/       # 投影数据
│   └── sessions/          # 关联的 ACP 会话回放数据
```

**Trace/Replay 系统**：

流程运行产生追踪数据包（run bundle），支持：
- 步骤级回放和检查
- React Flow 可视化查看器（`examples/flows/replay-viewer/`）
- 每次尝试的 ACP/action 回执

### 输出格式

acpx 支持四种输出模式：

```bash
# text（默认）：人类可读的流式输出
acpx codex 'review this PR'

# json：NDJSON 事件流，用于自动化
acpx --format json codex exec 'review' | jq 'select(.type=="tool_call")'

# json-strict：抑制非 JSON 的 stderr 输出
acpx --format json --json-strict codex exec 'review'

# quiet：仅最终助手文本
acpx --format quiet codex 'give me a summary'

# suppress-reads：隐藏文件读取内容
acpx --suppress-reads codex exec 'inspect repo'
```

JSON 事件包含稳定的信封结构用于关联：

```json
{
  "eventVersion": 1,
  "sessionId": "abc123",
  "requestId": "req-42",
  "seq": 7,
  "stream": "prompt",
  "type": "tool_call"
}
```

### 配置系统

两层配置，后者优先：

```
1. 全局: ~/.acpx/config.json
2. 项目: <cwd>/.acpxrc.json
3. CLI 标志永远最高优先
```

配置示例：

```json
{
  "defaultAgent": "codex",
  "defaultPermissions": "approve-all",
  "nonInteractivePermissions": "deny",
  "authPolicy": "skip",
  "ttl": 300,
  "timeout": null,
  "format": "text",
  "agents": {
    "my-custom": { "command": "./bin/my-acp-server" }
  },
  "auth": {
    "my_auth_method_id": "credential-value"
  }
}
```

### 一致性测试套件

acpx 包含数据驱动的 ACP 核心 v1 一致性测试（`conformance/`）：

```
conformance/
├── README.md         # 测试说明
├── cases/            # 测试用例定义
├── profiles/         # 测试配置文件
├── runner/           # 测试运行器
└── spec/             # ACP 规范引用
```

CI 集成了冒烟测试覆盖和夜间覆盖，验证 acpx 对 ACP 规范的符合度。

### 构建工具链

| 工具 | 用途 |
|---|---|
| `pnpm@10.23.0` | 包管理器 |
| `tsdown` | TypeScript 构建（ESM, Node22 target） |
| `tsgo`（@typescript/native-preview） | TypeScript 类型检查（原生加速） |
| `oxlint` | 代码检查（替代 ESLint） |
| `oxfmt` | 代码格式化 |
| `husky` + `lint-staged` | Git 钩子 |
| `markdownlint-cli2` | Markdown 检查 |
| Node.js 内置 `--test` | 测试运行器 |

---

## How Well - 做得怎么样

### 发布节奏与成熟度

| 指标 | 状态 |
|---|---|
| 首个版本 | v0.1.3 (2026-02-18) |
| 最新版本 | v0.4.0 (2026-03-29) |
| 发布频率 | 6 周内约 20 个版本，极其活跃 |
| 阶段 | **Alpha** — CLI/运行时接口可能随时变化 |
| 测试覆盖 | 行覆盖 83%、分支覆盖 76%、函数覆盖 86%（CI 门禁） |

### 优势

1. **协议选择正确**：ACP 是正在被广泛采纳的标准，选择做 ACP 客户端而非自己发明协议
2. **Agent 生态覆盖广**：15+ 内置 Agent，几乎覆盖所有主流编程 Agent
3. **架构设计精良**：
   - 队列所有者模式优雅地解决了并发问题
   - 会话持久化和崩溃恢复保证了健壮性
   - Flows 引擎的 ACP/runtime 边界划分清晰
4. **社区贡献活跃**：多个外部贡献者持续贡献 Agent 集成和 bug 修复
5. **工程质量高**：
   - 使用最新工具链（tsgo、oxlint、oxfmt）
   - CI/CD 完整（GitHub Actions）
   - 覆盖率门禁和一致性测试
6. **文档全面**：VISION.md 清晰定义边界，AGENTS.md 为贡献者提供完整指引
7. **设计克制**：明确 "不应成为什么"，避免功能膨胀

### 不足与风险

1. **Alpha 阶段不稳定**：接口可能随时变化，下游构建存在断裂风险
2. **对 ACP 协议的强依赖**：ACP 本身也在快速演进（v0.17.0），协议变更直接影响 acpx
3. **适配器质量参差不齐**：不同 Agent 对 ACP 的实现程度不一，acpx 需要大量兼容性处理
4. **Flows 仍为实验性**：多步骤工作流尚未稳定
5. **Windows 支持仍在完善**：多个 Windows 相关的修复表明平台兼容性仍有挑战
6. **未支持的 ACP 方法**：session/fork、session/list 等关键方法仍标记为 unstable
7. **单一包管理器**：强制 pnpm，可能对某些环境造成不便

### 与同类工具的对比

| 维度 | acpx | 直接使用各 Agent CLI | 自建编排层 |
|---|---|---|---|
| 统一接口 | 一套命令面 | 每个 Agent 各异 | 需自行实现 |
| 会话管理 | 内置持久化/恢复 | 各 Agent 各异 | 需自行实现 |
| Agent 间通信 | 结构化 ACP | PTY 抓取 | 取决于实现 |
| 多 Agent 切换 | 一行命令 | 需要重写逻辑 | 需要适配层 |
| 维护成本 | 依赖社区 | 跟踪每个 Agent | 完全自负 |

---

## 对 MASS 的启示

### 1. ACP 协议是趋势

acpx 的快速发展和广泛采纳验证了 ACP 作为 Agent 间通信标准的价值。对于 MASS 的 Agent 平台：

- 应该关注 ACP 协议的发展方向
- 考虑原生支持 ACP 作为 Agent 通信接口
- 可以参考 acpx 的适配器模式来设计多 Agent 接入层

### 2. Agent 优先的 CLI 设计

acpx 明确以 "Agent/编排器是主要用户，人类是次要用户" 的定位设计 CLI，这种思路值得借鉴：

- 结构化输出（JSON/NDJSON）优先于人类友好的文本
- 稳定的退出码和事件信封用于自动化消费
- 非交互模式的完善考虑

### 3. 会话管理是基础设施

acpx 的会话持久化、队列所有者、崩溃恢复等机制表明，健壮的会话管理是 Agent 协作的核心基础设施，值得投入。

### 4. Flows 的 ACP/Runtime 边界

acpx Flows 的核心设计原则——"ACP 负责推理，运行时负责监督"——是一个值得参考的架构模式，避免了将工作流逻辑混入 Agent 的长对话中。

### 5. 一致性测试的价值

acpx 的 ACP 一致性测试套件确保了协议实现的正确性。对于任何基于协议的系统，早期建立一致性测试都是好实践。

---

## 参考链接

- **GitHub 仓库**: https://github.com/openclaw/acpx
- **npm 包**: https://www.npmjs.com/package/acpx
- **ACP 协议官网**: https://agentclientprotocol.com
- **ACP 协议规范**: https://github.com/agentclientprotocol/agent-client-protocol
- **OpenClaw 主项目**: https://github.com/openclaw/openclaw
- **ACP SDK**: https://www.npmjs.com/package/@agentclientprotocol/sdk
- **架构文档**: https://github.com/openclaw/acpx/blob/main/docs/2026-02-17-architecture.md
- **Flows 架构**: https://github.com/openclaw/acpx/blob/main/docs/2026-03-25-acpx-flows-architecture.md
- **ACP 覆盖路线图**: https://github.com/openclaw/acpx/blob/main/docs/2026-02-19-acp-coverage-roadmap.md
- **CLI 参考**: https://github.com/openclaw/acpx/blob/main/docs/CLI.md
- **Vision 文档**: https://github.com/openclaw/acpx/blob/main/VISION.md
- **Discord 社区**: https://discord.gg/qkhbAGHRBT
