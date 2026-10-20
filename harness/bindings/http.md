---
type: Contract
title: HTTP 承载
description: MASS v1 资源式 HTTP JSON API、操作回执、错误映射与可选 SSE 实时观察合同。
status: stable
sources:
  - id: direction
    resource: 项目负责人在 2026-10-20 的 HTTP 方案与独立 agent 审阅请求
    title: 面向业务客户端的 REST 与可选实时观察
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

HTTP 将请求交给 [RuntimeService](../modules/service/runtime-service.tsp) 和 [SessionService](../modules/service/session-service.tsp)。参数、目标、成功结果与 OperationError 的业务语义和进程内调用一致；HTTP 不遍历原生 Agent 聚合目录，不隐式启动 Runtime 或恢复 Session。进程内事件处理器不作为远程 API 暴露。

cwd 按 Agent 所在运行环境解释，远端路径不能被本机目录存在性检查拒绝。配置与资源响应只返回秘密引用，不返回解析后的秘密。

本页确定 HTTP 形式；字段、联合类型和业务约束引用当前 TypeSpec，不另外维护资源 DTO。现有语言实现是否符合此合同须单独验收，已有 JSON-RPC 路由不成为本合同的兼容要求。

## 基础规则

- API 路径以 `/v1` 为前缀；健康探针不带版本。字段使用 TypeSpec 中的大小写，通常为 camelCase；状态、事件 type 等值保持原声明。
- 有 JSON 请求体时使用 `Content-Type: application/json`，编码为 UTF-8；成功 JSON 与错误 JSON 均使用此媒体类型。SSE 使用 `text/event-stream`。
- 成功直接返回操作声明的资源、数组、回执或分页值，不增加 `success`、`data`、`result` 包装。错误直接返回 `{code, reason, message}`。
- 必填、可选与 null 按 TypeSpec 区分；缺失不自动补 null。日期时间使用 RFC 3339 字符串，媒体二进制按 ContentBlock 声明使用 Base64。
- `unknown` 和开放 map 在 HTTP 上只能承载 JSON 值：null、布尔、有限数值、字符串、数组和字符串键对象。禁止 NaN、Infinity、循环引用、函数、语言对象的隐式字符串化，以及丢字段的序列化；非 JSON 原生值须由来源 adapter 明确转换或报错，HTTP 不静默改写。大整数使用方须采用可保留精度的 JSON 编解码，不先转为浮点数。
- 拒绝无效 JSON、重复对象键、未声明的固定结构字段、只读字段写入及未知 query 参数。RuntimeConfig、InteractionAnswer、工具动态值和 `_meta` 等明确开放位置不套用固定字段白名单，仍由对应业务合同校验。
- GET 和 DELETE 不接受请求体。无业务输入的动作 POST 不带请求体；带空 JSON 对象 `{}` 也接受，其他值拒绝。接口不通过 GET 产生业务副作用。
- Runtime 名称与 namespace/name 按类型约束校验，逐段进行 URL 百分号编码，服务器只解码一次。Session ID 和交互引用按下文的 base64url 规则编码，不直接把原生字符串拼到路径中。所有路径参数均不得解释为文件路径。
- 查询字符串按 UTF-8 URL 编码；单值参数重复出现时报错。`limit` 为十进制正整数，默认 100，不静默截断；其他默认值与枚举约束保持 Service 合同。
- 所有 API 响应及 SSE 使用 `Cache-Control: no-store`；不提供 ETag、条件 GET、If-Match 更新或 JSON Patch。认证网关不得把 GET 的结果缓存成跨用户共享状态。

## Runtime 路由

Runtime 是全局命名资源；`{runtime}` 为 metadata.name。RuntimeCreate 的 metadata 只接受 name 与可选的空 namespace，不接受系统生成字段。

