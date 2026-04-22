# gsd-pi-acp ACP 协议支持调研

> 调研日期：2026-04-22
> 项目路径：/Users/jim/code/zoumo/gsd-pi-acp
> ACP SDK 版本：@agentclientprotocol/sdk v0.12.0

## 项目概述

**语言/框架**：TypeScript / Node.js 20+  
**定位**：将 `gsd` 或 `pi` CLI（`--mode rpc`）适配为 ACP 协议的 Agent Adapter  
**传输方式**：stdio only（NDJSON JSON-RPC 2.0）  
**主要依赖**：
- `@agentclientprotocol/sdk` v0.12.0
- `zod` v3.25.0（Zod 验证）
- `tsup` v8.0.0（构建）

**双后端**：自动检测 `gsd` 或 `pi`（优先 `PI_ACP_PI_COMMAND` 环境变量 > `which gsd` > `pi`）

---

## 1. ACP 方法支持

### 实现的方法

| 方法 | 文件:行 | 状态 | 说明 |
|------|---------|------|------|
| `initialize` | agent.ts:72 | ✅ 完整 | 协议 v1，返回 agentInfo/agentCapabilities/authMethods |
| `session/new` | agent.ts:106 | ✅ 完整 | 启动后端子进程，Auth 检查，推送可用命令 |
| `session/prompt` | agent.ts:237 | ✅ 完整 | 含斜杠命令展开和 turn 队列 |
| `session/cancel` | agent.ts:295 | ✅ 完整 | 通过 `proc.abort()` 取消当前 turn |
| `session/load` | agent.ts:333 | ✅ 完整 | 重连已保存的会话，重放历史 |
| `authenticate` | agent.ts:231 | ⚠️ Stub | 接受认证请求（no-op，Terminal Auth 在带外处理） |
| `session/set_mode` | agent.ts:462 | ⚠️ 部分 | 仅支持 thinking level 模式，其他 mode 返回 invalidParams |
| `unstable_listSessions` | agent.ts:300 | ✅ | 列出历史会话（50 条/页游标分页） |
| `unstable_setSessionModel` | agent.ts:428 | ✅ | 按 provider/modelId 设置模型 |

### 明确不支持（README.md:169）

| 方法 | 原因 |
|------|------|
| `fs/read_text_file` | pi/gsd 本地读写，不委托给 client |
| `fs/write_text_file` | 同上 |
| `terminal/create` 等 5 个 | 不委托给 client 终端 |
| `session/request_permission` | 工具审批请求映射为文本消息，无 Zed 权限 UI |

---

## 2. 声明的 agentCapabilities

```typescript
// agent.ts:89-102
agentCapabilities: {
  loadSession: true,
  mcpCapabilities: {
    http: false,      // 不支持 HTTP MCP
    sse: false        // 不支持 SSE MCP
  },
  promptCapabilities: {
    image: true,              // 图片传递给 gsd/pi
    audio: false,             // 不支持音频
    embeddedContext: false    // 不支持 embedded context
  },
  sessionCapabilities: {
    list: {}          // 支持 unstable_listSessions（Zed session picker）
  }
}
```

---

## 3. `_meta` 扩展能力

### 3.1 AuthMethod `_meta["terminal-auth"]`（auth.ts:32-38）

Zed 编辑器 Terminal 登录 UI 扩展：

```typescript
// 在 authMethods 中声明
_meta: {
  "terminal-auth": {
    command: string,            // 启动命令
    args: string[],             // 命令参数
    label: "Launch pi" | "Launch gsd"  // 显示标签
  }
}
```

**触发条件**：Client 在 `initialize` 时声明 `clientCapabilities._meta["terminal-auth"]`（agent.ts:44-50），Agent 才返回此扩展。Zed 读取后渲染 "Authenticate" 横幅。

### 3.2 `session/update` 中的 `_meta.piAcp`（session.ts 多处）

内部队列/状态追踪，当前 client 忽略此字段：

```typescript
// 各类 session_info_update 中附带
_meta: {
  piAcp: {
    queueDepth: number,      // 等待队列中的 prompt 数
    running: boolean,        // 当前是否有 turn 在执行
    startupInfo: string | null  // 启动信息（仅 newSession 响应中有）
  }
}
```

**出现位置**：
- agent.ts:213 — newSession 响应含 startupInfo
- session.ts:329 — 排队通知
- session.ts:357 — 队列清空通知
- session.ts:403 — turn 开始通知
- session.ts:429 — turn 错误通知
- session.ts:690 — turn 完成，含队列状态更新

---

## 4. 自定义扩展方法（`_` 前缀）

