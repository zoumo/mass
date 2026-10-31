---
type: Module Design
title: MetaStore 模块设计
description: 独立管理 Runtime 与 Session 资源，配置选择唯一权威后端，默认 SQLite。
status: stable
sources:
  - id: split-direction
    resource: 项目负责人在 2026-10-20 确认拆分 MetaStore 与 EventStore，并配置选择元数据权威后端
    title: 独立存储职责与元数据权威
---

# MetaStore 模块设计

MetaStore 是独立模块，由 Service 注入和调用，保存 Runtime、Session 完整资源。它隐藏文件或数据库实现，不启动 Agent，不发布事件，不读取或管理事件日志，也不依赖 EventStore。其接口、配置、连接、打开、关闭及恢复分别管理。[^split-direction]

## 配置与唯一权威

[MetaStoreConfig](config.tsp) 在启动时通过 backend 选择 sqlite 或 file，默认 sqlite。每次打开仅选择一种权威后端，Runtime 与 Session 使用同一选择；配置不是双写或回退优先级。未知配置、选定后端损坏或不可用时明确失败，不静默改用另一个后端。

- sqlite：SQLite 保存完整 metadata/spec/status，是元数据唯一真相源。事务内完成资源写入、版本、generation 与唯一性约束；不要求生成资源文件副本，也不能从事件文件重建资源数据库。数据库必须按权威数据备份和恢复。
- file：文件保存完整资源，是元数据唯一真相源；在此后端范围内保证条件更新、唯一性及失败恢复。不要求同时维护 SQL 完整副本。

后端选择不改变 CRUD、资源身份、快照和并发语义。修改配置不等于迁移数据；不自动导入、合并、复制或回退到其他后端。已有数据切换后端须单独完成显式迁移与核验，本轮不定义迁移工具。存储地址及配置文件承载由实现资料维护。

## MetaStore 的操作与资源保证

MetaStore 采用常规资源操作：create、get、list、update、delete。update 接收完整资源；当前不提供 JSON Patch，MetaStore 不接收 patch 文档。操作轮廓及并发规则如下，TypeSpec 见 [meta-store.tsp](meta-store.tsp)，查询分页与错误分类仍有待决。业务操作许可与 Runtime 配置校验属于 Service；持久化和资源一致性规则由 MetaStore 内置。

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
| resourceVersion | 乐观并发版本 | MetaStore 生成的不透明字符串，调用方不解析、不排序、不递增 |
| generation | spec 修订号 | 创建为 1，仅 spec 变化时递增 |

操作轮廓如下；资源类型由泛型 MetaStore 确定，RuntimeMetaStore 与 SessionMetaStore 分别绑定资源、查询和列表返回类型：

```text
create(resource) → resource
get(namespace, name) → resource
list(query) → resource list
update(resource) → resource
delete(namespace, name, resourceVersion) → void
```

create 不接受调用方指定的 resourceVersion，由 MetaStore 分配初始值。update 使用输入资源 metadata.resourceVersion 作为预期版本；delete 显式携带预期版本。MetaStore 原子比较当前版本并完成写入或删除，不匹配则返回冲突且不修改资源。Service 重新读取后决定如何处理，不能盲目覆盖并发修改。

spec 或 status 等持久内容发生变化时生成新的 resourceVersion；只有 spec 变化才递增 generation。resourceVersion 的生成机制由 MetaStore 隐藏，不能用 generation 替代，也不能从 EventStore 的 SQL 投影状态推导。内容完全不变的写入是否保留 resourceVersion 尚待明确；跨资源引用检查也不能仅凭单资源版本自动获得原子性。

get/list 与写入结果返回资源快照；修改返回对象不会直接改变持久数据。Service 读取资源快照、应用完整 spec 替换并完成业务校验，再以该快照的 resourceVersion 调用 update。发生冲突后由 Service 重新读取并重新判断操作；当前不提供 JSON Patch。删除不存在资源的行为及准确错误模型仍待定义。

引用检查与在途预留仍需要避免“检查后又登记新引用”的竞态；这些保证如何落到 CRUD 的条件更新或其他并发约束，需要后续明确，不能只用普通先查再写代替。

## 写入、查询与失败

Service 校验业务请求后调用 MetaStore；MetaStore 在所选权威后端完成持久提交后返回资源快照，查询读取同一后端。SQLite 权威时流程是条件校验、SQL 事务提交、返回；文件权威时由文件协议完成提交，不再要求等待 EventStore SQL。

失败只依据本模块的提交证据结算；提交结果未知时不假定没有写入，不通过另一个后端猜测成功。不得因 EventStore 副本缺失或重建而重建、清空或覆盖元数据数据库。

## Service 与 EventStore 的边界

Service 负责 Session 解析、运行绑定、业务许可与跨模块操作顺序，分别注入 MetaStore、EventStore 与 Runtime。MetaStore 不为每条事件提供目录查询，不更新事件 SQL，也不在资源事务中调用 EventStore。两个模块不提供跨模块事务，不共享权威或提交意图。

Session 删除与事件保留/清理的跨模块失败策略需要单独明确；不得在拆分时引入隐式级联或宣称原子删除。归档继续保留身份和历史，同名重建使用独立身份。Runtime 名称归属不因后端选择改变。

## 验收

通过公共 MetaStore 接口对两个后端执行相同 CRUD、唯一性、generation、resourceVersion、快照与重启测试；默认配置实际选择 SQLite，错误后端和故障不回退，EventStore 重建不影响资源。文件后端不依赖事件 SQL，SQLite 后端不依赖元数据 JSONL。

[^split-direction]: 当前人类请求明确指定 MetaStore 通过配置选择唯一真相源，默认 SQLite，并与文件权威的 EventStore 分开管理。