| HTTP | Service 操作 | 请求体 | 成功 |
|---|---|---|---|
| `POST /v1/runtimes` | createRuntime | RuntimeCreate 的创建字段 | 201，Runtime |
| `GET /v1/runtimes` | listRuntimes | 无；query：type、state | 200，Runtime[] |
| `GET /v1/runtimes/{runtime}` | getRuntime | 无 | 200，Runtime |
| `PUT /v1/runtimes/{runtime}/spec` | updateRuntime | RuntimeSpec 本身 | 200，Runtime |
| `POST /v1/runtimes/{runtime}/start` | startRuntime | 无 | 200，Runtime |
| `POST /v1/runtimes/{runtime}/stop` | stopRuntime | 无 | 200，Runtime |
| `DELETE /v1/runtimes/{runtime}` | deleteRuntime | 无 | 204，无响应体 |

创建返回的 Location 为 `/v1/runtimes/{runtime}`，按路径参数规则编码。创建只持久登记、不启动；PUT 完整替换期望 spec，不接受 metadata/status，不要求 resourceVersion，不热更新或重启。Runtime 列表不分页，读取失败不能返回伪装成全量的部分数组。

## Session 路由与寻址

集合入口为 `/v1/sessions`。SessionCreate 的 metadata 必须提供 namespace/name，不接受 uid、createdAt、generation、resourceVersion。创建不隐式发送用户输入，成功前必须满足持久身份与原生打开的 Service 保证。

单资源支持两种等价路径：

- ID：`/v1/sessions/{sessionId64}`。
- 名称：`/v1/namespaces/{namespace}/sessions/{name}`。

下表用 `{session}` 表示上述任一完整单资源路径；所有后缀在两种路径上均提供，通过同一个 SessionService 操作解析，不维护另一套业务逻辑。不接受 query 或 body 额外提供另一种 SessionRef；不使用重定向，以免客户端重新发送副作用请求。

`sessionId64 = base64url(UTF-8(status.sessionId))`，不带 `=` padding，只使用 `A-Z a-z 0-9 - _`。解码必须为合法 UTF-8 且重新编码与原片段完全相同；空值、非法字符、padding 或非规范编码返回 400 invalid_argument。解码后的值原样交给 Service，不去空格、不作 Unicode 规范化、不按 `/`、`.` 或 `%` 再解释。这样原生 ID 包含 slash 或 dot segment 时也可无损寻址。JSON 中的 sessionId 与 InteractionRef 始终为原值，不使用路径编码替换身份。

| HTTP | Service 操作 | 请求体 | 成功 |
|---|---|---|---|
| `POST /v1/sessions` | createSession | SessionCreate 的创建字段 | 201，Session |
| `GET /v1/sessions` | listSessions | 无；query 见下文 | 200，Page<Session> |
| `GET {session}` | getSession | 无 | 200，Session |
| `POST {session}/resume` | resumeSession | 无 | 200，Session |
| `POST {session}/archive` | archiveSession | 无 | 200，Session |
| `POST {session}/inputs` | sendPrompt | SessionInput 本身 | 200，InputReceipt |
| `POST {session}/interactions/{interaction64}/answer` | answerInteraction | InteractionAnswer 本身，可为 null、标量、数组或对象 | 200，InteractionReceipt |
| `POST {session}/cancel` | cancel | 无 | 200，CancelReceipt |
| `GET {session}/events` | listHistoryEvents | 无；query 见下文 | 200，HistoryPage |
| `GET {session}/events/stream` | subscribeEvents | 无 | 200，SSE |

`interaction64 = base64url(UTF-8(InteractionRef))`，编码与校验规则同 sessionId64。例如原值 `a/b` 编码为 `YS9i`，原值 `..` 编码为 `Li4`。答案不包 `{answer: ...}`，不得由 HTTP 层把 null 当作缺少请求体。回答是否有效由原交互合同校验；过期或重复回答不能提交给新运行。

创建返回的 Location 使用名称路径，使调用者可以按创建时已知名称核对资源；响应仍包含跨 Runtime 唯一的 status.sessionId。列表可按 namespace、runtimeRef、state 筛选，after、limit 在 HTTP query 中平铺，映射到 SessionQuery.page；默认跨所有 namespace、Runtime，包含离线和归档会话。

本版本不提供 Session PUT/PATCH/DELETE、配置选项写入、批量命令或任意事件注入。归档保留持久身份与历史，不能作为删除别名。

