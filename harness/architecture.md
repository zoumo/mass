---
type: Architecture
title: MASS 总体架构
description: MASS 整体逻辑架构：外部入口、模块职责与依赖、资源与运行、事件与持久化及跨模块生命周期。
status: stable
generated:
  by: codex/gpt-6
  at: 2026-10-19 20:10:29+08:00
sources:
- id: direction
  resource: 项目负责人在 2026-10-19 本次 Runtime 职责讨论中的说明
  title: Service、Store 与 Runtime 的职责和协作
---

# MASS 总体架构

本文描述 MASS 的整体逻辑架构：HTTP、CLI 与进程内入口如何进入 Service，Service 如何依赖 Store 与 Runtime，以及资源、原生运行、事件和历史如何协作。模块内部职责与不变量分别由各模块的 design.md 维护；具体 API、数据结构和错误由 TypeSpec 合同维护。

## 模块职责与依赖

| 模块 | 职责 | 接口 |
|---|---|---|
| [Service](modules/service/design.md) | 输入输出、业务校验、资源状态与操作编排 | RuntimeService、SessionService |
| [MetaStore](modules/metastore/design.md) | 资源 CRUD、generation、版本与唯一性；配置选择权威后端 | MetaStore |
| [EventStore](modules/eventstore/design.md) | 文件权威的事件追加与 SQL 查询投影 | EventStore |
| [Events](modules/events/design.md) | 每 Session 一个 Broadcast，统一管理 Handler，订阅封装队列 Handler | EventBroadcast、EventHandler、EventSubscription |
| [Runtime](modules/runtime/design.md) | 类型配置、Driver 构造、实际运行与原生会话 | RuntimeRegistry、RuntimeDriver |

```text
HTTP / CLI / 进程内调用
          │
          ▼
Service：RuntimeService / SessionService
       ├─ MetaStore：元数据；配置选择 sqlite（默认）或 file 权威
       ├─ EventStore：事件历史查询；文件权威 → 可重建 SQL 查询副本
       ├─ Events：每 Session 一个 EventBroadcast → 统一 Handler（订阅为队列 Handler）
       └─ Runtime：RuntimeRegistry → RuntimeDriver
```

MetaStore 与 EventStore 是独立模块，分别管理接口、配置、打开、关闭和恢复，不共享提交事务。MetaStore 的唯一权威由配置选择，默认 SQLite；EventStore 始终文件权威，SQL 仅保存可重建的完整事件查询副本。Service 负责业务协调，两个存储模块不相互调用。

Service 分别依赖 MetaStore、EventStore、Events 和 Runtime。Events 核心只依赖事件与共享类型，只管理实时 Handler，不接收运行关联或历史职责；Service 装配需要的状态/业务 Handler，不直接导入 Service 或存储实现。运行时只依赖共享值合同，不接收资源模型、名称目录或分页。HTTP 负责协议映射，Service 保持与传输无关。[^direction]

## 资源与运行的区别

Runtime 是 metadata/spec/status 资源，停止后仍保留。Driver 是按类型和配置构造的执行对象，实际进程或连接可以停止和重建。getRuntime 返回资源快照；获取资源不授予 Driver，不隐式启动 Agent。

RuntimeRegistry 只按类型校验配置并构造 Driver；create 接收 Runtime 的 name 供 Driver 标识实例，不登记当前运行。Service 独占维护 Runtime 名称到当前运行绑定的映射，绑定原 Driver、运行配置快照和原操作关联；Registry 不提供当前实例的查找或移除，也不解释 Session 的 namespace/name 或资源 generation。Session 全局 sessionId 可以直接寻址，名称引用由Service 模块转换。

Runtime 更新只修改期望配置，已运行 Driver 继续使用原快照。Driver.start 成功后由Service 模块记录 appliedGeneration；DriverSession 不返回 generation。版本一致不证明健康。

## 创建与恢复

1. Service 模块校验名称，读取 Runtime 资源并预留实例引用；资源创建与配置更新通过 RuntimeRegistry.validateConfig(type, config) 校验配置，不创建运行实例。
2. 创建或恢复 Session 时按需启动所属 Runtime，无需调用者先 start；与显式 start 共用按名称串行协调的流程，调用 Registry.create 构造 Driver，由 Service 登记本次运行绑定，再调用 start 并保存本次配置快照。已运行时取得原 Driver；停止中或同一进程仍有旧运行责任时不自动重建。冷进程没有当前运行映射时，显式 start/resume 可接管持久 unknown Runtime，这只表示旧本地 owner 不可达。
3. Driver 创建原生会话并返回 sessionId；Service 模块原子登记全局 ID 索引、名称键和 spec.runtimeRef 名称归属，再公开成功。
4. 持久化失败时，Service 模块协调 Driver 清理本次运行；未知结果保留原操作责任。

恢复先验证原资源归属与已提交历史，再由 Driver 内部读取和验证恢复材料并连接原会话。不能以新建替代恢复。Session 仅按 Runtime 名称归属，不比较 Runtime uid；原生会话与恢复材料仍由 Driver 验证。查询和历史读取不执行激活。

