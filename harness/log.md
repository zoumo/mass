# 设计知识包更新记录

## 2026-11-07

- **冷重启接管 unknown Runtime**：经负责人确认，服务启动仍将缺少结算证明的 Runtime 保留为 unknown；冷进程没有当前运行映射时，显式 start/resume 可建立新 Driver，并把旧本地 owner 视为不可达。该接管不改变启动校正事实，也不证明旧 provider 工作或后端资源已经结束；同一进程已有运行绑定或未结算责任时仍拒绝重建。同步 Service、Runtime、总体架构与合同入口，接口修订为 v0.2.19。

## 2026-11-06

- **空闲 Session 自动 close 与按需启动**：经负责人确认，删除 Session archive/archived，统一使用 close/offline。close 从任意状态接纳并立即阻止新 prompt、answer、cancel、resume；需要调用 SDK 时，SDK Session close 成功是唯一 native 成功条件，随后仍须结算 MASS 已接纳交付并进入 offline。失败或结算未知保持 closing 且不自动重试，重复 close 幂等并可从 closing 显式重试；无句柄 unknown 的 close 与已 offline 重试按例外规则直接结算。服务启动不启动 Runtime 或 Session，旧在线状态校正为 unknown，旧 archived 记录迁移为 offline；create/resume 按需启动 Runtime，prompt/answer/cancel 不隐式恢复。新增 daemon `sessionIdleTimeout`，默认 10m、0 禁用、负数或无效值拒绝，只自动 close 已确认 idle 的 Session，不停止 Runtime。同步 Service、Runtime、事件、HTTP、CLI、持久化、词汇、架构与实现差距记录，接口修订为 v0.2.18；本次仅更新 harness 与根 `GLOSSARY.md`，Go 实现未修改。

- **记录 Claude block 条目身份草案**：按负责人 2026-11-06 讨论，新增 draft 合同，明确 thinking/text 使用 `message.id` 与 content block 索引生成 `itemId`，tool 使用原生 `tool_use.id`；chunk 与完整事件共用 `itemId`，客户端按 item 闭合。未修改 TypeSpec 或实现。

- **增加会话功能开关**：经负责人授权，DriverSessionSpec 增加可扩展的 features，首个开关 agentQuestions 默认 true；DriverSession 增加 effectiveFeatures，SessionStatus 增加实际生效值。请求开启但 Runtime 不支持时降级为 false，不返回错误；默认值与具体 Runtime 是否支持无关。两个 Driver 分别使用 Codex thread 级 config 与 Claude 的 AskUserQuestion 禁用参数实现，接口修订为 v0.2.17。

## 2026-11-01

- **显式新输入恢复**：经负责人授权，SessionResume 增加可选 initialInput，SessionResumed 返回临时回执；省略保持零输入，输入与回执不进入持久恢复配置。Claude 在验证保存身份/cwd 并定向原会话后可先提交新输入，再等待同一原生 ID/cwd；成功与事实发布仍受实际身份及持久提交门控。明确早到完成、未知业务效果和原所有者取消清理；同步 HTTP、CLI 与可选 Runtime 能力，接口修订为 v0.2.16。

- **显式首条输入创建**：经负责人授权，SessionCreate 增加 create-only initialInput，SessionCreated 返回可选回执；明确同一所有者、先登记工作后提交、真实身份、有界早到事实、持久提交后发布、未知结算和恢复不重放。CLI 增加显式文本或 SessionInput JSON 文件，省略仍零输入；保持 Claude 本地写入与 Codex 原生接受证据区别。接口修订为 v0.2.15。

## 2026-10-22

- **精简 harness 工作规则**：合并 AGENTS.md 中重复的设计权威、实现记录、文档状态和验证要求；模块职责与动态值细节改为引用权威合同。确认 Go 原则统一使用 principles/golang，保留历史记录中的原路径。

- **采用 harness-agent 规范**：将 Go 原则目录迁到 principles/golang，区分应用与工具库目录规范，补充 SDK 规则。保留已有 principles/AGENTS.md，缺失测试前置条件报告未执行，内部引用同步更新。

