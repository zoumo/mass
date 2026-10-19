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
---

# Service 模块设计

## 职责与所有权

应用服务模块对外提供 RuntimeService 和 SessionService，拥有 Runtime、Session 的持久目录、namespace/name、uid、创建时间、配置修订、状态投影和资源查询。它依赖 [Store](../store/design.md) 持久化资源、索引与历史，依赖[运行时模块](../runtime/design.md)执行原生操作；两个依赖都不反向调用 Service。

Service 是应用服务，不是通用 CRUD 仓库：创建 Session、恢复、归档和停止 Runtime 都需要协调持久记录与实际运行。此模块承担失败补偿、在途操作所有权和迟到结果结算，使调用者无需管理半完成操作。[^direction]

## 对 Store 与 Runtime 的依赖

HTTP、CLI 与进程内调用都进入 RuntimeService 或 SessionService。Service 的输入输出是语言无关业务合同，不使用 HTTP 请求、状态码、连接或传输流对象；HTTP 层负责解析、认证上下文及协议映射。

Service 决定业务前置条件、操作顺序、资源状态与失败补偿。MetaStore 提供资源 CRUD、唯一性约束、筛选分页及内置 generation 维护，HistoryStore 提供历史批量追加、读取和 syncedEventId 同步进度；RuntimeRegistry/RuntimeDriver 提供构造、实例查找及实际运行能力。Store 不启动 Agent，Runtime 不读取公共资源目录。

暂不提供 JSON Patch。公共 updateRuntime 接收完整 RuntimeSpec，Service 校验后向 MetaStore.update 提交完整资源并携带读取快照的 resourceVersion；冲突后的重新读取与业务判断由 Service 负责。Session 尚无公共配置更新方法，本次不新增。

Service 不能用普通“先查再写”代替 Store 的原子唯一性检查或配置修订分配。创建 Session 成功后保存失败时，由 Service 协调 Driver 清理并保留未知结果责任；Store 事务不能包含外部 Agent 副作用。

Driver 的私有恢复材料仍由 Runtime 内部管理，不变成 Service 与 Store 之间的通用字段。Store 隐藏文件与数据库实现；文件保存完整数据，SQLite 仅作可重建索引。MetaStore 的 CRUD 与并发契约见其 TypeSpec，查询分页和错误分类仍待细化，HistoryStore 的批量追加、查询与同步进度见其 TypeSpec，见 [Store 设计](../store/design.md)。

## 阅读入口与接口版本

| 声明 | 内容 |
|---|---|
| [RuntimeService](runtime-service.tsp) | Runtime 资源创建、查询、更新、删除与启停编排 |
| [SessionService](session-service.tsp) | 全量 Session 目录、生命周期、输入、交互、历史和观察 |
| [运行时事件](../runtime/session-events.tsp) | 实时与历史共用的事件信封、type/payload 联合与顶层扩展 map |
| [models.tsp](models.tsp) | 资源、Metadata、名称引用、查询与分页 |
| [coordination.tsp](coordination.tsp) | 本模块内部停止接纳、会话排空和完成证明 |
| [observation.tsp](observation.tsp) | Session 列表分页与实时订阅 |
| [event-handlers.tsp](event-handlers.tsp) | 按会话构造、登记、交付与关闭事件处理器 |
| [共享历史模型](../../shared/history-models.tsp) | Service 与 Store 共用的历史查询、分页与同步进度 |

当前接口版本及校验命令统一见 [TypeSpec 入口](../../typespec/README.md)。签名与字段只在 TypeSpec 维护；本页解释其行为保证。

## 对外保证与 seam

RuntimeService 返回 Runtime 资源快照；getRuntime、listRuntimes 不返回 Driver，也不启动 Agent。listRuntimes 返回全部匹配资源的数组，不分页。SessionService 返回 Session 或操作结果，listSessions 默认跨全部 Runtime 与 namespace，包含离线和归档记录；Session 列表和历史查询保留分页。

按名称或 ID 解析目标属于Service 模块。SessionRef 支持互斥的 {namespace, name} 与 {sessionId}；两者解析到同一持久身份和通道。停止实例不删除会话，断线也不删除目录。目录读取失败必须明确失败，不能把部分列表当作全量结果。

## 查询与订阅

