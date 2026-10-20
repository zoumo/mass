# Huma v2、chi 与 OpenAPI 客户端生成调研

调研日期：2026-10-20。状态：方案建议与隔离验证，尚未接入产品。本文是 Go 实现调研资料；设计仍以 [HTTP 合同](../../harness/bindings/http.md)与 [ADR-0001](../../harness/adr/0001-defer-http-capacity-limits.md)为准。

建议采用 **chi v5 + Huma v2 + 项目统一注册脚手架 → OpenAPI 3.1 → 生成客户端**。Huma 负责 HTTP 类型绑定、路由声明和 OpenAPI 生成；chi 负责路由与中间件。Service、Runtime、Store 保持现有职责。生成客户端处理普通 HTTP；MASS 的 SSE 订阅状态、异常 EOF 与关闭语义由薄封装补齐。[Huma 路由适配](https://huma.rocks/features/bring-your-own-router/)、[OpenAPI 生成](https://huma.rocks/features/openapi-generation/)。

## 调研基线

当前 Go 模块使用 Go 1.26.9，尚未依赖 Huma 或 chi；HTTP 实现在 [pkg/transport](../pkg/transport/server.go)，客户端在 [pkg/client](../pkg/client/client.go)。实现仍有旧 `/v1`、Runtime `/spec`、Session ID base64url 以及 1 MiB 请求体上限，与本次更新后的规范存在差距。接入工作应按当前合同迁移，不能直接把旧实现生成的文档当作新规范。

通过 Go module proxy 核对并下载源码后，本次服务端验证使用 Huma **v2.39.1**、chi **v5.5.0**；各自 go.mod 声明 Go 1.25.0、1.24，当前项目工具链满足要求。版本是本次可复现验证输入，不表示项目必须永久固定该版本。[Huma go.mod](https://github.com/danielgtaylor/huma/blob/v2.39.1/go.mod)、[chi go.mod](https://github.com/go-chi/chi/blob/v5.5.0/go.mod)。

## 脚手架职责

建议在现有 Go 模块内部建立 HTTP 专用脚手架，先服务 MASS 自身，不先抽成通用框架或增加第二个服务端实现。

```text
harness 的 HTTP / TypeSpec 合同
        ↓ 实现并核对
Go Service 值类型 + HTTP 输入输出类型 + 路由注册
        ↓ 同一注册过程
Huma OpenAPI 3.1 JSON/YAML
        ↓ 固定生成工具版本
客户端生成代码 + MASS SSE / 错误 / URL 薄封装
```

脚手架包含四个入口：

1. 创建 chi router 与 Huma API，统一安装认证、媒体类型、缓存头、严格 query/JSON 校验和错误转换。
2. 注册全部业务路径，包含全局 ID 与 namespace/nameOrID 两组 Session 路径；operationId 显式固定且唯一，例如 `getSessionByID`、`getSessionByNameOrID`，不能依赖生成的名称随重构变化。
3. 注册/补充 JSON schema，特别是 SessionEvent、ContentBlock 联合类型、可空字段和动态 JSON；源运行值仍使用统一的领域类型。
4. 提供离线 OpenAPI 导出命令，只构造注册表，不监听端口、不读取个人数据、不启动 Runtime 或模型。可以沿用已有 Cobra 命令结构；不必为了 Huma 再迁移到 humacli。Huma 官方示例同样通过 `api.OpenAPI().YAML()` 导出而不启动服务。[导出示例源码](https://github.com/danielgtaylor/huma/blob/v2.39.1/docs/docs/features/cli.md)。

路由在 OpenAPI 的 `paths` 中完整写入 `/apis/v1`，`servers` 只提供 origin/base URL；避免客户端重复拼接前缀。导出以 3.1 为主，只有明确需要的旧生成器才使用 `Downgrade()` 产生 3.0.3 派生产物，并单独验证 null/union 语义。Huma 可提供在线 `/openapi.json`、`/docs`、`/schemas`，但离线生成并不要求开放它们；是否公开文档不是本次接入的前置条件。[配置与降级](https://huma.rocks/features/openapi-generation/)。

## Huma 默认行为与 MASS 合同的差异

下表基于 v2.39.1 源码和本地隔离验证。可配置不等于默认满足合同。

| 事项 | 已观察到的默认行为 | 建议脚手架处理 |
|---|---|---|
| 请求体大小 | 超过 1 MiB 返回 413 | 每个 operation 明确 `MaxBodyBytes: -1`；零值会启用默认上限，遵守 ADR-0001 |
| 请求体读取时间 | 默认 5 秒，配置零值也恢复默认 | 不隐式带入框架限制；本方案设 `BodyReadTimeout: -1`，服务器/代理已有超时另行核对 |
| 成功对象 | 默认 create hook 可注入 `$schema` | 移除默认 schema-link create hook；OpenAPI schema 仍正常生成 |
| 参数/结构校验错误 | 默认 422、`application/problem+json`，附字段值 | 框架错误转换成脱敏 `{code,reason,message}` 和约定状态；业务 `unsupported` 仍是 422，不能无差别重写 |
| 未知 query | 默认忽略 | 启用 `RejectUnknownQueryParameters`，转换其错误格式与状态 |
| 单值 query 重复 | `limit=1&limit=2` 取第一个 | 脚手架先拒绝重复；数组 `types` 使用 `query:"types,explode"`，再校验合法事件类型 |
| JSON 重复键 | 接受且后值覆盖前值 | 自定义 JSON codec，在任何深度拒绝重复键，包括开放 map |
| 开放 JSON 大整数 | `9007199254740993` 变成 `9007199254740992` | codec 使用 `json.Decoder.UseNumber()`；保留 JSON 原值，禁止先转浮点数 |
| 交互答案 null | 普通 `Body any` 同时接受空 body 和 `null` | 明确 `RequestBody.Required=true`；schema 为开放 JSON，允许字面 `null` |
| 特殊路径 ID | humachi + chi 可把 `a%2Fb`、`a%252Fb`、`%2E%2E` 各解码一次 | 不再手动二次 PathUnescape；按后续确认的合同拒绝完整值 . / ..，其余合法 ID 仍需验证客户端及代理链路 |
| 未知路由/错误方法 | 不能依赖 Huma 覆盖 router 的全部入口 | chi 的 404/405 与认证中间件一并输出 MASS JSON 错误；认证须先于资源存在性检查 |

对应一手依据：[Operation 限制参数](https://github.com/danielgtaylor/huma/blob/v2.39.1/openapi.go)、[默认配置](https://github.com/danielgtaylor/huma/blob/v2.39.1/defaults.go)、[参数与 JSON 处理](https://github.com/danielgtaylor/huma/blob/v2.39.1/huma.go)、[自定义错误](https://huma.rocks/features/response-errors/)、[humachi 参数解码](https://github.com/danielgtaylor/huma/blob/v2.39.1/adapters/humachi/humachi.go)。

`huma.NewError` / `NewErrorWithContext` 是全局 hook，须在初始化阶段设置一次；不能每个请求临时切换。领域 OperationError 应作为自己的 StatusError 返回，保留 code/reason 和对应业务状态。OpenAPI 中也要登记相同错误 schema、媒体类型及状态；只改变运行时 hook 不足以证明生成文档一致。固定结构未知字段、只读字段写入、GET/DELETE body、无输入 POST 的 `{}` 规则、Accept 的 q 值，以及请求断开不撤销已接纳操作，均需继续按 MASS 合同验证。[错误 hook 与注册源码](https://github.com/danielgtaylor/huma/blob/v2.39.1/error.go)。

## 类型与 schema 的单一维护方向

Huma 根据 Go 类型反射生成 schema，但不能从任意 `MarshalJSON` 推断真实 wire union。当前 [SessionEvent](../pkg/events/events.go) 的 payload 是私有字段，通过自定义序列化输出 `type` 和 `payload`；直接反射不会自动得到完整的 17 分支合同。

建议由 HTTP 层的 schema registry 显式描述 `oneOf + discriminator(type)`，复用现有分支字段类型，并补齐 required、nullable、const/enum、readOnly 与开放 map；ContentBlock 同样处理。通过注册表覆盖或 `RegisterTypeAlias` / HTTP 专用 SchemaProvider 描述序列化，不让领域模块为了生成文档依赖 Huma，也不再创建第二套运行时事件模型。[Registry 扩展接口](https://github.com/danielgtaylor/huma/blob/v2.39.1/registry.go)、[SchemaProvider](https://huma.rocks/features/schema-customization/)。

创建输入应复用字段定义、只暴露可写 metadata 与 spec，避免 readOnly 字段被默认忽略而不报错。OpenAPI 是实际路由注册的生成产物；harness TypeSpec 仍是业务合同，需通过既有事件示例、schema 校验和合同场景检查防止漂移。当前没有验证 TypeSpec → Go → Huma 的完整自动生成链路，因此不把这条额外工具链列为首版前置条件。

## SSE 的接入方式

建议使用普通 `huma.Register` 返回 `*huma.StreamResponse`：先完成权限检查、目标解析和 `SubscribeEvents`，失败时返回普通非 2xx JSON；成功后才由 Body 回调写入 SSE 响应头、ready、业务帧、心跳及终止帧。[StreamResponse](https://huma.rocks/features/response-streaming/)。

Huma 的 `sse.Register` 在进入用户发送回调前已经设置 200 并 flush。不能在该回调中才调用可能失败的 SubscribeEvents，否则不满足“订阅建立失败仍返回普通 HTTP 错误”的合同。可以借助前置 resolver/middleware 做准备，但会增加资源清理路径；目前优先复用现有 MASS streaming 生命周期。[sse.Register 源码](https://github.com/danielgtaylor/huma/blob/v2.39.1/sse/sse.go)。

OpenAPI 明确登记 `text/event-stream`、控制帧和 SessionEvent schema；不能把 SSE 描述成普通 JSON 数组后期待通用 SDK 自动产生正确流式客户端。保持下列 wire 规则：

```http
Content-Type: text/event-stream
Cache-Control: no-cache, no-store, no-transform
X-Accel-Buffering: no
```

代理配置属于实现与部署资料：NGINX 可为 SSE 路由显式设置 `proxy_cache off`、`proxy_buffering off`，并发送 `X-Accel-Buffering: no`；该头可能由代理消费，不要求最终客户端仍看到。不能让上层配置重新启用缓存或忽略缓冲控制，须通过实际代理链路验收逐帧交付。[NGINX 代理缓冲文档](https://nginx.org/en/docs/http/ngx_http_proxy_module.html#proxy_buffering)。

客户端封装只消费流：处理 `subscription.ready`、`session.event`、`subscription.closed`、`subscription.error` 与注释心跳，未见终止帧的 EOF 作为异常；关闭订阅释放连接，不取消 Agent，不自动重连、重发输入或把历史分页当作流续传。生成工具不能替代这些已定义的业务语义。

## 客户端生成方案

Go/massctl 首选 **oapi-codegen v2.8.0 的 models + client + MASS 薄封装**。它直接使用 OpenAPI 文件，生成范围小，适合替换当前手写 HTTP 调用；推荐的前提是补齐开放 JSON 保真与特殊 ID 路径处理。若希望默认保留动态 JSON、生成更显式的 union，**ogen v1.24.0** 是有竞争力的备选。

| 候选 | 一手资料与本轮结果 | 判断 |
|---|---|---|
| oapi-codegen v2.8.0 | 已支持初始 OpenAPI 3.1；含 const、nullable 和 discriminator 的代表性样本生成编译通过；原始 HTTP 方法可暴露 response body | 首选轻量基础；开放 JSON 默认 interface{} 会丢大整数精度，需要类型 override / codec |
| ogen v1.24.0 | 同样样本生成编译通过；oneOf 为 sum type，开放 JSON 为 jx.Raw；实测保留大整数与 literal null | 备选；需控制生成范围与 SSE 自动重连行为 |
| OpenAPI Generator Go | 官方 3.1 总体支持仍标 beta；Go 模板会读取完整 response body | 多语言统一工具需求明确时再考虑；本轮未执行生成 |
| humaclient v0.3.0 | 从 huma.API 生成；有 typed SSE，但 README 明确不支持 oneOf/anyOf | 不作为主链：不是独立 OpenAPI 文件生成，且当前事件/内容联合类型受限 |
| openapi-typescript + openapi-fetch | 官方支持 3.1；fetch 支持 `parseAs: "stream"` 和请求头 | TypeScript 候选；本轮未执行 TS 生成，仍需 SSE 和数字保真策略 |

来源：[oapi-codegen v2.8.0 release](https://github.com/oapi-codegen/oapi-codegen/releases/tag/v2.8.0)、[ogen 示例](https://ogen.dev/docs/examples/)、[OpenAPI Generator Go](https://openapi-generator.tech/docs/generators/go/)、[OpenAPI Generator 兼容性](https://github.com/OpenAPITools/openapi-generator#11---compatibility)、[humaclient](https://github.com/danielgtaylor/humaclient)、[openapi-typescript](https://openapi-ts.dev/introduction)、[openapi-fetch API](https://openapi-ts.dev/openapi-fetch/api)。

不要依据旧资料先把 Huma 的 3.1 降级成 3.0：oapi-codegen v2.8.0 已落地初始 3.1 支持，本次也实际验证了代表性样本。首版以 3.1 为共同输入，只有工具实测不支持时才讨论降级。建议生成配置如下：

```yaml
package: generated
output: client.gen.go
generate:
  models: true
  client: true
output-options:
  nullable-type: true
```

生成代码独立存放，MASS 客户端封装依赖生成代码；用户输入原始 ID，由封装负责单次编码，不要求使用者手工 escape。Bearer 通过 request editor / transport 注入；禁止副作用自动重试，显式关闭重定向跟随。动态 schema `{}` 不得为了方便生成而收窄成 object；对于允许任意 JSON 的字段，优先用生成配置或 overlay 映射到 json.RawMessage / 保真 JSONValue，不手改生成文件。该 override 的完整产品方案尚未验证。[oapi-codegen 配置与扩展](https://github.com/oapi-codegen/oapi-codegen)。

### 已观察到的客户端差异

独立样本包含 Bearer、两个联合分支、可空可选布尔、开放 JSON 和重复 types；不是全量 MASS schema。

| 场景 | oapi-codegen | ogen |
|---|---|---|
| `a/b`、`a%2Fb`、`a b?#` | 路径分别正确 escape | 同样正确 |
| ID `.` / `..`（后续已确认为非法） | 默认相对 URL 解析会移除路径段；SDK 必须在构造请求前拒绝 | 默认保留裸点号段；SDK 同样必须在构造请求前拒绝 |
| 重复 types | 实测生成重复 query 参数 | 源码采用 form/explode；未单独执行该查询 |
| null 答案 | nil 发送字面 `null` | jx.Raw("null") 发送字面 `null` |
| 动态大整数响应 | 转 interface{} 后丢精度；union 原始 RawMessage 保真但 As 方法仍可能转浮点 | jx.Raw 保留原值 |
| SSE | 原始返回 `*http.Response` 的方法可用；WithResponse 路径 io.ReadAll 等待流结束 | 普通 stream 可得 io.Reader；typed SSE 模板含重连，0 retries 不是关闭而是无限重试 |

因此 SSE 使用生成的原始 response 接口，MASS 自行处理控制帧；不能直接使用生成的“读取完整响应”方法。ogen typed SSE 的重连选项须另行验证，不能把默认无限重连作为 MASS 行为。[ogen v1.24.0 SSE 模板](https://github.com/ogen-go/ogen/blob/v1.24.0/gen/_template/client.tmpl)、[OpenAPI Generator Go 完整读取模板](https://github.com/OpenAPITools/openapi-generator/blob/master/modules/openapi-generator/src/main/resources/go/api.mustache)。

### 点号 ID 的问题已通过合法性规则明确

调研观察到 Node 24.21.0 的 WHATWG Request 会将 `%2E`、`%2E%2E` 作为 dot segment 归一化，部分 Go 生成客户端也会改写这些路径。[WHATWG URL 规则](https://url.spec.whatwg.org/#single-dot-path-segment)。

负责人随后确认：完整原始 sessionId 等于 `.` 或 `..` 是非法值。当前 [Session ID 合法性](../../harness/modules/service/session.md#session-id-合法性)已要求创建/恢复拒绝这类身份，SDK 在构造 URL 前拒绝，HTTP 在单次解码后拒绝；不再要求生成客户端保留它们，也无需为它们设计替代 URL 编码。原有探针记录的是框架默认行为，不能当作修订后 MASS 应接受这些值的证据。

`a.b`、`a..b` 等普通含点号的 ID 仍合法；slash、百分号等合法内容继续按单个路径段 URL escape，并验证端到端无损还原。此项属于身份合法性，与 ADR-0001 暂不设容量限制的决定无关。

## 验证与接入顺序

服务端隔离目录：`.scratch/huma-openapi-research/server-probe/`，独立 go.mod / go.sum，未修改产品依赖。通过 `GO111MODULE=on go test -v ./...` 执行，结果保存在 `probe-results.txt`：

- 对默认与基础配置分别记录 16 个 HTTP 场景，确认路径、query、错误、超大请求、动态 JSON 与 SSE 的实际响应。
- 断言内置 SSE helper 在用户回调之前提交响应；验证 StreamResponse 可在回调前返回普通 404，以及成功流的三个响应头和帧格式。
- 7 个调整后断言通过：合法 null、缺少 body、开放 JSON 大整数、重复键、未知 query、对象内大整数、固定结构未知字段。
- 不监听网络或启动 Agent，导出 3.1、3.0.3 和调整后的 OpenAPI JSON。

客户端隔离目录：`.scratch/huma-openapi-research/client-probe/`。oapi-codegen v2.8.0 与 ogen v1.24.0 已对代表性 3.1 样本完成生成、编译和请求/响应探针；Go runtime 依赖锁定在该目录的 go.mod / go.sum。复现生成命令如下，均在对应隔离目录执行：

```sh
rtk proxy env GO111MODULE=on go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config oapi.yaml openapi.json
rtk proxy env GO111MODULE=on go run github.com/ogen-go/ogen/cmd/ogen@v1.24.0 -target ogen -package ogenprobe openapi.json
rtk proxy env GO111MODULE=on go test -v ./...
```

另在 `.scratch/huma-openapi-research/pipeline-probe/` 完成了实际串联：直接复制服务端验证生成的 `openapi-adjusted.json`（Huma OpenAPI 3.1，两个代表性操作及自定义错误），运行 oapi-codegen v2.8.0，生成客户端编译通过。该步骤没有集成测试，也不包含全量 SessionEvent union；它证明最小生成链路可运行，不能替代完整合同验收。

这些测试是框架与生成工具可行性证据，不是 MASS 全部路由或真实 Agent 验收。仍需验证完整 schema、全部 OperationError、真实代理逐帧交付、客户端取消、并发订阅、权限顺序与已接纳工作的生命周期。

建议按以下顺序接入：先建立无副作用的 Huma 注册和导出入口，再完成统一 codec/错误/参数规则和事件 schema；用一个 Runtime 操作、一个含 null/动态值的交互操作、一条历史查询和一条 SSE 跑通客户端，再迁移全量路由。CI 检查生成可重复、schema 与实际响应一致、客户端编译与往返场景通过；生成文件不手工修改。

本次新增调研文档与隔离证据；未修改 harness、产品服务端/客户端或产品 go.mod，未宣告框架接入完成。
