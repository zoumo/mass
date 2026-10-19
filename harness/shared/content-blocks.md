---
type: Contract
title: ContentBlock
description: 消息输入、输出与工具结果共用的 ACP 五种内容块，不设置公共 Base。
status: stable
sources:
  - id: acp-content
    resource: https://agentclientprotocol.com/protocol/v1/content
    title: ACP Content
  - id: acp-schema
    resource: https://agentclientprotocol.com/protocol/v1/schema
    title: ACP Schema
  - id: direction
    resource: 项目负责人在 2026-10-20 确认按 ACP 五种类型，不增加 json，并简化公共结构
    title: 本轮 ContentBlock 设计
---

# ContentBlock

准确声明见 [content-blocks.tsp](content-blocks.tsp)。消息输入、消息输出、思考、工具结果及增量统一使用 ContentBlock[]，不设置 ContentBlockBase，也不增加工具内容包装。五种类型只共享一个可选 annotations 字段，直接在各类型声明即可。[^direction]

## 类型与字段

| type | 必填 | 可选 |
|---|---|---|
| text | text | annotations |
| image | data、mimeType | uri、annotations |
| audio | data、mimeType | annotations |
| resource | resource | annotations |
| resource_link | uri、name | mimeType、title、description、size、annotations |

字段形状采用 ACP 的五种标准内容块。text 是原样文本，可含 Markdown。image/audio 的 data 是必填 Base64 字符串，不含 data URI 前缀；图片的可选 uri 仅标识来源，不替代 data。resource 内嵌完整内容，resource_link 仅保留引用。[^acp-content]

resource 必须包含 uri，并在 text 与 blob 中选择一个；mimeType 可选。text 是文本正文，blob 是 Base64 二进制正文，二者不能同时提供。JSON 作为 mimeType=application/json 的文本资源承载，不增加独立 json 类型，也不把 JSON 对象直接填入 text。[^acp-schema]

annotations 描述内容的展示与使用提示：audience 是 user/assistant 数组，priority 在 0 到 1 之间，lastModified 表示内容的修改时间。它们均可省略；size 是非负字节数。[^acp-schema]

## 与事件和存储的关系

事件 type 表达消息、思考或工具等语义；内容块 type 表达内容的载体，二者不能混用。数组保持内容顺序，工具的输出可用性与截断仍由 ToolEventFields.output 表达。

事件扩展 _meta 继续位于 SessionEvent 顶层，类型为 Record<unknown>；本轮不增加内容块级扩展 map。这里只参考 ACP 内容字段，不要求在所有层级复制 ACP 的扩展字段或引入 ACP 传输协议。

文件必须完整保留已经接纳的内容块，包括内嵌媒体和资源正文。resource_link 明确只承诺引用，不承诺引用对象已复制、永远可访问或自动下载；文件完整恢复保证恢复的是这条引用。需要独立于源地址保留内容时，应使用内嵌资源。image 的 uri 也不能作为省略 data 的理由。

同一 ContentBlock 结构可用于输入与输出，不表示所有 Runtime 都支持全部类型。不支持的输入必须明确报错，不能静默丢块。Base64 正文、MIME 与实际媒体一致性以及资源访问能力由 adapter/具体边界验证，TypeSpec 编译只检查声明和值的类型关系。

## 预览与剩余事项

[event-examples.json](../modules/runtime/event-examples.json) 的 contentBlocks 包含五种类型，以及内嵌文本、二进制资源两种分支；示例源见 [event-examples.tsp](../modules/runtime/event-examples.tsp)。重新生成与校验命令见 [TypeSpec 入口](../typespec/README.md)。

message 与 thinking 共用消息模型、itemId/messageId 定位和内容块处理规则；内部多个独立内容块均按原顺序保留，只以事件 type 区分消息与思考的含义。chunk 中的内容按交付顺序追加；非文本块按独立块追加，不拼接 Base64。MASS 不跨事件保存或累积 delta，不将未观察内容伪造成完整文本。脱敏或不支持内容的精确信息承载仍需在具体 adapter 中定义。媒体大小限制、资源访问和持久文件编码仍需在对应合同确定；内容类型定义不宣称具体 Agent 已实现多模态支持。

[^acp-content]: ACP Content 定义 text、image、audio、resource 与 resource_link 的主要字段和内嵌媒体规则。
[^acp-schema]: ACP Schema 定义文本/二进制内嵌资源与 annotations；本包采用当前明确字段，不引入所有层级的 ACP 扩展字段。
[^direction]: 本轮确认统一 ContentBlock[]、采用 ACP 五种类型，不额外加入 json；为简化结构省去公共 Base。
