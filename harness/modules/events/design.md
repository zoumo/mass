---
type: Module Design
title: Events 模块设计
description: 每个 Session 一个实时 Broadcast，只接收统一 SessionEvent，统一管理 Handler 与队列订阅。
status: stable
sources:
  - id: realtime-direction
    resource: 项目负责人在 2026-10-21 要求移除 EventDelivery 和运行关联，按 Session 身份寻址，Broadcast 只管实时流并简化接口
    title: 简化实时 Broadcast
---

# Events 模块设计

Events 只负责实时事件分发。Service 为每个 Session 管理一个 EventBroadcast，把 Runtime 产生的统一 SessionEvent 交给它；Broadcast 将事件交给登记的 EventHandler。外部 Subscription 是其中一个队列 Handler 的读取封装。[^realtime-direction]

## 语言无关的接口表达

准确声明见 [pipeline.tsp](pipeline.tsp)、[event-handlers.tsp](event-handlers.tsp) 和 [observation.tsp](observation.tsp)。TypeSpec 描述逻辑能力和数据类型，不要求各语言复制方法名或调用形状。

- EventBroadcast：publish(event)、addHandler(handler)、subscribe()、close()。
- EventHandler：只读属性 required（默认 false），以及 handle(event)、close()。TypeSpec 用 required() 表达属性读取，语言可以用字段或访问方法。
- EventSubscription：可取消的异步 SessionEvent 流，具有按序接收、流结束、终止原因查询和主动取消四种能力。TypeSpec 的 receive/error/close 是能力表达，不强制 next() 或任何特定语言的流类型。

publish 与 handle 都直接接收 SessionEvent，不增加事件包装、context、committed 标记、来源泛型或 Runtime/Driver/run/work 关联。Handler 无 name 或 order；不提供公开 removeHandler、工厂、优先级、处理阶段恢复或自动重试。语言映射集中见 [Go 实时订阅映射](../../bindings/go-events.md)，模块正文不规定具体容器、并发原语或执行器。

## Session 身份与职责

Service 按全局 sessionId 或 namespace/name 解析 Session，两种引用定位同一个 Broadcast；不是两套独立 key 空间，也不把 Runtime 名称拼入 key。名称复用时先解析当前持久 Session 身份，不把新 Session 接到旧对象上。Broadcast 的事件必须与其所属 Session 的身份及名称相符。

Broadcast 不查询资源目录，不定位 Driver，不验证运行来源，不调用 Runtime，也不负责运行控制。Service 在调用 publish 之前完成业务准入和上游事件路由校验；这些内部业务信息不传给 Broadcast 或 Handler。Handler 只得到 SessionEvent，不能据此取得某个原运行的控制权限。

历史追加、查询、SQL 重建和事实恢复均归 EventStore 与 Service。Broadcast 不访问 EventStore，没有内建持久化 Handler，不接收“历史已提交”状态，不以发布结果证明历史提交。Service 保留持久事实先经 EventStore.append 成功、再 publish 的既有业务次序；Broadcast 本身只看传入的实时事件，无需知道 Service 是否执行过持久化。历史查询/重建不调用 publish 或 Handler。

Service 在共享 Driver 读取循环之外按 Session 有界排队，串行完成自己的持久化及 publish；不在公共读流循环里等待慢会话。历史失败时由 Service 保留原事件与原 ID 并结算，不向 Broadcast 注入恢复阶段。Broadcast 不另建用于持久重试的输入队列。

## Handler 加入顺序与 required

addHandler 接收已构造实例，加入成功才移交所有权；一个实例只能加入一个 Broadcast，重复加入明确拒绝。不使用名称或顺序字段，也不提供公共移除操作。普通 Handler 注册后随 Broadcast 关闭；Subscription 可以通过内部登记句柄解除自己的队列 Handler，该句柄不暴露为另一个管理接口。

同一 Session 的 publish 串行处理，在本次分发点确定已登记的 Handler，严格按加入顺序逐一调用并等待 handle 返回。required 在加入时固定，默认 false；只决定失败是否中止，不重新排列 Handler。订阅 Handler 也加入同一序列，因此后来加入的 required 不会被移动到已有订阅之前。

- required=true：handle 失败或抛错时，停止本事件的后续调用，publish 返回错误。此前完成的动作或投递不回滚；因此错误不表示整事件从未交付，调用方不能盲目重送。
- required=false：handle 失败或抛错时记录诊断，继续其余 Handler。普通失败不转成整事件发布失败。

