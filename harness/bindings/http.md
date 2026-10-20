---
type: Contract
title: HTTP 承载
description: MASS v1 资源式 HTTP JSON API、操作回执、错误映射与可选 SSE 实时观察合同。
status: stable
sources:
  - id: direction
    resource: 项目负责人在 2026-10-20 的 HTTP 方案与独立 agent 审阅请求
    title: 面向业务客户端的 REST 与可选实时观察
  - id: path-prefix
    resource: 项目负责人在 2026-10-20 要求为 HTTP URL 添加 apis 前缀
    title: 统一业务 API 路径前缀
  - id: resource-paths
    resource: 项目负责人在 2026-10-20 要求 Runtime 原路径 PUT、Session nameOrID 寻址及原始 ID URL escape
    title: 简化资源路径与统一 Session ID 编码
  - id: http-caching
    resource: https://www.rfc-editor.org/rfc/rfc9111.html#section-5.2.2
    title: HTTP Cache-Control 响应指令
  - id: claude
    resource: https://platform.claude.com/docs/en/api/typescript/beta/sessions/events
    title: Claude Managed Agents 的发送、历史与实时流接口
  - id: openai
    resource: https://developers.openai.com/api/docs/guides/agents-api/sessions
    title: OpenAI Agents API 的异步会话与 HTTP 输入
---

# HTTP 承载

## 选择与职责

公共 HTTP 接口采用资源式 REST、JSON 请求与响应，以及独立、可选的 SSE 实时观察。不采用 JSON-RPC envelope，不要求客户端初始化协议会话或保持连接；Agent 工作的生命周期独立于单次 HTTP 请求与订阅连接。业务服务、CLI、脚本与界面使用同一接口。此选择参考托管 Agent API 的 Session 资源与独立事件流组织方式，不复制厂商业务模型。[^claude][^openai]

HTTP 操作遵循既有 Runtime 与 Session 业务合同，不隐式启动 Runtime 或恢复 Session。进程内事件处理器不作为远程 API 暴露。

cwd 按 Agent 所在运行环境解释，远端路径不能被本机目录存在性检查拒绝。配置与资源响应只返回秘密引用，不返回解析后的秘密。

本页只规定 URL、方法、寻址、校验与处理行为、状态码、认证及 SSE 协议。具体请求与响应字段、schema、示例和 OpenAPI 文档由代码生成，不在本页手工维护；生成产物必须符合本页及业务合同。框架、代码组织和生成工具不属于本页范围。

## 基础规则

- 业务 API 的统一路径前缀为 `/apis/v1`，其中 `/apis` 为 API 根路径，`v1` 为 HTTP 路由版本。该前缀适用于全部 Runtime、Session、历史与 SSE 路由，以及创建响应的 Location。健康探针独立位于 `/livez`、`/readyz`，不带 `/apis` 或版本。[^path-prefix]
- 本合同只定义带 `/apis/v1` 前缀的业务路由；旧 `/v1` 路径不提供兼容别名或重定向，按不存在的路由返回 404 not_found。
- 字段使用 camelCase；状态、事件 type 等值保持业务合同声明。
- 有 JSON 请求体时使用 `Content-Type: application/json`，编码为 UTF-8；成功 JSON 与错误 JSON 均使用此媒体类型。SSE 使用 `text/event-stream`。
- 成功直接返回操作声明的资源、数组、回执或分页值，不增加 `success`、`data`、`result` 包装。错误直接返回 `{code, reason, message}`。
- 必填、可选与 null 按接口声明区分；缺失不自动补 null。日期时间使用 RFC 3339 字符串，媒体二进制使用 Base64。
- `unknown` 和开放 map 在 HTTP 上只能承载 JSON 值：null、布尔、有限数值、字符串、数组和字符串键对象。禁止 NaN、Infinity、循环引用、函数、语言对象的隐式字符串化，以及丢字段的序列化；无法表示为 JSON 的值明确报错，不静默改写。大整数使用方须采用可保留精度的 JSON 编解码，不先转为浮点数。
- 拒绝无效 JSON、重复对象键、未声明的固定结构字段、只读字段写入及未知 query 参数。运行配置、交互答案、工具动态值和 `_meta` 等明确开放位置不套用固定字段白名单，仍由对应业务合同校验。
- GET 和 DELETE 不接受请求体。无业务输入的动作 POST 不带请求体；带空 JSON 对象 `{}` 也接受，其他值拒绝。接口不通过 GET 产生业务副作用。
- Runtime 名称与 namespace/name 按类型约束校验。路径参数按 UTF-8 逐段进行 URL 百分号编码（URL escape），服务器只解码一次；Session ID 的原始值同样按单个路径段编码，不使用 base64url。交互引用单独使用下文的 base64url 规则。所有路径参数均不得解释为文件路径。
- 查询字符串按 UTF-8 URL 编码；单值参数重复出现时报错。`limit` 为十进制正整数，默认 100，不静默截断；其他默认值与枚举约束保持业务合同。
- 所有 API 响应至少使用 `Cache-Control: no-store`；SSE 的完整响应头要求见下文；不提供 ETag、条件 GET、If-Match 更新或 JSON Patch。认证网关不得把 GET 的结果缓存成跨用户共享状态。

