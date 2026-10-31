---
type: Implementation Status
title: 实现差距记录
description: 集中记录绑定源码版本、检查日期和证据的实现差距，不替代模块合同。
status: stable
sources:
  - id: module-review
    resource: 项目负责人在 2026-10-22 的 Module 设计审视与 harness 更新授权
    title: 设计合同与实现进度分开维护
---

# 实现差距记录

本页记录检查时的实现事实，不改变 [Module 合同](index.md#模块)。每次更新须绑定日期、源码版本、相关工作区变更、检查方式与证据；未核对的能力保持未验证。已有证据不能自动证明后续源码仍符合合同。设计变化历史见[更新记录](log.md)。

## 2026-10-22 静态核对

检查基线为分支 v2，HEAD `422805b627b324c00cae43d5630dc04dd5ddcfbe`，包含检查时已有的未提交工作区变更。本次只读核对源码与当前合同，不执行 Go 测试或真实 Agent 验收，不据此声明模块整体已经完成。关键源码内容标识见下表后的 SHA-256 清单。

| 范围 | 静态观察与差距 | 证据 |
|---|---|---|
| 存储拆分与元数据权威 | 已有独立 MetaStore/EventStore Interface 和分别打开的 Adapter；默认配置选择 SQLite 元数据权威，可显式选择 file。旧设计页中“尚未拆分、元数据 SQLite 仍为派生副本”的进度描述已过期。该观察不证明全部恢复、并发和故障场景通过。 | [MetaStore](../mass-go/pkg/metastore/metastore.go)、[后端选择](../mass-go/pkg/metastore/backend/open.go)、[EventStore](../mass-go/pkg/eventstore/eventstore.go)、[程序组装](../mass-go/cmd/massd/main.go) |
| Events | 已有 EventBroadcast、Handler、内部队列订阅与取消实现；不能继续用“尚未实现”概括。并发、关闭及全部事件合同的一致性本次未重新验证。 | [Broadcast](../mass-go/pkg/events/broadcast.go) |
| Registry 当前运行映射 | Implementation 仍由 Registry 保存 drivers 并提供 Get/Remove，Service 另有 runs。与当前合同要求 Registry 只校验和构造、Service 独占当前运行映射不符，待实现调整。 | [Registry](../mass-go/pkg/runtime/runtime.go)、[Service](../mass-go/pkg/service/service.go) |
| EventStore 自动续同步 | Implementation 在 uncertain 时拒绝继续操作并提示 reopen，另有显式 Reconcile。SQL 已保存文件位置及哈希，但不能以存在检查点证明当前合同的失败后自动续同步已实现。待通过公共 Interface 验证后续触发自动续接、原 ID 幂等、位置不提前推进及不补广播。 | [可用性检查及 Reconcile](../mass-go/pkg/eventstore/file/file.go)、[SQL 检查点](../mass-go/pkg/eventstore/file/replica.go) |
| Service 停止协调 | 公共 StopRuntime 已在 Service 中实现；本次删除的是设计中的固定组装层多步 Interface。完整最终事件排空、并发与未知责任行为本次未重新验收，不将其列为已全部通过。 | [停止实现](../mass-go/pkg/service/lifecycle.go) |
| 显式异步 load | 当前公开 Service 和 Driver Interface 尚无 loadSession/cancelHistoryLoad，与 [load 合同](modules/service/load.md)存在差距；本次未核对 SDK 升级及全部客户端承载。 | [Service Interface](../mass-go/pkg/service/service.go)、[Driver Interface](../mass-go/pkg/runtime/runtime.go) |

本次未核对按需启动与服务重启校正的完整实现，不沿用旧文档中的进度结论。状态存储和异步状态投影按现有合同保留，已讨论的旧运行投影限制暂时接受，本次不作修订。

### 源码内容标识

以下 SHA-256 绑定本次静态观察的实际文件内容，包含已有工作区修改；它们不是测试或验收结果。

| mass-go source | SHA-256 |
|---|---|
`pkg/metastore/metastore.go` | `e3dabd937ecf757005a4b048367758648cc4e2515a0d79f2f5c93c879130a9bf`
`pkg/metastore/backend/open.go` | `3fc01415eab67324bec6f512a94ab9a29b33167d4d1f128e64c2cd53f1870e96`
`pkg/eventstore/eventstore.go` | `89737f47ab6929c5a5e035e17d702dad058c838ce91c92d2f33334d31069d743`
`cmd/massd/main.go` | `bfbef534ed9d912ce79ab778fc2a722ad7135fed6502152ec69d61d4237a229d`
`pkg/events/broadcast.go` | `e9bd825f0244fa5b1a51fd72d7e73f97756cb3d007de4017d327a7537a9c9387`
`pkg/runtime/runtime.go` | `df4e4957ffa6b7f583177767413f2a688431ff591ab7bc0f7bf2a5d35015a43e`
`pkg/service/service.go` | `eca55cac0f838179af68a7f9211679820f69a504d40ad78a83c83aab4f7789c4`
`pkg/eventstore/file/file.go` | `e2a17db7f992e77757ad06a74de1b2f27437efaf43d3e7a9545b70723a4141a6`
`pkg/eventstore/file/replica.go` | `ebb0118c22b244e60d684bcddc7a7773f8214c7a25a586dc0bb5695b5039c4e8`
`pkg/service/lifecycle.go` | `cf6020fac123c92c702601f3f85c8892f3060a81a9dff33b2583fd3dc06cbeb0`
