---
type: Module Design
title: Runtime 模块设计
description: 按类型校验配置，构造 Driver 并管理实际运行资源。
status: stable
sources:
- id: direction
  resource: 项目负责人在 2026-10-19 关于应用服务与运行时拆分的讨论
  title: 两个模块的职责与依赖
---

# Runtime 模块设计

## 职责与依赖

运行时模块封装不同 Agent 的类型配置、进程或连接、原生会话、恢复和事件来源。它只依赖共享值合同；不导入Service 模块的 Runtime、Session、Metadata、SessionRef、分页或状态模型。[^direction]

[应用服务模块](../service/design.md)保存资源并编排运行；运行时模块管理实际运行资源。资源记录停止后仍存在，Driver 所持的进程或连接则可以释放。

## 接口

| 接口 | 职责 |
|---|---|
| [RuntimeRegistry](runtime-registry.tsp) | 校验类型配置并构造未启动 Driver，不管理当前运行映射 |
| [RuntimeDriver](runtime-driver.tsp) | 启停实例运行资源，操作原生 Session |

类型到具体实现的映射由运行时模块内部维护，不要求 Service 注册、获取工厂或管理插件。未知类型明确失败。

validateConfig(type, config) 独立校验类型与配置，不创建 Driver、不启动 Agent、不修改持久恢复数据。create(type, name, config) 同样校验配置，并构造一个未启动 Driver；未知类型或配置错误返回 OperationError。create 不登记、查找或复用当前运行，不启动 Agent、不修改持久恢复数据；start 才取得运行资源。name 就是 Runtime.metadata.name，供 Driver 标识实例，不是 Registry 的映射键。

## 当前运行的所有权

Service 独占管理当前进程中的 Runtime 名称 → 运行绑定映射，保存原 Driver、配置快照、启动和停止责任。Registry 只隐藏类型到 Driver 构造方式的映射，不保存当前运行，不提供 get/remove；同名并发启动的唯一性由 Service 保证，不由 Registry 的构造调用保证。

