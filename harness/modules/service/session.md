---
type: Module Design
title: 应用服务模块内的 Session 管理
description: 展开 sessionManager 对全量会话目录、实例归属、路由和生命周期的管理职责。
status: stable
generated:
  by: codex/gpt-6
  at: 2026-10-19T20:37:16+08:00
sources:
  - id: direction
    resource: "项目负责人在 2026-10-19 本次 Runtime 职责讨论中的说明"
    title: Service 对 Store 和 Runtime 的依赖
---

# 应用服务模块内的 Session 管理

## 职责与所有权

本文是 [应用服务模块](design.md) 内部 sessionManager 的职责展开。它统一管理全部 Runtime 实例的持久 Session，负责会话目录、名称预留、Runtime 名称归属、命令路由、创建发布、close 和恢复编排。调用者使用独立的 SessionService 公共接口。SessionService 实现通过运行时模块提供的 RuntimeDriver 协调原生控制与会话历史。[^direction]

Agent 生成 sessionId，MASS 校验其合法性后原样保存；用户命名 sessionName 与它独立。sessionId 在本模块管理的全部 Runtime 和 namespace 中唯一；Runtime 名称归属仍用于路由与恢复验证。内部存储定位符不成为第二个公共会话 ID。

Session 的持久存在与连接状态独立。断线或 close 不删除身份和历史。恢复重新建立运行连接，同一持久会话不能同时具有两个相互竞争的有效运行控制者。

## Session ID 合法性

sessionId 的完整原始值不得等于 `.` 或 `..`。这是身份合法性规则，适用于创建、恢复、持久身份和所有按 ID 的操作；不通过 URL 编码、裁剪、改名或生成替代 ID 绕过。包含点号的其他值（例如 `a.b`、`a..b`）不因本规则被拒绝。

客户端按原始 JSON 字符串传输 ID；Service 在资源查询或副作用之前校验，非法值返回 invalid_argument，reason=invalid_session_id。名称与 ID 使用互斥 SessionRef，不根据字符串形状或查询结果猜测引用类型。

原生打开或恢复若返回上述非法 ID，不得公开创建或恢复成功、登记为合法持久身份或发布对应会话事件；按原操作的资源清理与未知结果规则结算，不伪造新 ID。读取到这类非法持久身份时明确失败，不能将其恢复为合法会话。

## 对外保证

创建通过统一入口按 spec.runtimeRef 指定目标实例，metadata.namespace/name 指定会话唯一键。恢复和后续操作支持 {namespace, name} 或 {sessionId} 两种互斥的 Session 引用，由 sessionManager 从持久归属解析实例，请求不能临时替换归属。Runtime 在接纳命令时固定 Session 与具体操作目标，此后的名称复用或恢复不改变已接纳命令的目标。

查询身份、历史、能力和当前状态不激活 Runtime。未知的当前状态保持未知，不能填为空闲。准确字段见 [Runtime 与会话操作](design.md)。

应用服务模块 封装持久记录和运行连接的协作，使调用者无需自行补偿半完成创建或管理迟到资源。通用生命周期逻辑通过内部 seam 使用 adapter，不依赖某个 Agent 的原生对象做判断。

## 对 RuntimeService 的内部依赖

SessionService 通过Service 模块维护的运行绑定取得运行时模块提供的 RuntimeDriver；公共 getRuntime 只返回信息。名称引用先解析为 sessionId，再调用 Driver。运行绑定、使用责任及恢复材料由运行时模块的 Driver 内部管理；关闭与事实排空确认完成后，Service 模块结算在途操作。

