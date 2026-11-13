---
type: Module Design
title: Service 模块设计
description: 管理 Runtime 与 Session 资源、目录查询、持久化和跨运行操作编排。
status: stable
generated:
  by: codex/gpt-6
  at: 2026-10-19 20:37:16+08:00
sources:
- id: direction
  resource: 项目负责人在 2026-10-19 本次 Runtime 职责讨论中的说明
  title: 一个 agentruntime module，两组公共接口，内部管理实例与全量会话
- id: k8s
  resource: https://kubernetes.io/docs/concepts/overview/working-with-objects/
  title: Kubernetes 对象的 metadata、spec 与 status 分层
- id: naming
  resource: https://kubernetes.io/docs/concepts/overview/working-with-objects/names/
  title: Kubernetes 资源名称、唯一键与 UID
- id: namespace-validation
  resource: https://raw.githubusercontent.com/kubernetes/apimachinery/master/pkg/api/validation/generic.go
  title: Kubernetes namespace 使用 DNS label 校验
- id: agentrun-session
  resource: /Users/jim/code/antgroup/piston/agentrun/pkg/api/session.go
  title: agentrun Session spec，本地 master 7fc5095，2026-10-19 核对
- id: agentrun-model
  resource: /Users/jim/code/antgroup/piston/agentrun/docs/adr/0007-remove-session-create-model-id.md
  title: agentrun 不在创建参数中指定 modelId 的决策
- id: idle-session-close
  resource: 项目负责人在 2026-11-06 确认的空闲 Session close 与按需启动范围
  title: close/offline、idle 自动 close、按需启动与重启校正
---

# Service 模块设计

## 职责与所有权

应用服务模块对外提供 RuntimeService 和 SessionService，拥有 Runtime、Session 的持久目录、namespace/name、uid、创建时间、配置修订、状态投影和资源查询。它分别依赖 [MetaStore](../metastore/design.md) 管理资源、[EventStore](../eventstore/design.md) 管理事件，依赖[运行时模块](../runtime/design.md)执行原生操作；另将验证后的事件交给独立 [Events](../events/design.md) 模块处理与广播；存储和 Events 核心均不反向调用 Service。

Service 是应用服务，不是通用 CRUD 仓库：创建 Session、恢复、close 和停止 Runtime 都需要协调持久记录与实际运行。此模块承担失败补偿、在途操作所有权和迟到结果结算，使调用者无需管理半完成操作。[^direction]

## 对存储、Events 与 Runtime 的依赖

HTTP、CLI 与进程内调用都进入 RuntimeService 或 SessionService。Service 的输入输出是语言无关业务合同，不使用 HTTP 请求、状态码、连接或传输流对象；HTTP 层负责解析、认证上下文及协议映射。

Service 决定业务前置条件、操作顺序、资源状态与失败补偿。MetaStore 提供资源 CRUD、唯一性约束、筛选分页及内置 generation 维护，EventStore 提供历史批量追加、读取和 syncedEventId 同步进度；RuntimeRegistry 提供配置校验和 Driver 构造，RuntimeDriver 提供实际运行能力；当前运行的查找、登记与移除统一由 Service 负责。Events 提供每 Session 一个 EventBroadcast，只管实时 Handler，Subscription 由队列 Handler 封装；历史直接由 Service 调用 EventStore，不进入 Broadcast；存储不启动 Agent，Runtime 不读取公共资源目录。

暂不提供 JSON Patch。公共 updateRuntime 接收完整 RuntimeSpec，Service 校验后向 MetaStore.update 提交完整资源并携带读取快照的 resourceVersion；冲突后的重新读取与业务判断由 Service 负责。Session 提供当前运行的单项瞬态 setSessionConfig；不更新持久 spec，规则见 [配置合同](../runtime/session-config.md)。

Service 不能用普通“先查再写”代替 Store 的原子唯一性检查或配置修订分配。创建 Session 成功后保存失败时，由 Service 协调 Driver 清理并保留未知结果责任；Store 事务不能包含外部 Agent 副作用。

Driver 的私有恢复材料仍由 Runtime 内部管理，不变成 Service 与 Store 之间的通用字段。MetaStore 与 EventStore 分别隐藏持久化实现。MetaStore 通过配置选择唯一权威后端，默认 SQLite，可选文件；EventStore 保持文件权威和 SQL 可重建。两个模块不相互调用，不提供跨模块事务；Service 协调身份、业务许可和生命周期。各自合同见 [MetaStore](../metastore/design.md) 与 [EventStore](../eventstore/design.md)。

## 阅读入口与接口版本