- **合并知识管理约定**：将 conventions.md 的知识组织、OKF 元数据、来源、接口表达及文档维护规则合并到 harness/AGENTS.md，删除原文件并同步入口和 Module 规则引用。

- **词汇表移至根目录**：将 harness/glossary.md 正文迁至根目录 GLOSSARY.md，替换原跳转页；同步导航、内容归属说明与词汇表内部链接。

- **合并重复工作规则**：设计权威、写入授权和冲突裁决统一维护在 harness/AGENTS.md；conventions.md 保留知识组织与文档维护约定，通过链接引用工作规则。

- **规范 Module 设计文档内容**：在 modules/AGENTS.md 维护目录规则；Module 设计正文、专题页与 TypeSpec 注释只维护设计合同和验收要求，不承载实现差距、进度、验证结果或任务执行范围，也不添加差距记录导航。上层约定和入口只保留规则链接，差距记录入口统一保留在索引和工具入口；同步清理 Module 页面中的相关链接与执行范围说明。

- **收口 Module 职责与内部协调**：按负责人逐项确认，EventStore 正常追加自动同步 SQL，失败后在后续同步触发时从已验证消费位置续接，调用方无需先恢复或重新打开；保留原 ID 幂等、查询不静默漏项及不补广播。RuntimeRegistry 仅校验配置与构造 Driver，当前运行映射由 Service 独占；完整停止流程由 Service 内部的同一操作拥有，组装层只注入依赖，删除固定的多步协调 TypeSpec，保留最终事件结算和未知责任保证。接口修订为 v0.2.14。
- **集中实现差距记录**：Module 正文维护当前合同与验收要求，实现差距集中到 implementation-gaps.md，绑定检查日期、源码版本和静态证据，更新记录只保留设计历史。状态投影的已讨论限制暂时接受，现有状态存储与异步 Handler 合同不变；本次只更新 harness，不修改实现。

## 2026-10-21

- **Runtime 按需启动与重启状态校正**：按负责人确认，创建/恢复 Session 自动启动所属 Runtime，与显式 start 共用同一次串行协调；查询、历史、订阅不激活，停止栅栏与未知释放仍阻止重建。服务启动不恢复 Runtime 或 Session，开放业务接口前由 Service 校正持久状态：确认释放的普通 Session 为 offline，显式归档的保持 archived，旧在线状态缺少结算证明的为 unknown；Runtime 保留已确认的 stopped，否则为 unknown。同步架构、Service/Runtime、状态注释、HTTP/massctl 和验收场景，接口修订为 v0.2.13；本次仅更新 harness，Go 实现未修改。

- **load 改为恢复加回放**：按负责人确认，load 恢复同一原生会话并异步回放全部保留主历史，成功保留执行连接，本地提交/分发结算后直接接受后续输入，不再要求再次 resume。普通 resume 保持不回放；补充 nativeResumed、回放输入屏障、成功运行与失败清理的区别、未知恢复结果保留占用及 completed 后旧取消拒绝。同步 Runtime/Service/HTTP/massctl 合同，接口修订为 v0.2.12；MASS 只更新设计。

- **异步加载原生保留历史**：新增显式 load/cancel-load 合同；先建 Service 接收绑定，再沿既有 adapter → Service → EventStore → Broadcast 管线回放。接纳、原生读取结束与本地提交/分发完成分别表达；历史带 replay 来源，状态和旧交互不复活。首版只允许新的空目标并拒绝重复/部分重试，保留失败及原 UUID 追加责任。普通 resume 不重放。同步 Runtime/Service/事件、HTTP/massctl 与 TypeSpec v0.2.11；仅设计，MASS 未实现、未升级 SDK。

- **订阅归 Broadcast、顺序 Handler 与语言映射**：按负责人确认，Broadcast 提供 subscribe 内部创建单队列 Handler，Service 只委托；移除公共 removeHandler 和 Handler name/order，普通 Handler 随 Broadcast 关闭，订阅取消只解除自身内部登记。保留只读 required，严格按加入顺序串行；required 失败停止当前事件后续调用，普通失败继续，不调整顺序、不回滚或自动重试。Subscription 通用合同为可取消异步流，表达接收、结束和终止原因；独立 Go 语言映射使用只读 channel、Err 和 Close。接口修订为 v0.2.10；先校验 harness，再同步 spec/tickets，当前不实现。

