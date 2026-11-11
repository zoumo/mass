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

## 2026-11-11 在线主 Session 单项配置核对

检查基线 `5dae1aa795b567c042f24dd0155307254ef4cd71`，分支
`codex-sdk-gap-02-live-session-config-setter` 的 setter 变更；固定提交、源码清单及实际
检查结果见 `/Users/jim/.codex/worktrees/7379/mass/.scratch/sdk-source-of-truth-gap-fixes/ticket02-report.md`
与同目录 `ticket02-focused-race-final.log`。本次 deadline 修复基于 setter 提交
`878d85af9f4a7f8b8d71596e9ad3b38293fcc750`，新增源码清单为
`ticket02-deadline-source-manifest.json`，最终聚焦证据为 `ticket02-deadline-final-results.json`。
本记录只报告实现与已观察差异，不修改配置合同。

| 范围 | 本次实现与证据边界 |
|---|---|
| 统一入口 | 可选 SessionConfigDriver、Service、两种 HTTP 寻址、client 与 massctl set-config 贯通 SDK 原主 Session ConfigController。Go 直接复用 SDK change/receipt，并保留错误链；HTTP 成功映射 camelCase，失败保持 code/reason/message。 |
| 原运行与瞬态 | 复用 prompt 的准入与原运行在途责任；close/stop 封闭新请求并等待旧责任结算。两 provider 的可控原生 peer 验证并发 setter、旧请求重复响应隔离及恢复初始值。setter 不保存 spec/generation/appliedGeneration、不制造配置观察；不是当前生效证明。 |
| 聚焦离线选择 | `LiveSessionConfiguration` 覆盖原生成功/拒绝/派发未知、SDK NotSent 与可选能力、HTTP/CLI/client 校验、Service 准入/未完成 load/idle 计时恢复。以报告及固定源码日志的实际退出码为准；合成 peer 不代表真实 Agent 验收。 |
| SDK NotSent deadline 适配 | 按父 chat 2026-11-11 的决定，仅 SDK unavailable/set_config_failed + receipt.sent=false + 原错误链 errors.Is(DeadlineExceeded) 映射公开 code 为 deadline_exceeded/504；SDK reason、原 SDK error/cause 和 receipt 保留。两 provider 真实 SDK + 合成 peer 的同一场景先 RED 后 GREEN，并验证过期调用未产生原生写入。true/nil、取消、字符串伪装、其他原因或明确结论不重分类；放弃等待仍未知。边界与整票 race 以新增 ticket02-deadline-* 日志为准。未 repin、未改公共合同或通用 MapError。 |
| 未验证 | 未执行完整 check、独立 code-review、真实 Agent、后续 captures 或真实 resume/load 验收。SDK/testify pins、原始 recordings 和个人数据保持原样。 |

## 2026-11-11 主 Session 初始配置聚焦核对

检查基线 `5de7d4cf7be4f06ef0c632da4f28b90c068d8a58`，分支
`codex-sdk-gap-01-initial-session-configuration` 的本次初始配置变更。检查时包含该 ticket
未提交变更；最终提交及源码 SHA-256 清单保存在
`/Users/jim/.codex/worktrees/7379/mass/.scratch/sdk-source-of-truth-gap-fixes/ticket01-report.md`
与同目录 `ticket01-source-manifest.json`，不以本记录代替固定源码验收。

`go test -list InitialSessionConfiguration` 证明选择非空；相同选择的
`go test -race -count=1 -timeout=180s -v -run InitialSessionConfiguration` 在 runtime、recovery、
service、transport、client、massctl/subcommand 和 integration 七个包退出 0。
最终原始日志为同目录 `ticket01-focused-race-final.log`。这只证明选中的离线场景，
未运行全量 check、独立 review 或真实 Agent。

