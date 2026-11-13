---
type: Contract
title: JSON-RPC 业务协议
description: MASS 的 JSON-RPC 2.0 消息、方法、错误、实时订阅与业务生命周期映射。
status: draft
sources:
  - id: direction
    resource: 项目负责人在 2026-11-12 确定仅使用 WebSocket、JSON-RPC 2.0，要求更新设计并选择 sourcegraph/jsonrpc2
    title: 统一协议与服务实例
  - id: jsonrpc
    resource: https://www.jsonrpc.org/specification
    title: JSON-RPC 2.0 Specification
---

# JSON-RPC 业务协议

## 职责与范围

业务方法统一采用 JSON-RPC 2.0，当前仅通过 [WebSocket](websocket.md) 承载。客户端连接明确的 massd 实例；不提供 stdio、proxy、内嵌运行或 REST/SSE 兼容入口。此方向与 Go 库选型已确认；本页的详细消息 profile 和绑定字段作为合同草案维护。[^direction]

Service 仍拥有资源、当前运行、操作接纳与结算、历史及事件发布。JSON-RPC 只映射调用、结果与通知；请求 ID、连接与 subscriptionId 不进入资源 spec、SessionEvent 或存储。Runtime 能力、按需启动、close、默认配置合并和恢复身份保证均沿用业务合同。

输入 cwd 按 Agent 运行环境解释；配置与资源只返回秘密引用。进程内 Handler 管理不作为远程方法暴露。协议不能直接访问 Store 或 Driver 形成第二条业务路径。

## 基础规则

采用 JSON-RPC **2.0** 的请求、响应和通知信封，所有消息带 `"jsonrpc":"2.0"`。以下的 MASS 方法集版本 `"1"` 不是 JSON-RPC 1.0。MASS 首版定义受限的应用 profile：

- 每条传输消息只承载一个 JSON 对象，不支持 batch；数组整体返回 Invalid Request，不执行其中任何元素。
- 客户端业务调用必须带 `id`；接受字符串或 0 至 9007199254740991 的整数，拒绝 null、负数和小数。SDK 生成不重复的非空字符串 ID。同一连接内调用者不复用 ID；服务端发现同一在途 ID 被重复提交时关闭该连接，避免关联歧义。ID 不是幂等键。
- `params` 为对象；零参数方法允许省略或 `{}`，不接受位置数组或 `null`。
- 成功在 `result` 内直接放业务值；无返回值使用 `null`。失败只带 `error`，不把 OperationError 放进成功结果。
- 字段使用 camelCase。日期、内容块、开放值、缺失与 null 的语义沿用业务类型。
- 拒绝重复 JSON 键、非法 UTF-8、非有限数值、固定结构的未知字段和只读字段写入。明确开放的 config、answer、`_meta` 仍允许合同规定的 JSON 值。
- 不静默字符串化语言对象、丢字段或损失大整数精度。没有无损 JSON 表示时明确失败。
- v1 不接受客户端通知作为业务命令。结构合法但没有 ID 的通知不执行、不回复，只记录脱敏诊断；不能因它没有响应而让副作用悄悄发生。
- 服务端仅发响应和已定义的通知；v1 没有服务端发起、要求客户端回复的业务请求。
- 畸形信封使用标准协议错误。正常运行中，合法但未知的方法返回 Method not found，不映射为业务 unsupported。

本页基于 JSON-RPC 2.0，明确采用单消息、命名参数和上述 ID 范围的应用约束；不宣称 batch、null/负数/小数 ID 或任意 JSON-RPC 客户端的完整兼容。客户端业务方法必须请求回执，通知形式不接纳业务副作用；服务端事件使用标准 notification。Go 库边界见 [Go JSON-RPC 绑定](go-jsonrpc.md)。[^jsonrpc]

## 初始化与版本