无标准 `_` 前缀 ACP 方法。但两个非标准方法使用 `unstable_` 前缀：

| 方法 | 文件:行 | 说明 |
|------|---------|------|
| `unstable_listSessions` | agent.ts:300 | Zed session picker 专用（非标准方法名） |
| `unstable_setSessionModel` | agent.ts:428 | 私有扩展，Zed 直接依赖 |

---

## 5. `session/update` 事件类型

| 事件类型 | 文件:行 | 说明 |
|----------|---------|------|
| `agent_message_chunk` | session.ts:444, 619, 636 | 流式 Assistant 文本输出 |
| `agent_thought_chunk` | session.ts:452 | 思考输出（thinking 模式开启时） |
| `tool_call` | session.ts:478, 524 | 工具调用发起（pending 状态） |
| `tool_call_update` | session.ts:536, 555, 605 | 工具进度/完成/失败/diff |
| `session_info_update` | session.ts:329, 357, 403, 429, 690 | 元数据更新（title/updatedAt/_meta.piAcp） |
| `user_message_chunk` | session-lifecycle.ts:87 | 历史重放用户消息 |
| `available_commands_update` | session-lifecycle.ts:40 | 会话初始化后推送斜杠命令列表 |
| `current_mode_update` | agent.ts:476 | 切换 thinking level 后通知 |

**未发送**：`config_option_update`（无 ACP 层配置项）、`plan`（不支持规划模式）

---

## 6. Session Modes 与 ConfigOptions

### Session Modes（Thinking Levels）

```typescript
// model-utils.ts
type ThinkingLevel = 'off' | 'minimal' | 'low' | 'medium' | 'high' | 'xhigh'
```

- `setSessionMode()` 仅接受上述 thinking level 值（agent.ts:462-482）
- 其他 mode（edit/plan）一律返回 `invalidParams` 错误
- 变更后发送 `current_mode_update` 通知

### ConfigOptions

**未实现**——无 ACP 层 configOptions 暴露。模型/模式配置通过 `unstable_setSessionModel` 和 `setSessionMode` 单独处理。

---

## 7. 内置斜杠命令（builtin-commands.ts）

| 命令 | 输入 | 说明 |
|------|------|------|
| `/compact` | 可选自定义指令 | 手动压缩会话上下文 |
| `/autocompact` | `on\|off\|toggle` | 切换自动上下文压缩 |
| `/export` | — | 导出会话为 HTML 文件 |
| `/session` | — | 显示会话统计（消息数/tokens/cost/文件路径） |
| `/name` | `<name>` | 设置会话显示名称 |
| `/steering` | `all\|one-at-a-time` | 控制 pi steering 消息的发送模式 |
| `/follow-up` | `all\|one-at-a-time` | 控制 pi follow-up 消息的发送模式 |
| `/changelog` | — | 显示 pi changelog |

内置命令与文件命令（`.gsd/prompts/**/*.md`、`.pi/agent/prompts/**/*.md`）合并后通过 `available_commands_update` 推送（session-lifecycle.ts:40-41）。

---

## 8. 认证方法

```typescript
// auth.ts:13-42
{
  id: 'pi_terminal_login',
  name: 'Launch pi in the terminal',
  type: 'terminal',
  args: ['--terminal-login'],
  env: {},
  _meta: { 'terminal-auth': { command, args, label } }  // Zed 扩展
}
```

**Terminal Auth 流程**：
1. Client 以 `--terminal-login` 启动 Agent
2. Agent 通过 `spawnSync()` 交互式启动后端子进程（index.ts:17-21）
3. 用户在终端完成 API Key/登录配置
4. 子进程退出，Agent 退出
5. Client 正常重启 Agent

---

## 9. Pi RPC 后端接口

Agent 通过 NDJSON RPC 与后端子进程（pi/gsd `--mode rpc`）通信：

```typescript
// pi-rpc/process.ts:39-62
type PiRpcCommand =
  | { type: 'prompt'; message: string; images?: unknown[] }
  | { type: 'abort' }
  | { type: 'get_state' }
  | { type: 'get_available_models' }
  | { type: 'set_model'; provider: string; modelId: string }
  | { type: 'set_thinking_level'; level: ThinkingLevel }
  | { type: 'compact'; customInstructions?: string }
  | { type: 'set_auto_compaction'; enabled: boolean }
  | { type: 'get_session_stats' }
  | { type: 'set_session_name'; name: string }
  | { type: 'export_html'; outputPath?: string }
  | { type: 'get_messages' }
  | { type: 'get_commands' }
  // ... 等
```

---

## 10. Pi 后端事件 → ACP 事件映射