required 的成功指本次 handle 完成；Handler 若仅入队，成功不代表后台动作成功。需要等待业务动作成功的 Handler 必须在 handle 内完成该动作，但它可能阻塞所属 Session。状态 Handler 和订阅 Handler 固定 required=false，不能把状态写入变成通知屏障。

Broadcast 不自动重试或暂停后续事件，也不维护恢复游标。publish 成功不证明外部读取、异步动作或历史成功；身份不匹配、对象关闭或无法开始分发时明确拒绝。事件在不同 Handler 间保持不可变，不由前一个 Handler 改写再交给后一个。

加入、订阅取消与事件分发确定先后；取消之后不开始新的队列投递。回调、取消和关闭的同步不能使自身注销等待自己的当前 handle，也不能让用户回调阻塞其他 Session 的公共读流循环。慢业务可在 Handler 内使用自有有界待办，Broadcast 不加第二层转发队列。

## Subscription 的异步流合同

Service 解析 Session 后只调用其 Broadcast.subscribe。Broadcast 内部创建共享一个有界队列的队列 Handler 和 Subscription，队列 Handler 为 required=false；原子加入成功后才返回流，构造失败清理本次资源。Service 不负责构造 Handler、队列或解除登记回调，也不提供第二条订阅分发路径。

该流按序交付登记之后收到的 SessionEvent，不补历史、不自动重连。每个订阅只有一个消费端和一个队列；外部稍晚开始接收，期间已接纳的事件仍可按序读取。

异步流区分“没有新事件，继续等待”和“流已结束”。结束可以是正常或异常，终止原因在结束对读取者可见前固定；活动期间暂时没有错误不证明未来成功。流结束后不接纳新事件，已接纳缓冲仍可读完，随后稳定结束并保留原终止原因。

队列满时非阻塞地异常终止这个订阅，保存错误、解除自己的内部 Handler 登记并唤醒等待者；不得静默丢事件后继续伪装完整实时流，也不影响其他 Handler。取消订阅同样解除自己的登记并终止流，但不覆盖之前的错误。取消与 publish 竞争时须结算已进入的投递，禁止向已终止或已释放的队列继续写入。

主动取消与 Broadcast 关闭均不等待外部把缓冲读完；未读事件的存储随该流资源生命周期释放。取消只结束观察，不取消 Agent 工作、不关闭 Session 或 Broadcast。模块合同不指定接收、等待和终止在目标语言中的方法形状，语言绑定须保持这些可观察语义。

## 生命周期

Session close、断线或 Runtime 停止不销毁 Broadcast、健康 Handler 和订阅；同一 Session 恢复继续使用。close 成功后 Broadcast 与订阅仍可用于读取已接纳事实。没有订阅者时，Service 的历史责任仍存在；创建离线 Session 的 Broadcast 也不启动 Agent。进程重启只重新构造实时对象，不恢复订阅、不补历史事件。

应用 shutdown 由 Service 先停止新业务与消费者登记，按 Driver 合同持续接收最终事件，结算自己的历史和实时发布调用，再 close 各 Broadcast。Broadcast.close 仅停止新的发布/登记并关闭所有自有 Handler，订阅随队列 Handler 结束；不以关闭成功表示异步业务队列已完整消费或 Agent 已释放。Handler 关闭失败如实报告并保留清理责任，其他实例仍须尝试清理；不关闭共享存储或 Driver。

## 验收与范围

通过公共 EventBroadcast 接口验证统一 SessionEvent、两种 Session 寻址复用对象、Handler 加入顺序、required 中止和普通错误继续、不可变事件及关闭。订阅验证单队列、原子登记后才返回、首次读取前的事件保留、取消竞争、等待接收唤醒和慢消费者异常终止。Service 集成验证文件→SQL 提交后才发布、无订阅仍持久化、历史查询不触发实时分发、状态更新允许失败及落后、历史不受实时 required 失败回滚、停 Runtime A 不影响 B。

队列容量、语言级取消和清理期限由组装配置明确给出，不另加未经确认的默认值。可靠重放、恰好一次、失败阶段恢复、运行来源上下文和 Handler 自动控制 Agent 均不属于实时 Broadcast 合同。

[^realtime-direction]: 负责人明确采用统一事件、Session key 与纯实时分发。

## 显式 load 的分发

[load](../service/load.md)将新导入事实经 Service 校验、EventStore 提交后，调用同一个 publish；历史来源仅是 SessionEvent.replay 字段，不增加运行上下文或第二条总线。Broadcast 不执行原生历史读取或自动补历史；只读历史查询/SQL 重建仍不发布。晚订阅不保证收到 load 开始，调用者查询已提交历史和 Session.status.historyLoad；publish 成功不证明订阅者读完或异步动作完成。