- **简化为纯实时 Broadcast**：负责人要求去掉 EventDelivery、运行关联和过度设计。发布与 Handler 直接使用统一 SessionEvent；Service 按 sessionId 或 namespace/name 定位同一 Broadcast。接口收敛为 publish/addHandler/removeHandler/close，Subscription 由 Service 登记一个队列 Handler 后返回 next/close。删除来源泛型、上下文、命令 adapter、Handler 分类/排序/提交标记、Factory、resume/drain 与内建历史 Handler。历史由 Service 直接协调 EventStore，先持久提交再实时发布；Broadcast 不管历史。v0.2.9 取代此前 v0.2.7/v0.2.8 的相关处理链合同；先校验 harness 再同步计划，当前不实现。

- **Session Broadcast 与统一 Handler**：按负责人确认，每个持久 Session 在应用进程内一个 EventBroadcast，由 Service 管理身份映射；广播只管理 Handler，Subscription 是专用单队列 Handler 的读取封装，不再维护独立 watcher 或全局 Handler 登记。统一 publish/addHandler/removeHandler/subscribe 等接口；EventDelivery 与 HandlerContext 逐事件传递进程内原来源，Service adapter 按来源验证状态和命令。Runtime 停止/归档保留 Broadcast、健康 Handler 与订阅，最终上游排空后再次 drain；应用 shutdown 才关闭对象和消费者。保留 required 失败屏障与异步状态允许失败；接口修订为 v0.2.8。先校验 harness，再同步本地 spec/tickets；当前不实现。

- **独立 Events 处理与广播模块**：按负责人确认，将 handler、分 Session 处理链、实时订阅、背压和关闭从 Service 移入 Events。内建 EventStore 提交为首个 required，其余 required 按 order 串行成功后才分发；失败暂停该 Session，显式恢复，不回滚已提交历史。async 独立有界串行交付，状态投影采用允许失败的 continue，不阻塞广播。关闭先结算 required/分发屏障，再限时排空 async 并结束订阅，未完成如实报错；Service 保留来源校验、生命周期和命令 adapter。接口修订为 v0.2.7；先更新 harness 再同步 spec/tickets，当前不实现。

## 2026-10-20

- **拆分 MetaStore 与 EventStore**：按负责人确认，模块、接口、生命周期和恢复独立管理，原 HistoryStore 改名 EventStore。MetaStoreConfig 选择唯一权威后端，默认 sqlite，可选 file，不双权威、不自动回退；EventStore 文件权威，SQL 为可重建完整查询副本，文件提交后更新 SQL，append 成功后由 Service 发布。同步架构、词汇、承载和 TypeSpec 入口，接口修订为 v0.2.6。旧双库完整副本计划被替代，新 spec/tickets 暂停执行；本次仅文档和合同变更，不继续实现。

- **确定配置读取选型**：采用 Viper 读取配置，默认配置文件使用 YAML；通过 Cobra flag 绑定统一显式参数、配置文件和默认值的优先级，配置实例独立。

- **确定 HTTP 客户端选型**：采用 Resty 作为 Go 默认 HTTP 客户端，提供官方仓库链接；超时、重试、重定向与流式响应处理遵循所调用接口的合同。

- **保存 Go lint 配置模板**：将当前 Go 实现的 golangci-lint 配置原样复制到 principles/go/templates/golangci-lint.yaml，作为通用 v2 配置基线；技术选型增加模板入口，目录规则明确通用模板归属。项目实际配置保持独立维护。

- **确定日志、lint 与可观测性选型**：采用标准库 log/slog、golangci-lint 和 OpenTelemetry Go，补充通用使用要求；技术栈按领域分节、不使用表格，第三方库和工具均提供官方链接。具体版本、执行命令与部署配置留在实现资料，本次不代表日志或遥测接入已完成。

- **清理原则中的项目关联规则**：逐篇检查 principles，将 agent-workflow.md 的知识读取、写入授权和工作流程合并至 harness/AGENTS.md，删除原页并更新入口链接；测试原则不再依赖 Runtime 领域名，Go 目录与选型规则继续作为通用语言级规范保留。

