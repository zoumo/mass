---
type: Contract
title: 持久化承载
description: MetaStore 可配置唯一权威后端；EventStore 文件权威、SQL 查询副本可重建。
status: draft
sources:
  - id: split-direction
    resource: 项目负责人在 2026-10-20 确认 MetaStore 默认 SQLite 权威、可选文件，以及 EventStore 文件权威
    title: 两条独立持久化路径
---

# 持久化承载

## 模块与权威

[MetaStore](../modules/metastore/design.md) 与 [EventStore](../modules/eventstore/design.md) 是两个独立模块，分别管理配置、连接、文件、打开、关闭与恢复，不共享提交意图或跨模块事务。Service 分别调用并协调业务操作，两个存储模块不相互调用。[^split-direction]

| 模块 | 唯一权威 | SQL 的角色 |
|---|---|---|
| MetaStore，默认 sqlite | 元数据 SQLite | 权威资源存储，不可当缓存删除 |
| MetaStore，配置 file | 完整资源文件 | 不要求维护元数据 SQL 副本 |
| EventStore | 完整事件文件及恢复材料 | 完整事件查询副本，可从事件文件重建 |

MetaStoreConfig.backend 在启动时选择 sqlite 或 file，默认 sqlite；同一次打开不双写两种权威。配置错误或所选后端不可用时明确失败，不静默回退。切换配置不迁移、合并或导入数据；已有数据必须先显式迁移和核验，迁移工具不在本轮范围。

MetaStore 保存 Runtime、Session 完整 metadata/spec/status、uid、createdAt、generation、resourceVersion 和 sessionId。Session.spec.runtimeRef 保存 Runtime 名称，不使用或比较 Runtime uid 作为跨资源归属。SQLite 后端以事务维护 CRUD、版本和唯一性，不要求权威资源文件；文件后端独立保证同样的公共语义。

Runtime.spec 顶层持久保存 `features` 与 `mcpServers` 的 Session 默认值；它们不属于类型专属 config。Session.spec 只保存显式 features、MCP server 覆盖值及其余会话配置，不保存 create、resume 或 load 时解析出的 Runtime 默认副本。MetaStore 不执行两者合并；Service 每次打开 Session 时读取当前 Runtime spec。Runtime 更新因此不必改写 Session，Session.status.effectiveFeatures 仍是运行观察值，不是配置默认值。

## 元数据提交与读取

业务校验 → 所选权威后端条件提交 → 返回成功。查询读取同一后端，不等待事件 SQL，不从 EventStore 推导完整资源。元数据 SQLite 必须按权威数据备份和恢复；不能从事件文件重建，也不能因事件 SQL 损坏而清空。文件后端所需的跨文件提交协议由该后端承担。

## 事件提交与读取

EventStore 直接保存统一 SessionEvent，保留 adapter 生成的 eventId、time、完整名称与 payload。日志按稳定 sessionId 区分身份；日志及随附材料包含独立重建事件 SQL 所需信息，不查询 MetaStore 补全日志。事件内部格式可包含恢复边界，不改变公共 SessionEvent。

追加顺序是：校验与幂等检查 → events.jsonl 持久追加 → SQL 事务写入完整事件并推进内部文件消费位置 → append 成功 → Service 调用实时 Broadcast.publish(SessionEvent) → 统一 Handler 分发（订阅也是队列 Handler）。仅写本批新事件，旧历史不全量重写；恢复意图不保存全量历史。只持久化合同规定的五种事件，chunk 与临时通知不落历史。

同一 Session 按接纳顺序提交与发布，后续持久事件不得越过失败的前序事件；无跨 Session 全局顺序。历史 SQL 按持久追加位置分页，游标不依赖重建后变化的行号。公开 syncedEventId 表示同一视图最后持久且可读取事件，不是 SQL 内部消费位置。

文件成功但 SQL 失败表示事实已保存、查询副本尚未完成，不撤销文件。明确报告未知结果或副本未就绪，不允许后续追加越过未结算前缀或查询静默漏项，不发布该次持久事件。后续同步触发由 EventStore 自动从已验证消费位置继续处理文件尾部，SQL 事件与消费位置原子提交；失败不推进位置，下一次触发继续。调用方无需先恢复或重新打开 Store，以原 eventId 重试仍保持幂等。SQL 落后时不能仅根据 SQL 缺项判定原事件未保存。不得以事件副本故障重建或切换 MetaStore。

有效事件 SQL 从已确认且校验通过的消费位置增量续同步；位置不能替代对应文件前缀和记录校验。事件 SQL 缺失或损坏、无法使用原位置时流式重建，正文与身份索引不全量驻留内存；重建只依赖事件文件和其恢复材料。启动校验完成、查询视图一致后才开放对应能力；读取和重建不激活 Runtime、不重演业务处理器。只读检查不修复文件或持久副本，临时磁盘副本由自身清理。

## 生命周期与恢复边界

RuntimeDriver 自行保存私有原生恢复材料，不进入公共事件 SQL。全部已提交公共历史与 Driver 私有证据分别通过验证后，才允许修复未提交尾部；已提交损坏或分歧尾部明确失败，保留证据，不截断旧历史。两个存储模块各自维护排他写入和并发读取规则。

Session 解析、事件归属校验和跨模块操作顺序由 Service 负责；Events 仅负责实时 Handler 分发与关闭，历史提交由 Service 直接协调 EventStore。Agent 状态观察异步投影 MetaStore，失败不阻塞广播；这不改变生命周期必需元数据提交。Session 删除后的事件保留/清理与部分失败策略尚待明确，不能把元数据删除与日志清理假定为共同事务；close 仍保留身份、历史与恢复材料，同名重建不得串历史。事件 SQL 重建不改变元数据版本或资源身份。

旧持久资源中的 `archived` 只作为启动迁移输入，不属于当前公共状态或查询值。Service 在开放业务接口前将其状态写为 `offline`，保留 metadata、spec、sessionId、历史、UUID 与同步位置，并更新 resourceVersion；重复启动可重复校正。持久化和查询实现不得继续接受新写入的 `archived`，也不得把它解释为空结果。

编码失败明确报错，不静默丢字段；不新增容量限制或隐式格式转换。除 `archived` 到 `offline` 的启动校正外，不增加自动数据迁移。SQL 提交不等于 SSE 网络送达，不新增可靠投递承诺。

## 尚待细化的协议

本轮确认模块、权威选择及写入方向；具体布局与格式版本、文件持久屏障与掉电强度、半写恢复、准确等价比较、增量投影校验、游标期限、后端迁移和跨模块删除结算需继续定义。此页不把设计更新宣告为已完成实现。

[^split-direction]: 当前人类请求明确授权更新 harness、spec 与 tickets，先不继续实现。

## Session 主配置

Session spec 可保存初始显式 `config.model/effort` 字符串。旧记录省略 config 保持可读，不自动迁移个人数据。在线 setter 不保存回执、当前值或恢复材料，不修改 spec/generation/appliedGeneration；恢复重新请求初始值。可变配置不参与 ID/cwd/账号与数据归属的恢复身份比较。详见[配置合同](../modules/runtime/session-config.md)。