| 范围 | 本次观察与边界 |
|---|---|
| 初始 config | create/load 的 model/effort 经 CLI、client/HTTP、Service、Driver 传给 SDK；Codex 严格帧验证 thread/start 与 thread/resume 的 model/model_reasoning_effort，Claude 原生合成进程 argv 验证 --model/--effort。非空值无翻译；空字符串按 SDK 省略。 |
| 持久与恢复 | spec 保存显式初始值；离线测试读取持久记录并 resume 同一 ID/uid/spec；load 保存本次 config，完成后在同一恢复连接继续输入。恢复材料排除 config，旧记录可读，ID/cwd/账号验证保留。 |
| 拒绝与隔离 | CLI/client/Service 的非法 UTF-8/NUL 在副作用前拒绝；HTTP 拒绝 null、非字符串、孤立 surrogate 及其他非法编码。两 provider 集成先拒绝非法请求再使用同名目标完成原生打开。临时目录及进程清理由测试拥有并读回。 |
| 后续缺口 | 瞬态 setter 仅定稿合同，未实现；新 captures、真实 completion/permission、真实 resume/load 与最终全量验收属于后续 ticket，未验证。Runtime 未增加 model/effort 默认值，SDK/testify pins 及旧 recordings 保持原样。 |

## 2026-11-08 Runtime Session 默认配置核对

检查源码为分支 `codex-runtime-session-defaults` 的提交 `59581a95be515aec4c8790951037dc6a455a5f9d`，检查时该提交无未提交修改。该变更在 Runtime spec 顶层增加 `features` 与 `mcpServers`，由 Service 在 Session 操作时解析，Session 仅持久化显式值。完整 `make check-go` 在该 SHA 上退出码为 0，原始日志为 `/tmp/pr9-final-check-20261108T223505.log`（RTK 对仿真冗长输出做了中间截断，日志首尾保留源码 SHA、退出码与结束 HEAD）。以下结论来自源码与离线测试核对，不证明真实 Codex、Claude 或外部 MCP server 已通过端到端验收。

| 范围 | 当前实现观察 | 证据与边界 |
|---|---|---|
| Runtime 资源模型 | `RuntimeSpec` 在 `config` 之外提供可选 features/mcpServers；公共 MCP 校验覆盖非空名称与唯一性、transport 字段组合、HTTPS/回环 HTTP、无 URL userinfo、SSE 拒绝 HTTP-only 认证字段、敏感 header/env 只接受环境变量引用，以及环境变量名结构。Runtime 启动不消费默认值，OpenAPI 已发布 `name.minLength=1`。 | [资源模型](../mass-go/pkg/resource/models.go)、[Session 默认值解析与 MCP 校验](../mass-go/pkg/runtime/session_config.go)、[Runtime 配置校验](../mass-go/pkg/service/runtime_config.go)、[OpenAPI](../mass-go/pkg/client/generated/openapi.json) |
| Session 合并 | create、resume 与 load 调用同一解析入口，读取当前 Runtime spec 后按字段合并 features、按名称合并 MCP server；同名 MCP 保持默认位置，新名称追加。Session 空列表不构成声明：不清空 Runtime 默认项，Runtime 也无默认值时合并结果仍为 nil 并保留 ambient。 | [Service 合并入口](../mass-go/pkg/service/session_config.go)、[create](../mass-go/pkg/service/service.go)、[resume](../mass-go/pkg/service/lifecycle.go)、[load](../mass-go/pkg/service/load.go) |
| 持久化边界 | Runtime 保存默认值；Session spec 保存显式覆盖值，不保存解析后的 Runtime 副本。恢复材料排除 features/mcpServers，因此默认值更新不改变恢复身份材料。 | [Session load 持久化](../mass-go/pkg/service/load.go)、[恢复材料](../mass-go/pkg/runtime/recovery/material.go) |
| Driver 合同与适配 | loadSession 接收完整 `DriverSessionSpec`；非 nil 合并列表（含空列表）是完整 server 集合，nil 保留 provider ambient。Claude 设置 `options.StrictMCPConfig`，以 strict 方式应用合并值，并在原生打开前按子进程环境校验敏感 env/header 的 `${VAR}` 存在且非空；敏感值保留原生引用由 CLI 展开，解析值不进入公开错误。Codex 要求显式、存在、可从别名解析为不同文件的绝对 `env.CODEX_HOME`，其 `config.toml` 不得在任意层级声明 `mcp_servers`；create、resume 与 load 均通过线程配置把 Session cwd 及其全部祖先目录、symlink 规范路径标记为 `untrusted`，同时显式传入生成的 MCP server 配置，阻止项目层配置重新泄漏 ambient server。Codex stdio env、普通 header 与多段内联 `${VAR}` 均按子进程一致的环境语义解析；精确引用映射为 `env_headers` / `bearer_token_env_var`，缺失、空值、非法 UTF-8/NUL、header CRLF、重复 header 与未解析引用均在原生打开前失败，公开错误不包含解析后的 secret。两者在原生打开/恢复前应用合并值，并分别拒绝不支持的 transport 或字段；公共 Service 级测试覆盖显式空列表与 nil 的边界。 | [Driver 接口](../mass-go/pkg/runtime/runtime.go)、[Codex MCP](../mass-go/pkg/runtime/codex/mcp.go)、[Codex load](../mass-go/pkg/runtime/codex/load.go)、[Claude MCP](../mass-go/pkg/runtime/claude/mcp.go)、[Claude load](../mass-go/pkg/runtime/claude/load.go)、[环境引用测试](../mass-go/pkg/runtime/codex/mcp_test.go)、[Claude 引用测试](../mass-go/pkg/runtime/claude/mcp_test.go)、[空列表边界测试](../mass-go/pkg/service/session_defaults_test.go) |
| CLI 与 HTTP | Runtime create/update 与 Session create/load 已接入 features/MCP JSON 输入；Runtime update 未传字段保留旧值、显式 null 清除，Session 输入作为显式覆盖。OpenAPI 与生成客户端已更新。 | [CLI Runtime](../mass-go/cmd/massctl/subcommand/runtime.go)、[CLI Session](../mass-go/cmd/massctl/subcommand/session.go)、[CLI 选项](../mass-go/cmd/massctl/subcommand/session_options.go)、[OpenAPI](../mass-go/pkg/client/generated/openapi.json) |
| 验证证据 | `make check-go` 在源码 SHA `59581a95be515aec4c8790951037dc6a455a5f9d` 上退出码 0，覆盖 lint、race 单元、race 集成、仿真、构建与格式检查；日志 `/tmp/pr9-final-check-20261108T223505.log`。Codex 历史 load 的 `thread/resume` 项目隔离由协议级集成测试 `TestTicket06CodexLoadUsesExplicitMCPIsolation` 覆盖；该用例通过 Runtime 已声明的空默认列表触发隔离，Session 空列表本身不构成声明。隔离真实 Codex 探针只验证了 `thread/start` 的 project trust 行为；resume 的真实探针因测试 thread 无 turn 不可用。未执行真实模型、真实 Codex/Claude MCP 连接、MCP 工具调用或远端 MCP server 验收。 | 上述源码 SHA 与日志；完整离线检查通过，不代表外部系统行为已验证。 |