| 声明 | 内容 |
|---|---|
| [RuntimeService](runtime-service.tsp) | Runtime 资源创建、查询、更新、删除与启停编排 |
| [SessionService](session-service.tsp) | 全量 Session 目录、生命周期、输入、交互、历史和观察 |
| [运行时事件](../runtime/session-events.tsp) | 实时与历史共用的事件信封、type/payload 联合与顶层扩展 map |
| [models.tsp](models.tsp) | 资源、Metadata、名称引用、查询与分页 |
| [停止协调](#停止-runtime-的协调职责) | 本模块拥有的完整停止行为与完成条件 |
| [observation.tsp](observation.tsp) | Session 列表分页，引用 Events 拥有的实时订阅 |
| [Events](../events/main.tsp) | 每 Session 的实时 EventBroadcast、统一 Handler 与队列订阅 |
| [共享历史模型](../../shared/history-models.tsp) | Service 与 Store 共用的历史查询、分页与同步进度 |

当前接口版本及校验命令统一见 [TypeSpec 入口](../../typespec/README.md)。签名与字段只在 TypeSpec 维护；本页解释其行为保证。

## 对外保证与 seam

RuntimeService 返回 Runtime 资源快照；getRuntime、listRuntimes 不返回 Driver，也不启动 Agent。listRuntimes 返回全部匹配资源的数组，不分页。SessionService 返回 Session 或操作结果，listSessions 默认跨全部 Runtime 与 namespace，包含离线记录；Session 列表和历史查询保留分页。

按名称或 ID 解析目标属于Service 模块。SessionRef 支持互斥的 {namespace, name} 与 {sessionId}；两者解析到同一持久身份和通道。停止实例不删除会话，断线也不删除目录。目录读取失败必须明确失败，不能把部分列表当作全量结果。

## 查询与订阅

Session 列表使用 PageRequest.after/limit 和 Page<Session>.items/next；默认 limit=100，正整数，不设合同级最大值，实现超出自身容量时明确报错。按 namespace、name、uid 的字符串字典序升序返回，游标为排他位置并绑定除 limit 外的全部过滤条件。翻页不持有跨页快照；并发状态变化可能使资源进入或离开过滤集合，新增且排在已读位置之前的资源需要重新查询才能看到。无效、过滤条件不符或已失效游标明确报错。游标不依赖 SQLite 临时行号，不能解析或代替历史游标。

历史查询使用共享 HistoryQuery 与 HistoryPage，types 精确匹配并按 OR 组合，after 使用 HistoryCursor，limit 默认 100。Service 解析 Session 身份后转交 Store，不丢弃过滤、next 或 syncedEventId；具体语义见[会话历史](history.md)。

Service 按 sessionId 管理唯一 EventBroadcast，按名称和 ID 定位同一对象。subscribeEvents 解析身份及准入后只调用 Broadcast.subscribe，由 Broadcast 创建队列 Handler 并返回 EventSubscription。通用订阅是可取消的异步流：按序接收统一事件、区分等待和结束、查询终止原因、主动取消；不要求某种语言的方法形状。取消只解除自己的内部登记，不取消工作或停止 Runtime。慢订阅溢出明确异常结束，已有缓冲仍可读完，终止原因保持稳定。语言映射见 [Go 实时订阅](../../bindings/go-events.md)。

## 统一资源结构

Runtime 与 Session 都采用 metadata/spec/status。RuntimeSpec 含 type、类型专属 config，以及顶层 Session 默认值 features、mcpServers；名称仅在 metadata。Runtime namespace 必须为空，name 全局唯一；Session 的名称键是 namespace/name，并具有跨 Runtime 和 namespace 唯一的 sessionId。

两类资源复用 ObjectMetadata；uid、createdAt、generation、resourceVersion 由系统生成。创建输入仅接受 namespace/name 与 spec；名称创建后不变，uid 区分同名重建。名称采用 DNS subdomain 规则，namespace 采用 DNS label 规则，准确约束见 models.tsp。namespace 不自动代表权限隔离。

Session 通过 spec.runtimeRef 持久保存 Runtime 名称，Service 按该名称定位当前 Runtime 资源和 Driver，不保存或比较归属 Runtime 的 uid。同名 Runtime 的 metadata.uid 变化不构成归属或恢复拒绝条件；能否恢复原生会话仍由 Driver 验证恢复材料和当前配置。metadata.uid 只标识资源本身。

## 实例来源与变更策略

RuntimeService 支持在线登记、更新和删除。配置文件的初始导入及重启合并规则仍待定义；不能静默覆盖已经确认的在线修改。

updateRuntime 直接接收 RuntimeSpec，完整替换期望 type/config 及 Session 默认值；不接受 metadata、status 或前置版本条件。metadata.name、namespace、uid、createdAt 不通过更新修改。RuntimeRegistry 负责类型专属 config 校验，Service 负责 Session 默认值的公共结构校验；合法更新不因运行状态或会话引用而拒绝。删除仍须检查持久引用、在途创建和实际资源释放。

直接调用 updateRuntime 或 HTTP 时，`features` 与 `mcpServers` 也按完整 spec 替换：省略即清除对应默认值，不能借此修改已打开 Session。CLI 的部分更新便利规则见 [massctl](../massctl/design.md#runtime)；Service 与 HTTP 的全量替换语义不变。

MetaStore 在 create 时初始化 generation 为 1，在 spec 真正变化时原子递增；Service 不计算或传入新 generation。相同 spec 重复提交、状态更新、启停均不递增，准确比较与规范化规则待定义。MetaStore 通过独立的 resourceVersion 进行乐观并发控制，更新使用预期版本，冲突后由 Service 重新读取并决定处理；每次返回本次提交的快照；状态写入不能用旧资源对象覆盖期望配置。

## RuntimeConfig 与实例登记

Service 保存 spec.type 和 spec.config。资源创建与配置更新调用 RuntimeRegistry.validateConfig(type, config) 校验配置；校验失败直接返回错误，校验成功只保存资源，不创建 Driver、不启动 Agent，也不修改当前运行实例。RuntimeType 为开放字符串，RuntimeConfig 为 Record<unknown>；配置字段按具体类型解释，当前不设置全局封闭字段列表，具体约定与校验责任见[运行时配置](../runtime/design.md#配置与恢复)。未知类型和不合法配置明确失败，不能任意透传原生参数。

spec.features 与 spec.mcpServers 不属于 RuntimeConfig，也不传给 Registry 的类型配置校验。Service 在持久写入前校验其公共结构，包括 MCP 列表内名称唯一性和 transport 字段组合；类型专属支持在 Session 打开时由 Driver 校验。完整归属、字段与合并规则见 [Runtime Session 默认配置](../runtime/session-defaults.md)。

## 状态与配置生效

1. createRuntime 通过 MetaStore 保存配置，由 MetaStore 初始化 generation 为 1；尚未启动时没有 appliedGeneration。
2. Runtime 采用按需启动：createSession 和 resumeSession 在业务校验通过后自动启动所属 Runtime，调用者无需先调用 startRuntime。显式 startRuntime 保留；两种入口共用按 Runtime 名称串行协调的启动流程，原子捕获 type/config/generation，调用 RuntimeRegistry.create(type, name, config) 构造 Driver，由 Service 登记本次运行绑定，再调用 Driver.start。Session 默认值不进入本次类型配置快照。成功后记录所捕获的 appliedGeneration 和实际能力。
3. 启动中更新只改变期望 spec。已运行时再次 start 返回当前资源，不热更新、不创建第二个 Driver；新 Session 复用旧运行的类型配置快照，但 create/resume/load 仍读取最新持久 Runtime spec 中的 Session 默认值。
4. 应用新配置必须先完成停止，再启动。停止中仍可保存新 spec；清理使用原 Driver、原类型和原配置。
5. 确认停止后清除当前 appliedGeneration 与能力；结果未知时保留最后确认事实和原操作责任。

Session 默认值的生效独立于 Runtime 运行代际。createSession、resumeSession 和 loadSession 在确定当前运行绑定后，读取最新持久 Runtime spec，把 features 按字段、mcpServers 按名称与 Session 显式值合并，再调用 Driver。该读取不改变运行绑定、类型配置快照、generation 或 Session spec；已经打开且仍有效的 Session 不被热更新。Runtime 更新后无需 restart，下一次 Session 打开操作使用新默认值。

并发创建、恢复和显式启动共享同一次启动及其结果，不构造第二个 Driver。启动失败时，本次 Session 操作失败，不继续调用原生 open/resume；未知结果不自动重试。停止栅栏建立后不允许按需启动绕过栅栏或自动重开接纳；与停止并发的 Session 操作须重新确认目标运行可接纳。正常完成停止后，后续新的创建或恢复请求可以按最新配置再次按需启动。

按需启动只适用于 createSession 和 resumeSession；不自动恢复该 Runtime 下的其他 Session。createRuntime、配置更新、get/list、历史读取和实时订阅均不触发启动。sendPrompt、回答和取消要求已有有效的 Session 运行，不隐式恢复离线会话。显式 loadSession 仍按其独立合同要求 Runtime 已启动。

Service 独占维护 Runtime 名称 → 当前运行绑定的映射，保留原 Driver、配置快照与原操作关联；RuntimeRegistry 不保留当前运行。Driver 不接收 generation，也不返回资源状态；Session.status.runtimeGeneration 由Service 模块从此绑定读取，不能取最新期望 generation。Session 自身 appliedGeneration 另行记录，二者不等同。

例如期望版本为 4，而当前 Driver 使用版本 3，则 Runtime 的 generation/appliedGeneration 为 4/3。版本一致不代表运行健康。RuntimeConfig 更新成功也不保证原 Session 可以在新类型配置上恢复；兼容性由对应 Driver 验证。features 与 mcpServers 的更新不进入该兼容性快照，而是在下一次 Session 打开时重新合并。

## 资源状态

RuntimeStatus.state 使用 [RuntimeState](../../shared/states.tsp)：`stopped`、`starting`、`running`、`stopping`、`unknown`。创建 Runtime 资源后为 `stopped`；启动成功进入 `running`。启动明确失败且确认无遗留资源时回到 `stopped`，无法确认资源状态时为 `unknown`。停止协调开始后为 `stopping`，只有确认本实例拥有的资源释放后才进入 `stopped`。停止结果未知时保持 `stopping` 并保留原操作责任。`running` 不单独证明全部会话健康，也不表示远端共享服务由 MASS 独占。

SessionStatus.state 使用 [SessionState](../../shared/states.tsp)，替换原先独立的 lifecycle、connection、work；SessionQuery.state 按同一枚举筛选：

| 值 | 含义 |
|---|---|
| `offline` | 持久身份存在，MASS 当前无运行绑定且已结算其接纳的责任，可以恢复；不声称无法观测的 provider 工作已经停止 |
| `opening` | 正在创建或恢复运行连接 |
| `idle` | 连接可用，已确认当前没有执行中的工作 |
| `running` | 正在执行工作 |
| `requires_action` | 当前工作需要用户采取行动后才能继续，例如回答问题或确认权限 |
| `closing` | close 已接纳但尚未成功；本次尝试失败时也保持此值，并通过 reason 说明 |
| `unknown` | 无法确认当前运行或工作状态；用于重启或旧运行状态缺少证明，不表示普通 close 失败 |

断线后不能证明工作已停止时进入 `unknown`，不能推断为 `offline` 或 `idle`。取消被接受后仍保持 `running` 或 `requires_action`，直到有工作终止证据才进入 `idle`；若连接同时失效，则按实际证据确定状态，不能强制为空闲。

`archived` 不是 SessionState 或 SessionQuery.state 的合法值。新请求使用该值时在查询或写入前返回参数错误，不能当作空结果或兼容别名。

### Session close

SessionService.closeSession 从任意状态接纳，包括 `opening`、`idle`、`running`、`requires_action`、`closing`、`offline` 和 `unknown`。接纳后立即建立关闭栅栏：新的 prompt、answer、cancel、setSessionConfig 和 resume 被拒绝，get、list、history、watch 等只读操作继续可用。关闭与已接纳业务操作、resume 和其他 close 使用同一 Session 操作序列；先被接纳的一方保留其目标，close 随后接管清理。close 不撤销已经产生的外部副作用，也不声称此前工作成功完成。[^idle-session-close]

对需要调用 SDK 的 close 尝试，唯一成功条件是 Driver 对底层 SDK Session close 返回成功；再次 close 已 `offline` 的 Session 直接成功，对无当前 SDK 句柄的 `unknown` Session 则只完成 MASS 内部清理。SDK close 成功即表示 MASS 接受本次 native close，即使 Codex 原生 thread 可能由 provider 继续处理，或 Claude SDK 内部以自己的方式结束进程；MASS 不额外 interrupt、等待工作终止、强杀、按 PID 清理或探测后端健康。成功后 Service 仍须结算已接纳 delivery、history commit 和实时发布责任，清除当前运行版本、能力和实际功能，保留身份、历史与恢复材料，之后才写入 `offline`。SDK close 已成功但这些 MASS 内部责任尚未结算或结算失败时，Session 保持 `closing`、返回错误，并在后续显式 close 中继续结算，不能先公开 `offline`。close 不删除 Session，也不停止所属 Runtime；关闭最后一个 Session 后 Runtime 仍保持 `running`。

SDK close 失败或结果无法确认时，Session 保持 `closing` 并记录可公开、脱敏的原因；不得自动回到 `idle`、`running` 或 `offline`。失败不会自动重试。再次 close 是显式重试：当前仍有可调用句柄时幂等调用 SDK close；本次 close 已证明无需 SDK 目标（无句柄 `unknown`，或原操作已证明未产生原生资源）时，不要求句柄，继续未完成的 MASS 结算；已经确认 SDK close 成功、仅 MASS 内部责任未结算时，即使不再有 SDK 句柄也继续结算；已有 native close 尝试但结果尚未确认且没有可调用句柄时返回错误并保持 `closing`。并发或重复 close 不产生相互矛盾的结果。对已 `offline` 的 Session 再次 close 直接成功。

对 `unknown` Session，如果当前进程持有 SDK Session 句柄，按普通 close 调用 SDK；如果没有当前句柄，不调用不存在的目标，只完成 MASS 内部清理并进入 `offline`。清理包括结算已接纳 delivery、history commit 和实时发布，并终结可归属该 Session 的未完成 open、resume 或 load 责任：保留已提交的部分事实与真实错误，禁止重放或继续输入，不把责任终结当作原生操作成功。仍存在无法结算的内部责任时保持 `closing`，不进入 `offline`；再次 close 继续这条不需要 SDK 目标的清理重试。这里的 `offline` 只表示 MASS 已无当前运行绑定且内部责任已结算，不声称无法观测的 provider 工作或旧外部资源已经停止；该 close 接纳是 `unknown` 进入 `offline` 的唯一例外，其他情况不能从 unknown 推断离线。close 失败不改变此前已经发生的外部事实，也不允许新的业务操作越过关闭栅栏。

close 接纳时若 `opening`、open、resume 或 load 尚未产生当前 SDK 句柄，Session 保持 `closing` 并接管原操作的结果，不启动新的 open 或 resume。原操作随后产生句柄时，close 调用该句柄的 SDK close；原操作明确证明没有产生原生资源时，close 完成 MASS 内部清理并可进入 `offline`；原操作失败或结果无法确认时保持 `closing`。`closing` 重试若没有可用于 SDK close 的句柄，按上文规则继续可完成的 MASS 结算；已有 native close 尝试但结果未确认时返回错误并保持 `closing`，不能推断 close 成功。

### Session 空闲自动 close

daemon 对所有 Session 使用单一 `sessionIdleTimeout` 配置，YAML 键为 `sessionIdleTimeout`，启动参数为 `--session-idle-timeout`，默认 `10m`。`0` 禁用自动 close；负数或无效 duration 在 daemon 启动时拒绝。配置遵循 daemon 的现有优先级和严格字段校验。实现不得设置阻止约 `5s` 值使用的最小值。首版不提供 per-Session 或 per-Runtime 覆盖。[^idle-session-close]

只有当前运行绑定能够证明 Session 为 `idle`，且没有在途 open、resume 或 close，没有等待交互，也没有已知子 Agent 或工具工作时，才启动空闲计时。任何该 Session 已接纳的业务活动或已知运行事件都会重置计时；get、list、history 和 watch 本身不重置。计时器触发时必须基于当前状态、当前运行绑定和待办重新校验，不能依据可能落后的持久状态关闭已经活跃的 Session。

到期后调用与人工 close 相同的 closeSession 路径，并采用相同的成功、失败和重试规则。自动 close 失败保持 `closing`，不自动重试。计时器只存在于当前应用进程，不持久化；服务重启后不为旧 Session 建立自动启动或回收责任。

已知后台活动可以延长 Session 生命周期；无法观测的静默后台工作不承诺获得保活。`idle` 只表示当前已观察到没有前台工作或已知待办，不证明所有不可观测的后台任务已经结束。空闲调度不修改 Runtime 配置 generation，也不写入 Session generation。

### 显式恢复与输入准入

createSession 和 resumeSession 是仅有的 Session 按需启动入口。所属 Runtime 尚无当前运行绑定时，按持久 spec 启动，并与显式 startRuntime 共用同一条按名称串行启动协调；并发 create、resume 和 start 只建立一个运行绑定。显式 startRuntime 保留；createRuntime、配置更新、get/list、history、watch 和 close 不启动 Runtime。显式 loadSession 的启动前置条件保持其独立合同，不从本规则获得隐式启动。

resumeSession 从 `offline` 或重启后的 `unknown` 接纳。它按需启动 Runtime，并调用 provider 原生 resume；成功后依据实际观察进入 `idle`、`running` 或 `requires_action`，失败按[恢复规则](session.md#恢复)结算。`unknown` 上的 resume 失败继续为 `unknown`，不伪装成 `offline` 或成功；resume 不重放旧输入、工具调用、待处理交互或其他历史工作。`closing` 和其他存在有效运行控制者的状态拒绝 resume。

sendPrompt 不隐式 resume，也不启动 Runtime。`unknown` 的输入准入不能只看持久状态字符串，必须同时核对当前运行绑定及其状态观察：

- 没有当前 live Session 绑定，或当前绑定已经处理过任一状态观察且最新观察为 `unknown` 时，拒绝输入；失败发生在任何派发副作用前，错误包含当前状态并要求显式 resume。
- 绑定刚由成功的显式 createSession 或 resumeSession 建立，且 Service 尚未处理该运行的首个状态观察时，允许后续 sendPrompt。该例外只证明显式建立的原生连接可由当前 Driver 接受输入，不启动 Runtime、不重新 resume，也不把持久 `unknown` 解释为可恢复成功。首个观察一旦被处理为 `unknown`，后续输入重新要求显式 resume。
- `offline`、`opening`、`closing` 仍严格拒绝；`idle`、`running` 和 `requires_action` 按当前有效绑定准入。

上述拒绝均在产生任何派发副作用前完成。answer 和 cancel 对无有效运行的 Session 同样在派发前失败，返回当前状态并要求先显式 resume，不存在隐含恢复路径。

状态只描述当前可证明的事实，不替代接受、历史提交、工作终止和 close 结果各自所需的证据。Reason 为字符串，各具体问题自行定义 reason 取值，不维护全局枚举或统一词表。运行时 state_update 使用 SessionState 中 idle/running/requires_action 或 null 表达工作观察；Service 同步协调打开和 close 等业务生命周期；Agent 状态观察可由状态 Handler 自行异步投影，按 Session 身份、资源版本、当前运行身份及 closing 保护更新；Handler 不接收 Runtime/工作上下文，也不获得运行控制能力，失败记录诊断且不阻塞广播。查询状态允许落后，不保证最终追上；业务准入仍校验实际运行与操作证据。映射与终态保护见[事件状态投影](../runtime/events.md#状态投影)。

## 服务重启与状态校正

服务启动只打开持久目录、校验数据并校正旧运行状态，不自动启动 Runtime，不自动恢复 Session，也不从历史重放输入、工具或交互。持久资源存在不表示当前进程已经持有有效运行连接；重启前保存的 idle 不能直接作为重启后的可用状态。

Service 在开放业务接口前，通过 MetaStore 完成状态校正；不由 get/list 的读取副作用触发，也不由 MetaStore 自行推断运行状态。校正只更新 status 及其 resourceVersion，保留 metadata 身份、createdAt、spec、generation、sessionId、历史 eventId 和同步位置。更新失败时不开放带有旧在线状态的业务接口；中断后再次启动可重复校正，不自动进行原生操作。

| 重启前的事实 | 重启后的 Session 状态 |
|---|---|
| 旧 persisted state 为 archived | 迁移为 offline，保留身份、历史、UUID 与同步位置，不建立当前运行绑定 |
| 原本为 offline 且无未结算责任 | offline |
| 保存为 opening、idle、running、requires_action 或 closing，但跨重启缺少释放或结算证明 | unknown，通过 reason 说明旧运行未确认 |
| 原本为 unknown 或有未结算操作/load | unknown，继续保留原责任，不把重启当作结算证据 |

保留未结算责任不等于要求一直停留到原生结果可知；用户显式 close 后按其清理规则接管并终结 MASS 内部责任，但不会把原生结果未知伪造成成功。

正常应用 shutdown 在完成事实排空和资源释放后，将已关闭 Session 记录为 offline，不创建用户请求的 close 历史或保留旧运行控制。异常退出后，即使旧状态是 idle，也不能仅因本地连接消失便断言原生工作或资源已释放。未知责任需要显式接管，不能直接按 offline 执行冲突恢复；本轮不新增独立跨重启结算 API。

Runtime 同样不能沿用旧 running 作为当前可用证明。已确认停止的保留 stopped；旧 starting/running/stopping 或已有不确定责任的校正为 unknown。Service 当前运行映射为空不证明旧运行资源释放。冷进程没有该 Runtime 的当前运行映射时，显式 start/resume 可以把持久 unknown 视为旧本地 owner 已不可达并建立新 Driver；该动作不证明旧 provider 工作或后端资源已经结束。同一进程已有运行绑定或未结算责任时仍不能重建。

确认 close 成功后清除当前运行版本和能力；结果未知时保留最后确认的版本与能力作为旧观察，并通过 closing/reason 表达不确定性，这些字段不能授予当前执行许可。离线 Session 只有成功 resume 后，才依据新的实际观察进入 idle、running 或 requires_action；不能用历史状态或旧交互恢复当前控制能力。

## 内部运行能力的取得与释放

Service 通过 RuntimeRegistry.create 构造 Driver，并在自己的当前运行映射中登记本次运行绑定；后续查找只读取此映射。Registry 只维护类型到构造方式的映射。Service 按名称串行协调实例生命周期并固定原 Driver；start 才取得进程或连接。启动明确失败且无遗留资源才移除本次绑定；结果未知保留原绑定与操作责任，迟到清理不能移除后来建立的运行。

SessionService 按 Session.spec.runtimeRef 定位所属 Runtime；创建和恢复在校验后按需启动，再通过运行绑定取得 Driver。已有运行复用原配置和 Driver，不因新请求热更新。查询、历史读取和订阅绝不隐式激活。原操作在副作用前关联，调用超时不解除资源责任。

Driver 内部维护恢复材料、原生连接和运行来源；Service 模块不接收或交回恢复材料。close 后需结算全部已接纳操作与核心事实；SDK 失败或无法确认的结果继续归原 close 责任管理，不能移除预留后自动重试。

## 停止 Runtime 的协调职责

完整停止流程由 Service 内部的同一停止操作拥有，RuntimeService 和 SessionService 是同一 Module 的两组公共能力。调用者只调用 stopRuntime；组装层只注入依赖，不传递 barrier、排空证明或驱动分阶段停止。以下行为及完成条件是合同，不固定内部对象之间的调用结构：

1. 原子封闭该实例的新激活、创建、恢复、load、输入和交互回答，覆盖尚无 sessionId 的在途创建。
2. SessionService 处理全部会话及在途操作，包含 load 预留与回放；必要时调用 Driver.closeSession、cancel 或 cancelHistoryLoad，提交已接纳事实并结算内部使用责任。
3. 会话及原操作排空确定完成后调用原 Driver.stop；停止期间持续接收最终事件，并再次确认 Service 的历史提交和实时发布待办均已结算。Driver 有未结算执行时仍拒绝停止，不能绕过安全检查。
4. 只有实际运行资源释放和最终事件结算都得到确认，才由 Service 移除原运行绑定、更新 Runtime 状态并公开停止成功。同名串行协调持续到移除完成，旧清理不能误删新绑定。

barrier、排空证明或其他协调手段可作为 Service 的 internal seam，由 Implementation 自行选择，不要求各语言实现固定的 beginStop/drainRuntimeSessions/finishStop Interface。

同实例并发停止共享原责任。排空、停止或释放未知时保留接纳关闭及操作所有权；不能静默重开。取消、关闭、已接纳事实交付和只读查询仍可推进。停止期间持续读原 Driver 流，在最终事件交付后结算 Service 自己的历史追加和 publish 调用；不调用 Broadcast.close。停止后保留资源目录、历史、Session Broadcast、健康 Handler 和订阅，显式恢复沿用原关系；应用 shutdown 才关闭 Broadcast 和消费者；显式启动按最新配置重新建立运行。

单 Session 异常不能通过停止健康共享运行掩盖；其他 Runtime 独立。远程 Driver.stop 只释放本实例拥有的连接与任务，不能宣称停止了远端共享服务。

## Session spec 与 agentrun 的参考关系

SessionSpec 在运行时 DriverSessionSpec 的 cwd、systemPrompt、additionalDirs、config、features 和 mcpServers 上增加 runtimeRef。路径按 Agent 所在环境解释；额外目录不代表授权。systemPrompt 是配置，创建与恢复不隐式发送业务输入。features 是持久的功能请求，字段省略时继承当前 Runtime 默认值或 schema 默认值；Driver 返回实际生效值，Session.status.effectiveFeatures 记录本次运行的实际值。mcpServers 是本次 Session 的显式覆盖项，打开时与当前 Runtime 默认列表按名称合并。请求开启但 Driver 不支持时降级为 false；其他显式不支持的选项必须报错。Session spec 只保存显式值，不写回合并结果。

字段沿用此前 agentrun 参考讨论，bundleDir 与私有恢复材料由运行时实现管理；不加入无约束 _meta 或 permissions。MCP 使用受约束的 mcpServers 值，不接受任意透传 map。SessionService 当前没有 updateSession 或 deleteSession，close 保留身份和历史。Runtime 的 Session 默认值设置不属于更新 Session spec。

## 事件、历史与恢复

Service 作为 RuntimeDriver.events() 的唯一消费者接收同一 Driver 下全部 Session 的事件，根据 sessionId 从已有绑定定位唯一 EventBroadcast，并核对 adapter 已补齐的 namespace/name、Runtime 与原运行来源。上游来源校验留在 Service；Broadcast 和 Handler 只接收统一 SessionEvent，不接收运行关联。完整事实先提交后发布；实时预览不冒充历史。创建时提前到达的事件由原操作有界暂存，身份持久化成功后才交付。具体流程见[Session 事件路由](session.md#事件路由)。

恢复由Service 模块先校验身份、原实例归属和已提交公共历史；Driver 再内部验证恢复材料并恢复同一 sessionId。失败不新建替代会话、不重演输入或消费者动作。历史和事实证据的校验职责见[会话历史](history.md)。

## 错误与未知结果

OperationError 共享 code、reason、message 三个字段。code=outcome_unknown 表示无法确定副作用结果，不能视为未执行或自动重试。接受输入、历史提交、工作结束和资源释放分别需要证据；取消确认不证明工作已结束。

旧命令和交互固定接纳时的运行及目标，恢复不能重新定向。reason 为字符串，由各具体问题自行定义；具体操作关联的公开查询机制与跨重启幂等规则仍待设计。

## 验收场景

- 已停止实例仍可查询，列表不启动 Agent；Runtime 列表无分页，Session 目录可分页。
- 并发启动只有一个运行所有者；更新与启动并发不会误报 appliedGeneration。
- 停止状态下直接创建或恢复 Session，自动启动所属 Runtime；并发请求共用一次启动，其他 Runtime 和 Session 不被自动激活。
- 启动失败不调用原生 open/resume；同一进程停止中、已有运行绑定或未结算资源时不绕过栅栏、不自动重试。冷进程无当前运行映射的 unknown Runtime 只由显式 start/resume 接管。
- 查询、历史、订阅和无有效运行的输入不触发启动或恢复；prompt 对 detached unknown、已观察到 unknown、offline、opening 和 closing 在 Driver open/resume/prompt 等任何派发副作用前失败并要求显式 resume。
- 显式 create/resume 刚建立 live 绑定、初始观察尚未到达的 unknown 可在不启动 Runtime、不重新 open/resume 的前提下接受 prompt；该例外不适用于已 detached 或已处理 unknown 观察的 Session；显式 start 仍可用。
- close 从 active 状态接纳并调用 SDK close；成功为 offline，失败保持 closing；重复 close 与 offline 重试结果安全；关闭最后一个 Session 不停止 Runtime。
- unknown 有当前 SDK 句柄时 close 调用 SDK，无句柄时完成内部清理到 offline；失败继续保持 closing。
- close 在 opening/open/resume/load 尚未产生句柄时保持 closing 并接管原结果；有句柄后再调用 SDK，不能新建 open/resume。
- 空闲 Session 到期使用同一条 close 路径；查询、历史、watch 不重置计时，失败不自动重试。
- sessionIdleTimeout 默认 10m、0 禁用、负数或无效值启动失败、约 5s 可显式配置。
- 服务重启不启动 Runtime 或 Session；旧 archived 迁移为 offline，缺少结算证明的旧在线状态为 unknown；冷进程无当前运行映射的 unknown Runtime 可由显式 start/resume 建立新 Driver，unknown Session 随即可恢复；unknown resume 失败仍为 unknown。已有运行绑定或未结算责任时仍拒绝。
- SessionQuery.state=archived 被拒绝，不返回空页或兼容别名。
- 启动状态校正完成前不开放业务接口；校正失败明确报错，重复启动不修改身份、spec、generation 或历史，不复活旧工作与交互。
- 运行中更新不替换 Driver；停止使用原 Driver 并保留未确定责任。
- Runtime 更新 features/mcpServers 不需要重启；下一次 create/resume/load 使用新默认值，已打开 Session 不变。
- Session 显式 feature 按字段覆盖 Runtime 默认值；MCP server 同名替换、新名称追加，空列表不清空默认项。
- Session spec 与恢复材料只保存显式 Session 选项，不保存本次解析出的 Runtime 默认值。
- 两种 Session 引用解析到同一记录；跨 Runtime 重复 sessionId 拒绝第二次登记并清理本次资源。
- 创建成功但持久化失败时不公开 Session，清理仅作用于本次运行。
- 事件早于目录提交时不误投；旧运行事件不更新新运行状态。
- Session 按 runtimeRef 名称定位 Runtime，不比较 Runtime uid；恢复由 Driver 校验原生会话与配置，不重放业务动作。
- 通过 RuntimeService 单独停止实例，仍执行完整排空与释放协调。

## 定稿清单

配置导入、各类型的具体配置/能力名称、事件剩余细节和操作查询仍待确认。共享类型已采用开放字段或不透明字符串，列表分页、历史查询与实时订阅已定义；这些声明不表示具体 Driver 已支持全部业务能力。RuntimeDriver.events() 已定义单消费者共享流；语言绑定的取消方式、具体队列容量和公平调度实现仍待确定，不声明事件集成已经实现。

[^direction]: 当前讨论确定 Service 依赖 Store 持久化资源、依赖 Runtime 执行运行操作；HTTP 只做协议映射，具体接口由负责人定义。

[^idle-session-close]: 负责人在 2026-11-06 确认 close/offline、空闲自动 close、按需启动和重启状态校正的终态范围。

## 显式 load

[异步 load](load.md)纳管已知原生 sessionId 的保留历史，只接受新空目标。先完成唯一预留和 Service 接收绑定，再调用 Driver；异步结果由 Session.status.historyLoad 查询。原生完成与本地提交/分发完成分别结算，失败保留部分历史并拒绝重复导入。load 复用原生 resume，成功保留同一恢复运行，完成本地结算后直接启用输入；普通 resume 不导入历史。加载中及失败时拒绝输入，旧交互永不复活。

主 Session 的初始 `config.model/effort`、瞬态 setter、原运行在途责任及错误以[配置合同](../runtime/session-config.md)为准。初始值保存在 Session spec，create/resume/load 传给 SDK；setter 不持久，close/resume 重新请求初始值。可变配置不参与恢复身份验证。
