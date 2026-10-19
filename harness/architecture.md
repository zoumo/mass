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
| [Store](modules/store/design.md) | 资源 CRUD、generation 与历史持久化；隐藏存储实现 | MetaStore（CRUD）、HistoryStore（追加、查询、同步进度） |
| [Runtime](modules/runtime/design.md) | 类型配置、Driver 构造、实际运行与原生会话 | RuntimeRegistry、RuntimeDriver |

```text
HTTP / CLI / 进程内调用
          │
          ▼
Service：RuntimeService / SessionService
       │                    │
       ▼                    ▼
     Store                Runtime
资源、索引、历史       RuntimeRegistry → RuntimeDriver
```

Store 分为 MetaStore 与 HistoryStore 两项职责。文件系统保存完整持久数据；SQLite 仅作可完全重建的索引与查询加速层，具体规则见 [Store](modules/store/design.md)。

Service 同时依赖 Store 和 Runtime；二者不反向调用 Service。运行时只依赖共享值合同，不接收资源模型、名称目录或分页。HTTP 负责协议映射，Service 保持与传输无关。[^direction]

## 资源与运行的区别

Runtime 是 metadata/spec/status 资源，停止后仍保留。Driver 是按类型和配置构造的执行对象，实际进程或连接可以停止和重建。getRuntime 返回资源快照；获取资源不授予 Driver，不隐式启动 Agent。

RuntimeRegistry 按 Runtime 的 name 管理当前 Driver，Registry.create 也接收同一个 name。Service 维护 Runtime 名称、原 Driver、运行配置快照和原操作关联；Registry 不解释 Session 的 namespace/name 或资源 generation。Session 全局 sessionId 可以直接寻址，名称引用由Service 模块转换。

Runtime 更新只修改期望配置，已运行 Driver 继续使用原快照。Driver.start 成功后由Service 模块记录 appliedGeneration；DriverSession 不返回 generation。版本一致不证明健康。

## 创建与恢复

1. Service 模块校验名称，读取 Runtime 资源并预留实例引用；资源创建与配置更新通过 RuntimeRegistry.validateConfig(type, config) 校验配置，不创建运行实例。
2. 需要启动时串行调用 Registry.create 构造并登记 Driver，再调用 start 并保存本次配置快照；已运行时通过 Registry 取得原 Driver。
3. Driver 创建原生会话并返回 sessionId；Service 模块原子登记全局 ID 索引、名称键和 spec.runtimeRef 名称归属，再公开成功。
4. 持久化失败时，Service 模块协调 Driver 清理本次运行；未知结果保留原操作责任。

恢复先验证原资源归属与已提交历史，再由 Driver 内部读取和验证恢复材料并连接原会话。不能以新建替代恢复。Session 仅按 Runtime 名称归属，不比较 Runtime uid；原生会话与恢复材料仍由 Driver 验证。查询和历史读取不执行激活。

## 事件、历史与名称通道

每个 Driver 通过 events() 交付一条含其全部 Session 的事件流，Service 独占消费，按 sessionId 和原运行关联从已有绑定映射 Session 通道。完整事实先提交再发布，实时预览不成为持久历史。创建前到达事件由原操作暂存，身份提交后才交付；旧运行事件不能更新新运行状态。单 Session 保序、分会话背压隔离和停止排空规则见 [Driver 事件流](modules/runtime/events.md#driver-事件流)，不定义跨 Session 的业务顺序。

Session、历史及消费者的Service 侧职责分别见 [Session](modules/service/session.md)、[历史](modules/service/history.md)、[处理器](modules/service/handlers.md)。统一 SessionEvent 由 Runtime 定义，adapter 优先取原始 time、缺失时生成，创建统一事件时生成一次 UUID 作为 eventId，同一事件重试保留 UUID；当前不做原生事件去重；Service 在打开/恢复时传入名称，adapter 按原运行绑定补齐完整事件，Service 核对归属并协调提交发布，Store 原样保存。事件移除 seq，HistoryStore 使用不透明游标分页并返回 syncedEventId；ID 不表示排序关系。

## 停止协调

Service 模块封闭新激活、会话创建/恢复、输入和回答，排空会话、在途操作与已接纳事实，再调用原 Driver.stop；确认释放后从 Registry 移除实例。Driver 对未结算执行仍执行保护检查；结果未知时不能更新为已停止或自动重新接纳。

停止后保留 Runtime 配置、Session 身份和历史。不同实例独立；单会话错误不停止健康共享运行。远程 Driver 只能释放自己拥有的连接与任务。

## 合同与实现状态

[接口版本与校验入口](typespec/README.md)统一管理 Service 与 Runtime 的 TypeSpec。HTTP 与存储映射在 bindings；资源 CRUD、目录分页不进入运行时接口。Runtime 列表不分页，Session 和历史列表可分页。

资源状态、开放配置与能力、消息、事件、分页和订阅均已有 TypeSpec 声明；unknown 仅用于有意开放的动态值。各语言实现需要遵守相同的名称归属、接受证据、恢复、历史提交及释放规则；尚未确定的文件提交和编码等规则在对应合同中列明。此设计不声明实现已经完成。

[^direction]: 当前讨论确定 Service 依赖 Store 持久化资源、依赖 Runtime 执行运行操作；HTTP 只做协议映射，具体接口由负责人定义。
