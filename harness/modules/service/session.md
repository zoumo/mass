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

本文是 [应用服务模块](design.md) 内部 sessionManager 的职责展开。它统一管理全部 Runtime 实例的持久 Session，负责会话目录、名称预留、Runtime 名称归属、命令路由、创建发布、归档和恢复编排。调用者使用独立的 SessionService 公共接口。SessionService 实现通过运行时模块提供的 RuntimeDriver 协调原生控制与会话历史。[^direction]

Agent 生成 sessionId，MASS 校验其合法性后原样保存；用户命名 sessionName 与它独立。sessionId 在本模块管理的全部 Runtime 和 namespace 中唯一；Runtime 名称归属仍用于路由与恢复验证。内部存储定位符不成为第二个公共会话 ID。

Session 的持久存在与连接状态独立。断线或归档不删除身份和历史。恢复重新建立运行连接，同一持久会话不能同时具有两个相互竞争的有效运行控制者。

## Session ID 合法性

sessionId 的完整原始值不得等于 `.` 或 `..`。这是身份合法性规则，适用于创建、恢复、持久身份和所有按 ID 的操作；不通过 URL 编码、裁剪、改名或生成替代 ID 绕过。包含点号的其他值（例如 `a.b`、`a..b`）不因本规则被拒绝。

客户端在构造 URL 前校验原始 ID；HTTP 对路径参数完成一次 URL 解码后校验，非法值返回 400 invalid_argument，reason=invalid_session_id。SessionService 的进程内 ID 引用也必须在资源查询或副作用之前拒绝同样的值。nameOrID 解码后等于 `.` 或 `..` 时同样拒绝，不进入名称/ID 匹配。

原生打开或恢复若返回上述非法 ID，不得公开创建或恢复成功、登记为合法持久身份或发布对应会话事件；按原操作的资源清理与未知结果规则结算，不伪造新 ID。读取到这类非法持久身份时明确失败，不能将其恢复为合法会话。

## 对外保证

创建通过统一入口按 spec.runtimeRef 指定目标实例，metadata.namespace/name 指定会话唯一键。恢复和后续操作支持 {namespace, name} 或 {sessionId} 两种互斥的 Session 引用，由 sessionManager 从持久归属解析实例，请求不能临时替换归属。Runtime 在接纳命令时固定 Session 与具体操作目标，此后的名称复用或恢复不改变已接纳命令的目标。

查询身份、历史、能力和当前状态不激活 Runtime。未知的当前状态保持未知，不能填为空闲。准确字段见 [Runtime 与会话操作](design.md)。

应用服务模块 封装持久记录和运行连接的协作，使调用者无需自行补偿半完成创建或管理迟到资源。通用生命周期逻辑通过内部 seam 使用 adapter，不依赖某个 Agent 的原生对象做判断。

## 对 RuntimeService 的内部依赖

SessionService 通过Service 模块维护的运行绑定取得运行时模块提供的 RuntimeDriver；公共 getRuntime 只返回信息。名称引用先解析为 sessionId，再调用 Driver。运行绑定、使用责任及恢复材料由运行时模块的 Driver 内部管理；关闭与事实排空确认完成后，Service 模块结算在途操作。