服务重启只恢复持久目录，不自动启动 Runtime 或恢复 Session。Service 在开放业务接口前[校正旧运行状态](modules/service/design.md#服务重启与状态校正)：旧 archived 记录迁移为 offline，原本 offline 且无未结算责任的保持 offline，旧在线状态缺少结算证明的为 unknown；Runtime 保留已确认的 stopped，否则为 unknown。不能以重启前的 idle/running 或空的 Service 当前运行映射证明当前运行可用或旧资源释放；查询、历史和订阅均不触发激活。冷进程无对应运行映射时，显式 start/resume 可接管 unknown Runtime，并视为旧本地 owner 不可达，但仍不证明旧 provider 工作或后端资源已经结束。createSession 与 resumeSession 按需启动 Runtime，sendPrompt、answer 和 cancel 不隐式 resume。

## 事件、历史与名称通道

RuntimeDriver.events() 返回共享 SessionEvent 流，由 Service 独占消费。Service 完成业务路由校验，按 Session 串行处理：持久事件先调用 EventStore.append 完成文件→SQL 提交，再把同一个 SessionEvent 交给 EventBroadcast.publish；临时通知直接发布。文件或 SQL 失败由 Service 保留原 ID 结算，不能称为从未提交。共享读流循环不等待某 Session 的慢持久化或 Handler，Service 的分会话队列有界。

Service 为每个 Session 管理一个实时 Broadcast。sessionId 与 namespace/name 是同一持久身份的两种寻址方式，解析后定位同一对象，不以 Runtime 名称组成 key。Broadcast 直接调用已登记 Handler.handle(event)，统一使用 SessionEvent；不接收来源上下文、持久状态或处理阶段，不访问 EventStore，也不提供重试恢复链。

Broadcast.subscribe 内部创建并加入一个 required=false 的队列 Handler，返回可取消的异步事件流；Service 只解析目标再委托，不组装订阅。每个订阅只有一个有界队列，语言无关的流合同与 Go 映射分别维护。Handler 按加入顺序串行执行，required 失败停止本事件后续调用，普通失败诊断后继续；required 不改变顺序，先前动作不回滚。状态更新 Handler 为 required=false，可自行异步处理，其失败和慢订阅不能阻断健康消费者；状态允许落后，不保证最终追上。业务生命周期必需写入仍由 Service 同步协调，实时事件不是运行控制授权。

Session 和历史业务职责见 [Session](modules/service/session.md)、[历史](modules/service/history.md)；实时分发见 [Events](modules/events/design.md)。实时、历史存取与 Runtime 共用 SessionEvent，保留原 eventId、time、名称和 payload；eventId 不参与排序，历史使用不透明游标。历史查询、SQL 重建和恢复不调用 Broadcast 或 Handler，不补广播。

## 停止协调

Service 模块封闭新激活、会话创建/恢复、输入和回答，排空会话、在途操作与已接纳事实，再调用原 Driver.stop；确认释放和最终事件结算后，由 Service 移除原运行绑定。组装层只负责依赖注入，完整停止流程由 Service 内部的同一停止操作拥有。Driver 对未结算执行仍执行保护检查；结果未知时不能更新为已停止或自动重新接纳。

Session close 与 Runtime 停止由 Service 结算对应运行的业务操作、历史提交和已接纳的实时 publish 调用；Driver 停止期间继续读流，最终上游排空后再确认 Service 的待办已处理，不能过早封闭发布。Session 空闲自动 close 使用同一 close 路径，只回收 idle Session，不停止 Runtime。保留 Session 的 Broadcast、健康 Handler 与订阅，恢复沿用原关系；停止 A 不关闭 B 的 Broadcast。应用 shutdown 才在全部应接纳事件交付完成后关闭各 Broadcast，关闭其 Handler（关闭不等于异步动作全部完成），订阅随队列 Handler 结束。失败和超时如实报告；存储连接由组装层在 Handler 不再使用后关闭。

停止后保留 Runtime 配置、Session 身份和历史。不同实例独立；单会话错误不停止健康共享运行。远程 Driver 只能释放自己拥有的连接与任务。

## 合同与实现状态

[接口版本与校验入口](typespec/README.md)统一管理 Service 与 Runtime 的 TypeSpec。HTTP 与存储映射在 bindings；资源 CRUD、目录分页不进入运行时接口。Runtime 列表不分页，Session 和历史列表可分页。

资源状态、开放配置与能力、消息、事件、分页和订阅均已有 TypeSpec 声明；unknown 仅用于有意开放的动态值。各语言实现需要遵守相同的名称归属、接受证据、恢复、历史提交及释放规则；尚未确定的文件提交和编码等规则在对应合同中列明。此设计不声明实现已经完成。

[^direction]: 当前讨论确定 Service 依赖 Store 持久化资源、依赖 Runtime 执行运行操作；HTTP 只做协议映射，具体接口由负责人定义。