每条新连接首先调用 `initialize`，只用一次请求/响应，不另加 initialized 通知或协议 Session 资源。`protocolVersion` 专指 MASS 方法集版本；JSON-RPC 版本固定由顶层 `jsonrpc` 表达，两者分别校验。

```json
{
  "jsonrpc": "2.0",
  "id": "1",
  "method": "initialize",
  "params": {
    "protocolVersion": "1",
    "clientInfo": {"name": "massctl", "version": "example"}
  }
}
```

```json
{
  "jsonrpc": "2.0",
  "id": "1",
  "result": {
    "protocolVersion": "1",
    "serverInfo": {"name": "mass", "version": "example"}
  }
}
```

- `protocolVersion` 必填；clientInfo 可选，提供时 name/version 必填。serverInfo 必填。
- v1 只接受精确版本 `"1"`。不支持时返回协议错误后关闭；客户端不得静默换协议重发业务请求。
- 未初始化的业务请求返回协议错误，不调用 Service。重复 initialize 返回协议错误，不重置连接或已有订阅。
- 初始化成功响应必须先完成本地发送排序，连接才允许业务派发。客户端必须等到成功响应后再发业务请求，不流水发送初始化和业务。
- initialize 不创建 Session、不启动 Runtime、不补历史。成功只证明协议入口已就绪。
- v1 方法集固定且必须完整实现；原生能力继续由 Runtime/Session capabilities 表达，不增加第二套 provider 能力表。
- 破坏性协议变更升级 protocolVersion；软件版本、业务合同版本与原生 Agent 版本不能替代它。

初始化是额外的一次往返，用于客户端与独立升级的 massd 在业务副作用发生前确认协议兼容。

## 方法与寻址

准确声明见 [JSON-RPC TypeSpec](jsonrpc.tsp)，业务值引用现有 Service TypeSpec，`?` 表示属性可省略。create 输入应用现有 Create visibility，拒绝只读 metadata/status。没有新增业务 DTO 或削弱原有保证。

| method | params | result |
|---|---|---|
| `runtime.create` | `{input: RuntimeCreate}` | Runtime |
| `runtime.get` | `{runtimeRef: string}` | Runtime |
| `runtime.list` | `{query?: RuntimeQuery}` | Runtime[] |
| `runtime.update` | `{runtimeRef: string, spec: RuntimeSpec}` | Runtime |
| `runtime.start` | `{runtimeRef: string}` | Runtime |
| `runtime.stop` | `{runtimeRef: string}` | Runtime |
| `runtime.delete` | `{runtimeRef: string}` | null |
| `session.create` | `{input: SessionCreate}` | SessionCreated |
| `session.get` | `{session: SessionRef}` | Session |
| `session.list` | `{query?: SessionQuery}` | Page&lt;Session&gt; |
| `session.resume` | `{session: SessionRef, input?: SessionResume}` | SessionResumed |
| `session.close` | `{session: SessionRef}` | Session |
| `session.prompt` | `{session: SessionRef, input: SessionInput}` | InputReceipt |
| `session.answer` | `{session: SessionRef, interaction: InteractionRef, answer: InteractionAnswer}` | InteractionReceipt |
| `session.setConfig` | `{session: SessionRef, change: ConfigChange}` | ConfigReceipt |
| `session.cancel` | `{session: SessionRef}` | CancelReceipt |
| `session.history` | `{session: SessionRef, query?: HistoryQuery}` | HistoryPage |
| `session.load` | `{input: SessionLoad}` | SessionLoadAccepted |
| `session.cancelLoad` | `{session: SessionRef, loadId: string}` | HistoryLoadStatus |
| `session.subscribe` | `{session: SessionRef}` | `{subscriptionId: string}` |
| `session.unsubscribe` | `{subscriptionId: string}` | null |

`SessionRef` 直接使用已有互斥联合：

- `{sessionId: "原始 ID"}`；
- `{namespace: "demo", name: "review"}`。