## 回执、超时与重试

201 只在创建的 Service 成功保证达成后返回。200 的输入、回答和取消响应表示完成该方法定义的回执，不表示 Agent 工作完成；保留 InputReceipt.acceptance 的缺失/false/true 区别。归档和 Runtime 停止成功仍以各自 Service 的资源释放保证为准。

本版本不使用 202、Operation 资源、后台轮询句柄或 Idempotency-Key：现有 Service 未定义对应合同。请求 ID、追踪 ID、Session 名称与事件 UUID 均不代替输入幂等键。收到 `Idempotency-Key` 请求头时返回 400 invalid_argument，避免调用方误认为已启用去重。

客户端断开、代理超时或 HTTP 请求截止不撤销已接纳操作，不自动取消 Agent 工作、归档 Session 或释放资源责任；显式取消使用 cancel 接口。服务端不能因连接断开就把原操作视为确定未执行或重新派发。

有副作用的请求超时或连接中断时，客户端不得自动重发。HTTP 能确认未派发时可返回 deadline_exceeded；一旦已派发且无法确认结果，返回 outcome_unknown。没有收到可验证的响应时，客户端也必须按结果未知处理。若原操作后来完成，可在资源查询中观察其可证明状态；资源存在或状态变化不一定能确认某次输入是否接纳。没有公开操作查询与业务幂等合同前，不承诺未知输入可以安全重试或跨服务重启查询。

## 历史与分页

`GET {session}/events` 将 after、limit、types 映射为 HistoryQuery。多类型使用重复参数，例如 `?types=agent.message&types=agent.tool_result`；不使用逗号拼接、JSON 数组或 `types[]`。单个空值与未知事件类型拒绝；省略 types 表示全部类型。类型集合按共享合同消除重复和顺序差异，实时类型合法但没有持久记录。

Session 列表返回 `{items, next?}`，历史返回 `{events, next?, syncedEventId}`；空列表仍为 200，不能改成 204。after 是上次响应的 next，省略从头开始；next 缺失表示本次读取视图无后续匹配记录，不表示 Agent 已结束。不同分页类型的游标不能互换。

保留 [HistoryQuery/HistoryPage](../shared/history-models.tsp) 的过滤、读取视图、游标与同步语义。事件 UUID 不排序、不作 after，syncedEventId 不代表上游或业务消费已经追平。查询不激活 Runtime、不恢复 Session、不执行处理器。分页读取不是实时流的自动续传机制。

## SSE 实时观察

SSE 是可选的实时观察通道，传递统一 SessionEvent；工作输入、回答与取消始终通过独立 HTTP 请求。调用方可以只轮询状态与历史而不建立 SSE。

### 建立与编码

1. 服务器先完成认证、授权、目标解析与 Service 订阅登记；失败返回普通非 2xx OperationError JSON，不发送 SSE 响应头。
2. 订阅登记后返回 200、`Content-Type: text/event-stream`、`Cache-Control: no-store`，禁用代理缓冲；发送并 flush 首帧 `subscription.ready`，data 为 `{}`。此帧表示该订阅已经生效，其后按订阅顺序交付登记之后由 Service 发布到该会话观察通道的事件，不按命令接纳时间或原生事件时间筛选。准备首帧期间的事件必须有界保留，溢出明确异常结束。
3. 每个业务帧使用 `event: session.event`，data 为一个完整、单行 JSON 编码的 SessionEvent，不增加业务包装，不把 type 改成 SSE 专属值。控制帧不属于 SessionEvent，也不进入历史或处理器。
4. 空闲时至少每 15 秒发送一次 SSE 注释心跳；心跳不证明 Agent 健康或工作完成。部署须关闭流缓冲并使代理空闲超时大于心跳间隔。

```text
event: subscription.ready
data: {}

event: session.event
data: {"eventId":"...","namespace":"demo","sessionName":"task","sessionId":"...","type":"agent.message","time":"2026-10-20T10:00:00+08:00","itemId":"...","payload":{"messageId":"...","content":[{"type":"text","text":"完成内容"}]}}

```

