---
type: Module Design
title: 会话历史
description: 管理事实提交、历史读取、实时观察与恢复校验。
status: stable
generated:
  by: codex/gpt-6
  at: 2026-10-19T18:19:17+08:00
sources:
  - id: direction
    resource: "项目负责人在 2026-10-19 本次 Runtime 职责讨论中的说明"
    title: Service 对 Store 和 Runtime 的依赖
---

# 会话历史

## 职责与所有权

调用者通过 Service 的 Session 管理接口查询历史与订阅事件。本文维护业务协作；实时 Handler 分发由独立 [Events](../events/design.md) 模块承担。[^direction]

Service 负责 Session 解析、来源校验、只读历史与恢复编排；EventStore 负责持久日志与查询投影；Service 协调事实提交后调用 Broadcast；Events 只负责实时 Handler 分发和消费生命周期。事件结构、时间与事件身份来源归 Runtime。

Runtime adapter 决定一个原生观察如何转换为公共事实，并自行管理恢复所需私有证据。Service 校验公共事实的归属，Service 直接协调 EventStore 追加；Broadcast 不参与历史提交，也不解析原生坐标或私有证据版本。

文件提交与编码的待决事项见[持久化承载](../../bindings/storage.md)；本模块不绑定存储引擎。

## 完整事实与预览

完整条目来自真实 Agent 观察。预览用于显示生成中的内容，不因空闲、取消、失败、断线、归档或停止而自动补成完整历史。用户输入副本不能替代真实完整用户条目。

条目完成与前台工作结束分别表达。工具调用、执行结果和控制批准也分别表达；调用参数完整不证明已经执行。输出缺失、显式空值和已知截断保持区别。

MASS 不在内存跨事件保存或拼接 delta，只有 chunk 的 adapter 不自动得到完整消息历史。

持久事件种类由[运行时事件合同](../runtime/events.md)定义。完整观察并不意味着所有状态通知都必须持久化；预览与其他实时通知不进入历史，所有事件仍使用统一 eventId。

## 提交与发布

Service 在共享读取循环之外按 Session 串行协调：

1. 完成 Session 及上游事件路由校验，保持 Runtime adapter 提供的统一 SessionEvent、原 eventId 与 time。
2. 对持久事件直接调用 EventStore.append：文件持久追加，SQL 完整事件和内部消费位置事务提交。临时通知不调用历史追加。
3. append 成功后调用该 Session 的 Broadcast.publish(event)；临时通知直接 publish。Broadcast 只实时分发，不执行持久提交 Handler，也不接收 committed 或内部来源。

同一工具调用与结果共享 itemId，但有独立 eventId，不能覆盖历史。历史按持久追加顺序读取；Broadcast 按 Service 的 publish 顺序实时分发。两者不构成跨模块事务，历史可已提交但由于关闭或故障而未发布，发布也不能证明外部已经读取。

追加失败或结果未知时由 Service 保留原事件及 UUID，重试仍使用原 ID，不盲目用新 ID 重送。EventStore 在后续同步触发时自动从已验证消费位置续接，Service 不管理 SQL 位置、不驱动副本恢复或要求调用方重新打开存储；事实是否已经可读仍依据 EventStore 的返回和查询证据判断。后序事件不能越过该 Session 尚未结算的持久事实；这一业务顺序由 Service 负责，不由 Broadcast 的暂停/恢复链实现。普通 Handler 失败诊断后继续；required Handler 失败停止本事件后续分发并返回错误。均不回滚历史或先前已完成的实时投递；Service 不能把实时错误当作尚未写入历史或整事件未发布的证据，也不盲目重送。

持久化屏障强度见 EventStore 与承载合同。刷新、掉电持久性、元信息原子保存和跨记录事务是不同保证；实时分发成功不是其中任一证明。

## 历史与实时观察

历史查询返回已提交公共事件，不返回私有证据，不激活 Runtime，也不调用业务处理器。读取验证持久身份、记录结构和顺序，不能用请求页之外的损坏记录隐藏错误。

实时观察与历史读取是 SessionService interface 中语义不同的会话操作。订阅只观察之后的事件，不自动补历史；取消订阅只结束观察，不取消 Agent 工作。Service 解析 sessionId 或 namespace/name 到同一 Broadcast，只调用 Broadcast.subscribe，由 Broadcast 内部创建登记队列 Handler 后返回可取消的异步流；读取与关闭接口见 [EventSubscription](../events/observation.tsp)，HTTP 只映射这一行为。

实时订阅资源有界，慢订阅者可以断开；消费者过载不能迫使核心修改已提交事件。跨断线连续消费、持久消费游标和恰好一次业务处理都需要额外合同，不能从实时通知推断。

## 统一历史查询

SessionService.listHistoryEvents 与 EventStore.list 共用 [HistoryQuery、HistoryPage](../../shared/history-models.tsp)。Service 按名称或 ID 解析同一 sessionId 后传递查询，返回 events、next、syncedEventId，不再使用资源列表的 Page<T>/PageCursor。

query.types 为 OR 精确匹配，省略表示全部类型，空数组非法；实时类型没有持久记录可返回。先过滤再按持久追加顺序分页，after 是排他的 HistoryCursor，绑定 Session 与规范化过滤条件（类型集合忽略重复和次序），不绑定 limit。默认 limit=100，不设合同级最大值，实现容量不足时明确报错；next 仅在该页读取视图内存在后续匹配事件时返回。syncedEventId 来自同一读取视图的全会话同步进度，不因过滤改变。

