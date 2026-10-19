---
type: Contract
title: SessionEvent
description: 实时、存储与历史读取共用的事件信封、按 type 绑定的 payload 与顶层扩展 map。
status: stable
sources:
  - id: direction
    resource: 项目负责人在 2026-10-20 确认统一 SessionEvent、直接保存名称、按 type 绑定 payload 与顶层任意值扩展 map
    title: 本轮事件合同方向
---

# SessionEvent

## 权威声明与范围

准确声明见 [session-events.tsp](session-events.tsp)。实时发布、HistoryStore 存储与历史读取共用 SessionEvent，不另设 SessionNotification 或 HistoryRecord。事件直接保留 namespace、sessionName 和 sessionId，消费者无需补查 MetaStore 才能显示会话名称。[^direction]

类别和 payload 形状曾参考旧事件示例，现行合同以本页和 TypeSpec 为准；可直接阅读本目录生成的 [事件示例](event-examples.json)。旧示例及其版本、完成标志和 payload 内扩展位置不作为开发依据。

## 职责归属

Runtime 拥有 SessionEvent、事件类型及各自绑定的 payload，adapter 将原生事件转换为这套统一模型。Service 在 openSession/resumeSession 时传入 DriverSessionAddress，adapter 固定 namespace/sessionName 与原运行的绑定，获得真实 sessionId 后补齐事件。Driver.events() 只交付名称与身份完整的 SessionEvent；Service 核对已有绑定后协调持久提交与发布，不补写或猜测缺失名称，也不另建半完整事件模型。Runtime 不查询 MetaStore，不导入 Service 资源 DTO。

adapter 优先从原始事件取得 time，缺失时自己生成；Service 与 HistoryStore 保留该值，不用接收或存储时间替换。eventId 则由 adapter 在创建统一 SessionEvent 时生成一次 UUID，不直接复用原生消息、条目或工具 ID。同一事件的发布、持久保存与失败重试使用原 UUID，不能在每次 append 重试时重新生成。

## Driver 事件流

[RuntimeDriver.events()](runtime-driver.tsp) 返回一个 [EventStream<SessionEvent>](event-stream.tsp)。每个 Driver 对象只有一条流，其全部 Session 共用；Service 是唯一消费者，按 sessionId 路由，Runtime 不为各 Session 暴露单独事件流，也不为每个外部订阅者创建流。

EventStream.next 是语言无关的异步读取操作：返回下一事件、正常结束 void 或异常终止 OperationError。同一流不允许并发 next，终止后的读取保持原终止结果；等待取消由语言绑定表达，不等于取消 Agent 工作、关闭 Session 或解除 Service 的消费责任。Driver 级失败没有合法 sessionId 时通过流错误表达，不能伪造会话事件；Session 级错误只影响对应会话，不能无依据终止健康共享流。

### 顺序与来源

Runtime 保持同一 Session 内已接纳事件的交付顺序，Service 在路由与后续异步处理时继续保持该顺序；即使预览按过载策略丢弃，也不能重排保留的事件。不同 Session 的事件可以交错，不定义跨 Session 的业务顺序，eventId/time 均不能替代顺序保证。

