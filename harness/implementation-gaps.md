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

## 2026-11-07 合并后核对

检查基线是工作树 `mass-worktrees/idle-session-close`、分支 `codex-idle-session-close`。功能源码继承自 `a35598f9`、`a990e264`、`31278442`、`573b74ac`、`cbcee5d1`、`588423c1`，并已合并 `origin/main` 的 `45449665`，合并 HEAD 为 `d0ef83e`。本轮随后增加 `f5d842a`（unknown 输入准入测试）与 `b59268b`（close 错误结算修复），并修订 Service 合同、恢复说明和验收证据。`b59268b` 修复完整门禁暴露的合并后问题：SDK close 失败被直接保留，成功时仍要求本进程观察结束；Stop 在 Cancel 未知后继续尝试 close，避免未知释放被重试误报为成功。

检查包括：`go test -race ./pkg/service -run Ticket03`（18 tests）、Claude live 容量失败（4 tests）、Codex disconnect（连续 3 次）、Codex/Claude SDK close failure targeted、隔离真实 Codex 五秒验收，以及最终完整 `make check`（integration 287.097s，`/tmp/mass-ticket07-final-check.log`）。完整检查同时通过 lint、unit、integration、simulation、build 和格式检查；不把此前的 offline 结果外推到真实 Claude。