Session 列表使用 PageRequest.after/limit 和 Page<Session>.items/next；默认 limit=100，正整数，不设合同级最大值，实现超出自身容量时明确报错。按 namespace、name、uid 的字符串字典序升序返回，游标为排他位置并绑定除 limit 外的全部过滤条件。翻页不持有跨页快照；并发状态变化可能使资源进入或离开过滤集合，新增且排在已读位置之前的资源需要重新查询才能看到。无效、过滤条件不符或已失效游标明确报错。游标不依赖 SQLite 临时行号，不能解析或代替历史游标。

历史查询使用共享 HistoryQuery 与 HistoryPage，types 精确匹配并按 OR 组合，after 使用 HistoryCursor，limit 默认 100。Service 解析 Session 身份后转交 Store，不丢弃过滤、next 或 syncedEventId；具体语义见[会话历史](history.md)。

EventSubscription 提供异步 next 和幂等 close，独立于 Driver 的单消费者流；订阅只观察注册后的事件，不补历史。慢订阅溢出明确异常结束；正常关闭释放等待及缓冲，异常终止后保留原错误。结束观察不取消工作、归档会话或停止 Runtime。传输层断开和取消等待的映射由语言绑定负责。

## 统一资源结构

Runtime 与 Session 都采用 metadata/spec/status。RuntimeSpec 只含 type、config，名称仅在 metadata。Runtime namespace 必须为空，name 全局唯一；Session 的名称键是 namespace/name，并具有跨 Runtime 和 namespace 唯一的 sessionId。

两类资源复用 ObjectMetadata；uid、createdAt、generation、resourceVersion 由系统生成。创建输入仅接受 namespace/name 与 spec；名称创建后不变，uid 区分同名重建。名称采用 DNS subdomain 规则，namespace 采用 DNS label 规则，准确约束见 models.tsp。namespace 不自动代表权限隔离。

Session 通过 spec.runtimeRef 持久保存 Runtime 名称，Service 按该名称定位当前 Runtime 资源和 Driver，不保存或比较归属 Runtime 的 uid。同名 Runtime 的 metadata.uid 变化不构成归属或恢复拒绝条件；能否恢复原生会话仍由 Driver 验证恢复材料和当前配置。metadata.uid 只标识资源本身。

## 实例来源与变更策略

RuntimeService 支持在线登记、更新和删除。配置文件的初始导入及重启合并规则仍待定义；不能静默覆盖已经确认的在线修改。

updateRuntime 直接接收 RuntimeSpec，完整替换期望 type/config；不接受 metadata、status 或前置版本条件。metadata.name、namespace、uid、createdAt 不通过更新修改。RuntimeRegistry 负责类型专属配置校验；合法更新不因运行状态或会话引用而拒绝。删除仍须检查持久引用、在途创建和实际资源释放。

MetaStore 在 create 时初始化 generation 为 1，在 spec 真正变化时原子递增；Service 不计算或传入新 generation。相同 spec 重复提交、状态更新、启停均不递增，准确比较与规范化规则待定义。MetaStore 通过独立的 resourceVersion 进行乐观并发控制，更新使用预期版本，冲突后由 Service 重新读取并决定处理；每次返回本次提交的快照；状态写入不能用旧资源对象覆盖期望配置。

## RuntimeConfig 与实例登记

