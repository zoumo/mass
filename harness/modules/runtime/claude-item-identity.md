---
type: Contract
title: Claude 条目身份
description: 以原生 content block 为单位的 MASS itemId、实时与历史映射，以及 chunk/full 闭合规则。
status: draft
sources:
  - id: direction
    resource: 项目负责人于 2026-11-06 确认的简化方案：thinking/text 使用 message.id 与 blockIndex，tool 使用原生 tool_use.id
    title: block 粒度 itemId 方案
  - id: sdk
    resource: /Users/jim/code/github/agent/claude-agent-acp/node_modules/@anthropic-ai/claude-agent-sdk/sdk.d.ts
    title: Claude Agent SDK 流式 assistant frame 声明
  - id: transcript
    resource: /Users/jim/code/zoumo/mass/mass-go/tests/testdata/simulation/recordings/claude/basic/material/raw/raw.jsonl
    title: Claude CLI 实时帧与 transcript apiBlockIndex 对照
  - id: acp
    resource: /Users/jim/code/github/agent/claude-agent-acp/src/v2/message-blocks.ts
    title: ACP v2 block 身份与闭合参考实现
---

# Claude 条目身份

## 范围与结论

Claude 的 `message.id` 是 response 级身份。一个 response 可以包含多个 thinking、text 和 `tool_use` content block；因此客户端不能只凭 `message.id` 判断某个 thinking/text 已经结束，也不能把同一 response 的所有 thinking/text 合并成一个条目。[^sdk]

Claude adapter 采用 block 粒度：

```text
一个可见 thinking/text content block = 一个 MASS item
一个 tool_use                         = 一个 MASS tool item
```

`itemId` 是给客户端消费条目的不透明身份，不用于事件排序或全局去重。客户端只按 `itemId` 追加和闭合 block，不按 `message.id` 闭合整个 response。

通用 SessionEvent 仍允许一个条目包含多个 ContentBlock；Claude adapter 在本方案中选择 block-per-item，目的只是让 thinking/text 能按 block 独立闭合，不改变其他 Runtime 的粒度。

## 核心身份

```text
responseId = 原生 message.id
blockIndex = 实时 content_block.index / 历史 apiBlockIndex

主会话 thinking/text itemId = <responseId>:<blockIndex>
子会话 thinking/text itemId = <parentToolUseId>:<responseId>:<blockIndex>
tool itemId         = tool_use.id
```

- `itemId` 直接由原生身份组成，不带 `claude` 或 `block` 前缀；客户端仍把整个值当作不透明身份。
- `parentToolUseId` 取子 Agent 事件的原生 `parent_tool_use_id`；主会话没有该 lane。两个 lane 都只在对应原生范围成立，客户端不得解析分隔符。
- `blockIndex` 是该 response 内所有 content block 的原生非负整数索引，不是按 thinking/text 分别计数。索引唯一，所以 thinking 和 text 不需要在 ID 中再加 kind。
- 空 thinking、`redacted_thinking`、仅含 signature 的 thinking 和零长度 text 不产生条目；它们仍然占用原生 block 索引，后续 block 不改号。
- `"\n\n"` 这类非空文本是可见 block，必须产生条目；不能把 whitespace-only 内容当成空内容丢弃。
- `content_block.index` 和 `apiBlockIndex` 只用于生成身份或放入 `_meta` 诊断；客户端不得解析 `itemId` 的组成部分。
- 原生 `message.id` 和 block 坐标都存在时，live 和 history 对主会话同一 block 得到相同 `itemId`。`message.id` 或 block 坐标缺失时只能使用 observation-scoped 回退，不承诺跨 live/history 一致。

## Payload 与元数据

消息和思考事件继续使用 `MessageEventPayload`，但 Claude adapter 固定：

```text
itemId            = 当前会话 lane 的 thinking/text itemId
payload.messageId = itemId
```

`payload.messageId` 是当前事件合同中的条目定位字段，不在这里表示 Claude 原生 `message.id`。原生 response 身份放在顶层 `_meta`：

```json
{
  "_meta": {
    "claude": {
      "responseId": "msg_A",
      "blockIndex": 1,
      "parentToolUseId": null
    }
  }
}
```

`_meta.claude.responseId` 只用于诊断或需要时分组，不是闭合键。Claude 没有原生的 message phase；`payload.phase` 在 Claude 事件中省略，`phase` 不是完成标志，完成只由完整事件类型表达。

子 Agent 事件继续使用实际 `parent_tool_use_id` 作为 lane，并在 `_meta.claude.parentToolUseId` 保留同一原生值。`parentToolUseId` 不是公共 parent item 字段，也不改变 `payload.messageId = itemId`。

## 实时映射