不提供 nameOrID 字段，不按 UUID 外形猜测、不尝试另一种引用作为回退。ID 和 interaction 都作为原始 JSON 字符串传输，无 URL escape 或 base64url。继续保留 Service 已有的合法性限制，包括 sessionId 不得为 `.` 或 `..`；是否删除旧限制是独立的业务合同决定。

`session.answer` 的 answer 属性必填，但值可以为 null；缺失和显式 null 不等价。分页保持 SessionQuery.page、HistoryQuery.after/limit/types 的原始嵌套，不复制 HTTP query 的平铺规则。

runtime.update 仍完整替换 spec；create/resume/load 仍使用 Runtime Session 默认配置合并规则。create 与 load 的显式 `config` 保存在 Session spec；create/resume 的 initialInput 仅在调用者明确提供时发送，receipt 不持久化。session.load 的结果只表示接纳，之后查询 status.historyLoad；不因为没有 HTTP 202 就改变异步语义。

所有方法保留 [RuntimeService](../modules/service/runtime-service.tsp) 和 [SessionService](../modules/service/session-service.tsp) 的前置条件、成功保证和错误。`session.setConfig` 只映射既有瞬态单项更新，不回写 spec。新协议不新增 Session 删除或任意事件注入。

## 回执、超时与重试

```json
{
  "jsonrpc": "2.0",
  "id": "3",
  "method": "session.prompt",
  "params": {
    "session": {"sessionId": "example-session"},
    "input": {"content": [{"type": "text", "text": "检查这个项目"}]}
  }
}
```

```json
{
  "jsonrpc": "2.0",
  "id": "3",
  "result": {
    "submission": "example-submission",
    "acceptance": {"sent": true}
  }
}
```

此响应只表达实际证据，不表达工作完成。原生接受和记录没有证据时保持缺失，不补 false 或 true。

客户端可以并发发送请求，响应可乱序。协议模块不能串行等待长调用完成后才读下一个请求，否则 cancel、answer、查询会被阻塞。业务顺序由 Service 建立；同一连接先发送 A 后发送 B，不承诺 A 的业务效果先于 B。依赖前一结果时必须等待响应。

本地超时或 context 取消只结束该调用的等待，不自动发 cancel、close 或重发。未发送到传输前取消可报告本地未发送；开始发送后无法证明服务端未接纳时，副作用结果按未知处理。迟到响应不触发第二次操作，ID 不复用以免错误匹配。

连接断开只撤销仍未接纳的连接工作和观察资源。已经接纳的操作由 Service 继续持有、结算；连接 context 不能直接成为业务责任的唯一存活条件。需要取消 Agent 工作必须显式 session.cancel，取消 load 必须携带原 loadId。

close 仍等待现有合同规定的结算，成功返回 offline；失败或未知保持 closing。不为追求“所有请求都立即返回”而擅自把 start/stop/create/close 改成另一套后台任务协议。

## 订阅与通知

### 建立

1. 校验连接状态并解析 SessionRef，固定持久 Session 身份。
2. 调用同一个 Service.subscribeEvents，取得已有的有界 Subscription。
3. 分配连接内唯一且不复用的 subscriptionId，登记路由。
4. 将 subscribe 成功响应排入该连接的唯一发送序列，确保该消息先于本订阅通知写出。
5. 然后才允许转发该订阅的事件。

步骤 2 至 5 之间的事件保留在已有订阅队列，不增加无界准备队列。步骤 4 失败或连接已关闭时释放本次订阅，不遗留 Handler。订阅在步骤 2 后即可能终止：响应成功排队后，再发送已经发生的终止事实，不能伪造持续健康状态。

subscribe 响应就是订阅就绪证明，不再发送独立 ready 通知。客户端要观察某次输入的后续进展，应先等待 subscribe 响应，再发送 prompt。普通请求连接上的 prompt 响应与订阅连接上的事件没有跨连接到达顺序保证。

### 通知形状

