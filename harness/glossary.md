---
type: Glossary
title: MASS 领域词汇
description: 统一 Runtime、实例、Session、事实与消费的含义。
status: stable
generated:
  by: codex/gpt-6
  at: 2026-10-19T20:10:53+08:00
sources:
  - id: direction
    resource: "项目负责人在 2026-10-19 本次 Runtime 职责讨论中的说明"
    title: Service 对 Store 和 Runtime 的依赖
---

# MASS 领域词汇

本词汇表定义当前设计中的 Runtime、实例、Session、事实与消费。定义与实现语言、协议库及源码布局无关。[^direction]

## 运行与会话

**Agent**：实际执行智能工作、提供原生会话和运行事实的外部运行系统。
_Avoid_: 将 MASS 与其控制的 Agent 当成同一个对象。

**Service 模块**：通过 RuntimeService、SessionService 接收业务输入、返回业务输出；依赖 Store 持久化，依赖 Runtime 执行，协调资源状态、并发与失败补偿。

**Store 模块**：隐藏持久化实现，提供 MetaStore 与 HistoryStore 两项职责；文件保存完整数据，SQLite 仅作可完全重建的索引与查询加速层。不执行原生运行操作。

**MetaStore**：资源元数据的 CRUD 与查询接口，保存完整资源及 Runtime 名称归属，内置唯一性约束、generation 维护与 resourceVersion 并发控制。接口见 [MetaStore](modules/store/meta-store.tsp)。

**HistoryStore**：按 sessionId 批量追加和读取历史记录，并提供 syncedEventId 最后同步事件身份；按持久追加顺序读取并以不透明游标分页，不暴露 SQLite 内部索引进度。接口见 [HistoryStore](modules/store/history-store.tsp)。

**Runtime 模块**：按类型校验配置、构造 Driver 并封装原生运行与恢复；类型映射是内部细节。

**RuntimeRegistry**：提供独立配置校验，按 type、name、config 构造并原子登记未启动 Driver，维护当前进程的 name → RuntimeDriver 映射；不保存资源记录，不隐式启停实例。

**Runtime（资源类型）**：单个受管理实例的 metadata/spec/status 数据结构。

**RuntimeDriver**：运行时模块提供的具体实例执行接口，负责实际资源启停和原生会话操作，由Service 模块消费。
_Avoid_: 将 RuntimeDriver 当作全局会话目录，或作为外部调用入口。

**Runtime 类型**：系统提供的一种运行适配能力，决定实例配置规则与运行行为。
_Avoid_: 将类型名、实例名和底层 Agent 产品名混用。

**Runtime 实例**：RuntimeService 管理的命名运行对象，具有稳定身份和类型配置，Session 通过它的名称保存运行归属；独立于某次进程或连接的存活。
_Avoid_: 将 Runtime 实例称为 应用服务模块，或将重新启动运行资源称为新建实例。

**资源 Metadata**：Runtime 与 Session 共用的 namespace、name、uid、createdAt、generation。Runtime 无 namespace，名称全局唯一；Session 的唯一键为 namespace/name。uid 区分同名重建，createdAt 是首次持久创建时间，generation 表示 spec 版本。
_Avoid_: 用 generation 计数全部状态更新，或用同名判断同一资源身份。

**resourceVersion**：MetaStore 分配并持久保存的不透明并发版本，update/delete 以预期版本进行原子条件写入；与仅表示 spec 变化的 generation 独立，SQLite 重建时保持原值。

**运行资源**：Runtime 实例使用的进程或外部连接，可以停止、失联和重建。
_Avoid_: 将本地连接结束视为远端运行资源已经停止。

**Session**：归属于一个稳定 Runtime 实例的持久会话对象，具有 Agent 返回的会话身份、用户名称、配置与历史，是输入和交互的操作目标。
_Avoid_: 将 Session 等同于当前连接，或将归档等同于删除。

**sessionId**：Agent 创建并返回、由 MASS 原样保存的会话标识；在全部 Runtime 与 namespace 中唯一，归档和恢复保持不变，可独立用于 SessionRef 寻址。
_Avoid_: 根据 ID 格式推断实例归属；归属由持久记录确定。