| Claude 实时数据 | MASS 事件 | itemId |
|---|---|---|
| `message_start.message.id` | 不产生事件；记录 responseId | 无 |
| `content_block_start`，type=thinking/text | 不产生事件；记录 index 和类型 | 无 |
| `content_block_start`，type=tool_use | 不产生事件；记录 index 和 `tool_use.id` | 无 |
| `content_block_delta`，thinking_delta | `agent.thinking_chunk` | `<responseId>:<index>` |
| `content_block_delta`，text_delta | `agent.message_chunk` | `<responseId>:<index>` |
| `content_block_delta`，input_json_delta | 可选 `agent.tool_call_chunk` | `tool_use.id` |
| 完整 assistant frame 中的 thinking block | `agent.thinking` | 与对应 chunk 相同 |
| 完整 assistant frame 中的 text block | `agent.message` | 与对应 chunk 相同 |
| 完整 assistant frame 中的 tool_use block | `agent.tool_call` | `tool_use.id` |
| user frame 中的 tool_result block | `agent.tool_result` | `tool_use_id`，即同一 tool itemId |
| `content_block_stop`、`message_stop` | 不产生内容事件，也不闭合条目 | 无 |

实时 adapter 维护 `(parentToolUseId, responseId, blockIndex)` 到 block 状态的关联，并记录每个 block 的类型和已观察内容。`content_block_start` 给出权威索引；后续 chunk 使用同一索引。`content_block_stop` 可以结束该 block 的原生增量关联，但不产生 MASS 完成事件，也不能删除 index 到 block 的映射，因为完整 assistant frame 可能在其后到达。

完整 assistant frame 按 content block 拆分，每个可见 block 单独发一个完整事件，不能把多个 thinking 或 text 合并成一条，也不能把 thinking 和 text 放进同一条消息。

实时 adapter 以 `message_start` 捕获的 responseId 作为当前 response 身份；assistant frame 缺失 `message.id` 时复用该 responseId，不能把每条 frame 的 UUID 当作 responseId。

Claude SDK 通常按每个完成的 content block 生成一个只含该 block 的 assistant frame，但 adapter 不能把这一点当成合并多 block frame 的许可。frame 只有一个同类可见 block 时，按同一 lane、responseId、类型和已跟踪 block 状态唯一匹配到 `blockIndex`；如果 frame 含多个 block，只有每个 block 自带 `apiBlockIndex` 或等价坐标才算可证明，否则 adapter 不得猜测或合并；无法证明对应关系的 block 不能合成正常 full。

实时 chunk 或完整 thinking/text 缺少 `responseId` 或可证明的 `blockIndex` 时，adapter 失败关闭该内容，不生成猜测身份。工具仍可直接使用原生 `tool_use.id`。

## 历史映射

| Claude transcript 数据 | MASS 事件 | itemId |
|---|---|---|
| assistant thinking block，`message.id` + `apiBlockIndex` | `agent.thinking` | `<message.id>:<apiBlockIndex>` |
| assistant text block，`message.id` + `apiBlockIndex` | `agent.message` | `<message.id>:<apiBlockIndex>` |
| assistant tool_use block | `agent.tool_call` | `tool_use.id` |
| user tool_result block | `agent.tool_result` | `tool_use_id` |
| user text block | `user.message` | `<record.uuid>:<contentOrdinal>` |

历史 assistant record 的 `apiBlockIndex` 与实时 `content_block.index` 表示同一 API block 坐标；`message.id` 用于 response 身份。历史导入按原生 record 和 content 顺序处理，一条 record 含多个可见 block 时发多个事件，它们共享 `replay.nativeRecordId`，但 `itemId` 必须不同。record 只有一个起始 `apiBlockIndex` 时，按内容数组位置使用从该值开始的连续坐标；隐藏或空 block 仍占用位置。

`replay.nativeRecordId` 继续保存 JSONL record 的 `uuid`，`replay.nativeParentId` 保存 `parentUuid`；二者不是 block 身份，不能拿来替代 `itemId`。user 消息没有 chunk/full 闭合语义，`contentOrdinal` 只用于同一 record 内多个 text block 的去重。

`apiBlockIndex` 缺失时，adapter 只能在能证明同一 `message.id` 内 block 顺序的情况下推导索引；不能证明时使用 `<record.uuid>:<contentOrdinal>` 作为 observation-scoped 回退，并明确不承诺与实时身份一致。`contentOrdinal` 是 record 内容数组的零基位置；不能为了拼出稳定 ID 而按内容猜测或重排 block。

## chunk 与 thinking 的对应

`agent.thinking_chunk` 和 `agent.message_chunk` 共用 `MessageEventPayload`。thinking 的语义由事件类型表达，不由 ContentBlock 类型表达；两者的 `content` 都是普通的 `text` content block。

思考增量：

```json
{
  "type": "agent.thinking_chunk",
  "itemId": "msg_A:0",
  "payload": {
    "messageId": "msg_A:0",
    "content": [{"type": "text", "text": "先检查"}]
  },
  "_meta": {
    "claude": {"responseId": "msg_A", "blockIndex": 0}
  }
}
```

对应完整思考：

