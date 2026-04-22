# codex-acp ACP 协议支持调研

> 调研日期：2026-04-22
> 项目版本：0.11.1
> 仓库：OpenAI Codex CLI 的 ACP 适配器

## 项目概述

**语言/框架**：Rust（Edition 2024）  
**定位**：将 OpenAI Codex CLI 适配为 ACP 协议的 Agent Adapter  
**传输方式**：stdio only（Tokio 异步 + compat 层桥接 stdin/stdout）  
**主要依赖**：
- `agent-client-protocol = "0.10.4"`（含 unstable features）
- `codex-core`, `codex-protocol`, `codex-apply-patch`, `codex-shell-command`, `codex-login` 等
- Tokio 异步运行时

---

## 1. ACP 方法支持

### 完整实现

| 方法 | 文件:行 | 说明 |
|------|---------|------|
| `initialize` | codex_agent.rs:218 | 返回协议版本 V1、agentCapabilities、authMethods、agentInfo |
| `authenticate` | codex_agent.rs:256 | 支持 3 种认证：ChatGpt、CodexApiKey、OpenAiApiKey |
| `logout` | codex_agent.rs:325 | 调用 auth_manager.logout() |
| `session/new` | codex_agent.rs:332 | 创建新 thread，含会话配置 |
| `session/load` | codex_agent.rs:380 | 从 rollout history 加载会话 |
| `session/list` | codex_agent.rs:451 | 列出 threads，25 条/页分页 |
| `session/close` (unstable) | codex_agent.rs:513 | 关闭会话，清理资源 |
| `session/prompt` | codex_agent.rs:532 | 提交用户 prompt 到 thread |
| `session/cancel` | codex_agent.rs:544 | 取消当前会话操作 |
| `session/set_mode` | codex_agent.rs:550 | 设置审批预设模式 |
| `session/set_model` | codex_agent.rs:561 | 设置 AI 模型 |
| `session/set_config_option` | codex_agent.rs:574 | 设置配置项（mode/model/reasoning_effort） |
| `session/request_permission` | thread.rs:2545 | 工具执行权限请求 |

### 未实现

- `fs/read_text_file`、`fs/write_text_file`：文件系统通过 session_roots 沙盒化，不走 ACP FS 协议
- `terminal/create` 等：终端输出通过 `_meta.terminal_*` 扩展走 tool_call_update（见 §3）

---

## 2. 声明的 agentCapabilities

```rust
// codex_agent.rs:230-238
AgentCapabilities::new()
    .prompt_capabilities(PromptCapabilities::new()
        .embedded_context(true)    // 支持 @-mention 嵌入上下文
        .image(true))              // 支持图片输入
    .mcp_capabilities(McpCapabilities::new()
        .http(true))               // 支持 HTTP MCP 服务器（不支持 SSE）
    .load_session(true)            // 支持 session/load
    .auth(AgentAuthCapabilities::new()
        .logout(LogoutCapabilities::new()))  // 支持 logout
    .session_capabilities(SessionCapabilities::new()
        .close(SessionCloseCapabilities::new())  // 支持 close
        .list(SessionListCapabilities::new()))   // 支持 list
```

**Agent Info**：
- name: `"codex-acp"`
- version: `"0.11.1"`
- title: `"Codex"`

---

## 3. `_meta` 扩展能力

### 3.1 终端输出扩展（thread.rs:1855-2005）

```rust
// tool_call_update 中的 _meta 字段
_meta.terminal_info   → { terminal_id: String }    // 工具调用 ID
_meta.terminal_output → { terminal_id: String }    // 终端输出流
_meta.terminal_exit   → { terminal_id: String }    // 终端退出状态
```

### 3.2 客户端能力检测（thread.rs:2440-2451）

```rust
// 检查 client 是否支持 terminal_output 扩展
fn supports_terminal_output(&self, active_command: &ActiveCommand) -> bool {
    active_command.terminal_output
        && client_capabilities.meta
            .get("terminal_output")
            .is_some_and(|v| v.as_bool().unwrap_or_default())
}
```

即：Client 须在 `initialize` 的 `clientCapabilities._meta.terminal_output: true` 才启用终端流式输出。

### 无其他自定义 `_meta` 扩展

---

## 4. 自定义扩展方法（`_` 前缀）

**无**。所有方法遵循标准 ACP 规范，无自定义 `_` 前缀方法。

---

## 5. `session/update` 事件类型