- **明确 principles 内容边界**：新增目录 AGENTS.md，原则仅维护跨项目可复用的工作、编码、测试与语言级规范；将当前 Go 实现的路径、构建命令和运行数据注意事项从原则正文移至根 AGENTS.md，同步原则索引。

- **修正 Go 命令代码归属**：负责人确认二进制入口放在 cmd/<程序名>/main.go，程序专属命令代码放在 cmd/<程序名>/subcommand/；pkg 保留可复用业务模块。同步 massctl 设计并迁移命令实现，不改变命令行为。

- **确认 massctl 首版范围并启动实现**：配置 flag 首版提供 --executable，其余字段走完整 config；配置 flag/文件/内联 JSON 三种方式互斥，多个字段 flag 可组合，无配置更新为参数错误。不提供完整资源文件创建、completion 或 answer 命令。负责人授权按已确认模块范围开始改造，human 字段和本地错误分类可继续细化，文档保持 draft。

- **确认 CLI 全局约定**：服务地址采用 --url > MASS_URL > 本机 24368，认证读取 MASS_TOKEN；默认 human，JSON 输出完整结果、watch 输出 JSONL，错误走 stderr。分页默认 100、不自动翻页；退出码为 0/1/2/130。普通请求默认超时 30 秒，0 表示不限时，watch 仅限制建连就绪等待；提供 help/version，首版无健康检查和配置管理命令。

- **确认 Session 创建、历史与观察参数**：创建必填 NAME、namespace、runtime、cwd，可选互斥的系统提示文本/文件与可重复额外目录，不发送 prompt；history 首版只保留 --limit/--after，不提供类型筛选；watch 无专属参数，观察订阅后的事件，不补历史、不自动重连，Ctrl-C 只关闭观察。

- **简化 Session 输入范围**：prompt 首版仅接受 --text 文本，构造单个 text 内容块，不提供文件、JSON、多模态输入或隐式 stdin；Service/HTTP 输入合同不变。answer 延后讨论，参数不定稿。

- **Go 规则独立与通用化**：目录结构与技术选型改为项目无关、单文件可读的通用规则，移除 MASS 路径、业务模块示例、项目合同及其他文档引用；保留 cmd/pkg 强制布局和已确认的三项技术选型。

- **建立 Go 工程规则**：按负责人要求在 principles/go 下新增目录结构与技术选型两份规则；强制 cmd 的子目录对应可编译二进制，pkg 的子目录对应模块。技术选型先确定 Cobra 管理 CLI/flag、testify 测试并尽可能使用 suite、Huma v2 + chi 承载 HTTP。同步原则索引与读取入口。

- **收敛 HTTP 规范职责**：HTTP 文档只规定 URL、方法与处理语义、状态码、认证和 SSE 协议；移出内部调用方式、手写 DTO 清单、完整 JSON 示例与具体代理配置。请求/响应 schema 和 OpenAPI 由代码生成；框架选型放入 Go 规则，代理配置保留在实现资料。

- **拒绝点号 Session ID**：按负责人确认，原始 sessionId 恰为 `.` 或 `..` 时非法；同步 Service/Runtime 的创建、恢复、持久身份与引用校验，以及 HTTP/客户端的编码前和单次解码后校验。更新 Huma/OpenAPI 调研结论，这两个值不再是需兼容的合法 ID；普通含点号 ID 仍按原值处理。接口知识包修订为 v0.2.5，本次仅更新规范与调研资料。

- **确认 Session 统一寻址与列表范围**：已有单个 Session 的命令统一使用 NAME_OR_ID，显式 namespace 时按名称，不提供时按全局 ID；无默认 namespace、--id 或查询回退。Session 列表首版不提供筛选，只保留 --limit/--after 分页。

- **确认 Runtime 删除命令**：`runtime delete NAME` 无专属参数，不允许删除正在运行的 Runtime，不自动停止，保留 Service 的 Session 引用与资源释放检查；不级联、不提供 --force、不交互确认。成功时 human 显示提示，JSON stdout 留空，退出码为 0。

