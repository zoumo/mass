# Go HTTP v1 验收

日期：2026-10-20（Asia/Shanghai）。规格为
[harness HTTP v1](../../harness/bindings/http.md)，接口知识包 v0.2.0。
本次改造 Go HTTP、客户端/CLI 及必要的核心回执与错误值，不修改 harness 合同。

## 变更与边界

- 删除实验 HTTP 操作/观察入口，17 个 Service 操作映射到 REST；创建 201 + Location，删除 204，其余成功直接返回 JSON 值。
- Runtime 与 Session 的创建只接受可写 metadata/spec；Runtime PUT 只替换 spec。两类 Session 路径共用 Service，不重定向副作用请求。
- Session ID 和交互引用使用规范 base64url 路径编码，JSON 保留原值。输入、回答和取消分别使用核心定义的回执；取消 target 关联接纳时的工作。
- 普通 JSON 与 SSE 共用动态值检查，保留大数精度；拒绝重复键、未知固定字段、只读字段、非法 query 和媒体类型。五类 ContentBlock 均能无损通过 HTTP codec；现有原生 adapter 执行文本输入，合法但不支持的媒体在原生派发前返回 422。
- 显式本机单用户模式或实例级 Bearer 认证；直接远程监听要求 TLS。客户端包括自定义 http.Client 均不自动跟随重定向，不添加业务重试。
- SSE 登记后 ready、按 Service 发布顺序交付、注释心跳、显式正常/异常终止、有界过载、写入截止；HTTP 断开不撤销已接纳工作。
- 请求体读取有 10 秒截止。失败或早期拒绝在有效截止下关闭 body，避免 net/http 隐式 drain 使响应或关闭阻塞；完整读取后清除截止，不传给 Agent 工作。

## 验收矩阵

| 层次 | 证据 | 验证内容 |
|---|---|---|
| HTTP 协议集成 | transport/TestRESTRoutes、TestStrictHTTPBoundary | 17 操作、名称/ID 别名、直接 JSON、201/204/Location、字段和 query 拒绝、旧路由不存在 |
| JSON 与引用 | TestOpaqueRefsAndAnswers、TestResponseRejectsDynamicCoercion | 特殊字符 ID、null/标量/数组回答、精确大数、五类内容块、拒绝隐式语言对象转换及非法 Base64 |
| 认证与错误 | TestAuthenticationMediaAndProbes、TestErrorMappingAndDisconnectOwnership | 认证先于目标解析、Bearer/本机模式、探针、状态码和 code/reason 保留、已派发 context 不随客户端断开取消 |
| 真实 TCP 边界 | TestBodyReadHasIndependentDeadline、TestSlowBodyCannotBlockErrorOrShutdown、TestEarlyAuthenticationFailureDoesNotDrainBody | 设置/清除截止，停发 body 后收到 504，早期拒绝立即 401，服务关闭不因 drain 阻塞 |
| 客户端协议 | client/TestFramingSuite 与新增 client 测试 | 只有明确 closed 正常 EOF；错误保持；Close 释放 Next；忽略心跳；Token、REST 路由、大数/null 与自定义客户端禁止重定向 |
| 实际产品入口与 TLS | vertical/TestHTTPAuthenticatedDaemon | 启动实际 daemon、校验测试证书与 Bearer、无凭据的两个寻址别名和流均拒绝、直接 JSON、探针、非法访问模式启动失败及监听端口释放读回 |
| 双 Agent 合成端到端 | vertical/TestEntrypointSuite | 实际 massctl → REST/SSE → Service → 文件 Store → 固定 SDK/mock 子进程；无输入创建、文本输入、权限/问题回答、取消、归档/恢复、历史/实时事实一致与目录分页 |
| 合法但不支持的媒体 | TestCodexUnsupportedMedia、TestClaudeUnsupportedMedia | 媒体返回 unsupported，随后首个文本输入仍能执行；idle cancel 返回 noActiveWork 且没有 target，归档后的观察明确 unavailable |
| 故障与完整历史 | vertical/TestFailureSuite | 慢订阅明确 resource_exhausted；140 条完整事实仍持久可读；取消不宣告终止、原工作隔离、未知责任和释放规则继续成立 |

## 独立审阅

按 code-review 的两个轴分别审阅，均只读：

- Standards：发现请求体读取可能无限等待，以及超时后 net/http 隐式 drain 的后续风险。已修复；审阅 agent 独立执行三项请求体/TCP 测试通过，复核无剩余可靠性阻塞。
- Spec：修正订阅不可用/过载和不支持媒体的核心错误分类，SSE 保留 code/reason，补齐 Base64 校验并统一输出检查。复核无剩余实质阻塞。

审阅是 agent 的代码与合同复核，不是人工批准或真实模型验收。

## 命令与证据

最终检查使用仓库入口：

```sh
rtk proxy make check-go
rtk proxy python3 mass-rs/scripts/check_docs.py
rtk git diff --check
```

最终结果：make check-go 退出码为 0；文档检查通过（39 份文档、198 个内部链接），git diff --check 通过。73 个源码/fixture/依赖输入和 53 个 harness 文件的验证前后摘要均一致。

make check-go 包含 Go vet、全量 race 测试、massctl/massd 构建和 gofmt 检查。
本机原始输出及验证前后源码 SHA-256 清单位于
`.scratch/http-v1-20261020/`，包括 check-go.txt、source-before.json、
source-after.json、harness-before.json 和 harness-after.json。源码摘要覆盖 Go 源码、
JSON fixture、go.mod/go.sum 与根 Makefile；校验前后必须一致。
保留首轮发现旧测试仍依赖错误描述文字的失败输出 check-go-initial.txt；
该断言已改为检查稳定 code/reason，最终结果以修正后的完整检查为准。

## 未验证与未实现范围

双 Agent 端到端是合成验收：使用真实固定 SDK 和严格 mock CLI，不调用账户、
模型或真实工具。原生 CLI/账户/模型、真实持久恢复和 Linux 本次未验证，
Rust 实现未改造或验收。TLS 进程测试使用自有测试证书和凭据，证明本机产品
入口的认证/传输行为，不证明特定远程代理部署。

幂等键、Operation 查询、待决交互补查、webhook、连续续传和细粒度权限不在
HTTP v1 合同中。文件持久化的原有实验边界继续适用；没有新增 SQLite 或
跨重启未知操作自动结算保证。
