# ACP 会话历史回放能力核对

核对日期：2026-10-21。本文为探索记录，不改变 harness 合同；仅核对官方协议与文档，未运行真实 Agent 验收。

## ACP 的确提供历史回放

| 协议 | 请求 | 能力与边界 |
|---|---|---|
| v1（官网标为 Latest） | `session/load`，参数含 `sessionId`、绝对 `cwd`、`mcpServers` | 必须先检查 `initialize` 返回的 `loadSession: true`；通过 `session/update` 回放会话，再回复 load 请求。 |
| v2（官网标为 Draft） | `session/resume` + `replayFrom: { "type": "start" }` | 回放 Agent 保留的会话历史；省略或传 null 不回放。 |

来源：[v1 Session Setup](https://agentclientprotocol.com/protocol/v1/session-setup)、[v2 Session Setup](https://agentclientprotocol.com/protocol/v2/session-setup)、[v2 迁移指南](https://agentclientprotocol.com/protocol/v2/migration)。

```json
{
  "jsonrpc": "2.0",
  "id": 3,
  "method": "session/resume",
  "params": {
    "sessionId": "known-session-id",
    "cwd": "/absolute/project",
    "replayFrom": { "type": "start" }
  }
}
```

v2 的三个实现约束：

- 回放内容是仍被保留的对话，不能据此声称恢复了最初每个网络事件。消息可以是完整替换或 chunks，live-only 内容可以缺失；回放命令消息不执行命令。
- Agent/thought message ID 不保证跨重连稳定，不能仅凭它们把全量回放合并进既有历史。
- 回放后、resume 响应前必须同步当前前台状态。历史 `state_update` 不能当成当前活动；resume 成功不能直接推定 idle。

以上来源：[v2 Session Setup 的 Resuming Sessions 与 Current-State Synchronization](https://agentclientprotocol.com/protocol/v2/session-setup)。

客户端必须在发 resume 前安装接收处理器；若处理器转发到另一个队列，收到响应仍不代表本地投影已经排空。Rust SDK 的 v2 还由 `unstable_protocol_v2` 特性控制，不能把 SDK 发布版本号当作已协商协议版本。来源：[官方 Rust SDK Protocol V2](https://github.com/agentclientprotocol/rust-sdk/blob/main/md/protocol-v2.md)。

## Claude 官方 SDK 有独立的只读历史入口

Python 官方接口是 `get_session_messages(session_id, directory=None, limit=None, offset=0)`，返回 user/assistant 消息、UUID、原始 `message`，并定义 `parent_tool_use_id` 和 `parent_agent_id`。它能作为 Go SDK 设计参照；不能因此推定 MASS 当前依赖的 Go SDK 已有同名能力。来源：[Python SDK reference](https://code.claude.com/docs/en/agent-sdk/python#get_session_messages)。

官方示例明确该接口读取 transcript，不需要启动 Agent；它按消息链重建会话，并按从旧到新的 offset 分页。来源：[Building a session browser](https://platform.claude.com/cookbook/claude-agent-sdk-05-building-a-session-browser)。

## 对 MASS 的建议（推论，尚非设计合同）

用户目标是提供显式 API，按 Agent、cwd 与原生 session ID 找到已有会话，把历史送入 MASS 的正常 `adapter → Service → EventStore → Events` 流程，以验证整条管线。只导出文件或直接写 EventStore 不能满足该目标。

建议保留独立的“历史回放/导入”操作，把“读取外部历史”“纳管并提交公共历史”“恢复继续执行”分别定义。底层读取可以只读，但读取结果必须经过正常事件转换、归属校验、持久提交及实时分发。应定义完成屏障、失败后重试、重复导入与实时事件交界，并使用隔离的验证数据目录。保持普通恢复流程不自动重投旧业务动作。ACP 可以提供一种历史来源，不必为使用原生 SDK 的 Driver 强行引入 ACP。

历史完整性应按来源明确：保留的主会话消息、工具内容、可证明关联的子会话、缺失/压缩/截断情况。只读历史接口与 ACP replay 都不能自行证明“所有历史原始事件完整无缺”。回放能验证转换、存储与分发，但无法补回未保存的历史 delta、历史权限等待或原始通知时序，也不应重新执行模型和工具。上述覆盖范围需由各原生 SDK 的源码和真实会话补充验证。

## MASS 与固定 Go SDK 的源码核对

本次工作区为 MASS 分支 `v2`，依赖 `github.com/zoumo/agent-sdk-go v0.0.0-20261018163950-6dbf3e9c8264`。仅静态检查源码和本机 CLI help/version：Codex 0.163.0、Claude Code 2.1.258；没有读取个人会话或发送模型输入。

- 当前 `RuntimeDriver` 只有 open/resume，没有外部历史加载入口。Service 的普通 resume 先要求已存在的持久 Session；两个 adapter 还要求 MASS 私有恢复材料。因此仅知道外部原生 ID/cwd 不能走现有 resume。（源码：`../pkg/runtime/runtime.go`、`../pkg/service/lifecycle.go`、`../pkg/runtime/codex/driver.go`、`../pkg/runtime/claude/driver.go`。）
- Codex adapter 恢复时显式 `ExcludeTurns: true`，不转换旧 turns。SDK 已有 `ReadThread`/`ResumeThread`，但固定版本没有 `thread/turns/list`、`thread/items/list` 的专用参数类型和便捷方法；可经 `Call` 调用，正式扩展宜补齐类型和分页读取能力，不能把一次 includeTurns 当成通用全量保证。（SDK：`codex/client.go`、`codex/thread.go`、`codex/types/types_generated.go`。）
- Codex 官方当前文档建议 metadata read 加分页历史：turns 默认最新优先、items 默认最旧优先，turns 的 itemsView 默认 summary，应明确 full。具体原生版本是否支持需要真实验收，不能用当前文档替代固定版本行为。来源：[Codex App Server](https://developers.openai.com/codex/app-server/)。
- Claude 固定 Go SDK 只用 `ResumeID` 启动恢复，没有对外的会话 transcript 读取/分页能力；`tools/replay` 是录制协议测试工具，不能当作用户 session 历史接口。建议在 SDK 内增加原生历史读取、会话身份与目录核对、消息链重建和分页，再供 MASS 使用。（SDK：`claude/client.go`、`claude/types/options.go`、`claude/tools/replay/replay.go`。）

特别需要重构的 seam：Codex `adapt` 将内容转换和当前 work/retired 过滤混在一起，未归属当前 work 的旧 turn item 会被丢弃；目前 item 分支仅覆盖 agentMessage、userMessage、reasoning、commandExecution。Claude `adapt` 对不支持的完整 content block 会报错，并过滤未证明归属的其他 session。回放应复用同一套内容转换，保留实时运行关联检查，不能伪造 turn/started 或主动执行输入来绕过过滤。对未知项要报告覆盖缺口，避免静默丢弃后声称全量成功。（两个 adapter 的 `driver.go`。）

建议操作名为 `load`，表达加载已知原生 Session 并回放保留历史；`open` 容易与现有 openSession（新建）及连接生命周期混淆。候选 CLI 为 `massctl session load --runtime <name> --cwd <absolute-path> --session-id <native-id> --name <mass-name>`，仅为提案，尚未实现。默认从最早保留内容回放，并在同一请求对应的本地提交/分发屏障完成后报告成功；不要只以底层读取完成作为成功。第一版可限定空的目标历史，重复加载拒绝且不得覆盖；重复验证使用独立 MASS 数据目录，保留原生 session ID，不伪造第二个原生身份。

验收应逐项核对原始读取数、已转换项/事件数、未支持项、历史查询结果及订阅观察结果；项和事件不必一一对应。覆盖多页、工具调用/结果、缺失输出、内容块类型、已证明子会话关系、消费者过慢、提交失败与中途停止。历史状态与当前状态分别结算；仅验证历史回放不能证明实时 delta、权限往返或原始并发时序已通过。