服务重启后 Service 的当前运行映射为空，不自动重建 Driver，也不以空映射证明旧资源已经释放。Service 先[校正持久状态](../service/design.md#服务重启与状态校正)，之后由创建/恢复 Session 按需启动所属 Runtime，或由显式 start 启动；旧资源释放未知时不能直接重建。

Service 按名称串行协调构造、运行绑定登记、start、stop 与绑定移除。同名旧运行必须完整释放并结算最终事件后才能移除；启动明确失败且无遗留资源才解除本次绑定，结果未知保留原绑定与责任。迟到清理只处理原绑定，不能移除后来建立的运行。Driver 的执行方法自行检查运行状态。Registry 不处理资源 CRUD、分页、namespace 或配置 generation。

恢复私有状态可以按实例 name 组织，但名称不能直接作为任意文件路径。Session 按 Runtime 名称归属，同名资源的 uid 变化不影响名称解析；Driver 仍验证原生会话恢复材料和当前配置，不能用名称相同替代原生恢复验证。

## 配置与恢复

RuntimeType 为开放字符串，RuntimeConfig 为 Record<unknown>，见[共享类型](../../shared/types.tsp)。配置形状保持开放，由具体类型解释和校验；下列是可采用的字段例子，不是全局必填项或封闭列表：

| 候选 | 需要明确的规则 |
|---|---|
| command / args | 本地程序、默认值、版本与允许参数 |
| env / secretRefs | 环境继承、秘密引用格式与冲突规则 |
| url / authRef | 远端地址、认证和隔离要求 |

RuntimeRegistry 按类型负责配置校验，本地与远端参数不能任意混用。开放字段不表示所有 Driver 都支持任意配置；具体类型自行决定支持项和透传规则。秘密使用引用，解析失败明确报错，公开结果不携带明文秘密。

Driver 内部持久保存和读取恢复材料，不能仅依赖内存。恢复验证原会话、账号及数据归属，不以相同 URL、名称或 ID 格式替代验证；失败不能新建会话代替恢复。closeSession 释放本次运行但保留后续恢复所需材料。

DriverSessionSpec 只描述 cwd、systemPrompt 和 additionalDirs；Service 模块 SessionSpec 复用它并增加 runtimeRef。创建和恢复不隐式发送输入，不支持的选项明确失败。openSession/resumeSession 另接收 DriverSessionAddress（namespace、sessionName）；Service 在副作用前传入名称，adapter 固定在原运行绑定中。地址不是 Session spec，不参与配置版本。可选 CreationDriver 接收独立于 spec 的显式 SessionInput，校验先于副作用，同一打开所有者登记工作后提交；不支持的内容拒绝，不丢字段或转换为隐藏 prompt。恢复始终无初始输入。DriverSession 返回 sessionId、实际能力与仅显式创建时的 initialInputReceipt，配置 generation 由Service 模块的运行绑定记录。

## 远端安全要求

远端类型发送认证信息时必须使用安全传输；明文传输只允许回环地址。地址不得夹带凭据，认证秘密通过引用提供。引用缺失或解析失败时明确报错，不自动使用宿主上的其他凭据补齐。

秘密只在取得运行资源所需的阶段解析，不进入公开状态、错误、事件、历史、恢复材料或测试证据。错误诊断同样不得包含展开后的秘密。

协议版本相同只证明协议准入，不证明服务、账号或数据身份。相同 URL、名称或会话 ID 不能替代恢复归属验证；具体归属要求见[配置与恢复](#配置与恢复)。

远端 Driver 只释放己方拥有的连接和任务，不得停止共享远端服务。己方连接结束不证明远端工作或其他客户端结束，也不能替代 Session 释放证据。

## 生命周期与失败

Driver.start 取得本实例拥有的进程或连接，重复启动不创建第二个所有者。同类型可以有多个独立 Driver；不把类型注册等同于运行实例启动。

Service 模块先封闭接纳并排空会话，再调用 Driver.stop。Driver 若仍有未结算执行必须拒绝停止；释放未知时保留清理责任。远端资源释放遵守[远端安全要求](#远端安全要求)。

命令接纳时固定当前会话运行及工作；旧操作或事件不能在恢复后改绑。返回接受证据不表示工作完成；取消确认不表示前台终止。未知副作用返回 outcome_unknown，不能自动重发。单会话失败不应停止健康共享运行。

Runtime 拥有 [SessionEvent TypeSpec](session-events.tsp) 及[事件设计](events.md)，保持实时、存储和历史读取的统一事件模型。adapter 优先取原始 time，缺失时生成；创建统一事件时生成一次 UUID 作为 eventId，同一事件发布、保存和重试保持不变。Service 与 Store 不重写 ID/时间；暂不做原生事件去重，不承诺原生重放得到相同 UUID。事件不含 seq，EventStore 按追加顺序读取、用游标分页，并返回最后同步的 eventId。Driver 不提交公共历史，不维护完整资源目录或发布资源创建成功；adapter 使用打开/恢复时固定的名称补齐事件，Service 从会话绑定核对归属并提交历史。每个 Driver 通过 events() 提供一条单消费者流，多个 Session 共用，Service 按 sessionId 路由；顺序、背压和生命周期的主规则见[Driver 事件流](events.md#driver-事件流)。

## 事件交付

Service 在 start 前取得 events() 并启动接收，Driver 不为各 Session 或外部订阅者另建公共流。EventStream.next 的正常结束与错误终止分别表达；流异常不等于 Agent 业务失败或资源释放。只保证单 Session 内保留事件的顺序；Runtime 有界接纳与公平交付，Service 校验并按 Session 路由到其唯一 EventBroadcast，Service 先结算需要持久化的事实，Broadcast 只实时调用 Handler；外部订阅封装队列 Handler，不传运行关联，过载按订阅隔离。正常停止前持续读取和排空，不能先关闭消费循环。

## 验收与待决

类型查找与配置校验不启动 Agent；构造 Driver 不读取资源目录；运行时模块可独立编译。重复启动、错误配置、恢复身份不一致、未知释放与旧事件隔离需要在具体 Driver 实现中验证。事件交付还需验证重复取流拒绝、多个 Session 顺序与公平性、慢会话隔离、有界背压、关闭单会话不关闭共享流、停止排空和异常终止。

远端类型还需验证认证传输限制、秘密脱敏、缺失引用拒绝且不回退其他凭据，以及停止己方连接不影响其他客户端或共享服务。协议握手成功不能代替账号与数据归属验证。

共享类型已定义为开放字符串、配置 map、能力布尔 map、动态交互回答及接受证据，见[共享值说明](../../shared/types.md)。各类型具体配置键、能力名和交互内容由对应 Driver 定义，不能把宽松声明当成已实现能力。接口版本与工具见 [TypeSpec 入口](../../typespec/README.md)。

[^direction]: 当前讨论要求运行时模块负责类型及实际运行能力，应用服务模块负责资源目录和操作编排；依赖单向。

交互请求与结束见[交互合同](interactions.md)，配置选项见[配置合同](session-config.md)。SessionEvent 为 type/payload 判别联合；chunk 只追加、只做有界转发，完整历史不依靠内存累积 delta。

## 恢复并异步回放

显式 loadSession 按 [异步 load](../service/load.md)先恢复同一个原生会话，再沿唯一 Driver 流异步回放全部保留主历史。成功保留恢复的执行连接，本地结算后可直接继续输入；普通 resume 仍不回放。历史带 replay 来源，不能由旧状态恢复工作或审批。接纳、恢复、上游穷尽和本地提交/分发完成分开表达；失败/取消还须释放本次恢复资源，未知保留责任。