| 事件类型 | 文件:行 | 说明 |
|----------|---------|------|
| `agent_message_chunk` | thread.rs:2472 | 流式 Agent 文本响应 |
| `agent_thought_chunk` | thread.rs:2478 | Agent 推理/思考过程 |
| `tool_call` | thread.rs:2485 | 工具调用发起（动态工具/MCP/exec/patch） |
| `tool_call_update` | thread.rs:2490 | 工具调用状态更新（执行中/完成，含输出） |
| `plan` | thread.rs:2527 | Agent 计划（含步骤和状态） |
| `available_commands_update` | thread.rs:2683 | 可用命令（含自定义 prompts） |
| `config_option_update` | thread.rs:2985 | 配置项变更（mode/model/reasoning_effort） |
| `session_info_update` | thread.rs:1063 | 会话元数据更新（含 UsageUpdate） |
| `usage_update` | thread.rs:990 | Token 用量信息 |
| `user_message_chunk` | thread.rs:2465 | 用户消息回显 |

---

## 6. Session Modes 与 ConfigOptions

### Session Modes（Approval Presets）

从 `APPROVAL_PRESETS`（builtin_approval_presets）加载，示例：
- `read-only`：只读模式，沙盒环境
- 其他来自 codex-utils-approval-presets 的预设

当前模式通过匹配 approval policy 和 sandbox policy discriminants 确定。

### ConfigOptions（thread.rs:2808-2968）

| id | category | 说明 |
|----|---------|------|
| `mode` | Mode | Approval Preset 选择器；描述："Choose an approval and sandboxing preset for your session" |
| `model` | Model | AI 模型选择器；描述："Choose which model Codex should use" |
| `reasoning_effort` | ThoughtLevel | 推理深度选择器（仅当模型支持多个 effort 时）；描述："Choose how much reasoning effort the model should use" |

---

## 7. 内置斜杠命令（thread.rs:2756-2788）

| 命令 | 说明 |
|------|------|
| `/review` | 审查当前变更并找问题（可选额外指令） |
| `/review-branch` | 对比指定分支审查代码变更（需分支名） |
| `/review-commit` | 审查指定 commit 的变更（需 commit sha） |
| `/init` | 创建 AGENTS.md 配置文件 |
| `/compact` | 压缩对话以防上下文超限 |
| `/undo` | 撤销 Codex 最近一次 turn |
| `/logout` | 登出 Codex |
| 自定义 prompts | 从 workspace 加载，支持 `$KEY=value` 参数替换 |

---

## 8. 认证方法

initialize 响应中声明的 authMethods（codex_agent.rs:256）：

| 方法 | 类型 | 说明 |
|------|------|------|
| `ChatGpt` | OAuth/Browser | 需要浏览器 ChatGPT 登录 |
| `CodexApiKey` | API Key | `CODEX_API_KEY` 环境变量 |
| `OpenAiApiKey` | API Key | `OPENAI_API_KEY` 环境变量 |

---

## 9. MCP 支持

- 支持 HTTP MCP 服务器（`mcpCapabilities.http: true`）
- **不支持** SSE MCP 服务器（`mcpCapabilities.sse` 未声明）
- MCP 服务器在 `session/new` 时通过配置传入（codex_agent.rs:136-166）

---

## 10. 其他特性

### 文件系统沙盒

每个 session 有独立的 working directory，存储在 `session_roots` 中（codex_agent.rs:354, 439-442），不通过标准 ACP `fs/*` 方法暴露给 client。

### 历史重放

`session/load` 加载并重放 rollout history 恢复会话上下文（codex_agent.rs:435-443）。

### 权限请求流程（thread.rs:2545-2557）

```
工具调用 → request_permission（ToolCallUpdate + 权限选项）→ 等待 client 响应 → 允许/拒绝
```

涵盖：exec 命令、MCP 工具调用、代码 patch 应用。

### 自定义 Prompt 参数（prompt_args.rs）

自定义 prompts 支持 `$KEY=value` 语法参数替换。

---

## 11. 关键文件

| 文件 | 行数 | 说明 |
|------|------|------|
| `src/codex_agent.rs` | 684 | ACP Agent 实现，含 initialize/session/* 方法 |
| `src/thread.rs` | 5493 | Session/Thread 管理，含事件处理和权限流程 |
| `src/prompt_args.rs` | 315 | 自定义 prompt 参数解析 |
| `src/lib.rs` | 93 | stdio 传输层设置 |
| `src/main.rs` | 12 | 入口 |