## Runtime 路由

Runtime 是全局命名资源；`{runtime}` 为 Runtime 名称。创建只接受用户可写内容，拒绝系统生成字段。

| HTTP | 处理行为 | 成功 |
|---|---|---|
| `POST /apis/v1/runtimes` | 创建配置登记，不自动启动 | 201，Runtime 资源 |
| `GET /apis/v1/runtimes` | 列表查询；可按 type、state 筛选 | 200，资源数组 |
| `GET /apis/v1/runtimes/{runtime}` | 查询资源 | 200，Runtime 资源 |
| `PUT /apis/v1/runtimes/{runtime}` | 完整更新期望配置 | 200，Runtime 资源 |
| `POST /apis/v1/runtimes/{runtime}/start` | 启动 Runtime | 200，Runtime 资源 |
| `POST /apis/v1/runtimes/{runtime}/stop` | 停止 Runtime | 200，Runtime 资源 |
| `DELETE /apis/v1/runtimes/{runtime}` | 删除 Runtime | 204，无响应体 |

创建返回的 Location 为 `/apis/v1/runtimes/{runtime}`，按路径参数规则编码。PUT 请求体直接承载完整期望配置，只更新 spec，不接受 metadata/status，不要求 resourceVersion，不热更新或重启。Runtime 列表不分页，读取失败不能返回伪装成全量的部分数组。

## Session 路由与寻址