| method | params | 含义 |
|---|---|---|
| `session.event` | `{subscriptionId: string, event: SessionEvent}` | 原始统一事件 |
| `subscription.closed` | `{subscriptionId: string, error?: OperationError}` | 该订阅正常或异常结束 |

通知没有 JSON-RPC id。subscriptionId 是投递路由信息，不添加到 SessionEvent 本体、不写入历史，不替代 eventId。

每个订阅按 Service 提供的顺序交付，终止通知位于已进入发送序列的该订阅事件之后。不承诺不同订阅或不同 Session 之间的全局顺序。订阅的连接映射由 [WebSocket 绑定](websocket.md#连接与订阅隔离) 定义：客户端默认每个订阅独占连接，服务端每连接最多一个活动订阅；不增加控制连接或事件连接的协议角色。

### 取消与终止

- unsubscribe 只作用于本连接登记的 ID；不存在或已结束时幂等返回 null，不查询其他连接或泄露其信息。
- 取消建立转发栅栏，停止新事件进入连接发送队列，关闭底层 Subscription，丢弃尚未转发的缓冲。
- 连接可写时，已转发的事件先发，再发唯一一次 subscription.closed，最后返回 unsubscribe 成功响应。客户端收到成功响应后不会再收到该订阅事件。
- 并发主动取消和自然终止共享一个终止结算；已确定的错误不能被主动取消覆盖。重复取消不再发送 closed。
- cancel、Session close、idle 或 Runtime 停止不直接销毁健康订阅，仍遵守现有 Broadcast 生命周期。
- 连接断开时释放全部订阅；缺少明确 closed 就遇到 EOF 的活动订阅按异常结束处理。服务端不承诺断网后还能交付错误通知。

### 历史与恢复

新连接重新 initialize，再按需建立新订阅。协议不自动恢复订阅，不补发断线通知，不缓存或重发输入。历史查询只返回已提交事实，仍不保证历史分页与实时流无缝衔接。

交互继续采用既有 SessionEvent 加 session.answer，不增加反向 RPC。多个客户端看到同一交互时，由 Service/Driver 依据原交互身份校验回答，不在连接层引入独占“控制客户端”。待决交互补查及跨客户端接管不在本轮新增范围。

创建时带 initialInput、以及异步 load，均可能在客户端拿到 Session 身份并建立订阅前产生事件；这些早期实时通知不能保证收到。需要先观察再发送的客户端使用无输入创建、subscribe、prompt；已提交事实可查历史。

## 背压与发送顺序

订阅在 Service 中保留自己的有界队列；传输采用单一有序写入路径和明确写入期限。实现可以直接逐条消费并等待发送，不强制为每个订阅再建一层事件搬运队列。任何新增发送队列都必须有界，网络写入不能阻塞 Service 的历史提交或其他连接。

- 单个 Subscription 溢出时异常终止该订阅，保留原错误；连接仍可写时发送 subscription.closed。
- 网络写入超时、连接缓冲耗尽或传输失败时关闭该连接。默认独立订阅连接仅使对应观察失败，不关闭普通 RPC 连接或其他订阅连接。
- 客户端每个订阅独立消费，不在公共调用响应的读循环内等待业务回调。客户端本地订阅队列溢出也须明确结束观察并释放该订阅连接。
- subscriptionId 只负责关联，网络写入成功不证明客户端业务消费成功；本版没有逐事件 ACK、消费窗口或流级调度。
- 队列容量与写入期限由组装配置明确；不在通用协议中拍定数值。底层库默认限制不能隐式改写内容块或容量合同，见 [容量原则](../adr/0001-defer-http-capacity-limits.md)。


## 错误模型

| JSON-RPC code | 用途 |
|---|---|
| -32700 | JSON 无法解析 |
| -32600 | 信封无效，或本 profile 不接受的 batch |
| -32601 | 未知 method |
| -32602 | 方法 params 形状不符合合同 |
| -32603 | 协议实现内部错误，脱敏 |
| -32000 | Service OperationError，data 原样承载业务 code/reason/message |
| -32001 | 连接协议状态错误，data.reason 为 not_initialized / already_initialized / unsupported_protocol_version |

数值 code 区分协议错误和业务错误，不给每一种业务错误另发一个数字。解析或信封错误无法可靠提取合法 ID 时响应 id 为 null；业务响应准确回显调用 ID。对合法通知不回复错误。

```json
{
  "jsonrpc": "2.0",
  "id": "3",
  "error": {
    "code": -32000,
    "message": "Operation outcome is unknown",
    "data": {
      "code": "outcome_unknown",
      "reason": "provider_result_unknown",
      "message": "Operation outcome is unknown"
    }
  }
}
```

示例 reason 仅示意，不新增具体 Service 错误分类。程序判断 data.code/reason，不解析 message。

消息可完整分帧时，普通解析/方法/参数错误不自动销毁其他业务和订阅。无法继续分帧、在途重复请求 ID、传输故障及不支持协议版本时关闭连接。客户端收到无法关联的迟到响应可忽略；收到畸形服务端消息则报告协议失败并终止连接。

业务 invalid_argument 与 JSON-RPC Invalid params 不混为一谈：前者是形状合法后 Service 的业务校验；后者是绑定层无法构造声明的入参。

所有错误都不自动证明可安全重试。outcome_unknown、连接 EOF、代理错误或仅收到部分消息均不能宣称确定未执行。维持既有显式幂等操作规则，不增加通用幂等键或 Operation 资源。

保留既有业务错误的证据要求：deadline_exceeded 仅用于已确认本次操作未派发副作用的请求截止；unavailable 表示服务、依赖或订阅当前不可用，且未产生本次副作用。已派发而无法确认结果时使用 outcome_unknown，不能将客户端等待超时直接映射为前两者。


## 传输独立性与后续 HTTP 承载

Service 的业务输入输出和 SessionEvent 不依赖 JSON-RPC 或 WebSocket。JSON-RPC 的方法、结果与错误不包含 WS connection 对象、frame、连接 ID 或关闭码；订阅标识是临时观察句柄，不是业务身份。WebSocket 路径、认证握手、连接数量、断线与心跳单独维护在传输绑定。

未来增加 HTTP 有两条可行路径：HTTP POST 承载同一 JSON-RPC 方法，通知另由 SSE/长轮询承载；或把同一 Service 方法重新映射为 REST 资源与动作，观察使用 SSE。前者可保留方法及结果模型，后者需要重新定义路由和状态码。两者都必须重新定义认证、订阅句柄归属、取消与通知顺序；不能把现有连接内 subscriptionId 直接拿到任意 HTTP 请求上使用。

传输变更不要求改写 Session 身份、持久事件或已有工作生命周期；客户端连接和临时订阅需要重建。它不是一个无需适配的运行时开关，也不承诺旧 REST 客户端直接兼容。当前只实现一种入口，不预建通用 Transport 注册框架或 HTTP 备用实现。

## 验收

通过公开协议验证 initialize、完整方法映射、两种 SessionRef、null answer、初始输入、异步 load 和只读历史；验证错误 code/reason 保留、未知结果不重试、非法通知无副作用及标准响应关联。

订阅验证响应先于首事件、跨连接事件与输入回执不承诺先后、取消后无新事件、异常终止保留原因、close/Runtime 停止不误销毁健康观察、历史与实时不伪装连续。传输隔离、认证和代理链路场景见 WebSocket 绑定。

公开 Operation 查询、通用幂等键、可靠续传、待决交互补查与控制者选举均不属于本版能力。需交互的调用方须持续观察并接受断线期间可能丢失非持久通知的边界。

[^direction]: 本次要求先更新设计；不表示运行实现或验收已完成。
[^jsonrpc]: JSON-RPC 规定信封、请求关联、通知和错误；本页另行明确 MASS 业务 profile，不将库的限制表述为标准要求。
