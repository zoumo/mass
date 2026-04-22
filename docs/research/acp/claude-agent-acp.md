# claude-agent-acp ACP 协议支持调研

> 调研日期：2026-04-22
> 项目版本：0.30.0
> 仓库：https://github.com/agentclientprotocol/claude-agent-acp

## 项目概述

**语言/框架**：TypeScript / Node.js  
**定位**：将 `@anthropic-ai/claude-agent-sdk` 适配为 ACP 协议的 Agent Adapter  
**传输方式**：stdio only（ndjson NDJSON 换行分隔 JSON）  
**主要依赖**：
- `@agentclientprotocol/sdk` v0.19.0
- `@anthropic-ai/claude-agent-sdk` v0.2.114

---

## 1. ACP 方法支持

### Core 方法

| 方法 | 状态 | 文件:行 | 备注 |
|------|------|---------|------|
| `initialize` | ✅ | acp-agent.ts:364 | 返回协议版本 1、agentCapabilities、authMethods |
| `session/new` | ✅ | acp-agent.ts:496 | 创建新会话，触发 available_commands_update |
| `session/load` | ✅ | acp-agent.ts:537 | 加载持久化会话并重放历史 |
| `session/list` | ✅ | acp-agent.ts:550 | 列出所有会话（含分页） |
| `authenticate` | ✅ | acp-agent.ts:568 | 支持 gateway 认证方式 |
| `session/prompt` | ✅ | acp-agent.ts:576 | 主执行循环，含流式输出和工具调用路由 |
| `session/cancel` | ✅ | acp-agent.ts:1050 | 取消进行中的 prompt，解决 pending messages |
| `session/set_config_option` | ✅ | acp-agent.ts:1116 | 设置 mode、model、effort |
| `session/set_mode` | ✅ | acp-agent.ts:1106 | 设置权限模式 |
| `fs/read_text_file` | ✅ | acp-agent.ts:1234 | 透传给 client |
| `fs/write_text_file` | ✅ | acp-agent.ts:1239 | 透传给 client |
| `session/request_permission` | ✅ | acp-agent.ts:1244 | canUseTool() 实现三种模式 |

### Unstable 扩展方法

| 方法 | 文件:行 | 说明 |
|------|---------|------|
| `unstable_forkSession` | acp-agent.ts:508 | 从现有会话创建 fork |
| `unstable_resumeSession` | acp-agent.ts:527 | 恢复暂停的会话 |
| `unstable_closeSession` | acp-agent.ts:1082 | 清理会话资源 |
| `unstable_setSessionModel` | acp-agent.ts:1090 | 切换模型（含别名解析） |

### 未实现

`terminal/create`、`terminal/output`、`terminal/wait_for_exit`、`terminal/kill`、`terminal/release`  
→ 终端输出通过 `_meta.terminal_output` 扩展走 tool_call_update 实现（见 §3）

---

## 2. 声明的 agentCapabilities

```typescript
// acp-agent.ts:465-486
agentCapabilities: {
  _meta: {
    claudeCode: {
      promptQueueing: true,   // 支持多 prompt 队列
    }
  },
  promptCapabilities: {
    image: true,              // 支持 base64 和 URL 图片
    audio: false,             // 不支持音频
    embeddedContext: true,    // 支持 resource 内容块
  },
  mcpCapabilities: {
    http: true,               // 支持 HTTP MCP 服务器
    sse: true,                // 支持 SSE MCP 服务器
  },
  loadSession: true,          // 支持 session/load
  sessionCapabilities: {
    fork: {},                 // 支持 fork session
    list: {},                 // 支持 session/list
    resume: {},               // 支持 resume
    close: {},                // 支持 close
  }
}
```

---

## 3. `_meta` 扩展能力

### 3.1 NewSession 请求 `_meta`（acp-agent.ts:185-213）

```typescript
// Client 在 session/new params._meta 传入
_meta?: {
  claudeCode?: {
    options?: Options;                             // Claude Code SDK 选项
    emitRawSDKMessages?: boolean | SDKMessageFilter[];  // 原始 SDK 消息透传
  },
  additionalRoots?: string[];                      // 额外文件系统根路径
  systemPrompt?: string | { append: string };      // 覆盖或追加 system prompt
  disableBuiltInTools?: boolean;                   // 禁用内置工具（遗留）
}
```

### 3.2 ToolCallUpdate `_meta`（acp-agent.ts:234-254）

```typescript
// tool_call_update session/update 中的 _meta
_meta?: {
  claudeCode?: {
    toolName: string;           // 工具名称
    toolResponse?: unknown;     // 结构化工具响应
    parentToolUseId?: string;   // 嵌套工具调用的父 ID
  },
  terminal_info?: {
    terminal_id: string;        // 终端句柄标识符
  },
  terminal_output?: {
    terminal_id: string;
     string;               // stdout/stderr 输出
  },
  terminal_exit?: {
    terminal_id: string;
    exit_code: number;
    signal: string | null;
  },
}
```

