---
type: Module Design
title: Store 模块设计
description: 屏蔽持久化实现，分离资源元数据与历史存储；文件保存完整数据，SQLite 提供可重建索引。
status: stable
sources:
  - id: direction
    resource: 项目负责人在本轮讨论中确认 Service 使用 Store 操作资源，使用 Runtime 执行原生操作
    title: Service 的两个依赖
  - id: storage-direction
    resource: 项目负责人在本轮 Store 讨论中确认 CRUD、generation 内置、文件完整保存与 SQLite 可重建，并区分 MetaStore 和 HistoryStore
    title: Store 的资源规则与存储实现边界
---

# Store 模块设计

Store 提供持久化能力，由 [Service](../service/design.md) 调用。它屏蔽文件、数据库、索引及恢复机制，Service 不依赖文件路径、SQL 或数据库连接。Store 不依赖 Service 实现，不启动 Agent，不调用 RuntimeDriver，也不处理 HTTP 请求。[^direction]

## MetaStore 与 HistoryStore

持久化能力分为两个独立职责，接口可分别设计，不要求合成一个大接口：[^storage-direction]

| 接口 | 职责 | 当前状态 |
|---|---|---|
| MetaStore | Runtime、Session 完整资源及 Runtime 名称归属的 CRUD、查询、唯一性约束与 generation 维护 | 已定义 [MetaStore TypeSpec](meta-store.tsp)，部分查询与错误合同待细化 |
| HistoryStore | SessionEvent 批量追加、类型过滤查询与连续同步进度 | 已定义 [HistoryStore TypeSpec](history-store.tsp) 与统一事件，文件提交协议待定 |

二者可以复用底层文件或索引设施；职责分离不意味着必须使用不同数据库，也不表示具备跨 Store 事务。历史协作规则见[会话历史](../service/history.md)，接口与值类型分别在 [history-store.tsp](history-store.tsp) 和 [共享历史模型](../../shared/history-models.tsp) 维护；准确文件提交协议仍待定义。

## MetaStore 的操作与资源保证

MetaStore 采用常规资源操作：create、get、list、update、delete。update 接收完整资源；当前不提供 JSON Patch，MetaStore 不接收 patch 文档。操作轮廓及并发规则如下，TypeSpec 见 [meta-store.tsp](meta-store.tsp)，查询分页与错误分类仍有待决。业务操作许可与 Runtime 配置校验属于 Service；持久化和资源一致性规则由 MetaStore 内置。[^storage-direction]

- 保存 Runtime、Session 完整资源及 Runtime 名称归属；支持按名称和 sessionId 定位同一 Session。
- 原子保证 Runtime 名称、Session namespace/name 与全局 sessionId 唯一，不能覆盖其他操作的记录。
- 提供筛选查询：Runtime 列表不分页，Session 列表可分页。排序和游标语义待设计。
- create 初始化 generation 为 1；update 中 spec 真正变化时递增，相同 spec 保持原值。只修改 status 或其他非 spec 字段不递增。
- spec 比较、generation 分配与资源写入原子完成，返回本次写入后的资源快照；Service 不计算或传入新的 generation。
- 状态更新不能覆盖并发 spec 修改。spec 的准确相等比较与规范化规则仍待定义，不在存储层擅自增加类型专属默认值。

## 资源键与并发控制

资源键统一为 namespace/name，在对应资源类型内唯一，创建后不可修改。Runtime 的 namespace 为空；Session 使用非空 namespace。sessionId 可用于查询并具有全局唯一约束，不替代 CRUD 的资源键。

| 字段 | 含义 | 维护规则 |
|---|---|---|
| namespace/name | 资源键 | 创建后不可修改 |
| resourceVersion | 乐观并发版本 | Store 生成的不透明字符串，调用方不解析、不排序、不递增 |
| generation | spec 修订号 | 创建为 1，仅 spec 变化时递增 |

操作轮廓如下；资源类型由泛型 MetaStore 确定，RuntimeMetaStore 与 SessionMetaStore 分别绑定资源、查询和列表返回类型：

```text
create(resource) → resource
get(namespace, name) → resource
list(query) → resource list
update(resource) → resource
delete(namespace, name, resourceVersion) → void
```

create 不接受调用方指定的 resourceVersion，由 Store 分配初始值。update 使用输入资源 metadata.resourceVersion 作为预期版本；delete 显式携带预期版本。Store 原子比较当前版本并完成写入或删除，不匹配则返回冲突且不修改资源。Service 重新读取后决定如何处理，不能盲目覆盖并发修改。

spec 或 status 等持久内容发生变化时生成新的 resourceVersion；只有 spec 变化才递增 generation。resourceVersion 的生成机制由 Store 隐藏，不能用 generation 替代，也不能从 SQLite 索引状态推导。内容完全不变的写入是否保留 resourceVersion 尚待明确；跨资源引用检查也不能仅凭单资源版本自动获得原子性。

get/list 与写入结果返回资源快照；修改返回对象不会直接改变持久数据。Service 读取资源快照、应用完整 spec 替换并完成业务校验，再以该快照的 resourceVersion 调用 update。发生冲突后由 Service 重新读取并重新判断操作；当前不提供 JSON Patch。删除不存在资源的行为及准确错误模型仍待定义。

引用检查与在途预留仍需要避免“检查后又登记新引用”的竞态；这些保证如何落到 CRUD 的条件更新或其他并发约束，需要后续明确，不能只用普通先查再写代替。

## HistoryStore 的追加、查询与同步进度