每次调用读取自己的快照，不承诺跨页快照隔离；后续追加可在下一次读取出现。游标不能比较大小、与 eventId 互换或依赖易变的数据库行号；无效、过期或 Session/过滤条件不符时返回明确错误，不静默从头开始。游标编码与保留期限由 Store 实现定义；有效游标应在可重建索引重建后继续有效。

## EventStore 同步进度

EventStore 的完整接口见 [TypeSpec](../eventstore/event-store.tsp)。getSyncProgress 返回最后持久同步且可读取的 syncedEventId，尚无历史时为 null；它不是 SQLite 内部索引状态，没有大小关系，不能计算同步差值或直接宣称追平上游。list 返回相同读取视图的 syncedEventId，并通过不透明 after/next 游标分页；查询过滤不改变全会话同步位置。

实时预览可以领先完整历史。完整持久事实仍遵守先提交再发布的顺序。临时预览可以领先持久历史；已通过 append 成功后发布的持久事件必须已能查询。判断其他上游目标事件是否已经同步需要核对其存在，不能比较 eventId 大小或从空页推断上游没有新事件。

append 支持非空批次；成功覆盖本批全部事件，失败不保证整批未写入。调用方保留原事件，以原 eventId 重试；EventStore 内部自动续同步并执行幂等校验，旧事件重试不能使进度倒退。批次原子提交协议与准确幂等比较规则仍待定义。

Agent 的 session.state_update 由异步状态 handler 写 MetaStore，允许写入失败而继续广播；资源查询可以落后于通知，也不承诺一定追上。它不进入 EventStore，不阻断其他实时 Handler。状态保存遵守资源版本与 closing/archived 保护，不能从仅有 Session 身份的实时事件取得某个运行的操作权限；业务生命周期的必要写入不因此变为可选。

## 校验与恢复协作

通用校验检查元信息、公共记录、会话归属、eventId 唯一性和持久读取顺序。私有绑定、原生来源与公共投影的一致性由对应 Runtime adapter 校验。

全部已提交公共事件及 Runtime 管理的私有证据分别通过验证后，才允许修复未提交尾部；EventStore 不承载私有证据。已提交记录损坏不能当成尾部修复，也不能先改写数据再报告错误。只读查询不修复文件。

恢复次序由应用服务模块按 [Session 管理](session.md)规则编排。历史提交职责 不主动启动 Agent，不选择替代实例，不把旧记录重新投递给业务消费者。

## 容量与停止

完整持久事实在有界资源内等待或明确失败，不能静默丢失；预览允许按策略丢弃。持续压力可能传回共享运行连接，容量限制不等于整个进程内存上限。

停止区分已接纳事实和仍在上游的观察。Session 归档或 Runtime 停止时 Service 继续消费原 Driver，结算自己的历史追加和已接纳的实时 publish 调用；保留 Broadcast、健康 Handler 与订阅，恢复继续使用。仅应用 shutdown 才关闭 Broadcast，关闭 Handler 并如实报告未完成的清理（不保证异步动作全部消费），订阅随队列 Handler 结束；超时或未结算不宣告干净关闭。永久阻塞存储时不能同时承诺有限时间完成与全部成功提交。

## 验收场景

| 场景 | 应观察到的行为 |
|---|---|
| 只有增量，随后连接退出 | 无伪造的完整历史条目 |
| 持久化写入失败 | SSE 与处理器均收不到失败记录的已提交版本 |
| 文件已提交、SQL 同步失败后再次触发 | EventStore 自动续同步，原 ID 重试不重复，未同步前不发布；副本补同步不补广播 |
| 同一工具调用及结果 | itemId 一致，eventId 各自独立 |
| 慢订阅者 | 按有限策略断开，健康提交继续 |
| 恢复时私有证据不一致 | 在修尾或激活前失败，保留原数据 |
| 历史查询与恢复 | 查询不运行消费者，恢复不重演旧业务动作 |

## 待决事项

事件结构见[SessionEvent](../runtime/session-events.tsp)；持久化屏障强度与准确文件格式仍待确认。EventStore 的 syncedEventId 仅是最后同步事件身份，不等同于业务消费者进度；文件先提交、SQL 事务投影后才允许发布的顺序已确定；增量校验细节与持久业务消费者进度仍待设计。显式原生导入采用 [异步 load](load.md)，不改变本地历史查询不触发 Handler 的规则；只有显式 load 将新导入事实经提交后发布。历史查询与分页已有当前合同，具体实现与验收另行验证。

[^direction]: 当前讨论确定 Service 依赖 Store 持久化资源、依赖 Runtime 执行运行操作；HTTP 只做协议映射，具体接口由负责人定义。

## 原生历史回放

[load](load.md)先建立 Service 接收及原操作绑定，再读取 SDK 的异步原生历史，经正常 adapter 内容转换、归属校验、EventStore 追加和同一 Broadcast 分发。持久事实的 replay 字段保存历史来源，状态/交互不变成当前操作。native_completed 标记不证明本地完成；确认同一原生会话已恢复、前序追加及 publish 结算后才保存 completed，并保留恢复运行供后续输入。资源释放只属于失败/取消/归档清理，不能作为成功回放的前置条件。部分失败保留已提交事实，禁止按新 UUID 从头重送。