| 范围 | 当前实现观察 | 证据与边界 |
|---|---|---|
| Session close | 公共 Service、HTTP 和 massctl 已用 close 替换 archive；close 从任意 Session 状态接纳，调用当前 SDK Session close，成功进入 `offline`，失败保持 `closing`；offline 重复 close 幂等，unknown 无当前句柄时完成内部清理。关闭最后一个 Session 不停止 Runtime。 | [close 编排](../mass-go/pkg/service/close.go)、[Service 生命周期](../mass-go/pkg/service/lifecycle.go)、[HTTP/CLI vertical](../mass-go/tests/integration)、[真实 Codex 证据](../mass-go/docs/recovery.md) |
| 空闲自动 close | `sessionIdleTimeout` 默认 `10m`、`0` 禁用；仅当前 live 绑定可证明 idle 且无在途/交互/工具工作时计时，触发时重新校验并复用 manual close，失败不自动重试。 | [idle 实现](../mass-go/pkg/service/idle.go)、[Ticker 配置](../mass-go/cmd/massd)、可控时钟测试；真实 Codex 观察到 idle 后 5s 进入 offline，Runtime 保持 running。 |
| 重启校正与按需启动 | 启动只校正持久状态；archived 迁为 offline，旧在线 Session/Runtime 缺少结算证明时转 unknown，不启动 Driver 或 Session。create/resume 在无当前运行绑定时按需启动并复用显式 start 协调；prompt/answer/cancel 不隐式启动或恢复。 | [校正](../mass-go/pkg/service/startup.go)、[创建与恢复](../mass-go/pkg/service/lifecycle.go)、[Ticket03 测试](../mass-go/pkg/service/restart_lazy_ticket03_test.go) |
| unknown 输入准入 | prompt 不再按持久字符串一律拒绝 unknown：无 live 绑定或已处理 unknown 观察时，在 Driver open/resume/prompt 派发前失败；显式 create/resume 刚建立、初始观察未到达的 live unknown 可 dispatch。目标测试断言 detached/observed unknown 的 prompt、open、resume 计数不增加。 | [控制判定](../mass-go/pkg/service/lifecycle.go)、[fresh/detached/observed 测试](../mass-go/pkg/service/restart_lazy_ticket03_test.go)、[Service 合同](modules/service/design.md#显式恢复与输入准入) |
| close 错误结算 | Codex 的 SDK close 失败原样返回，不被观察状态覆盖；Claude 在 SDK close 返回后等待本进程观察结束，观察或原生错误仍阻止释放。Stop 在 Cancel 返回未知后仍尝试 close 并合并错误，避免后续重试把未确认释放误报为成功。 | [Claude close](../mass-go/pkg/runtime/claude/driver.go)、[Codex close](../mass-go/pkg/runtime/codex/driver.go)、[Stop 结算](../mass-go/pkg/service/lifecycle.go)、integration close/disconnect 用例。 |
| archive 移除与迁移 | `ArchiveSession`、HTTP archive 路径、massctl archive、`archived` 状态与筛选值已移除；旧 archived 记录启动时迁为 offline，保留身份、历史、UUID 和同步位置，不自动恢复。 | [迁移与移除测试](../mass-go/pkg/service/ticket05_remove_archive_test.go)、CLI/HTTP 回归；完整离线门禁通过，日志 `/tmp/mass-ticket07-final-check.log`。 |
| 真实 Agent 验收 | Codex 在合并前后均完成 5s idle auto-close、offline prompt 拒绝、自动 resume、manual close/resume，Runtime 全程 running。Claude 合并前旧源码曾 PASS；合并后当前源码两次在 create 阶段返回 `outcome_unknown: operation_unconfirmed`。 | Codex 当前证据：`/private/tmp/mass-ticket06-rerun-codex.zO4vVG`，端口 55261。Claude 失败日志：`/private/tmp/mass-ticket06-rerun-claude-20261107T185818-75394.log`、`/private/tmp/mass-ticket06-rerun-claude-20261107T185930-76836.log`。 |

Claude CLI `2.1.258` 的 bounded 诊断结果：SDK `initialize` 真实进程测试通过，但无用户输入时 CLI 只输出 SessionStart hook 事件，不在观察窗口内输出 `system/init`；MASS 创建仍要求零输入观察 native init 的 sessionId/cwd，因此没有用隐藏 prompt 或更弱身份证据伪造通过。当前 post-merge Claude 真实五秒验收保持 **unverified**，旧合并前 Claude PASS 不能覆盖。该问题不影响已验证的 Codex 路径或离线状态机结果。

其他限制保持不变：没有 crash/power-loss、Linux、真实账户数据所有权、provider 后端持久性或跨进程人工结算验收。`make check` 只证明离线 race/integration/simulation/build/lint 门禁，不替代真实 Agent 验收。

## 2026-11-06 静态核对

检查基线为分支 `codex-idle-session-close` 的 HEAD `bfb12ad4168d4c423e281b64525d5c0aa367a5e0`（`main`）。本次只有 `harness/` 与根词汇表的设计变更，`mass-go/` 无工作区修改。检查方式是只读检索当前 Go 源码和接口声明，未运行 Go 测试、SDK 合同测试、离线仿真或真实 Agent 验收。

| 范围 | 静态观察与差距 | 证据 |
|---|---|---|
| Session 生命周期接口 | 公共 SessionService 仍暴露 `ArchiveSession`，没有 `CloseSession`；持久状态与查询仍接受 `archived`。与 close/offline 终态、任意状态 close、幂等显式重试和删除 archived 筛选的合同不符。 | [SessionService 接口](../mass-go/pkg/service/service.go)、[生命周期与状态校验](../mass-go/pkg/service/lifecycle.go) |
| Runtime 按需启动 | `CreateSession` 和 `ResumeSession` 缺少当前运行绑定时直接失败，未复用显式 start 的串行启动协调；服务启动校正也不满足新的 archived→offline、旧在线→unknown 规则。 | [创建与恢复入口](../mass-go/pkg/service/service.go)、[恢复实现](../mass-go/pkg/service/lifecycle.go) |
| 空闲自动 close | `massd` 配置和参数中没有 `sessionIdleTimeout` / `--session-idle-timeout`，Service 没有可控时钟或 idle 计时调度；默认 10m、0 禁用和失败不重试尚未实现。 | [daemon 配置](../mass-go/cmd/massd/config.go)、[daemon 参数](../mass-go/cmd/massd/main.go) |
| Driver close | Codex 和 Claude Driver 在 Session active/sending 时仍先返回 `ErrWorkUnsettled`，没有把 close 交给底层 SDK；因此 SDK close 成功作为唯一成功条件、并发/重复安全与不额外 interrupt/kill 的 Driver 合同尚未满足。 | [Codex close](../mass-go/pkg/runtime/codex/driver.go)、[Claude close](../mass-go/pkg/runtime/claude/driver.go) |
| HTTP 与 CLI | HTTP 仍注册 archive 路径，Session 查询枚举仍包含 `archived`；massctl 仍提供 archive 子命令并使用 `ArchiveSession`。close 路径、命令和禁止旧筛选值尚未实现。 | [HTTP 生命周期路由](../mass-go/pkg/transport/session_recovery.go)、[查询枚举](../mass-go/pkg/transport/session_identity.go)、[CLI Session 命令](../mass-go/cmd/massctl/subcommand/session.go) |

本次结论仅限于上述静态差距。关闭 active/sending Session、重启迁移、空闲计时、HTTP/CLI vertical、SDK 合同与真实 Codex/Claude 行为均未执行，不能声明已验证。

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
