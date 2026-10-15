# ACP v2：实时增量、历史物化与会话恢复的协议边界

调研日期：2026-10-15。纯公开一手资料、固定提交的静态核验；没有运行模型请求。协议目标保持 ACP v2。本文给出研究事实与设计推论，不替项目裁定持久化策略。

## 固定证据

| 对象 | 固定提交 / 源码版本 | 已核验状态 |
|---|---|---|
| ACP schema | [`014b1391ee8a49f3fb7a5536cd4a6cce4116825c`](https://github.com/agentclientprotocol/agent-client-protocol/tree/014b1391ee8a49f3fb7a5536cd4a6cce4116825c)，schema crate 1.11.0 | v2 仍为 draft；存在独立 `schema/v2/schema.json` |
| Rust SDK | [`f5c0eef0972bbda8e75a151aeb088642911b3568`](https://github.com/agentclientprotocol/rust-sdk/tree/f5c0eef0972bbda8e75a151aeb088642911b3568)，core crate 3.3.0 | `unstable_protocol_v2` feature 显式开启 |
| Claude ACP adapter | [`78a29f58ba1eb1b54896d8d73f49402ee50aa087`](https://github.com/agentclientprotocol/claude-agent-acp/tree/78a29f58ba1eb1b54896d8d73f49402ee50aa087)，package 0.91.0，TS SDK 1.8.0 | 已有实验性 v2；默认仍走 v1 |
| Codex ACP adapter | [`296069e841634cd4bb9bc4515602d836e49231ec`](https://github.com/zed-industries/codex-acp/tree/296069e841634cd4bb9bc4515602d836e49231ec)，crate 0.16.0，ACP crate =0.14.0 | initialize 明确选择 `ProtocolVersion::V1` |

这里的版本为检查提交中的 manifest 版本，不等同于已验证某个 registry 发布包。官方在线文档也仍将 v2 标记 Draft：[公告](https://github.com/agentclientprotocol/agent-client-protocol/blob/014b1391ee8a49f3fb7a5536cd4a6cce4116825c/docs/announcements/acp-v2-draft.mdx)。没有用旧本地镜像推断当前采用率。

## 1. 增量与完整记录不是两种互斥的事件类型

消息实体以 `(sessionId, entityType, messageId)` 寻址。user message、agent message、agent thought 是不同类型，同一字符串 ID 可用于不同类型；只按 messageId 索引会发生混淆。同类型同会话内不得把旧 ID 用于另一个实体。

`user_message`、`agent_message`、`agent_thought` 是 upsert：省略 content 保持原值，null 清空，数组整体替换。对应的 `*_chunk` 追加内容。收到完整 content 后再收到 chunk 仍须追加；之后的新完整 content 再整体替换。它们须按接收顺序应用。

**协议允许一个消息完全由 chunks 表达，没有要求在消息结束时再补一个完整快照，也没有通用的 message-completed 必发事件。** 因而“只转发 chunk，等原生最终完整包落盘”不是通用 ACP v2 保证。MASS 若要历史保存完整消息而不保存 chunk，须在内存构造当前实体，或明确要求适配器另提供更强的完整记录合同。完整 content 更新本身也不等于实体永远不会再次改变。

证据：[Prompt lifecycle 的 Entity IDs、Agent Reports Output](https://github.com/agentclientprotocol/agent-client-protocol/blob/014b1391ee8a49f3fb7a5536cd4a6cce4116825c/docs/protocol/v2/prompt-lifecycle.mdx)、[v2 schema](https://github.com/agentclientprotocol/agent-client-protocol/blob/014b1391ee8a49f3fb7a5536cd4a6cce4116825c/schema/v2/schema.json)。此处“不存在必发最终包”是对该固定版本规范与 schema 的检查结论，不应扩大为未来版本保证。

## 2. Prompt 接受、前台完成、消息持久化必须分开

`session/prompt` 成功表示用户消息已经插入 ACP 会话，并返回 agent 分配的 messageId。它不表示只进入待处理队列，更不表示执行结束。用户消息 update 可以先于或后于该 response，需按 ID 对齐。插入后发生失败/取消，prompt 仍应成功返回该 ID；后续工作结果通过通知报告。

`state_update` 中的 running / requires_action / idle 表示会话前台工作。idle 结束前台工作，即使 stopReason 缺省或未知也成立；正常结束时 stopReason 只是 SHOULD。后台工作可在 idle 时继续发工具进度，agent 也可主动启动下一轮前台工作。因此不能认为所有通知都隶属于一个尚未完成的 prompt RPC，或 idle 表示会话所有实体永久终结。

丢失 prompt response 后是否已插入可能无法确定；重试可能插入新消息。接受成功不保证未来历史中仍保留该消息。

证据：[Prompt Accepted、Foreground and Background Work、Stop Reasons](https://github.com/agentclientprotocol/agent-client-protocol/blob/014b1391ee8a49f3fb7a5536cd4a6cce4116825c/docs/protocol/v2/prompt-lifecycle.mdx)。

## 3. 取消、故障与无 chunk 落盘的边界

正常取消：client 发 `session/cancel`，仍要接受途中的 pending updates；agent 应停止工作，发完待发送更新后必须发 idle + cancelled。工具未完成状态需单独处理，不能把取消当成成功结果。失败结束前台工作须发 idle，建议给 error stopReason 并将未完成工具标记 failed。**进程退出、连接断开无法由已退出的 agent 发通知，须由服务自行检测。**

设计推论：服务存活时，可在确定的前台边界物化内存中消息，并明确记录中断/失败；但这只是服务定义的历史提交策略。若服务/主机在该边界前崩溃，未落盘内容可能丢失。协议既不提供隐含 WAL，也不保证 agent 之后可重放所有丢失字节。SQLite 不能自行修复这一信息边界：不用任何存储保存过的内容，换数据库也无法恢复。

“不持久化 chunk”与“定期保存已经物化的部分消息快照”是不同策略；后者是否允许、何时提交、怎样标记 partial，应留给架构决策。不能悄悄采用部分快照再声称满足“只存最终结果”。

证据：[Failures / Cancellation](https://github.com/agentclientprotocol/agent-client-protocol/blob/014b1391ee8a49f3fb7a5536cd4a6cce4116825c/docs/protocol/v2/prompt-lifecycle.mdx)、[Cancellation](https://github.com/agentclientprotocol/agent-client-protocol/blob/014b1391ee8a49f3fb7a5536cd4a6cce4116825c/docs/protocol/v2/cancellation.mdx)。

## 4. 工具 patch、权限与用户回答

工具以 toolCallId upsert；content、locations、rawInput、rawOutput 等按字段合同替换，省略保持、null 清空；`tool_call_content_chunk` 追加 content，后来的完整 content 再替换。status completed 不要求每次补齐之前所有字段。历史必须保存应用 patch 后的工具实体，而不能仅保存最后一条 patch。terminalId 与 toolCallId 独立；terminal output 有自己的快照/字节增量规则，退出状态和工具状态也独立。

审批是 `session/request_permission` 的请求/响应；许可不等于工具成功。取消会话时 client 必须回答 pending permission 为 cancelled，不能只关闭 SSE。结构化用户问答由 `elicitation/create` 提供，须协商 form/url capabilities；它与审批以及工具执行结果是不同合同。HTTP 网关需要维持这些请求的关联及待回答状态，不能用用户发一条普通 prompt 代替它们。

证据：[Tool Calls](https://github.com/agentclientprotocol/agent-client-protocol/blob/014b1391ee8a49f3fb7a5536cd4a6cce4116825c/docs/protocol/v2/tool-calls.mdx)、[Elicitation](https://github.com/agentclientprotocol/agent-client-protocol/blob/014b1391ee8a49f3fb7a5536cd4a6cce4116825c/docs/protocol/v2/elicitation.mdx)。

## 5. Resume 与历史查询不能直接等同

v2 `session/resume` 合并了 v1 load/resume 的用途：默认/缺省 replayFrom 不重放历史；`replayFrom: {type: "start"}` 要求在响应前重放全部**仍保留**的历史。start 是 inclusive；未知游标应报参数错，不得静默忽略。历史可以是完整消息，也可以通过先 content: [] 再 chunks 重建。

保留的用户消息 ID 必须和 prompt response 相同，并跨重连/重启稳定；agent/thought ID 只有 SHOULD 尽量稳定，client 不得仅靠它们把重放与既有历史去重。请求全量 replay 时应重建这些类型的视图。Agent 无义务永久保留全部已插入消息；live-only notice 不属于历史。

设计推论：面向用户的稳定历史查询如需跨适配器统一，应由 MASS 自有历史格式与身份/序号管理。ACP resume 是恢复 agent 上下文与可选重放，不是已经定义好的外部历史查询数据库。实时 SSE 可完全独立于历史读取；恢复时如何利用 agent replay 修复或替换局部记录需另定规则。

证据：[Session Setup](https://github.com/agentclientprotocol/agent-client-protocol/blob/014b1391ee8a49f3fb7a5536cd4a6cce4116825c/docs/protocol/v2/session-setup.mdx)。

## 6. Rust 选型与实际 adapter 采用

Rust SDK 有显式 `Client.v2()` / `Agent.v2()` 等类型化入口，独立 feature `unstable_protocol_v2`。v2 client 收到 v1 初始化响应会失败；SDK 不会把 v1 自动翻译成 v2。双版本 router/connector 是显式组合两个实现。MASS 可维持 v2 目标，同时把尚未支持的 adapter 明确列为接入缺口。

Claude adapter 已实现 experimentalV2 router、v2 prompt 接受/状态转换、message IDs、resume replay 等，默认仍 v1；不能继续沿用早期“Claude 也只支持 v1”的笼统结论。Codex adapter 这次检查的提交明确选择 V1。因此“存在 v2 schema/SDK”和“目标 agents 都已可用 v2”是两个结论。本次未做运行时互通测试，不承诺某个安装包与 SDK 组合已验证可用。

证据：[Rust SDK v2](https://github.com/agentclientprotocol/rust-sdk/blob/f5c0eef0972bbda8e75a151aeb088642911b3568/md/protocol-v2.md)、[Claude serve router](https://github.com/agentclientprotocol/claude-agent-acp/blob/78a29f58ba1eb1b54896d8d73f49402ee50aa087/src/serve.ts)、[Claude v2 prompt](https://github.com/agentclientprotocol/claude-agent-acp/blob/78a29f58ba1eb1b54896d8d73f49402ee50aa087/src/v2/prompt.ts)、[Claude v2 session](https://github.com/agentclientprotocol/claude-agent-acp/blob/78a29f58ba1eb1b54896d8d73f49402ee50aa087/src/v2/session.ts)、[Codex initialize](https://github.com/zed-industries/codex-acp/blob/296069e841634cd4bb9bc4515602d836e49231ec/src/codex_agent.rs#L448)。

## 研究结论

ACP v2 本身不要求 MASS 引入数据库。它要求正确实现实体 patch、异步生命周期与交互关联。**“不落盘 chunks + 自有完整历史”需要内存物化与显式提交/中断政策，不是依赖一个协议必发的最终包。** 文件还是 SQLite 的决定取决于 MASS 自己的历史读取、元数据更新、并发与事务需求，不能由 ACP 协议直接推出。