上例仅展示帧形状，省略号不作为合法 UUID 示例。实际 data 必须符合 TypeSpec 事件约束。

### 结束与丢失

- Service.next 正常返回 void 时，服务器发送 `event: subscription.closed`、`data: {}` 并关闭连接。它只表示观察结束，不代表工作完成或成功。
- Service 返回 OperationError、序列化失败、慢消费者溢出或服务器关闭导致交付中止时，尽可能发送 `event: subscription.error`，data 为 OperationError，然后关闭。响应头已发送后不再改 HTTP 状态码。网络已断或队列不可写时不能保证错误帧送达；客户端未读到明确结束帧就遇到 EOF，必须作为异常断线处理。
- 客户端关闭连接只 close 本订阅并释放等待和缓冲，不取消工作、归档 Session 或停止 Runtime。慢订阅者不得通过无限缓冲影响核心事实提交。
- 不发送 SSE `id` 或 `retry` 字段；eventId 仍在 JSON 中。拒绝非空 Last-Event-ID 和所有续传 query 参数，返回 400 invalid_argument，不暗示支持 SSE 恢复。
- 客户端不得把自动重连视为连续交付。新连接是新订阅，会错过断线期间的预览与其他实时通知；只有已提交的持久事实可另查历史。MASS 不从 delta 拼造完整历史。

需要在提交输入之前观察其进展时，先等 subscription.ready，再发送输入；发送成功回执与 SSE 事件之间没有跨连接先后保证。idle、归档或一项交互结束均不自动关闭 SSE；订阅生命周期以 Service 的 next/close 为准。

## 错误与 HTTP 状态码

OperationError 的 code 是稳定分类，reason 是具体原因，message 是脱敏说明。HTTP 不解析 message，不丢失或重写 Service 返回的 code/reason。下表规定已知 code 的 HTTP 映射，不封闭或重新定义 Service、Runtime 与 Store 的错误分类；未知 code 作为 500 返回原值，不能被解释为可重试。HTTP 入口产生的参数、认证和协议错误使用表中的对应分类。

| code | HTTP | 含义 |
|---|---|---|
| invalid_argument | 400 | JSON、字段、query、游标、交互答案或参数无效 |
| unauthenticated | 401 | 缺少或无效凭据；附 `WWW-Authenticate: Bearer` |
| permission_denied | 403 | 凭据有效但目标或操作不在授权范围 |
| not_found | 404 | 路由或目标资源不存在 |
| already_exists | 409 | 名称或唯一身份冲突 |
| conflict | 409 | 当前状态、在途操作、引用或交互结算阻止操作 |
| unsupported | 422 | 已识别的业务操作或类型能力不支持 |
| resource_exhausted | 429 | 接纳容量、请求体/参数容量或速率限制 |
| unavailable | 503 | 服务、依赖或订阅当前不可用，且未产生本次副作用 |
| deadline_exceeded | 504 | 请求截止，已确认本次操作没有派发副作用 |
| outcome_unknown | 500 | 已派发操作的副作用结果无法确认；禁止自动重试 |
| internal | 500 | 其他内部错误，不宣告安全重试 |

HTTP 特有错误也使用 OperationError。错误方法返回 405，附 Allow，code=invalid_argument；不支持的 Content-Type 返回 415，code=invalid_argument；Accept 显式排除响应媒体类型时返回 406，code=invalid_argument。请求体超出部署限制返回 413，code=resource_exhausted；上表的 429 用于其他接纳容量或速率限制。认证应先于资源存在性检查，防止未认证查询泄露资源；同一请求有多项错误时不保证具体报错次序。

支持的业务动作必须按上述 Service seam 处理。路径不存在不得返回伪造成功，unsupported 不用来隐藏缺失实现。错误响应不能携带秘密、原生堆栈或未经脱敏的提供商错误。400/409/422 是请求或前置条件问题，不自动重试；429/503/504 也不能越过“副作用确定未派发”检查，仅在有此证据时才可按退避策略重试。代理生成的 5xx 不构成未派发证据。