当 `clientCapabilities._meta.terminal_output: true` 时，Bash 工具通过上述 `_meta` 流式传输终端输出，替代标准 ACP terminal/* 方法。

### 3.3 客户端能力检测 `_meta`

| 字段 | 用途 |
|------|------|
| `clientCapabilities._meta.gateway` | 是否支持 custom gateway 认证 |
| `clientCapabilities._meta["terminal-auth"]` | 是否支持终端认证 |
| `clientCapabilities._meta.terminal_output` | 是否支持 Bash 终端流式输出 |

### 3.4 原始 SDK 消息透传

当启用 `emitRawSDKMessages` 时，Agent 通过自定义扩展通知透传原始 SDK 消息：

```typescript
// _claude/sdkMessage 自定义通知
extNotification("_claude/sdkMessage", {
  sessionId: string,
  message: { type, subtype, ... }
})
```

---

## 4. 自定义扩展方法（`_` 前缀）

无直接 `_` 前缀 ACP 方法。Claude-specific 功能全部通过 `_meta` 字段实现向后兼容扩展。

---

## 5. `session/update` 事件类型

| 事件类型 | 文件:行 | 说明 |
|----------|---------|------|
| `agent_message_chunk` | acp-agent.ts:654, 697, 2235 | 流式 Assistant 文本/图片输出 |
| `agent_thought_chunk` | acp-agent.ts:2256 | Extended thinking 输出 |
| `tool_call` | acp-agent.ts:2344 | 新工具调用（pending 状态） |
| `tool_call_update` | acp-agent.ts:2285, 2327, 2406 | 工具执行进度/完成，含 terminal _meta |
| `plan` | acp-agent.ts:2272 | TodoWrite 工具条目映射为 PlanEntry[] |
| `usage_update` | acp-agent.ts:751, 869 | Token 用量快照（含 cost USD） |
| `current_mode_update` | acp-agent.ts:1167, 1301 | 权限模式变更 |
| `config_option_update` | acp-agent.ts:1415 | 会话配置变更（全量） |
| `available_commands_update` | acp-agent.ts:1395 | 可用斜杠命令/MCP 工具 |
| `user_message_chunk` | acp-agent.ts:2235 | 历史重放时的用户消息 |

---

## 6. Session Modes 与 ConfigOptions

### Session Modes（acp-agent.ts:1706-1745）

| Mode ID | 名称 | 说明 |
|---------|------|------|
| `auto` | Auto | 模型自动分类权限请求 |
| `default` | Default | 标准行为，需用户确认 |
| `acceptEdits` | Accept Edits | 自动接受文件编辑 |
| `plan` | Plan | 规划模式，不执行操作 |
| `dontAsk` | Don't Ask | 无弹框，未预批准则拒绝 |
| `bypassPermissions` | Bypass Permissions | 跳过所有权限检查（非 root 模式才可用） |

### ConfigOptions（acp-agent.ts:1870-1944）

| id | 类型 | 说明 |
|----|------|------|
| `mode` | select | 权限模式选择器 |
| `model` | select | 模型选择器（含版本） |
| `effort` | select | 推理深度：low/medium/high/xhigh（取决于模型） |

**Settings 优先级**：用户设置 > 项目设置 > 本地项目设置 > 企业托管设置

---

## 7. 工具支持与 ACP ToolKind 映射

| Claude Code 工具 | ACP ToolKind | 说明 |
|-----------------|-------------|------|
| Bash | execute | 含终端流式输出（通过 _meta） |
| Read | read | 文件读取 |
| Write | edit | 文件创建 |
| Edit | edit | 文件修改（含 diff） |
| Glob | search | 模式匹配 |
| Grep | search | 内容搜索 |
| WebFetch | fetch | URL 获取 |
| WebSearch | fetch | Web 搜索 |
| Agent/Task | think | 规划/委托 |
| TodoWrite | plan | 映射为 PlanEntry[] |
| ExitPlanMode | switch_mode | 退出规划模式 |

---

## 8. 认证方法

初始化响应中声明的 authMethods：

| 方法 | 类型 | 说明 |
|------|------|------|
| `gateway` | 自定义 | 当 client 声明 `auth._meta.gateway: true` 时可用 |
| `claude-login` | terminal | `claude /login`（适用于远程环境） |
| `claude-ai-login` | terminal | Claude 订阅登录 |
| `console-login` | terminal | Anthropic Console 登录 |

Gateway 认证使用环境变量：`ANTHROPIC_BASE_URL`、`ANTHROPIC_CUSTOM_HEADERS`、`ANTHROPIC_AUTH_TOKEN`

---

## 9. 其他特性

### Prompt 队列

多个 prompt 可排队顺序处理（`promptCapabilities._meta.claudeCode.promptQueueing: true`）：
- 当前 turn 执行时新 prompt 推入 `input` 队列
- 通过 `pendingMessages` Map 追踪顺序
- 支持通过 uuid 检测 prompt 重放

### Session Fingerprinting（acp-agent.ts:158-164）

快照会话定义参数，检测 cwd/MCP 服务器变更：
```typescript
JSON.stringify({ cwd: string, mcpServers: sorted_by_name })
```

### 模型别名解析（acp-agent.ts:1950-2017）

| 别名 | 解析为 |
|------|--------|
| `opus` | `claude-opus-4-6` |
| `sonnet` | `claude-sonnet-4-20250514` |
| `opus[1m]` | `claude-opus-4-6-1m` |

优先级：`ANTHROPIC_MODEL` 环境变量 > settings.model > 第一个可用模型

---

## 10. 关键文件

| 文件 | 行数 | 说明 |
|------|------|------|
| `acp-agent.ts` | 2549 | 核心 Agent 实现 |
| `tools.ts` | 799 | 工具结果转换（含 terminal _meta） |
| `settings.ts` | 314 | 多源设置管理 |
| `utils.ts` | 108 | stdio 流适配器 |
| `index.ts` | 74 | 入口，CLI 路由 |