Service 接收时固定 Driver、Runtime 名称及事件的原运行关联，按 sessionId 查已有会话绑定并核对 adapter 已补齐的名称。不需要每条事件查询 MetaStore；但未知 Session、创建早期事件和旧运行事件必须按[事件路由](../service/session.md#事件路由)处理，不能重新归属到当前同名运行。共享流的内部队列必须保留原来源关联，不能因多路汇聚而丢掉旧运行隔离。

### 背压与公平性

Runtime 的交付缓冲和 Service 的分会话处理队列都必须有界；不指定未经确认的容量或超时默认值。Runtime 应避免一个高流量 Session 长期独占全部接纳容量；Service 接收后按 Session 分别处理，不能在公共读取循环内等待某个 Session 的慢存储、处理器或外部订阅者。

完整持久事实不能静默丢弃，容量不足时等待接纳或明确失败，保留原操作和未知结果责任；实时预览可按明确的有界策略丢弃。隔离不等于无限接收：所有队列或共享传输持续耗尽时，仍可能形成 Driver 级背压或明确失败，不能同时承诺有界内存、永久不阻塞和完整事实永不失败。外部订阅者的慢消费由 Service 隔离，不直接成为 Driver 的新消费者。

### 生命周期

1. Service 取得未启动 Driver 后调用一次 events()，建立唯一接收循环，再调用 start；取得流不启动 Agent。重复 events() 返回错误，不建立新连接、不重放或重新分发已消费事件。
2. Session 创建、恢复和关闭都复用该流。关闭一个 Session 不关闭 Driver 流，也不影响其他 Session；新 Session 无需重新订阅。尚无真实 sessionId 的原生通知由 adapter 有界暂存，补齐后交付；已经完整但目录尚未提交的事件由 Service 按原创建操作有界暂存。
3. Driver 存活期间，Service 持续接收，即使没有外部订阅者。Service 消费循环故障或停止接收时必须显式协调停止接纳、恢复处理或停止 Driver，不能静默丢流后继续宣称健康；不通过第二次 events() 建立竞争消费者。
4. 停止时 Service 先封闭新工作并排空会话及已接纳事实；整个排空与 Driver.stop 期间继续消费，不能先撤掉读取循环。Driver 正常停止确认资源释放、已接纳事件完成交付后才正常结束流；Service 仍需完成已接收事实的持久提交与结算。
5. 意外断流以 OperationError 明确表达，不能当作全部 Session 空闲、工作完成、历史提交成功或资源已释放。已经接纳的完整事实仍须结算，不因流失败而丢弃。终止流不自动重连或补历史；后续显式重建 Driver 使用新流，旧流和旧运行责任不得转移到新流。

事件交付结束只描述流的生命周期；Driver.stop 的成功、HistoryStore.append 的成功和业务执行成功分别依据自己的证据判断。Service 对外的 subscribeEvents 仍是独立的广播订阅，每个订阅者有自己的流与过载处理，不直接读取 Driver.events()。

## 事件身份与历史顺序

统一事件不包含 seq，无需恢复连续计数器。eventId 是 UUID 字符串，仅用于唯一标识事件，不表示大小、时间或顺序；即使 UUID 实现含时间信息，消费者也不能据此排序。time 同样不是历史分页键。TypeSpec 使用 format(uuid) 标注格式，不限定 UUID 版本。

现阶段不要求 Agent 向 MASS 发送历史事件，不提供原生事实去重：不按原生 ID、itemId、内容 hash 或时间窗口合并通知。重新接收相同原生通知时，若 adapter 创建了新的统一事件，可以获得新的 UUID；不承诺跨重放生成相同身份。因此“不主动接收历史”不等于上游不会重复通知。

HistoryStore 对同一 sessionId/eventId 的相同事件执行写入幂等，不重复追加；这是应用内同一事件的写入重试保证，不是原生事件去重。重启后读取已持久保存的事件时原样保留文件中的 UUID；若仍需重试一个未确定结果的事件，必须保留该事件及原 UUID，不能仅凭原生通知重新生成后声称是同一次重试。未来需要历史导入或原生重放时，再讨论身份生成算法与去重合同，本轮不引入映射恢复算法。

HistoryStore 按成功持久追加顺序读取，同一批按输入顺序追加。Service 串行协调同一 Session 的追加责任，保证输入顺序符合已接纳的源事件顺序；Store 无法仅凭 eventId 推断遗漏或乱序。已存在的相同事件不重复追加，也不移动其位置。

HistoryStore 使用不透明游标分页，getSyncProgress 返回最后同步的 syncedEventId，尚无历史时为 null。ID 不可比较大小，不能计算差值或据此推断已追平；需要判断某个目标是否同步时必须核对该事件是否存在。分页游标独立于 eventId，不能互相代用。

## 公共 Base

| 字段 | 规则 |
|---|---|
| namespace | Session 的 namespace，直接保存 |
| sessionName | Session 的 metadata.name，直接保存 |
| sessionId | 全局唯一的原生会话身份 |
| eventId | adapter 创建事件时生成的 UUID；同一事件重试保持不变，仅标识身份 |
| type | Base 内统一声明的 SessionEventType |
| time | adapter 使用原始时间，缺失时生成；重试与同步不改写 |
| itemId | 条目事件提供的不透明身份；不解析成原生坐标 |
| _meta | 顶层可选扩展 map，键为 string，值可以是任意类型，即 Record<unknown>，对应 Go map[string]any |
| payload | 由 type 唯一决定的业务结构 |

_meta 与 payload 同级，所有事件都可以携带，不在 payload 中重复定义；不再定义消息完成标志。完整消息与增量由事件 type 区分，不依赖扩展字段判断完成。这里的 unknown 是明确允许的任意值，不是待决占位，也不限定为字符串值或 JsonValue。

_meta 是统一事件的一部分，Store 不把它移到独立表或要求消费者额外查询。具体传输或文件编码对扩展值的承载方式仍需定义，不能把编码失败解释为可以静默丢字段；也不能因此把本合同的 value 类型改成另一种受限类型。扩展 map 不授权输出凭据或 Runtime 私有恢复材料。

SessionEvent 是按 type 判别的联合，每个分支固定 type 与对应 payload，公共信封继续复用 SessionEventBase。消息与思考始终共用 MessageEventPayload，但工具调用、工具结果、工具增量及各类会话通知分别约束必填字段。不再提供可任意组合 type 与 payload 的宽松 SessionEvent<Payload>。

## 事件类型与 payload

| type | payload | 持久化 |
|---|---|---|
| user.message / agent.message / agent.thinking | MessageEventPayload：messageId、完整 content，可选 phase | 是 |
| agent.message_chunk / agent.thinking_chunk | MessageEventPayload：messageId、本次追加 content | 否 |
| agent.tool_call | ToolCallEventPayload：id、kind、完整 rawInput 与可选描述 | 是 |
| agent.tool_result | ToolResultEventPayload：id、kind、status 与可选实际输出 | 是 |
| agent.tool_call_chunk | ToolChunkEventPayload：id、本次追加 content | 否 |
| agent.plan | PlanEventPayload：planId、entries | 否 |
| session.state_update | SessionStateEventPayload：previousState、state，可选 stopReason/error | 否 |
| session.error | SessionErrorEventPayload：message，可选 reason | 否 |
| session.info_update | SessionInfoEventPayload：title 或 updatedAt 至少一个 | 否 |
| session.config_option_update | SessionConfigEventPayload：完整 configOptions 列表 | 否 |
| session.available_commands_update | SessionCommandsEventPayload：availableCommands | 否 |
| session.usage_update | SessionUsageEventPayload：used、size，可选 cost | 否 |
| session.interaction_requested | InteractionRequestedEventPayload：interaction、request、blocking | 否 |
| session.interaction_closed | InteractionClosedEventPayload：interaction、outcome，可选 reason | 否 |

TypeSpec 检查 type/payload 配对、相应必填字段和普通条目事件的 itemId。它不证明业务事实真实、ID 属于原运行或工具已执行，也不自动验证全部业务不变量；这些仍由 adapter、Service、Store 各自校验。

条目相关的消息、工具与计划事件提供 itemId。预览与完整事实使用相同 itemId；工具调用和结果共享 itemId，但具有不同 eventId，不能按 itemId 覆盖历史。会话更新无需虚构 itemId。统一模型不代表全部事件都进入历史；HistoryStore 只接收表中五种持久类型，并校验 eventId、sessionId 与名称归属。

## 内容与工具语义

消息、思考、工具结果及其增量内容统一直接使用共享 ContentBlock[]；不增加工具专属的 type=content/content 包装。数组顺序保持原内容顺序，适配原生事件时直接转换为内容块，不先包装再要求消费者解包。具体结构见 [ContentBlock](../../shared/content-blocks.md)：采用 ACP 的 text/image/audio/resource/resource_link 五种类型，不增加 json 或公共 Base。message 与 thinking 在数据结构和处理规则上相同，只由事件 type 区分消息与思考的含义。二者都由 itemId/messageId 定位条目，chunk 与完整消息沿用相同身份；同一条目内部可以包含多个独立 ContentBlock，均按数组与事件交付顺序处理，不为 thinking 单独引入块坐标、通道或合并规则。

工具的 rawInput/rawOutput 是工具自身的动态业务值，TypeSpec 以 unknown 表达；不是额外的原生协议封装或恢复证据。工具 content 与消息 content 使用同一内容块数组，工具字段仍允许缺省以表达未提供内容。ToolEventFields 的 output 表达 availability、completeness 和可选 truncation；没有观察到的输出不伪造成空输出，显式 null、空字符串和字段缺失保持区别。JSON 预览采用直接内容块。

调用参数完整不证明工具已经开始执行。completed、failed、declined 需要实际结果证据，否则使用 unknown。未提供输出时 availability=unavailable、completeness=unknown，不补 rawOutput/content；明确提供的空字符串仍是 provided。truncated 必须有已知截断证据并提供 retainedBytes，omittedBytes 只有确知时提供。retained 不承诺无限输出归档。

## 增量与内存

所有 *_chunk 都是追加事件，不是累计快照或替换操作；同一条目的片段按源交付顺序消费。文本片段追加到该条目的展示内容，工具参数片段可使用 text ContentBlock，不要求每个片段已是合法 JSON。非文本 ContentBlock 作为独立块按顺序追加，不拼接 Base64 或重写已交付内容。

MASS 不跨事件积累 delta，不在内存重建完整消息，也不把 delta 同步到历史；只允许交付所需的有界队列。显示端若需要累积展示，由显示端自行负责。完整事件只来自原生完整观察，仍按原完整内容保存；只有 delta 而无完整事实的 adapter 本轮只提供实时预览，不靠消息结束、取消或断线拼出完整历史。这样不承诺仅流式输出的 Agent 一定有完整消息历史。

完整消息与预览是同一条目的两种观察；完整消息到达时，显示端可用完整观察结束预览，但 MASS 不生成替换型 chunk。计划、可用命令和配置列表是独立通知，各自遵守自身快照语义；“增量追加”不表示把这些列表持续拼接。

## 状态投影

参考 agentrun 的工作开始/结束驱动与终态保护，继续使用已经确认的 SessionState。共享枚举位于 shared/states.tsp，Runtime 不依赖 Service 资源模型。adapter 将原生工作开始、结束和需要行动的事实转换为 session.state_update，state/previousState 只取 idle、running、requires_action 或 null（未知），不直接报告 Service 拥有的 opening、closing、archived。

| 事实 | Service 投影与限制 |
|---|---|
| 打开/恢复接纳 | opening；成功后依据实际观察决定工作状态，缺少观察时 unknown |
| 原生工作确实开始 | running；输入接受回执和消息 delta 不能单独替代开始证据 |
| 当前工作有阻塞交互 | requires_action；多个请求独立结算 |
| 原生工作结束 | 连接有效且无未结算工作/阻塞交互时为 idle；stopReason/error 解释结果，不等于成功 |
| 交互回答被接受 | 结算对应请求，不自动宣称工作开始或结束 |
| 工作级错误 | 记录错误；只有工作结束证据才能结束该工作，不能直接宣告全会话失效 |
| 原运行失联或原生状态未知 | unknown；不推断为 idle/offline，不解除原操作责任 |
| 归档/停止进入关闭 | closing；运行通知不得重开准入，释放确认后进入 archived 或按关闭目的进入 offline |

Service 先核对原 Driver、Runtime 名称、Session 运行及工作关联，再应用事件。旧运行/旧工作的开始、结束或错误不能覆盖新工作。closing/archived 的资源状态不会被迟到的 running/idle 更新覆盖；archived 只有显式恢复接纳才能进入 opening。上述保护借鉴 agentrun，但不引入它的 creating/deleting/error 状态或持久事件种类，仍保留本合同的五类持久事实。

## 配置与交互

会话更新只提供对应 type 的字段，不通过一个更新清空其他观察值。配置选项按 [ACP 风格模型](session-config.md) 支持 select、分组 select 与 boolean；configOptions 是完整列表快照，空数组表示清空。交互使用[独立请求与关闭事件](interactions.md)，通过原 InteractionRef 回答；不以 requires_action 字符串代替请求内容。事件类型存在不代表具体 Driver 已支持全部原生能力。

## JSON 实例预览

可直接阅读 [event-examples.json](event-examples.json)，包含全部 17 种事件各一例。示例值维护在 [event-examples.tsp](event-examples.tsp) 的 @example 中，工具先编译检查其类型，再导出 JSON；JSON 文件为生成产物，不直接手改。

```sh
rtk proxy npm run examples --prefix harness/typespec
rtk proxy npm run examples:check --prefix harness/typespec
```

示例 UUID、时间和内容固定，仅供预览，不代表 adapter 实际输出。普通编译检查覆盖类型赋值，不自动校验本页所有跨字段业务规则。JSON 的 contentBlocks 部分覆盖五种类型和两种内嵌资源分支，按共享 ContentBlock 声明编译检查。当前工具导出实际实例，不是 JSON Schema；顶层扩展 map 的任意值合同不因这些可 JSON 编码的例子而收窄。

## 示例

```json
{
  "namespace": "default",
  "sessionName": "demo",
  "sessionId": "s1",
  "itemId": "msg_1",
  "eventId": "c89c43a2-5f95-48da-b992-40515585c1b7",
  "type": "agent.message",
  "time": "2026-10-20T00:00:00Z",
  "payload": {
    "messageId": "msg_1",
    "content": [{"type": "text", "text": "README 已检查。"}]
  },
  "_meta": {
    "displayHint": "final",
    "labels": ["review"]
  }
}
```

发布、持久保存和历史读取使用同一个事件结构，不为查询删除名称或重组 payload。历史同步仍保留 adapter 提供的 eventId/time，具体提交与恢复规则见[会话历史](../service/history.md)。

## 剩余待决

当前不增加思考块坐标，不在内存保存累计 delta，按 type 绑定 payload、独立交互和 ACP 风格配置选项均已有声明。动态业务值与扩展值的具体编码、持久格式版本承载，以及断线后待决交互查询仍需单独定义。接口版本继续集中记录在 [TypeSpec 入口](../../typespec/README.md)，不从旧示例复制逐事件版本字段。

[^direction]: 当前确认 type 位于公共信封、每种 type 绑定 payload，顶层 _meta 对应 map[string]any；删除消息完成标志。