HistoryStore 提供 append(sessionId, events[])、list(sessionId, query) 和 getSyncProgress(sessionId)，按全局 sessionId 寻址。append 直接支持批量，单条写入使用长度为 1 的批次；同一批次不得混入其他 Session 的记录。

HistoryStore 直接保存 SessionEvent，不增加历史记录包装或私有证据字段。eventId 由 Runtime adapter 在创建统一事件时生成一次 UUID；Store 不重写 eventId/time。同一 sessionId/eventId 的相同事件可幂等重试，不重复追加、不改变原位置；同一 ID 对应不同内容则报错。事件 ID 仅用于唯一标识，不承担排序、计数或分页职责。不同 UUID 即使原生 ID 或内容相同，也不合并；同一 UUID 的写入幂等不等于原生事实去重，后者当前不提供。准确事件等价比较仍需细化，恢复私有材料由 Runtime 管理。

历史按持久追加顺序读取，批内按输入顺序追加；Service 串行协调同一 Session 的追加，使输入保持源事件顺序。Store 不能通过 eventId 大小或时间推断事件缺口，也不承诺自动重排迟到事件。

getSyncProgress 返回 syncedEventId，即最后同步、持久保存且可读取的事件身份，尚无事件时为 null；不是 SQLite 内部索引进度。旧事件重试不改变进度；ID 无大小关系，不能计算剩余数量，也不能据此宣称追平上游。

append 成功表示本批全部事件已经可持久读取，返回当前 syncedEventId；不承诺调用时长或整批事务。失败或结果未知时不能假定整批未写入；调用方核对进度和已存事件，使用相同 eventId 重试，不能重新生成 ID。文件层批次提交边界、持久化屏障和故障恢复协议仍待设计。

list 返回 SessionEvent、可选 next 游标和与本次读取视图一致的 syncedEventId。query.after 接受上页 next，调用方不得解析、自造或用 eventId 代替游标；游标绑定 Session 与过滤条件，不匹配或无效时明确报错。query.types 按事件 type 精确过滤，多类型为 OR，省略表示全部类型，不接受空数组。先筛选再按 limit 分页，next 仅在存在后续匹配事件时提供；过滤不改变全会话同步进度。空页不表示已追平上游。

文件保存稳定读取顺序和恢复所需信息。SQLite 重建不能改变已有事件身份、历史顺序或同步位置；有效游标的解释不能仅依赖重建会变化的数据库行号。游标具体编码、生命周期和过期规则仍待定义。HistoryStore 不发布实时事件，不运行消费者，也不激活 Runtime。

## 文件与 SQLite

文件系统保存完整持久数据，是恢复的权威来源；无数据库时也能完成存取。SQLite 是可选的索引与查询加速层，不是唯一数据来源。其全部内容都必须能从文件重建，不能保存只有数据库中才存在的资源字段、身份或恢复所需信息。[^storage-direction]

文件必须保留完整资源，包括 metadata/spec/status、uid、createdAt、generation、resourceVersion 及 spec.runtimeRef 名称归属。重建 SQLite 时原样恢复，不能重新生成身份、重置 generation、重新分配 resourceVersion 或将同名资源当成同一身份。历史若使用数据库索引，同样不得让索引成为历史记录的唯一副本；HistoryStore 的准确文件承载和提交协议仍待设计。

文件提交与 SQLite 更新由 Store 内部协调。文件已提交而索引更新失败时，不能把已发生的修改简单报告为未写入并让 Service 盲目重试。Store 必须识别并修复索引落后；查询不能静默返回缺失或过期数据。SQLite 缺失、损坏或需要重建时，不影响完整文件数据的保留。

事件已移除 seq，采用 adapter 生成的 UUID、Store 分页游标和最后同步事件身份。准确文件布局、提交点、原子替换与删除协议、唯一性维护、索引修复和重建期间的查询策略仍待设计。SQLite 的事务不能替代文件层所需的原子性和恢复保证；本次只确认权威数据与可重建索引的关系。

## Service 与 Runtime 的边界

Service 决定何时保存、如何处理业务错误，以及何时调用 Runtime。Store 保证持久化与资源原子存取的一致性；外部 Agent 副作用不在 Store 事务中。持久化失败后的 Agent 清理与未知结果结算由 Service 编排；文件与索引的一致性修复由 Store 自己承担。

RuntimeDriver 的私有恢复材料由 Runtime 管理；不能因为引入 Store，就要求 Service 接收或转交恢复材料。运行时内部是否复用某种底层存储设施不属于此公共依赖合同。

## 当前状态与验收方向

本轮确认 MetaStore 的 CRUD 形态、generation 内置维护、存储实现隐藏与文件完整保存/SQLite 可重建原则；已定义 MetaStore 与 HistoryStore TypeSpec，尚未新增产品实现。

具体实现应分别验证无 SQLite 运行、删除 SQLite 后完整重建、索引更新中断后的恢复、同名冲突与并发更新、resourceVersion 冲突检查、generation 规则，以及查询不会因索引落后而静默缺项。此处列出验收目标，不表示这些场景已经实现或执行。

文件协议的当前要求与待决事项见[持久化承载](../../bindings/storage.md)。MetaStore 直接复用 Service 的资源值模型，不导入 Service 操作接口；值模型目前位于 service/models.tsp，运行时模块仍不依赖它。

[^direction]: 当前讨论要求应用服务通过 Store 持久化资源，通过 Runtime 管理实际运行。
[^storage-direction]: 当前讨论确认文件保存完整数据，SQLite 全部内容可由文件恢复；MetaStore 与 HistoryStore 分开讨论，generation 由 Store 内置维护。