Service 保存 spec.type 和 spec.config。资源创建与配置更新调用 RuntimeRegistry.validateConfig(type, config) 校验配置；校验失败直接返回错误，校验成功只保存资源，不创建或登记 Driver、不启动 Agent，也不修改当前运行实例。RuntimeType 为开放字符串，RuntimeConfig 为 Record<unknown>；配置字段按具体类型解释，当前不设置全局封闭字段列表，具体约定与校验责任见[运行时配置](../runtime/design.md#配置与恢复)。未知类型和不合法配置明确失败，不能任意透传原生参数。

## 状态与配置生效

1. createRuntime 通过 MetaStore 保存配置，由 MetaStore 初始化 generation 为 1；尚未启动时没有 appliedGeneration。
2. startRuntime 原子捕获 spec/generation，调用 RuntimeRegistry.create(type, name, config) 构造并原子登记 Driver，再调用 Driver.start。成功后记录所捕获的 appliedGeneration 和实际能力。
3. 启动中更新只改变期望配置。已运行时再次 start 返回当前资源，不热更新、不创建第二个 Driver；新 Session 也复用旧运行配置。
4. 应用新配置必须先完成停止，再启动。停止中仍可保存新 spec；清理使用原 Driver、原类型和原配置。
5. 确认停止后清除当前 appliedGeneration 与能力；结果未知时保留最后确认事实和原操作责任。

RuntimeRegistry 保存 name → Driver；Service 保留 Runtime 名称、原 Driver、配置快照与原操作关联。Driver 不接收 generation，也不返回资源状态；Session.status.runtimeGeneration 由Service 模块从此绑定读取，不能取最新期望 generation。Session 自身 appliedGeneration 另行记录，二者不等同。

例如期望版本为 4，而当前 Driver 使用版本 3，则 Runtime 的 generation/appliedGeneration 为 4/3。版本一致不代表运行健康。RuntimeConfig 更新成功也不保证原 Session 可以在新配置上恢复；兼容性由对应 Driver 验证。

## 资源状态

RuntimeStatus.state 使用 [RuntimeState](../../shared/states.tsp)：`stopped`、`starting`、`running`、`stopping`、`unknown`。创建 Runtime 资源后为 `stopped`；启动成功进入 `running`。启动明确失败且确认无遗留资源时回到 `stopped`，无法确认资源状态时为 `unknown`。停止协调开始后为 `stopping`，只有确认本实例拥有的资源释放后才进入 `stopped`。停止结果未知时保持 `stopping` 并保留原操作责任。`running` 不单独证明全部会话健康，也不表示远端共享服务由 MASS 独占。

SessionStatus.state 使用 [SessionState](../../shared/states.tsp)，替换原先独立的 lifecycle、connection、work；SessionQuery.state 按同一枚举筛选：

| 值 | 含义 |
|---|---|
| `offline` | 持久身份存在，当前无运行连接且已确认无未结算工作，可以恢复 |
| `opening` | 正在创建或恢复运行连接 |
| `idle` | 连接可用，已确认当前没有执行中的工作 |
| `running` | 正在执行工作 |
| `requires_action` | 当前工作需要用户采取行动后才能继续，例如回答问题或确认权限 |
| `closing` | 正在归档或关闭，尚未确认运行资源释放 |
| `archived` | 已明确归档，并确认会话运行资源释放，保留身份和历史 |
| `unknown` | 无法确认当前运行或工作状态 |

断线后不能证明工作已停止时进入 `unknown`，不能推断为 `offline` 或 `idle`。取消被接受后仍保持 `running` 或 `requires_action`，直到有工作终止证据才进入 `idle`；若连接同时失效，则按实际证据确定状态，不能强制为空闲。归档进入 `closing`，释放确认后才进入 `archived`；关闭超时仍保持 `closing`，通过 reason 说明结果未确定，并阻止冲突恢复。

恢复从 `offline` 或 `archived` 进入 `opening`，成功后依据实际事实进入 `idle`、`running` 或 `requires_action`。失败且确认没有遗留运行资源时回到恢复前的 `offline` 或 `archived`；副作用结果未确定时进入 `unknown` 并保留原操作责任，不能发起冲突恢复。创建期间若尚无持久身份，`opening` 不授权提前公开 Session 创建成功。

状态只描述当前可证明的事实，不替代接受、历史提交、工作终止和资源释放各自所需的证据。Reason 为字符串，各具体问题自行定义 reason 取值，不维护全局枚举或统一词表。运行时 state_update 使用 SessionState 中 idle/running/requires_action 或 null 表达工作观察；Service 结合打开、关闭、交互和原运行/工作关联投影资源状态。映射与终态保护见[事件状态投影](../runtime/events.md#状态投影)。

## 内部运行能力的取得与释放

Service 通过 RuntimeRegistry.create 构造并登记 Driver，用 get(name) 定位实例。类型映射与实例映射统一由 Registry 内部维护，调用方无需单独登记 Driver。Service 按名称串行协调实例生命周期并固定原 Driver；start 才取得进程或连接。启动明确失败且无遗留资源才移除登记；结果未知保留登记与原操作责任。

SessionService 按 Session.spec.runtimeRef，通过 RuntimeRegistry.get(name) 定位 Driver，将 SessionRef 解析成 sessionId 后调用。创建或恢复是否允许按需激活仍属明确的实例策略；查询和历史读取绝不隐式激活。原操作在副作用前关联，调用超时不解除资源责任。

Driver 内部维护恢复材料、原生连接和运行来源；Service 模块不接收或交回恢复材料。关闭会话后需结算全部已接纳操作与核心事实；未知结果继续归原操作管理，不能移除预留后重试。

## 停止 Runtime 的协调职责

停止协调完全属于Service 模块，coordination.tsp 不由运行时模块导入：

1. 原子封闭该实例的新激活、创建、恢复、输入和交互回答，覆盖尚无 sessionId 的在途创建。
2. SessionService 处理全部会话及在途操作，必要时调用 Driver.cancel/closeSession，提交已接纳事实并结算内部使用责任。
3. 排空确定完成后才产生完成证明；协调者再调用原 Driver.stop。Driver 有未结算执行时仍拒绝停止，不能绕过安全检查。
4. 只有实际运行资源释放得到确认，才执行 RuntimeRegistry.remove(name)、更新 Runtime 状态并公开停止成功。同名串行协调持续到移除完成，旧清理不能误删新登记。

同实例并发停止共享原责任。排空、停止或释放未知时保留接纳关闭及操作所有权；不能静默重开。取消、关闭、已接纳事实交付和只读查询仍可推进。停止后保留资源目录、历史与非运行资源消费者；显式启动按最新配置重新建立运行。

单 Session 异常不能通过停止健康共享运行掩盖；其他 Runtime 独立。远程 Driver.stop 只释放本实例拥有的连接与任务，不能宣称停止了远端共享服务。

## Session spec 与 agentrun 的参考关系

SessionSpec 在运行时 DriverSessionSpec 的 cwd、systemPrompt、additionalDirs 上增加 runtimeRef。路径按 Agent 所在环境解释；额外目录不代表授权。systemPrompt 是配置，创建与恢复不隐式发送业务输入；显式不支持的选项必须报错。

字段沿用此前 agentrun 参考讨论，bundleDir 与私有恢复材料由运行时实现管理；不加入无约束 _meta、permissions 或 mcpServers。SessionService 当前没有 updateSession 或 deleteSession，归档保留身份和历史。

## 事件、历史与恢复

Service 作为 RuntimeDriver.events() 的唯一消费者接收同一 Driver 下全部 Session 的事件，根据 sessionId 从已有绑定定位通道，并核对 adapter 已补齐的 namespace/name、Runtime 与原运行来源。完整事实先提交后发布；实时预览不冒充历史。创建时提前到达的事件由原操作有界暂存，身份持久化成功后才交付。具体流程见[Session 事件路由](session.md#事件路由)。

恢复由Service 模块先校验身份、原实例归属和已提交公共历史；Driver 再内部验证恢复材料并恢复同一 sessionId。失败不新建替代会话、不重演输入或消费者动作。历史和事实证据的校验职责见[会话历史](history.md)。

## 错误与未知结果

OperationError 共享 code、reason、message 三个字段。code=outcome_unknown 表示无法确定副作用结果，不能视为未执行或自动重试。接受输入、历史提交、工作结束和资源释放分别需要证据；取消确认不证明工作已结束。

旧命令和交互固定接纳时的运行及目标，恢复不能重新定向。reason 为字符串，由各具体问题自行定义；具体操作关联的公开查询机制与跨重启幂等规则仍待设计。

## 验收场景

- 已停止实例仍可查询，列表不启动 Agent；Runtime 列表无分页，Session 目录可分页。
- 并发启动只有一个运行所有者；更新与启动并发不会误报 appliedGeneration。
- 运行中更新不替换 Driver；停止使用原 Driver 并保留未确定责任。
- 两种 Session 引用解析到同一记录；跨 Runtime 重复 sessionId 拒绝第二次登记并清理本次资源。
- 创建成功但持久化失败时不公开 Session，清理仅作用于本次运行。
- 事件早于目录提交时不误投；旧运行事件不更新新运行状态。
- Session 按 runtimeRef 名称定位 Runtime，不比较 Runtime uid；恢复由 Driver 校验原生会话与配置，不重放业务动作。
- 通过 RuntimeService 单独停止实例，仍执行完整排空与释放协调。

## 定稿清单

配置导入、各类型的具体配置/能力名称、事件剩余细节和操作查询仍待确认。共享类型已采用开放字段或不透明字符串，列表分页、历史查询与实时订阅已定义；这些声明不表示具体 Driver 已支持全部业务能力。RuntimeDriver.events() 已定义单消费者共享流；语言绑定的取消方式、具体队列容量和公平调度实现仍待确定，不声明事件集成已经实现。

[^direction]: 当前讨论确定 Service 依赖 Store 持久化资源、依赖 Runtime 执行运行操作；HTTP 只做协议映射，具体接口由负责人定义。