停止 Runtime 时，SessionService 接受 内部组装层的 drainRuntimeSessions 调用，结算目标实例下全部会话及打开预留。排空确定完成后交回证明，由组装层继续请求 RuntimeService 释放资源。SessionService 不自行停止共享资源，RuntimeService 不反向调用 SessionService。具体次序与未知结果规则由 [应用服务模块 停止协调](design.md#停止-runtime-的协调职责)统一维护。

## 全量查询与目标解析

`listSessions()` 以应用服务模块管理的完整持久会话目录为范围，包含不同 Runtime 下的在线、离线与已归档会话。按 Runtime 筛选是可选条件；state、分页与排序规则见[Service 查询与订阅](design.md#查询与订阅)。查询不要求连接各 Agent，也不激活已停止的 Runtime。只能证明的当前状态才可返回，其他状态保持未知。

已有会话的查询、输入、交互、取消、归档和恢复都通过统一 Session 入口。sessionManager 解析 Session 引用、读取持久归属，再在内部定位 Runtime 实例。SessionRef 可以只提供全局唯一的 sessionId，或提供 namespace/name；两种形式必须解析到同一份持久记录，不接受混用字段。

全量查询指本应用服务模块管理的会话集合，不承诺自动发现或导入 Agent 侧尚未纳管的会话。运行资源失联不删除持久会话；目录本身读取失败时明确失败，不能将不完整结果伪装成全量结果。

## 事件路由

每个 Driver 的全部 Session 共用 RuntimeDriver.events() 返回的一条事件流，Service 是唯一消费者；Service 在 open/resume 时传入 DriverSessionAddress，adapter 按原运行绑定补齐 namespace/name；接收循环按 sessionId 定位已有会话绑定，核对名称后投递到对应会话的历史提交逻辑和观察通道，不要求每条事件查询 MetaStore。Driver 不负责按名称选通道，也不维护完整资源目录。

按 sessionId 或 namespace/name 订阅时，SessionService 都先解析到同一个持久 Session 身份，因此使用同一条逻辑通道。完整事实必须先提交，再向订阅者和处理器发布；预览走实时观察路径，不冒充已提交历史。

事件路由还验证内部固定的 Runtime 名称、原 Driver 与运行来源。sessionId 只标识持久会话，不能证明事件来自本次运行；恢复后的旧事件仍由原运行结算，不能更新新运行的状态。Driver 的共享流内部事件关联保留原运行来源，不能收到事件后读取“当前运行”并重新归属。

adapter 在获得真实 sessionId 前暂存原生早期通知，补齐名称和身份后才交给 Driver.events()；创建返回前或目录提交前到达 Service 的完整事件由原创建操作暂存，目录提交成功才向外发布。暂存有界，完整事实不能静默丢弃；超限需背压或明确失败。未知 ID、重复 ID 冲突及已失效来源不得猜测目标通道；失败由原操作结算，不能误投其他会话。

流的方法、顺序、背压和生命周期见[Driver 事件流](../runtime/events.md#driver-事件流)。Service 在启动 Driver 前取得并持续读取流，按 Session 分别处理以保留单会话顺序、隔离慢会话；关闭单 Session 不关闭共享流。Driver 级流失败通过 OperationError 处理，不伪造 sessionId。外部 Session 订阅由 Service 广播，不能直接消费或再次取得 Driver 流。

## 创建与公开成功

1. 校验名称（包括下述禁止 UUID 形式的规则）、实例、会话参数和已知能力；预留 namespace/name 唯一键及实例引用，固定 Runtime 名称、原 Driver 与配置。
2. 在产生副作用前分配本次操作关联。此时尚无 sessionId，也不伪造一个。
3. 通过Service 模块的运行绑定取得 Driver，再由 RuntimeDriver 接收 namespace/name 并请求 adapter 打开会话。原生打开期间不阻止所有其他名称的操作。
4. 收到真实 sessionId 和能力后，先校验 [Session ID 合法性](#session-id-合法性)，再从Service 侧运行绑定取得生效配置修订，原子检查并登记 sessionId 全局唯一索引，与 namespace/name、spec.runtimeRef 和会话配置一起持久保存。保存不能覆盖其他操作或实例的记录；冲突时不公开成功，并只清理本次取得的运行资源。
5. 保存成功后安装连接、接受消费者并公开会话成功。原生连接建立不等于已经完成这一步。

会话创建不隐式发送输入。名称预留、持久记录和运行资源必须始终能归属于具体操作。

| 结果 | 所有权与后续处理 |
|---|---|
| 明确未创建 | 没有取得资源时释放预留；取得过资源则先完成其结算和清理 |
| 持久化失败 | 不公开成功；只清理本次会话运行资源，保留健康共享资源 |
| 打开或清理结果未知 | 返回 code=outcome_unknown 的 OperationError，不伪造 ID；内部保留操作关联、预留与资源责任，结算迟到结果 |
| 已提交成功 | 返回持久身份和当前绑定观察；消费者才可使用该会话 |

调用者断开不撤销已经接纳的创建。结果未知期间，同名新请求不能覆盖原操作，也不能导致重复创建。内部操作关联本身不提供客户端幂等保证；当前三字段 OperationError 不公开操作关联，外部查询机制仍待定义。

## 归档

统一 Session 管理接口接收归档操作，sessionManager 协调原 Runtime 执行[关闭与释放规则](design.md)。释放确认前，Session 保持关闭未确定状态，并阻止冲突恢复。

关闭超时不删除 Session，也不宣告资源释放。迟到证据可以使后续查询变为已离线，但不改写先前返回的未知结果。归档保留历史与非运行资源消费者；同一 Session 恢复后继续原消费顺序。

## 恢复

恢复在同一 Session 内与归档和其他打开操作串行协调；已在线、关闭未确定或仍有打开结算的会话不能建立替代运行连接。

1. 读取并校验未被替换的持久元信息、Runtime 名称归属及配置绑定。
2. 先校验持久 sessionId 的合法性，再由会话历史验证全部已提交公共记录的格式、身份和连续顺序。
3. 由 adapter 校验公共记录所需的私有事实证据；验证通过后才处理未提交尾部。
4. 通过Service 模块的运行绑定取得 Driver，传入原会话的 namespace/name 并调用 resumeSession；Driver 在内部验证恢复材料，再连接原会话。恢复材料验证失败不得连接或新建会话。
5. 验证返回的 sessionId 合法且与保存身份相同，再启用本次会话运行，保留历史顺序与已存事件的 UUID，同步位置从完整文件恢复。

失败不新建替代会话，不将旧输入、工具动作或处理器动作重新执行，不把原生旧内容自动导入为新历史。返回不同 ID 的运行资源要按原操作所有权清理，不能替换持久身份。

不支持恢复的类型应在修尾和激活前明确失败。Runtime 按 spec.runtimeRef 名称定位，不比较其 uid；Driver 必须验证原生恢复材料，远端原生会话归属不能仅凭相同 URL 推定。

## 资源结构与版本

Session 名称遵循 DNS subdomain 规则，namespace 遵循 DNS label 规则；namespace/name 唯一。创建时 metadata.name 还必须通过[名称校验](#创建名称校验)。namespace 用于分组寻址，不替代权限隔离。createdAt 在首次持久创建时写入，之后不因恢复或归档修改。查询默认跨 namespace，可显式筛选。Session 的 Runtime 归属仅使用 spec.runtimeRef 名称。接纳操作时固定原 Driver 和具体运行/工作目标，名称复用不会改变已接纳操作的目标。

Session 采用 metadata/spec/status：metadata 直接共用 ObjectMetadata，保存 namespace、name、uid、createdAt、generation；spec 保存 runtimeRef 字符串形式的 Runtime 名称、cwd、systemPrompt、additionalDirs；status 保存 Agent 返回的 sessionId、统一的 SessionState 与运行能力。sessionId 即使离线也必须持久保留。

Session.status.appliedGeneration 表示本次会话运行应用的 Session spec 版本，runtimeGeneration 表示它使用的 Runtime spec 版本。后者来自Service 模块保存的 Driver 配置快照，不能复制 Runtime 的最新期望 generation。打开或恢复成功后记录这两个版本；确认关闭后清除当前运行版本和协商能力，保留身份与历史；结果未知时保留最后确认事实并表达不确定状态。

当前未增加 updateSession。归档、恢复、输入、交互与状态变化不改变 Session spec 或 generation。Runtime 更新不自动改变 Session spec；重新启动 Runtime 后恢复 Session，需要验证新运行配置与原会话兼容。

Session spec 的字段参考 agentrun 后按本项目职责筛选；bundleDir 保留内部管理，_meta 不作为透传入口。systemPrompt 只用于会话配置，创建与恢复不隐式发送用户 prompt。详见[字段合同](design.md#session-spec-与-agentrun-的参考关系)。

## 创建名称校验

创建 Session 时，metadata.name 必须满足 ResourceName 的 DNS subdomain 约束，并且不得是 UUID 形式。UUID 形式按以下完整字符串模式识别；保留所有版本位与 variant 位组合，包括全零值，不依赖某个 UUID 库支持的版本：

```text
^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$
```

该规则由 SessionService 在名称预留、持久写入与调用 RuntimeDriver 之前执行，HTTP、CLI 和进程内调用采用相同约束。拒绝时返回 code=invalid_argument、reason=session_name_is_uuid；HTTP 映射为 400。此限制仅针对 Session 创建名称，不改变 Runtime 名称约束，也不把原生 sessionId 限制为 UUID。

禁止 UUID 形式的名称可以避免 UUID ID 与新名称重叠，但原生 ID 仍可能是其他字符串；[HTTP nameOrID 解析](../../bindings/http.md#nameorid-解析)继续处理非 UUID ID 与名称冲突。查询、恢复、归档和历史读取不重新应用创建名称校验，既有持久身份不因此失去可访问性。

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
| 归档与恢复并发 | 不产生两个相互竞争的有效运行控制者 |
| 已提交历史后部损坏 | 即使请求第一页，也明确失败；不先修尾或激活 |
| 冷恢复成功 | 保持原 ID、已提交历史前缀、已存 UUID 与同步位置；不重演业务动作 |
| 同名 Runtime 的资源 uid 改变 | 按名称定位并由 Driver 验证恢复，不因 uid 不同拒绝 |

## 待决事项

未知操作的公开查询、跨重启保留与客户端幂等键见[Service 合同](design.md)。本轮未新增会话删除功能。

[^direction]: 当前讨论确定 Service 依赖 Store 持久化资源、依赖 Runtime 执行运行操作；HTTP 只做协议映射，具体接口由负责人定义。