## 认证、部署与健康探针

受保护的 API 使用 `Authorization: Bearer <token>`；不把 token 放入 URL、query、SSE data 或日志。非本机 HTTP 访问必须使用 TLS，可由受信反向代理终止。客户端可用支持请求头的流式 HTTP 客户端接收 SSE，不要求使用不能设置 Authorization 的原生 EventSource。

HTTP v1 面向单一信任域：有效 token 授予本实例全部 Runtime、Session、历史与操作的访问权限，不提供按用户、namespace 或资源拆分的权限。Runtime、Session、历史与订阅的全部路由均先校验该实例权限，名称路径与 ID 路径一致。namespace 只是分组，不能当作租户隔离；互不信任的调用方必须使用隔离实例。细粒度授权需要后续先定义 Service 的授权上下文、过滤与游标合同，不由 HTTP 在一页查询后过滤来伪装实现。

认证的 token 签发、校验与轮换由部署提供。本版本不增加账号或 ACL 管理 API；未配置认证的实例只允许显式的本机单用户模式，监听回环地址且不接受远程访问。仅绑定回环不能替代多用户环境的身份隔离；本机模式按单一受信用户授权，不支持跨用户服务。CORS 默认关闭，浏览器跨源接入需由部署显式配置允许源与请求头，不能把 CORS 当成认证。

| 路径 | 响应 | 判定 |
|---|---|---|
| `GET /livez` | 200，`{"status":"alive"}` | HTTP 进程仍能响应；不检查模型与 Runtime |
| `GET /readyz` | 就绪时 200，`{"status":"ready"}`；否则 503 OperationError | 完成必要初始化且入口正在接纳 API 请求；关闭接纳后不就绪 |

探针只提供上述最小状态，可由部署在受控网络免认证；不泄露配置或资源清单。GET 以外的方法为 405。readyz 不因尚未创建 Runtime、某 Runtime 停止或模型不可用而默认失败；具体 Runtime 健康由其资源 status 查询。就绪不证明存储每次读写、全部 Agent、认证凭据或模型实际可用。

## 当前边界与验收

HTTP v1 确定以上路由、JSON、状态码、授权入口与 SSE 形式，不引入另一套 Service 生命周期。以下能力不包含在本版本：公开 Operation 查询、跨重启操作结算查询、客户端幂等键、断线连续事件交付、待决交互补查与 webhook。交互请求是非持久实时通知，低频或晚订阅客户端可能只看到 requires_action 而缺少请求内容；本版本不承诺断线后恢复完整交互界面。需人工交互的客户端必须持续观察，并接受这一限制；后续能力须先扩展对应 Service 合同。

| 验收场景 | 应观察到的行为 |
|---|---|
| 只用普通 HTTP 提交后断开 | 工作不依赖连接，之后可查询状态与已提交历史 |
| 通过 ID 与名称操作同一 Session | 相同身份、权限、回执与业务效果，无重定向重发；opaque ID 经 base64url 无损还原 |
| 创建 Session 成功 | 持久身份已提交，Location 可查询，不自动发送 prompt |
| PUT Runtime spec 后查询 | 只改变期望配置，旧运行及其 appliedGeneration 不被伪造更新 |
| 输入或取消成功 | 仅返回对应回执，不宣告工作完成；未知结果不自动重发 |
| types 重复参数与分页 | 精确 OR 过滤，游标条件校验，返回 next 与 syncedEventId |
| SSE ready 后发送输入 | 登记之后 Service 发布的事件按序传递，不要求回执先于事件 |
| 慢订阅者、EOF 与重连 | 有界、异常明确；新流不伪装成连续历史 |
| 缺少凭据或无效 token | 拒绝全部资源与订阅路由，不因名称/ID 别名绕过认证；有效 token 作用域为整个实例 |
| 只有 livez/readyz 成功 | 不被解释为真实 Agent 或模型验收通过 |

[^claude]: 参考 Session 资源下分别发送、查询和订阅事件的 HTTP 组织方式。
[^openai]: 参考持久 Session 与异步执行独立于单次请求的组织方式。