停止 Runtime 时，由 Service 内部的同一停止操作结算目标实例下全部会话及打开预留，并协调原 Driver 的释放和最终事件结算；不要求组装层在 RuntimeService 与 SessionService 之间驱动多步协议。单会话操作不自行停止共享资源。完成条件与未知结果规则由 [Service 停止协调](design.md#停止-runtime-的协调职责)统一维护，内部调用结构由 Implementation 决定。

## 全量查询与目标解析

`listSessions()` 以应用服务模块管理的完整持久会话目录为范围，包含不同 Runtime 下的在线与离线会话。按 Runtime 筛选是可选条件；state、分页与排序规则见[Service 查询与订阅](design.md#查询与订阅)。查询不要求连接各 Agent，也不激活已停止的 Runtime。只能证明的当前状态才可返回，其他状态保持未知。

已有会话的查询、输入、交互、取消、close 和恢复都通过统一 Session 入口。sessionManager 解析 Session 引用、读取持久归属，再在内部定位 Runtime 实例。SessionRef 可以只提供全局唯一的 sessionId，或提供 namespace/name；两种形式必须解析到同一份持久记录，不接受混用字段。

全量查询指本应用服务模块管理的会话集合，不承诺自动发现或导入 Agent 侧尚未纳管的会话。运行资源失联不删除持久会话；目录本身读取失败时明确失败，不能将不完整结果伪装成全量结果。

## 事件路由

每个 Driver 的全部 Session 共用 RuntimeDriver.events() 返回的一条事件流，Service 是唯一消费者；Service 在 open/resume 时传入 DriverSessionAddress，adapter 按原运行绑定补齐 namespace/name；接收循环按 sessionId 定位已有会话绑定，核对名称后投递到对应会话的历史提交逻辑和观察通道，不要求每条事件查询 MetaStore。Driver 不负责按名称选通道，也不维护完整资源目录。

按 sessionId 或 namespace/name 订阅时，SessionService 都先解析到同一个持久 Session 身份，因此定位同一个 EventBroadcast。Service 维护 sessionId → Broadcast 映射，恢复不新建竞争对象；subscribeEvents 只委托 Broadcast.subscribe，由 Broadcast 内部创建队列 Handler、按加入顺序登记后返回异步 Subscription。Service 直接调用 EventStore 完成持久事实提交，再调用 Broadcast.publish(event)；Broadcast 只管实时 Handler 分发，不管理历史或运行关联。预览不冒充已提交历史。

事件路由还验证内部固定的 Runtime 名称、原 Driver 与运行来源。sessionId 只标识持久会话，不能证明事件来自本次运行；恢复后的旧事件在 Service 交付前由原操作核验和结算；这不形成 Broadcast 的逐事件运行上下文。Driver 的共享流内部事件关联保留原运行来源，不能收到事件后读取“当前运行”并重新归属。

adapter 在获得真实 sessionId 前暂存原生早期通知，补齐名称和身份后才交给 Driver.events()；创建返回前或目录提交前到达 Service 的完整事件由原创建操作暂存，目录提交成功才向外发布。暂存有界，完整事实不能静默丢弃；超限需背压或明确失败。未知 ID、重复 ID 冲突及已失效来源不得猜测目标通道；失败由原操作结算，不能误投其他会话。

流的方法、顺序、背压和生命周期见[Driver 事件流](../runtime/events.md#driver-事件流)。Service 在启动 Driver 前取得并持续读取流，按 Session 分别处理以保留单会话顺序、隔离慢会话；关闭单 Session 不关闭共享流。Driver 级流失败通过 OperationError 处理，不伪造 sessionId。外部 Session 订阅由 Service 解析身份和准入后委托同一个 Broadcast 的队列 Handler，不能直接消费或再次取得 Driver 流。

## 创建与公开成功

1. 校验名称（包括下述禁止 UUID 形式的规则）、实例、会话参数和已知能力；预留 namespace/name 唯一键及实例引用，固定 Runtime 名称、原 Driver 与配置。
2. 在产生副作用前分配本次操作关联。此时尚无 sessionId，也不伪造一个。所属 Runtime 尚未启动时，由 Service 按名称串行协调并按需启动；并发创建、恢复与显式 start 共用同一次启动，不要求调用者预先启动。
3. 启动成功后通过Service 模块的运行绑定取得 Driver，重新确认停止栅栏及业务准入，再由 RuntimeDriver 接收 namespace/name 并请求 adapter 打开会话。启动失败不调用原生打开；未知释放不能按需重建。原生打开期间不阻止所有其他名称的操作。
4. 收到真实 sessionId 和能力后，先校验 [Session ID 合法性](#session-id-合法性)，再从Service 侧运行绑定取得生效配置修订，原子检查并登记 sessionId 全局唯一索引，与 namespace/name、spec.runtimeRef 和会话配置一起持久保存。保存不能覆盖其他操作或实例的记录；冲突时不公开成功，并只清理本次取得的运行资源。
5. 保存成功后安装连接、接受消费者并公开会话成功。原生连接建立不等于已经完成这一步。

会话创建不隐式发送输入。SessionCreate 可显式提供顶层 initialInput，使用既有 SessionInput 语义。输入内容与 provider 支持在原生打开或提交前校验并快照；显式空输入拒绝，省略则零提交。initialInput 不属于 SessionSpec、持久资源或恢复材料，resume/load/restart 绝不重放。

显式输入由原 create 所有者提交一次，不在创建成功后重新寻址调用 sendPrompt。adapter 在 Submit 前登记工作；Claude 在等待真实 init 前提交调用者输入，Codex 在真实 thread 打开后提交。早到事实在有界缓冲中保持原来源和顺序；init 不重置已完成工作。没有真实身份时的权限请求不能静默跳过或用假 ID 回答；不支持的顺序明确失败并结算自有资源。未知归属不能结算当前工作。

创建成功返回 SessionCreated：既有 Session 字段和显式输入的 initialInputReceipt。回执 result 原样映射 SDK 四态：Claude 成功仅为 `applied_best_effort`，Codex 新提交为 `applied_new_work`；不得把 best-effort 升级为已纳入当前工作。Claude sent 仅证明本地写入，不声称 nativeAccepted；Codex nativeAccepted 来自原生 turn/start 响应，两者均不推断 nativeRecorded 或完成。回执不是持久资源字段。持久身份提交先于历史与实时订阅发布；资源和事件存储不是跨存储事务，创建成功不保证早到历史已全部落盘。已提交事实通过既有历史查询读取，实时订阅不补历史。

输入可能在资源提交前已产生业务副作用。提交失败、调用者断开和 Stop 只结算原所有者的资源与事实，不撤销业务副作用、不盲目重发、不停止健康共享 Runtime。未知结果保留预留与责任；查询持久身份和历史仅提供已有证据，不提供 create 幂等重试保证。名称预留、持久记录和运行资源必须始终能归属于具体操作。

| 结果 | 所有权与后续处理 |
|---|---|
| 明确未创建 | 没有取得资源时释放预留；取得过资源则先完成其结算和清理 |
| 持久化失败 | 不公开成功；只清理本次会话运行资源，保留健康共享资源 |
| 打开或清理结果未知 | 返回 code=outcome_unknown 的 OperationError，不伪造 ID；内部保留操作关联、预留与资源责任，结算迟到结果 |
| 已提交成功 | 返回持久身份和当前绑定观察；消费者才可使用该会话 |

调用者断开不撤销已经接纳的创建。结果未知期间，同名新请求不能覆盖原操作，也不能导致重复创建。内部操作关联本身不提供客户端幂等保证；当前三字段 OperationError 不公开操作关联，外部查询机制仍待定义。

## Close 与空闲回收

closeSession 从任意状态接纳。sessionManager 先在 Session 操作序列中建立关闭栅栏，拒绝新的 prompt、answer、cancel 和 resume；已经接纳的操作、原运行和迟到事实仍由原所有者结算。close 与 prompt、resume 竞争时，由同一序列确定先后：业务操作先被接纳的，close 随后接管其清理；close 先被接纳的，业务操作看到 `closing` 并在派发前失败。get、list、history 和 watch 不受关闭栅栏影响。

有当前 SDK Session 句柄时，包括 close 接管尚未完成的 open 或 resume，sessionManager 调用 Driver.closeSession；Driver 必须把 active/sending 状态交给底层 SDK，而不是提前判定工作未结算。open、resume 或 load 尚未产生句柄时，close 保持 `closing` 并接管原操作结果，不新建 open/resume；原操作产生句柄后再调用 SDK close，明确证明没有产生原生资源时才可完成内部清理到 `offline`，失败或未知继续保持 `closing`。没有当前句柄的 `unknown` Session 只完成 MASS 内部清理：结算已接纳交付与发布，终结未完成 open、resume 或 load 的本地责任并保留其部分事实和真实错误，无法结算时保持 `closing`；这里的 `offline` 不声称外部 provider 工作或旧资源已经停止。对 `offline` 再次 close 直接成功；对 `closing` 再次 close 是显式重试，已确认 SDK close 成功或本次 close 已证明无需 SDK 目标时继续 MASS 结算，已有 native close 结果未确认且无句柄时返回错误并保持 `closing`。

对需要调用 SDK 的 close 尝试，SDK close 成功是唯一 native 成功条件。成功后清除运行版本、能力和实际功能，结算已接纳的 delivery、history commit 与实时发布责任，将状态写入 `offline`，并保留持久身份、已提交历史、Broadcast、健康 Handler、订阅和恢复材料。SDK close 已成功但 MASS 内部结算失败或未知时保持 `closing` 并返回错误，后续显式 close 继续结算，不能先公开 `offline`。close 不取消已经发生的外部副作用，不关闭共享 Driver，不停止 Runtime，也不会因这是最后一个 Session 而回收 Runtime。

SDK close 失败或结果无法确认时保持 `closing`，记录可公开且脱敏的原因并返回错误；不会自动回到可输入状态，也不会自动重试。关闭期间及失败后，Session 的关闭栅栏持续存在；只有再次显式 close 才能推进。单 Session close 的调用者放弃等待只结束其等待，原 close 责任不因此消失。完整状态顺序与结算规则见[Service 的 Session close 合同](design.md#session-close)。

daemon 的空闲回收使用与人工 close 相同的入口。配置、计时条件、活动重置、到期重新校验和失败行为见 [Service 的空闲自动 close](design.md#session-空闲自动-close)；查询、历史和 watch 不延长生命周期，自动失败同样保持 `closing` 且不自动重试。

## 恢复

恢复在同一 Session 内与 close 和其他打开操作串行协调；已有有效运行控制者、`closing` 或仍有打开结算的会话不能建立替代运行连接。

1. 读取并校验未被替换的持久元信息、Runtime 名称归属及配置绑定。
2. 先校验持久 sessionId 的合法性，再由会话历史验证全部已提交公共记录的格式、身份和连续顺序。
3. 由 adapter 校验公共记录所需的私有事实证据；验证通过后才处理未提交尾部。
4. 所属 Runtime 尚未启动时，按[按需启动规则](design.md#状态与配置生效)启动；启动失败不继续恢复，旧运行责任未结算时拒绝冲突激活。通过Service 模块的运行绑定取得 Driver，重新确认停止栅栏及业务准入。读取当前 Runtime spec，把 features 按字段、mcpServers 按名称与 Session 显式值合并，连同原会话的 namespace/name 调用 resumeSession；Driver 在内部验证稳定恢复材料，再连接原会话。恢复材料验证失败不得连接或新建会话。
5. 验证返回的 sessionId 合法且与保存身份相同，再启用本次会话运行，保留历史顺序与已存事件的 UUID，同步位置从完整文件恢复。

失败不新建替代会话，不将旧输入、工具动作或处理器动作重新执行，不把原生旧内容自动导入为新历史。返回不同 ID 的运行资源要按原操作所有权清理，不能替换持久身份。

从 `offline` 恢复且确认本次资源已释放时保持 `offline`；副作用或清理结果无法确认时进入 `unknown`。从重启后的 `unknown` 恢复时，失败保持 `unknown`，不能因本次 SDK 调用返回失败或被清理就伪称 `offline`。显式 resume 只恢复同一原生会话与执行连接，不重放旧输入、工具调用、待处理交互或其他历史工作。

恢复可显式提供仅本次操作使用的 SessionResume.initialInput，使用既有 SessionInput；省略仍零输入，显式空输入和不支持的内容或可选恢复输入能力在原生恢复、提交及运行激活副作用前拒绝。输入快照由原恢复所有者提交一次，不持久为 spec、status 或恢复材料；后续 resume/load/restart 不重放。

显式输入恢复的排序区分目标验证与实际身份观察：先校验保存的恢复身份、cwd 与归属，再定向恢复原生原会话。Claude 使用原 ID 的 --resume 启动，在等待 system/init 前登记工作并提交本次新输入；不能以等待 init 作为提交前置条件。Codex 先恢复并验证原 thread，再提交。只有实际观察到与保存身份、cwd 一致的原生会话后，才可启用运行、公开成功和回执，或向历史/实时订阅 发布本次早到事实；请求中的 ID、初始化 ACK 或本地发送回执都不代替此身份门。

成功返回 SessionResumed，包含既有 Session 字段和可选的本次 initialInputReceipt；回执不持久。result 原样映射 SDK 四态，`applied_best_effort` 不代表输入已并入当前工作，`not_applied` 也不能伪装成成功。Claude sent 仅表示本地写入，Codex nativeAccepted 来自原生 turn/start 响应，不表示记录或完成。工作在提交前登记，身份验证前或回执返回前的终态事实保持原关联和顺序，安装回执不能重置已完成工作。未证归属、冲突及旧来源不能猜测关联。

输入在身份观察或本次绑定提交前可能已经产生不可回滚的业务效果。此后的身份/cwd 不符、提交失败、取消或资源释放确认均不能声称 NotSent 或撤销效果；失败不返回成功回执，不替换持久身份，不发布未经验证的事实，只清理原恢复操作的资源。资源释放与业务执行结果分别表达，不确定执行返回 outcome_unknown 且不自动重发；清理未知保留责任并阻止冲突恢复。调用者放弃等待不撤销原所有者；Stop/Shutdown 继续拥有取消、迟到结算与已接纳事实排空责任。

不支持恢复的类型应在修尾和激活前明确失败。Runtime 按 spec.runtimeRef 名称定位，不比较其 uid；Driver 必须验证原生恢复材料，远端原生会话归属不能仅凭相同 URL 推定。

服务启动不调用本节的恢复流程。启动前先按[服务重启与状态校正](design.md#服务重启与状态校正)处理持久状态：旧 archived 记录迁移为 offline，原本 offline 且无未结算责任的保持 offline，旧在线状态缺少结算证明的为 unknown。查询不隐式启动或恢复，输入也不替代显式 resume；恢复一个 Session 不自动恢复同 Runtime 的其他 Session。

## 资源结构与版本

Session 名称遵循 DNS subdomain 规则，namespace 遵循 DNS label 规则；namespace/name 唯一。创建时 metadata.name 还必须通过[名称校验](#创建名称校验)。namespace 用于分组寻址，不替代权限隔离。createdAt 在首次持久创建时写入，之后不因恢复或 close 修改。查询默认跨 namespace，可显式筛选。Session 的 Runtime 归属仅使用 spec.runtimeRef 名称。接纳操作时固定原 Driver 和具体运行/工作目标，名称复用不会改变已接纳操作的目标。

Session 采用 metadata/spec/status：metadata 直接共用 ObjectMetadata，保存 namespace、name、uid、createdAt、generation；spec 保存 runtimeRef 字符串形式的 Runtime 名称、cwd、systemPrompt、additionalDirs、初始显式 config、会话功能请求 features 与显式 MCP 覆盖 mcpServers；status 保存 Agent 返回的 sessionId、统一的 SessionState、运行能力与本次运行实际生效的 effectiveFeatures。sessionId 即使离线也必须持久保留。

Session.status.appliedGeneration 表示本次会话运行应用的 Session spec 版本，runtimeGeneration 表示它使用的 Runtime 类型配置快照版本。后者来自 Service 模块保存的 Driver 配置快照，不能复制 Runtime 的最新期望 generation；Session 默认值不属于该快照。打开或恢复成功后记录这两个版本、协商能力和实际生效功能；确认 close 后清除当前运行版本、协商能力和 effectiveFeatures，保留身份与历史；结果未知时保留最后确认事实并表达 `closing` 或 `unknown`。features 先继承当前 Runtime 默认值；Runtime 和 Session 都省略时各字段使用 schema 默认值。请求开启但 Driver 不支持时 effectiveFeatures 对应值为 false，请求本身不被拒绝。

当前未增加 updateSession。close、恢复、输入、交互与状态变化不改变 Session spec 或 generation。Runtime 更新不自动改变 Session spec；create、resume 和 load 在每次打开时重新读取当前 Runtime 的 features/mcpServers 默认值并合并，已打开且仍有效的 Session 不热更新。重新启动 Runtime 后恢复 Session 仍须验证新的类型 config 与原会话兼容。

Session spec 的字段参考 agentrun 后按本项目职责筛选；bundleDir 保留内部管理，_meta 不作为透传入口。systemPrompt 只用于会话配置，创建与恢复不隐式发送用户 prompt。详见[字段合同](design.md#session-spec-与-agentrun-的参考关系)。

## 创建名称校验

创建 Session 时，metadata.name 必须满足 ResourceName 的 DNS subdomain 约束，并且不得是 UUID 形式。UUID 形式按以下完整字符串模式识别；保留所有版本位与 variant 位组合，包括全零值，不依赖某个 UUID 库支持的版本：

```text
^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$
```

该规则由 SessionService 在名称预留、持久写入与调用 RuntimeDriver 之前执行，JSON-RPC、CLI 和进程内调用采用相同约束。拒绝时返回 code=invalid_argument、reason=session_name_is_uuid；远程入口保留业务错误分类。此限制仅针对 Session 创建名称，不改变 Runtime 名称约束，也不把原生 sessionId 限制为 UUID。

创建名称限制保持独立于传输；原生 ID 仍可能是其他字符串。[显式 SessionRef](../../bindings/jsonrpc.md#方法与寻址) 区分名称与 ID，不使用 nameOrID 解析。查询、resume、close 和历史读取不重新应用创建名称校验，既有持久身份不因此失去可访问性。

## 并发与资源隔离

实例引用预留与删除检查协调，避免检查“无引用”后又接入新 Session。Runtime 元数据更新不阻塞创建；打开会话绑定已启动运行的生效快照，不按最新期望 spec 改绑。不同 Session 的正常创建和查询不应依赖某个迟到打开操作完成。

任何回滚只操作本次创建的记录与资源，不删改已有会话、凭据或运行数据。名称与 ID 都不直接构成可由用户选择的文件路径。实现可以使用任意语言机制满足这些保证。

## 验收场景

| 场景 | 应观察到的行为 |
|---|---|
| 不同 namespace 使用同名 Session | 分别创建与查询，按 namespace/name 隔离寻址 |
| 创建 UUID 形式的名称（含全零值） | invalid_argument / session_name_is_uuid；不预留名称、不写入资源、不调用 Driver；各调用入口一致 |
| 同一 namespace 重复创建同名 Session | 唯一键冲突，不覆盖已有资源 |
| 原始 sessionId 为 `.` 或 `..` | ID 引用在查询或副作用前拒绝；原生返回时不公开成功或登记身份，并结算该操作的资源责任 |
| 两实例返回相同 sessionId | 拒绝第二次登记，不覆盖已有记录；按原操作清理本次运行资源 |
| 通过 sessionId 与 namespace/name 查询同一会话 | 两种引用返回同一身份，均无需提供 Runtime |
| 同时提供 sessionId 和名称字段 | 拒绝含糊引用 |
| 部分 Runtime 停止或失联 | 仍可查询其持久会话，查询不隐式启动或恢复 |
| 指定 Runtime 筛选会话 | 从同一目录返回对应子集 |
| 打开成功、持久化失败 | 无成功会话被公开；只清理本次资源 |
| 打开超时后迟到返回 ID | 原操作继续结算；同名创建仍受预留约束 |
| close 与 prompt/resume 并发 | 同一 Session 操作序列决定先后，不产生越过关闭栅栏的新派发或两个有效运行控制者 |
| 已提交历史后部损坏 | 即使请求第一页，也明确失败；不先修尾或激活 |
| 冷恢复成功 | 保持原 ID、已提交历史前缀、已存 UUID 与同步位置；不重演业务动作 |
| Runtime 默认值已更新后的恢复 | 使用最新 features/mcpServers 合并结果建立连接；不因旧默认值不同拒绝恢复，Session spec 不写回结果 |
| Session 显式 MCP 与 Runtime 默认同名 | 用 Session server 整体替换默认项并保持默认顺序；Session 新名称追加，空列表不删除默认项 |
| 同名 Runtime 的资源 uid 改变 | 按名称定位并由 Driver 验证恢复，不因 uid 不同拒绝 |

## 待决事项

未知操作的公开查询、跨重启保留与客户端幂等键见[Service 合同](design.md)。本轮未新增会话删除功能。

[^direction]: 当前讨论确定 Service 依赖 Store 持久化资源、依赖 Runtime 执行运行操作；通信入口只做协议映射，具体接口由负责人定义。

## 从原生会话加载历史

SessionService.loadSession 使用新的 namespace/name、Runtime、绝对 cwd 和已知原生 ID，遵守 [异步 load](load.md)。持久预留的 ID 在 nativeVerified 前是待验证主张，不能公开宣称原生已存在。建立 load 接收绑定后才启动生产；原生验证、完整回放及本地完成异步记录在 status.historyLoad。加载期间为 opening 并封闭业务输入；部分失败不激活。原生恢复、全部回放及本地结算成功后保留同一运行，直接接受后续输入，无需额外 resume；当前状态只依据真实当前证据，普通恢复不导入历史。

load 输入还可在顶层携带可选的 `features` 与 `mcpServers` 显式覆盖。它们作为新 Session 的显式 spec 值持久保存；Service 在调用 Driver 前读取当前 Runtime 默认值并合并，合并结果只用于本次原生恢复，不复制进 Session spec 或恢复材料。空 MCP 列表不删除 Runtime 默认项。

主 Session 的初始 `config.model/effort`、瞬态 setter、原运行在途责任及错误以[配置合同](../runtime/session-config.md)为准。初始值保存在 Session spec，create/resume/load 传给 SDK；setter 不持久，close/resume 重新请求初始值。可变配置不参与恢复身份验证。