- **确认 Runtime 停止命令**：`runtime stop NAME` 不增加专属参数，由 Service 协调排空与释放，成功返回 Runtime 并保留配置、Session 身份和历史；未知结果或超时不宣告停止成功，首版不提供 --force。

- **确认 Runtime 启动命令**：`runtime start NAME` 不增加专属参数，停止状态按最新配置启动，已运行时返回当前资源，不隐式重启；首版不提供 --restart，应用新配置使用独立 stop/start。成功返回 Runtime，JSON 模式输出完整资源。

- **确认 Runtime 配置更新语义**：显式配置 flag 覆盖对应字段并保留其他旧字段；--config-file/--config-json 提供完整配置，直接替换原 config，不递归合并。输入混用和空更新规则继续讨论。

- **暂缓 HTTP 容量限制**：按负责人要求记录 ADR-0001，当前不对请求体大小等 HTTP 容量边界设置强制限制，遇到具体问题再讨论。同步移除 HTTP 合同的请求体超限 413 强制要求，保留实际资源耗尽的错误映射；接口知识包修订为 v0.2.4。本次仅更新决策与合同。

- **确认 Runtime CLI 更新流程**：`runtime update` 先获取当前 spec，保留 type，接受部分配置并合并后提交完整 spec；不提供 --type，不修改 Service/HTTP 的完整替换合同。具体配置合并规则待确认，当前 HTTP 无条件更新保护。

- **确认 Runtime 列表范围**：massctl 首版 `runtime list` 不提供筛选参数，列出全部 Runtime；Service/HTTP 的筛选能力保持不变。

- **确认 Runtime 创建默认行为**：NAME 与 --type 必填，不默认选择类型；配置可省略，此时提交 `{}`，由 Runtime 类型校验并应用默认值。创建只登记资源，启动使用独立命令；配置输入组合与覆盖规则尚未确认。

- **确认 Runtime 创建输入方向**：massctl 使用 `runtime create NAME --type TYPE`，常用配置字段暴露为 flag，预设 flag 不足时推荐 `--config-file` 或 `--config-json`；具体字段与输入组合规则继续讨论，模块保持 draft。

- **Session 名称校验与 SSE 代理要求**：按负责人要求，创建 Session 时拒绝 UUID 形式的名称，由 Service 在副作用前统一校验；原生 sessionId 不限于 UUID，因此保留非 UUID ID 与名称冲突的保护。SSE 明确发送 `Cache-Control: no-cache, no-store, no-transform` 与 `X-Accel-Buffering: no`，部署同时关闭代理缓存及响应缓冲，并通过真实代理链路验收逐帧交付。接口知识包修订为 v0.2.3；本次为合同变更，未实现或运行验收。

- **简化 HTTP 资源路径**：按负责人要求，将 Runtime 配置更新改为 `PUT /apis/v1/runtimes/{runtime}`，请求体仍为 RuntimeSpec；namespace 下的 Session 路径改为 `{nameOrID}`，接受名称或原始 sessionId。两种 Session 路径统一使用 URL escape 与单次解码，补充 namespace 校验、名称/ID 歧义冲突和特殊字符验收；创建 Location 使用无歧义的全局 ID 路径。交互引用仍使用 base64url。接口知识包修订为 v0.2.2，TypeSpec 签名不变；本次只修改规范。

- **起草 massctl 模块**：按负责人要求新增 CLI 能力设计，采用资源、操作两级子命令，列出 Runtime/Session 操作、统一寻址、文件输入、输出与错误、分页和实时观察边界及验收场景。除子命令方向外，具体能力、命名、默认值和进程 I/O 约定保持 draft，待逐项确认；不代表实现完成，不修改 Service/HTTP 合同或 TypeSpec 接口版本。

- **统一 HTTP API 前缀**：按负责人要求，业务 URL 从 `/v1` 调整为 `/apis/v1`，覆盖 Runtime、Session 的 ID/名称寻址、历史、SSE 与创建响应 Location；旧路径不提供兼容别名或重定向。健康探针保持 `/livez`、`/readyz`。同步路径验收要求，接口知识包修订为 v0.2.1；Service/Runtime/Store 的 TypeSpec 签名不变。本次为规范变更，不代表语言实现已完成迁移。