```json
{
  "type": "agent.thinking",
  "itemId": "msg_A:0",
  "payload": {
    "messageId": "msg_A:0",
    "content": [{"type": "text", "text": "先检查 README。"}]
  },
  "_meta": {
    "claude": {"responseId": "msg_A", "blockIndex": 0}
  }
}
```

文本增量使用 `agent.message_chunk`，完整文本使用 `agent.message`；如果文本 block 的索引是 1，则两者的 `itemId` 都是 `msg_A:1`。完整事件只携带该 block 的内容，不携带同一 response 的其他 block。

## 闭合规则

客户端按 `itemId` 维护 block 状态：

```text
agent.thinking_chunk / agent.message_chunk
  -> 追加到该 itemId 的 preview

agent.thinking / agent.message
  -> 用完整 content 替换 preview，并标记该 item 已闭合

agent.tool_call
  -> 打开或更新该 tool item

agent.tool_result
  -> 标记该 tool item 已闭合
```

- chunk 只是追加，不是累计快照；客户端不要求 MASS 在内存中拼接完整消息。
- 同一 `itemId` 的完整事件是该 block 的终止观察。完整事件可以在没有 chunk 的情况下直接出现，此时直接创建已闭合条目。
- 完整事件之后到达的同 `itemId` chunk 丢弃，不能重新打开条目。
- 只有 chunk 而没有完整事件的 item 保持 incomplete；不能根据 `content_block_stop`、`message_stop`、idle、EOF 或断线推测完成。
- `aborted: true` 的 assistant frame 不产生正常完整事件，只保留已观察的 preview。
- 重复 assistant frame 按 `[parentToolUseId, assistant.uuid]` 抑制，不能因为重复 frame 生成第二个同身份完整事件。
- 一个 response 的多个 thinking/text block 可以分别闭合；一个 block 的完整事件不关闭同 response 的其他 block。
- `eventId` 只标识事件；工具调用和结果共享 `itemId`，但必须有不同 `eventId`，不能按 `itemId` 覆盖历史。

## Tool

`tool_use.id` 是工具调用和工具结果的共享身份：

```text
agent.tool_call.itemId   = tool_use.id
agent.tool_result.itemId = tool_use.id
```

工具调用参数片段如果作为 `agent.tool_call_chunk` 交付，也使用同一 `tool_use.id`。tool result 出现在 user record 中时，只生成 tool result，不额外生成一条 user message；同一 user record 同时含文本和 tool result 时，文本和工具结果分别按各自身份交付。

## 边界

- 主会话和子 Agent 使用不同 lane。当前 native history 导入只覆盖保留的主会话记录；子 Agent 历史和跨 live/history 身份一致性不在本方案承诺内。
- `payload.messageId` 不再承载原生 Claude response ID。需要 response 分组时使用 `_meta.claude.responseId`，不要重新让 `messageId` 兼任 response 语义。
- 本方案不新增 TypeSpec 字段，也不引入公共 parent item 字段；`replay.nativeParentId` 只表达 transcript 链关系。若未来需要公开的条目层级，应新增 typed 关系，而不是让客户端解析 `_meta` 或 `itemId`。
- 空的、隐藏的和 redacted thinking 不产生条目；它们占用的 block 索引不重排。
- 不迁移、不回填旧事件日志，也不做双写兼容；新身份只作用于新观察和新历史导入。旧本地数据可以直接删除。

## 验收

- 同一 response 同时有 thinking、text 和 tool_use 时，thinking/text 按 blockIndex 得到不同 itemId，工具使用独立 tool_use.id。
- 每个 thinking/text block 的 chunk 和完整事件使用同一 itemId；完整事件到达后同 itemId 的迟到 chunk 被忽略。
- 历史导入与实时在 `message.id` 和 `apiBlockIndex` 都存在时得到相同 thinking/text itemId。
- 子 Agent thinking/text 使用 `parent_tool_use_id` lane，不能与主会话同 responseId/index 冲突。
- 一个 assistant frame 含多个可见 block 且每个 block 都有可证明坐标时分别生成完整事件；坐标不可证明时不生成猜测 full，也不合并同类型 block。
- 空 thinking、redacted thinking 和零长度 text 不产生事件；whitespace-only 文本产生事件。
- live thinking/text 缺少 response 或 block 坐标时不生成猜测身份；history 缺少坐标时使用 record-scoped 回退。
- `content_block_stop`、`message_stop`、idle 和 EOF 不闭合条目；`aborted` frame 不产生正常完整事件。
- tool call 与 tool result 使用同一 itemId、不同 eventId；user tool_result 不额外生成用户消息。

[^sdk]: Claude Agent SDK 将流式 assistant frame 与原始 `stream_event` 分开；一个 response 的多个 content block 可以共享 `message.id`，text/thinking block 没有独立 ID。
[^transcript]: Claude CLI 的实时帧提供 `content_block.index`，transcript assistant record 提供 `apiBlockIndex`；两者表达同一 API content block 坐标。
[^acp]: ACP v2 的参考实现也按可见 block 生成身份，并用同一身份关联 chunk 与完整更新。