**sessionName**：metadata.name，与 namespace 共同构成 Session 唯一键；遵循 DNS subdomain 规则，与 Agent 返回的 sessionId 独立。
_Avoid_: 用名称相同证明实例归属或会话身份相同。

**Runtime 名称归属**：Session.spec.runtimeRef 保存 Runtime 的名称；解析、路由和恢复使用该名称，不保存或比较归属 Runtime 的 uid。原生会话恢复仍由 Driver 验证。

**恢复绑定**：Driver 为验证和恢复原会话而内部管理的私有信息，持久化与读取均由 Driver 负责。
_Avoid_: 将恢复绑定当成公开会话 ID 或凭据。

**RuntimeState**：受管理实例拥有的运行资源的当前状态，包括停止、启动中、运行、停止中和未知。

**SessionState**：持久会话的统一资源状态，包括离线、打开中、空闲、执行中、需要行动、关闭中、已归档和未知。
_Avoid_: 将取消确认视为空闲，将断线视为已归档，或将未知视为已释放。

**前台工作状态**：Agent 前台工作正在进行、被用户操作阻塞或已经结束的观察状态。
_Avoid_: 将前台空闲等同于后台活动全部结束，或与单次输入一一对应。

## 事实与历史

**SessionEvent**：实时、持久存储和历史查询共用的事件；直接保存 namespace、sessionName、sessionId，type 位于 Base，每种 type 固定对应的 payload。eventId 由 adapter 创建事件时生成 UUID，同一事件重试不变，不参与排序，暂不提供原生事件去重。顶层 _meta 为字符串键、任意值的扩展 map。

**思考（thinking）**：与消息（message）采用相同身份、内容块结构、增量和完整事实规则的内容，仅以事件类型区分其思考含义；内部多个独立内容块也按消息规则处理。

**会话条目**：会话中有独立身份、可以持续更新的一份消息、思考、工具调用或计划。
_Avoid_: 将每次增量更新视为一个新条目。

**条目完成**：某一条目结束生成、可以确定其完整内容的时点。
_Avoid_: 将条目完成等同于整个前台工作结束。

**完整事实**：适配方依据 Agent 实际观察提供的完整内容或明确状态事实。
_Avoid_: 用本地输入副本、增量拼接或连接退出推断完整事实。

**预览**：用于实时展示的内容更新，不承担完整历史记录的职责。
_Avoid_: 将预览缓存当成已提交内容。

**已提交事件**：通过规定持久化屏障的公共事件，以 UUID eventId 唯一标识，按持久追加顺序读取。
_Avoid_: 将输入接受回执或实时预览当成提交证明。

**私有事实证据**：适配方用于验证原生观察与公共事实对应关系的不透明材料。
_Avoid_: 将私有证据字段当成通用事件字段。

**会话历史**：已提交会话记录的有序集合，不包含每个内容增量的逐帧记录。
_Avoid_: 将历史查询称为实时流重放。

**实时事件流**：发送给当前订阅者的会话变化通知。
_Avoid_: 将实时事件流等同于完整会话历史或可靠消费进度。

## 消费与观察

**事件处理器（EventHandler）**：按持久 Session 构造、消费公共事件并更新自身状态或触发业务动作的处理逻辑；归档与恢复保留健康消费关系，失败关系需显式重新启用。
_Avoid_: 将处理器当成原生协议解析器或提交决策者。

**会话元信息**：描述会话身份、配置及相关信息的记录；当前观察值与持久身份具有不同有效期。
_Avoid_: 将所有会话信息统称为运行状态。

**可用能力**：系统已实现、实际连接支持且当前生命周期允许的功能范围。
_Avoid_: 将 Agent 宣告的全部能力或未来设计能力当成当前可用能力。

[^direction]: 当前讨论确定 Service 依赖 Store 持久化资源、依赖 Runtime 执行运行操作；HTTP 只做协议映射，具体接口由负责人定义。