## 2026-11-07 合并后核对

检查基线是工作树 `mass-worktrees/idle-session-close`、分支 `codex-idle-session-close`。功能源码继承自 `a35598f9`、`a990e264`、`31278442`、`573b74ac`、`cbcee5d1`、`588423c1`，并已合并 `origin/main` 的 `45449665`，合并 HEAD 为 `d0ef83e`。本轮随后增加 `f5d842a`（unknown 输入准入测试）与 `b59268b`（close 错误结算修复），并修订 Service 合同、恢复说明和验收证据。`b59268b` 修复完整门禁暴露的合并后问题：SDK close 失败被直接保留，成功时仍要求本进程观察结束；Stop 在 Cancel 未知后继续尝试 close，避免未知释放被重试误报为成功。

检查包括：`go test -race ./pkg/service -run Ticket03`（18 tests）、Claude live 容量失败（4 tests）、Codex disconnect（连续 3 次）、Codex/Claude SDK close failure targeted、隔离真实 Codex 与 Claude 五秒验收，以及最终完整 `make check`（integration 287.097s，`/tmp/mass-ticket07-final-check.log`）。完整检查同时通过 lint、unit、integration、simulation、build 和格式检查；真实 Agent 结论只覆盖两条验收实际使用的公开路径。

| 范围 | 当前实现观察 | 证据与边界 |
|---|---|---|
| Session close | 公共 Service、HTTP 和 massctl 已用 close 替换 archive；close 从任意 Session 状态接纳，调用当前 SDK Session close，成功进入 `offline`，失败保持 `closing`；offline 重复 close 幂等，unknown 无当前句柄时完成内部清理。关闭最后一个 Session 不停止 Runtime。 | [close 编排](../mass-go/pkg/service/close.go)、[Service 生命周期](../mass-go/pkg/service/lifecycle.go)、[HTTP/CLI vertical](../mass-go/tests/integration)、[真实 Codex 证据](../mass-go/docs/recovery.md) |
| 空闲自动 close | `sessionIdleTimeout` 默认 `10m`、`0` 禁用；仅当前 live 绑定可证明 idle 且无在途/交互/工具工作时计时，触发时重新校验并复用 manual close，失败不自动重试。 | [idle 实现](../mass-go/pkg/service/idle.go)、[Ticker 配置](../mass-go/cmd/massd)、可控时钟测试；真实 Codex 观察到 idle 后 5s 进入 offline，Runtime 保持 running。 |
| 重启校正与按需启动 | 启动只校正持久状态；archived 迁为 offline，旧在线 Session/Runtime 缺少结算证明时转 unknown，不启动 Driver 或 Session。冷进程无当前运行绑定时，显式 start/resume 可启动持久 unknown Runtime，因而 unknown Session 可继续原身份恢复；同一进程仍有运行绑定时仍拒绝重建。create/resume 复用显式 start 协调；prompt/answer/cancel 不隐式启动或恢复。 | [校正](../mass-go/pkg/service/startup.go)、[启动判断](../mass-go/pkg/service/service.go)、[恢复](../mass-go/pkg/service/lifecycle.go)、[冷重启测试](../mass-go/pkg/service/restart_lazy_ticket03_test.go) |
| unknown 输入准入 | prompt 不再按持久字符串一律拒绝 unknown：无 live 绑定或已处理 unknown 观察时，在 Driver open/resume/prompt 派发前失败；显式 create/resume 刚建立、初始观察未到达的 live unknown 可 dispatch。目标测试断言 detached/observed unknown 的 prompt、open、resume 计数不增加。 | [控制判定](../mass-go/pkg/service/lifecycle.go)、[fresh/detached/observed 测试](../mass-go/pkg/service/restart_lazy_ticket03_test.go)、[Service 合同](modules/service/design.md#显式恢复与输入准入) |
| close 错误结算 | Codex 的 SDK close 失败原样返回，不被观察状态覆盖；Claude 在 SDK close 返回后等待本进程观察结束，观察或原生错误仍阻止释放。Stop 在 Cancel 返回未知后仍尝试 close 并合并错误，避免后续重试把未确认释放误报为成功。 | [Claude close](../mass-go/pkg/runtime/claude/driver.go)、[Codex close](../mass-go/pkg/runtime/codex/driver.go)、[Stop 结算](../mass-go/pkg/service/lifecycle.go)、integration close/disconnect 用例。 |
| archive 移除与迁移 | `ArchiveSession`、HTTP archive 路径、massctl archive、`archived` 状态与筛选值已移除；旧 archived 记录启动时迁为 offline，保留身份、历史、UUID 和同步位置，不自动恢复。 | [迁移与移除测试](../mass-go/pkg/service/ticket05_remove_archive_test.go)、CLI/HTTP 回归；完整离线门禁通过，日志 `/tmp/mass-ticket07-final-check.log`。 |
| 真实 Agent 验收 | Codex 与 Claude 当前 post-merge 源码均完成 5s idle auto-close、offline prompt 拒绝、自动 resume、manual close/resume，Runtime 全程 running。Claude 通过公开的 create-with-`initialInput` 路径验收，发送的是真实用户 prompt；零输入 create 仍是独立未支持/未验证路径。 | Codex 当前可复核证据是日志 `/private/tmp/mass-ticket06-rerun-codex-20261107T185748-74332.log`，端口 55261；临时工作目录 `/private/tmp/mass-ticket06-rerun-codex.zO4vVG` 已清理。Claude 当前证据：`/private/tmp/mass-ticket06-rerun-claude.uxcHyU`，端口 49773，日志 `/private/tmp/mass-ticket06-rerun-claude-20261107T194331-48769.log`；CLI `2.1.258`，Session `64f0ba7a-ab9d-4042-956d-52e280896ae8`，`idle_to_offline=6s`。 |

Claude CLI `2.1.258` 的 bounded 诊断结果：零输入启动接受 SDK `initialize` 并输出 SessionStart hook，但不输出 `system/init`，因此零输入 create 仍是独立未支持/未验证路径，没有用隐藏 prompt 或更弱身份证据伪造通过。提供显式 `initialInput` 时，CLI 在真实用户帧后输出带匹配 `session_id`/`cwd` 的 native `system/init`，当前 post-merge 五秒验收据此 **PASS**；此前两次零输入失败是调用路径不匹配，不构成本次验收结果。

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