| Pi 事件 | ACP 翻译 | 文件:行 |
|---------|----------|---------|
| `message_update` (text_delta) | `agent_message_chunk` | session.ts:442 |
| `message_update` (thinking_delta) | `agent_thought_chunk` | session.ts:450 |
| `message_update` (toolcall_*) | `tool_call` (pending) | session.ts:462 |
| `tool_execution_start` | `tool_call` (in_progress) 或 `tool_call_update` | session.ts:496 |
| `tool_execution_update` | `tool_call_update` (in_progress + content) | session.ts:547 |
| `tool_execution_end` | `tool_call_update` (completed/failed，edit 含结构化 diff) | session.ts:566 |
| `auto_retry_start/end` | `agent_message_chunk`（格式化状态消息） | session.ts:617, 625 |
| `auto_compaction_start/end` | `agent_message_chunk`（格式化状态消息） | session.ts:633, 641 |
| `agent_end` | 解决 session/prompt Promise | session.ts:663 |
| `process_exit` | `settleAllPending('error')` | session.ts:697 |

---

## 11. 结构化 Diff 支持（session.ts:503-614）

Edit 工具执行前后做文件快照，生成结构化 diff：

```typescript
// tool_call_update 中的 content[].type = 'diff'
{
  type: 'diff',
  path: string,    // 文件相对路径
  oldText: string, // 编辑前内容
  newText: string  // 编辑后内容
}
```

Zed 通过此 diff 渲染可视化差异，替代纯文本工具输出。

---

## 12. Turn 队列机制（session.ts:293-341）

```
最大深度：20（可通过 PI_ACP_MAX_QUEUE_DEPTH 配置）
```

- 当前 turn 执行期间新 prompt 进入队列
- 每个排队 prompt 发送带位置的 `agent_message_chunk` 通知
- `agent_end` 事件解决 Promise，触发下一个排队 prompt

**崩溃恢复**：子进程退出时，`settleAllPending('error')` 解决所有 pending/排队的 prompt。

---

## 13. 双后端配置差异

| 配置项 | gsd | pi |
|--------|-----|-----|
| 默认命令 | `gsd` / `gsd.cmd`(Win) | `pi` / `pi.cmd`(Win) |
| Agent 目录 | `~/.gsd` | `~/.pi/agent` |
| 设置文件 | `~/.gsd/settings.json` | `~/.pi/agent/settings.json` |
| Prompts | `~/.gsd/prompts` | `~/.pi/agent/prompts` |
| Sessions | `~/.gsd/sessions/<cwd-hash>/` | `~/.pi/agent/sessions/...` |
| 启动参数 | `--mode rpc` | `--mode rpc --no-themes` |
| MCP 配置 | 写入 `.gsd/mcp.json` | 不透传 |

---

## 14. 已知合规性缺口（docs/acp-compliance.md）

### 高优先级 🔴

| 问题 | 影响 |
|------|------|
| `authenticate` 方法无验证逻辑（agent.ts:231-235） | 返回 success 但不验证 API Key 有效性 |
| `session/request_permission` 未调用 | pi 工具审批请求映射为文本消息，无 Zed 权限 UI |

### 中优先级 🟡

| 问题 | 影响 |
|------|------|
| `unstable_listSessions` 非标准方法名 | SDK 升级可能 breaking |
| `setSessionMode` 仅支持 thinking levels | 拒绝 edit/plan 等其他 ACP 模式 |

### 低优先级 🟢

| 问题 | 影响 |
|------|------|
| `_meta.piAcp` 未文档化 | 非标准扩展，client 忽略 |
| `sessionCapabilities.list: {}` 是 Zed hack | SDK 升级风险 |

---

## 15. 关键文件

| 文件 | 行数 | 说明 |
|------|------|------|
| `src/acp/agent.ts` | ~430 | ACP 方法处理器 |
| `src/acp/session.ts` | ~770 | 会话生命周期和 turn 队列 |
| `src/acp/session-lifecycle.ts` | — | 启动和历史重放 |
| `src/acp/builtin-commands.ts` | — | 内置斜杠命令定义 |
| `src/acp/auth.ts` | ~42 | 认证方法声明 |
| `src/acp/model-utils.ts` | — | Thinking level 和模型状态 |
| `src/pi-rpc/process.ts` | ~550 | 子进程 NDJSON RPC |
| `src/pi-rpc/schemas.ts` | — | Zod 验证 Schema |
| `src/backend/config.ts` | ~191 | 后端检测和配置（gsd vs pi） |
| `src/index.ts` | — | 入口，stdio 和信号处理 |