集合入口为 `/apis/v1/sessions`。创建必须提供 namespace 与名称，拒绝系统生成字段。名称必须满足[创建名称校验](../modules/service/session.md#创建名称校验)，UUID 形式的名称返回 400 invalid_argument，reason=session_name_is_uuid，且不得产生创建副作用。创建不隐式发送用户输入，成功前必须完成持久身份登记与原生会话打开。

单资源支持两种路径，均定位同一份持久 Session：[^resource-paths]

- 全局 ID：`/apis/v1/sessions/{sessionId}`，参数为 URL escape 后的原始 status.sessionId。
- namespace 内名称或 ID：`/apis/v1/namespaces/{namespace}/sessions/{nameOrID}`，参数为 URL escape 后的 metadata.name 或原始 status.sessionId。

下表用 `{session}` 表示上述任一完整单资源路径；所有后缀在两种路径上均提供，两种寻址的操作语义一致。不接受 query 或 body 额外指定另一个 Session；不使用重定向，以免客户端重新发送副作用请求。

### 路径编码

客户端对原始值按 UTF-8 编码，并对单个路径段做 URL escape，不编码整条 URL。Session ID 不限于 UUID，但完整原始值不得等于 `.` 或 `..`，见 [Session ID 合法性](../modules/service/session.md#session-id-合法性)。客户端在构造 URL 前拒绝这两个值；服务端单次解码后发现同样的值，返回 400 invalid_argument，reason=invalid_session_id。其他 ID 不裁剪空格、不作 Unicode 规范化，也不按名称规则校验。`/`、`%`、`?`、`#` 等字符必须编码；空值、无效百分号转义或无效 UTF-8 返回 400 invalid_argument。

| 原始 sessionId | URL 中的路径段 |
|---|---|
| `abc-123` | `abc-123` |
| `a/b` | `a%2Fb` |
| `a%2Fb` | `a%252Fb` |
| `a b?#` | `a%20b%3F%23` |
| `a..b` | `a..b` |

服务器先按编码后的路径确定路由与参数边界，再对参数解码一次；校验通过后，值中的 `/`、`.` 或 `%` 均是 ID 内容，不得再次拆段、解码或归一化。`%2E`、`%2E%2E`、`.%2E` 等单次解码后恰为 `.` 或 `..` 的形式同样非法。HTTP 客户端、代理和路由器必须保留编码后的路径段；不能无损保留这些值的部署不满足本合同，不得静默改写后匹配其他资源。JSON 中的 sessionId 始终保留原值。

### nameOrID 解析

先拒绝解码后恰为 `.` 或 `..` 的 nameOrID，再在路径指定的 namespace 中，按解码后的原值分别精确匹配 metadata.name 和 status.sessionId，不按 UUID 外形或其他字符模式猜测引用类型。ID 匹配的 Session 也必须属于该 namespace；其他 namespace 的同名或 ID 匹配不能成为候选。

- 仅命中一个 Session，或名称与 ID 命中同一 Session：使用该持久身份。
- 名称与 ID 命中不同 Session：返回 409 conflict，不按优先级猜测目标；调用方可改用全局 ID 路径。
- 没有命中：返回 404 not_found。目录读取失败必须返回实际错误，不能作为未命中继续执行。

目标解析后固定 sessionId；不得在副作用执行时重新按名称解析。两种路径均先完成认证。namespace 约束用于寻址，不提供租户隔离。

| HTTP | 处理行为 | 成功 |
|---|---|---|
| `POST /apis/v1/sessions` | 创建 Session | 201，Session 资源 |
| `GET /apis/v1/sessions` | 列表查询；筛选与分页见下文 | 200，资源分页 |
| `GET {session}` | 查询资源 | 200，Session 资源 |
| `POST {session}/resume` | 恢复 Session | 200，Session 资源 |
| `POST {session}/archive` | 归档 Session | 200，Session 资源 |
| `POST {session}/inputs` | 提交输入 | 200，输入回执 |
| `POST {session}/interactions/{interaction64}/answer` | 提交交互答案 | 200，回答回执 |
| `POST {session}/cancel` | 请求取消当前工作 | 200，取消回执 |
| `GET {session}/events` | 查询历史；筛选与分页见下文 | 200，历史分页 |
| `GET {session}/events/stream` | 订阅实时事件 | 200，SSE |

`interaction64 = base64url(UTF-8(原始交互引用))`，不带 `=` padding，只使用 `A-Z a-z 0-9 - _`。解码必须为合法 UTF-8 且重新编码与原片段完全相同；空值、非法字符、padding 或非规范编码返回 400 invalid_argument。JSON 中的交互引用保留原值。例如原值 `a/b` 编码为 `YS9i`，原值 `..` 编码为 `Li4`。请求体直接承载答案，可为 null、标量、数组或对象，不包 `{answer: ...}`，不得由 HTTP 层把 null 当作缺少请求体。回答是否有效由原交互合同校验；过期或重复回答不能提交给新运行。

创建返回的 Location 为 `/apis/v1/sessions/{sessionId}`，使用 URL escape 后的原始 ID，确保名称与其他会话 ID 冲突时仍可无歧义查询；响应仍包含跨 Runtime 唯一的 status.sessionId。列表可按 namespace、runtimeRef、state 筛选，after、limit 在 HTTP query 中平铺；默认跨所有 namespace、Runtime，包含离线和归档会话。

本版本不提供 Session PUT/PATCH/DELETE、配置选项写入、批量命令或任意事件注入。归档保留持久身份与历史，不能作为删除别名。

## 回执、超时与重试

201 只在创建成功保证达成后返回。200 的输入、回答和取消响应表示返回对应操作的回执，不表示 Agent 工作完成；输入回执保留 acceptance 的缺失/false/true 区别。归档和 Runtime 停止成功须满足各自业务合同的资源释放保证。

本版本不使用 202、Operation 资源、后台轮询句柄或 Idempotency-Key。请求 ID、追踪 ID、Session 名称与事件 UUID 均不代替输入幂等键。收到 `Idempotency-Key` 请求头时返回 400 invalid_argument，避免调用方误认为已启用去重。

客户端断开、代理超时或 HTTP 请求截止不撤销已接纳操作，不自动取消 Agent 工作、归档 Session 或释放资源责任；显式取消使用 cancel 接口。服务端不能因连接断开就把原操作视为确定未执行或重新派发。

有副作用的请求超时或连接中断时，客户端不得自动重发。HTTP 能确认未派发时可返回 deadline_exceeded；一旦已派发且无法确认结果，返回 outcome_unknown。没有收到可验证的响应时，客户端也必须按结果未知处理。若原操作后来完成，可在资源查询中观察其可证明状态；资源存在或状态变化不一定能确认某次输入是否接纳。没有公开操作查询与业务幂等合同前，不承诺未知输入可以安全重试或跨服务重启查询。

## 历史与分页

`GET {session}/events` 接受 after、limit、types 查询参数。多类型使用重复参数，例如 `?types=agent.message&types=agent.tool_result`；不使用逗号拼接、JSON 数组或 `types[]`。单个空值与未知事件类型拒绝；省略 types 表示全部类型。类型集合按共享合同消除重复和顺序差异，实时类型合法但没有持久记录。

Session 列表和历史均返回分页结果；空列表仍为 200，不能改成 204。after 是上次响应的 next，省略从头开始；next 缺失表示本次读取视图无后续匹配记录，不表示 Agent 已结束。不同分页类型的游标不能互换。

保留 [HistoryQuery/HistoryPage](../shared/history-models.tsp) 的过滤、读取视图、游标与同步语义。事件 UUID 不排序、不作 after，syncedEventId 不代表上游或业务消费已经追平。查询不激活 Runtime、不恢复 Session、不执行处理器。分页读取不是实时流的自动续传机制。

## SSE 实时观察

SSE 是可选的实时观察通道，传递统一 SessionEvent；工作输入、回答与取消始终通过独立 HTTP 请求。调用方可以只轮询状态与历史而不建立 SSE。

### 响应头与代理要求

成功建立 SSE 时，源服务必须发送：

```http
Content-Type: text/event-stream
Cache-Control: no-cache, no-store, no-transform
```

`no-store` 禁止缓存保存响应；`no-cache` 要求复用前重新验证，本身不禁止保存；`no-transform` 禁止中间节点转换内容。SSE 不提供缓存复用或条件 GET。[^http-caching]

缓存与响应缓冲是两个独立行为。SSE 路由必须关闭代理/CDN 缓存及响应缓冲，并禁用会延迟逐帧交付的压缩或内容转换。

服务端对完整事件帧、控制帧和心跳及时发送；验收须通过实际代理链路观察首帧和后续帧持续到达，不能只检查源站响应头。代理空闲超时必须大于心跳间隔。

### 建立与编码

1. 服务器先完成认证、授权、目标解析与 订阅登记；失败返回普通非 2xx 错误 JSON，不发送 SSE 响应头。
2. 订阅登记后返回 200 和上述 SSE 响应头，禁用代理缓存与缓冲；立即发送首帧 `subscription.ready`，data 为 `{}`。此帧表示该订阅已经生效，其后按订阅顺序交付登记之后发布到该会话观察通道的事件，不按命令接纳时间或原生事件时间筛选。准备首帧期间的事件必须有界保留，溢出明确异常结束。
3. 每个业务帧使用 `event: session.event`，data 为一个完整、单行 JSON 编码的 SessionEvent，不增加业务包装，不把 type 改成 SSE 专属值。控制帧不属于 SessionEvent，也不进入历史或处理器。
4. 空闲时至少每 15 秒发送一次 SSE 注释心跳；心跳不证明 Agent 健康或工作完成。部署须关闭流缓冲并使代理空闲超时大于心跳间隔。

### 结束与丢失

- 订阅正常结束时，服务器发送 `event: subscription.closed`、`data: {}` 并关闭连接。它只表示观察结束，不代表工作完成或成功。
- 订阅失败、序列化失败、慢消费者溢出或服务器关闭导致交付中止时，尽可能发送 `event: subscription.error`，data 为统一错误对象，然后关闭。响应头已发送后不再改 HTTP 状态码。网络已断或队列不可写时不能保证错误帧送达；客户端未读到明确结束帧就遇到 EOF，必须作为异常断线处理。
- 客户端关闭连接只关闭本订阅并释放等待和缓冲，不取消工作、归档 Session 或停止 Runtime。慢订阅者不得通过无限缓冲影响核心事实提交。
- 不发送 SSE `id` 或 `retry` 字段；eventId 仍在 JSON 中。拒绝非空 Last-Event-ID 和所有续传 query 参数，返回 400 invalid_argument，不暗示支持 SSE 恢复。
- 客户端不得把自动重连视为连续交付。新连接是新订阅，会错过断线期间的预览与其他实时通知；只有已提交的持久事实可另查历史。MASS 不从 delta 拼造完整历史。

需要在提交输入之前观察其进展时，先等 subscription.ready，再发送输入；发送成功回执与 SSE 事件之间没有跨连接先后保证。idle、归档或一项交互结束均不自动关闭 SSE；订阅结束以明确结束帧或连接关闭为准。

## 错误与 HTTP 状态码

错误响应的 code 是稳定分类，reason 是具体原因，message 是脱敏说明。HTTP 不解析 message，保留业务错误的 code/reason。下表规定已知 code 的 HTTP 映射，不封闭或重新定义业务错误分类；未知 code 作为 500 返回原值，不能被解释为可重试。HTTP 入口产生的参数、认证和协议错误使用表中的对应分类。

| code | HTTP | 含义 |
|---|---|---|
| invalid_argument | 400 | JSON、字段、query、游标、交互答案或参数无效 |
| unauthenticated | 401 | 缺少或无效凭据；附 `WWW-Authenticate: Bearer` |
| permission_denied | 403 | 凭据有效但目标或操作不在授权范围 |
| not_found | 404 | 路由或目标资源不存在 |
| already_exists | 409 | 名称或唯一身份冲突 |
| conflict | 409 | 当前状态、在途操作、引用或交互结算阻止操作 |
| unsupported | 422 | 已识别的业务操作或类型能力不支持 |
| resource_exhausted | 429 | 实际资源耗尽；此映射不要求 HTTP 预设容量或速率阈值 |
| unavailable | 503 | 服务、依赖或订阅当前不可用，且未产生本次副作用 |
| deadline_exceeded | 504 | 请求截止，已确认本次操作没有派发副作用 |
| outcome_unknown | 500 | 已派发操作的副作用结果无法确认；禁止自动重试 |
| internal | 500 | 其他内部错误，不宣告安全重试 |

HTTP 特有错误也使用统一错误格式。错误方法返回 405，附 Allow，code=invalid_argument；不支持的 Content-Type 返回 415，code=invalid_argument；Accept 显式排除响应媒体类型时返回 406，code=invalid_argument。HTTP 请求体大小等容量边界暂不设置强制限制，见 [ADR-0001](../adr/0001-defer-http-capacity-limits.md)；本版本不要求配置请求体上限或实现相应的 413 超限拒绝。认证应先于资源存在性检查，防止未认证查询泄露资源；同一请求有多项错误时不保证具体报错次序。

支持的业务动作必须遵守相应业务合同。路径不存在不得返回伪造成功，unsupported 不用来隐藏缺失实现。错误响应不能携带秘密、原生堆栈或未经脱敏的提供商错误。400/409/422 是请求或前置条件问题，不自动重试；429/503/504 也不能越过“副作用确定未派发”检查，仅在有此证据时才可按退避策略重试。代理生成的 5xx 不构成未派发证据。

## 认证、部署与健康探针

受保护的 API 使用 `Authorization: Bearer <token>`；不把 token 放入 URL、query、SSE data 或日志。非本机 HTTP 访问必须使用 TLS，可由受信反向代理终止。SSE 使用相同的 Bearer 认证要求。

HTTP v1 面向单一信任域：有效 token 授予本实例全部 Runtime、Session、历史与操作的访问权限，不提供按用户、namespace 或资源拆分的权限。Runtime、Session、历史与订阅的全部路由均先校验该实例权限，nameOrID 路径与全局 ID 路径一致。namespace 只是分组，不能当作租户隔离；互不信任的调用方必须使用隔离实例。细粒度授权需后续另行定义，不能通过过滤单页结果来伪装实现。

认证的 token 签发、校验与轮换由部署提供。本版本不增加账号或 ACL 管理 API；未配置认证的实例只允许显式的本机单用户模式，监听回环地址且不接受远程访问。仅绑定回环不能替代多用户环境的身份隔离；本机模式按单一受信用户授权，不支持跨用户服务。CORS 默认关闭，浏览器跨源接入需由部署显式配置允许源与请求头，不能把 CORS 当成认证。

| 路径 | 响应 | 判定 |
|---|---|---|
| `GET /livez` | 200 | HTTP 进程仍能响应；不检查模型与 Runtime |
| `GET /readyz` | 就绪时 200；否则 503，统一错误格式 | 完成必要初始化且入口正在接纳 API 请求；关闭接纳后不就绪 |

探针只提供上述最小状态，可由部署在受控网络免认证；不泄露配置或资源清单。GET 以外的方法为 405。readyz 不因尚未创建 Runtime、某 Runtime 停止或模型不可用而默认失败；具体 Runtime 健康由其资源 status 查询。就绪不证明存储每次读写、全部 Agent、认证凭据或模型实际可用。

## 当前边界与验收

HTTP 容量边界遵循 [ADR-0001](../adr/0001-defer-http-capacity-limits.md)：暂不预设请求体大小等强制限制，遇到具体问题后再讨论策略、阈值与验收要求。

HTTP v1 确定以上路由、JSON、状态码、授权入口与 SSE 形式，遵循统一业务生命周期。以下能力不包含在本版本：公开 Operation 查询、跨重启操作结算查询、客户端幂等键、断线连续事件交付、待决交互补查与 webhook。交互请求是非持久实时通知，低频或晚订阅客户端可能只看到 requires_action 而缺少请求内容；本版本不承诺断线后恢复完整交互界面。需人工交互的客户端必须持续观察，并接受这一限制；后续能力须先扩展对应业务合同。

| 验收场景 | 应观察到的行为 |
|---|---|
| 业务路由与创建响应 Location | 全部使用 `/apis/v1`；Runtime、Session 两种寻址、历史与 SSE 的路径一致；旧 `/v1` 路径返回 404，不重定向 |
| 只用普通 HTTP 提交后断开 | 工作不依赖连接，之后可查询状态与已提交历史 |
| 通过 ID 与名称操作同一 Session | 相同身份、权限、回执与业务效果，无重定向重发；原始 ID 经 URL escape 和单次解码无损还原 |
| nameOrID 与 namespace | 名称和 ID 均可寻址；跨 namespace 的 ID 不命中；命中不同会话时返回 409 且不产生副作用 |
| 路径特殊字符 | 合法 ID 中的 slash、百分号、空格与点号无损还原；编码分隔符不拆段，百分号不二次解码 |
| 非法点号 ID | 客户端在构造 URL 前拒绝原始 `.`、`..`；HTTP 单次解码后得到这两个值时返回 400，nameOrID 同样拒绝 |
| 创建 Session 成功 | 持久身份已提交，Location 可查询，不自动发送 prompt |
| PUT Runtime 资源路径后查询 | 只改变期望配置，旧运行及其 appliedGeneration 不被伪造更新 |
| 输入或取消成功 | 仅返回对应回执，不宣告工作完成；未知结果不自动重发 |
| types 重复参数与分页 | 精确 OR 过滤，游标条件校验，返回 next 与 syncedEventId |
| SSE 经过代理 | 源站发送规定响应头；代理不缓存、不攒帧，ready、业务帧和心跳及时交付，不等待流结束 |
| 创建 UUID 形式的 Session 名称 | 返回 400 invalid_argument / session_name_is_uuid，且无创建副作用 |
| SSE ready 后发送输入 | 登记之后发布的事件按序传递，不要求回执先于事件 |
| 慢订阅者、EOF 与重连 | 有界、异常明确；新流不伪装成连续历史 |
| 缺少凭据或无效 token | 拒绝全部资源与订阅路由，不因名称/ID 别名绕过认证；有效 token 作用域为整个实例 |
| 只有 livez/readyz 成功 | 不被解释为真实 Agent 或模型验收通过 |

[^claude]: 参考 Session 资源下分别发送、查询和订阅事件的 HTTP 组织方式。
[^openai]: 参考持久 Session 与异步执行独立于单次请求的组织方式。

[^path-prefix]: 本次请求确定业务 URL 在版本段之前增加 `/apis` 前缀。

[^resource-paths]: 本次请求确定 Runtime 在资源路径上更新配置，namespace 下支持 nameOrID；Session ID 使用原始值并做 URL escape。

[^http-caching]: RFC 9111 第 5.2.2 节的 no-cache、no-store 与 no-transform 响应指令。