- **定稿 HTTP v1**：按负责人“先起草、由另一 agent 审阅，无问题后写入 harness”的条件授权，经独立 agent 审阅、修订与复核通过，确定资源式 REST JSON API 与可选 SSE，不采用公共 JSON-RPC。明确 Runtime/Session 路由、两种会话寻址、输入/回答/取消回执、历史分页、错误映射、SSE ready/终止边界、实例级 Bearer 认证与健康探针。不透明 Session ID/交互引用的路径片段使用规范 base64url，JSON 中保留原值；错误 code 保持开放。接口知识包修订为 v0.2.0，Service/Runtime/Store 签名未改。幂等键、Operation 查询、细粒度授权、待决交互补查、webhook 与连续续传不在本版；定稿和 agent 文档审阅不代表实现或真实 Agent 验收完成。

- **更新确认状态**：按负责人要求，将已确认的整体架构、模块设计、事件与共享结构、工作原则、词汇及知识管理/合同入口从 draft 更新为 stable。HTTP 与持久化承载保留 draft，页面内的待决事项继续保持未确认；状态不代表实现或运行验收完成。

- **明确设计文档命名**：整体逻辑架构统一为 architecture.md，Service、Store、Runtime 的主设计统一为模块目录下的 design.md；补充整体架构的范围说明，同步索引与仓库引用。TypeSpec 工具使用入口保留 README.md。

- **建立 Harness**：知识目录迁为 harness，作为项目演进与发展的唯一真相源；开发原则归入 principles，增加 agent 工作入口及事先授权的写入规则。同步仓库入口、语言资料与校验工具路径，知识结构遵循 OKF v0.2。

- **整理当前设计**：文档独立描述 Service → Store / Runtime 的职责与依赖；接口和结构以当前 TypeSpec 为准，具体实现与真实 Agent 验收另行验证。
- **名称归属**：Session.spec.runtimeRef 持久保存 Runtime 名称，不保存或比较归属 Runtime 的 uid；停止协调按名称固定原 Driver。原运行、原工作和迟到操作继续按各自关联结算。
- **资源与配置**：Runtime、Session 使用 metadata/spec/status；状态采用 RuntimeState、SessionState，Reason 为开放字符串。Runtime 配置更新为全量替换，不热切换当前 Driver，不提供 JSON Patch。
- **存储合同**：MetaStore 提供资源 CRUD、generation 与 resourceVersion；HistoryStore 直接追加与查询统一事件。文件保存完整数据，SQLite 为可重建索引，准确文件提交协议仍待定义。
- **运行时合同**：RuntimeRegistry 校验配置、构造并登记未启动 Driver、按名称查找和移除；RuntimeDriver 执行实际启停与原生会话操作。每个 Driver 提供一条由 Service 独占消费的多会话事件流。
- **统一事件**：公共信封包含完整 Session 名称与身份，adapter 在打开/恢复时绑定名称并补齐事件；每种 type 固定 payload。eventId 为一次生成的 UUID，不表示顺序或原生事件去重。
- **内容与增量**：message、thinking 共用消息模型、条目身份及内容块规则。chunk 只追加，MASS 不跨事件累积 delta，也不依靠拼接生成完整历史。内容采用 ACP 的五种 ContentBlock。
- **观察与交互**：资源分页、历史查询和实时订阅已有声明；Service 与 Store 共用历史查询/分页模型。交互请求和关闭使用独立事件，配置选项采用 ACP 风格的选择、分组和布尔结构。
- **事件处理器**：进程内按 Session 构造并串行处理，支持登记、关闭、停用与显式启用；业务消费与核心持久提交分离，不提供可靠业务重放。
- **开发规范**：语言无关编码原则与测试验收要求独立维护在 principles/；远端认证、秘密、原生身份与资源所有权要求随 Runtime 维护。
- **删除过时内容**：删除旧文档来源清单、被替代的设计记录及 HTTP/存储格式摘录，移除对前一版文档目录的引用。HTTP 与持久化绑定仅保留当前要求和真实待决项。
