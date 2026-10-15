# Codex 持久化、实时输出与数据库必要性

日期：2026-10-15。固定公开源码：`openai/codex@ea27864f99f0b086cec2f9f0251b7190fb9844f1`。

## 结论

**对从零设计的单实例 Agent 服务，已确认的需求尚未证明数据库必需；文件可以承担会话元数据和完整历史记录。当前版本的 Codex app-server 本身却要求 SQLite 初始化成功，不能以“历史是 JSONL”推导“Codex 不依赖 DB”。** 前半句是针对需求的架构推论，后半句是源码事实。

本研究只回答存储与事件分工，不决定新项目的最终文件格式、模块结构或 API。没有继承旧项目的代码、兼容或数据迁移要求。没有运行 Codex、模型调用、恢复测试或性能测试；下述测试均是静态阅读。

## 实时事件与历史记录

`core/session` 将事件交给持久化策略后再发送给消费者；`LiveThread` 对原始输入执行 `persisted_rollout_items`，存储层收到的是筛选后的 canonical items。[codex-rs/core/src/session/mod.rs:2564-2622](https://github.com/openai/codex/blob/ea27864f99f0b086cec2f9f0251b7190fb9844f1/codex-rs/core/src/session/mod.rs#L2564-L2622)；[codex-rs/thread-store/src/live_thread.rs:220-270](https://github.com/openai/codex/blob/ea27864f99f0b086cec2f9f0251b7190fb9844f1/codex-rs/thread-store/src/live_thread.rs#L220-L270)。

策略显式丢弃 `AgentMessageContentDelta`、`PlanDelta`、`ReasoningContentDelta`、`ReasoningRawContentDelta`、`ExecCommandOutputDelta`，也不把所有实时通知当历史。Paginated 模式持久化 `ItemCompleted(TurnItem)`、turn 开始/结束/中断等有语义的记录；Legacy 模式保留另一组完整消息/工具结束事件。它不是逐帧 SSE/通知日志。[codex-rs/rollout/src/policy.rs:127-225](https://github.com/openai/codex/blob/ea27864f99f0b086cec2f9f0251b7190fb9844f1/codex-rs/rollout/src/policy.rs#L127-L225)。

需要精确理解“完整”：消息/工具 item 是完整的语义对象，但命令聚合输出和 MCP 结果可能按 64 KiB 策略截断；模型上下文 ResponseItem 还有独立持久化选择。不能承诺持久历史与实时字节完全相同。[codex-rs/rollout/src/policy.rs:16-19](https://github.com/openai/codex/blob/ea27864f99f0b086cec2f9f0251b7190fb9844f1/codex-rs/rollout/src/policy.rs#L16-L19)；[codex-rs/rollout/src/policy.rs:78-99](https://github.com/openai/codex/blob/ea27864f99f0b086cec2f9f0251b7190fb9844f1/codex-rs/rollout/src/policy.rs#L78-L99)；[codex-rs/rollout/src/policy.rs:228-279](https://github.com/openai/codex/blob/ea27864f99f0b086cec2f9f0251b7190fb9844f1/codex-rs/rollout/src/policy.rs#L228-L279)。

`RealtimeItem` 这个名字也不能理解为普通 delta：该分支在 Paginated 模式会持久化，它属于另一种实时会话的结构化历史。分类要看类型及策略，不能按包含 realtime/chunk 的名称机械判断。[codex-rs/rollout/src/policy.rs:21-49](https://github.com/openai/codex/blob/ea27864f99f0b086cec2f9f0251b7190fb9844f1/codex-rs/rollout/src/policy.rs#L21-L49)。

## 数据分工

| 数据 | 当前实现 | 权威性与必要性 |
|---|---|---|
| 会话完整历史、模型上下文、SessionMeta | rollout JSONL；可压缩、分段、有 ordinal | canonical history；不能靠 metadata DB 取代内容文件 |
| 可查询的 thread 元数据、标题、筛选排序信息 | `state_5.sqlite`，同时存在 JSONL/name-index 兼容路径 | 部分来自历史可回填，但部分 DB-only 元数据可能丢失；不能全称为可随时删除的缓存 |
| turn/item 分页读取与投影进度 | `thread_history_1.sqlite`：turns、items、projection state | 从 rollout 按 byte offset/ordinal 生成的查询投影；内容可重投影，但当前分页实现直接依赖 DB |
| 日志 | `logs_2.sqlite` | 观测数据；不是统一会话历史的必需构件 |
| goals 与继续执行的 deferrals | `goals_1.sqlite` | 功能状态，不等同于历史缓存；简单会话服务无需继承此功能 |
| memories、阶段输出、后台 jobs | `memories_1.sqlite`，可选 `memories_v2_1.sqlite` | memory 工作流状态，不能从聊天历史无损还原所有运行信息 |
| 未消费队列、队列 revision | `queue_1.sqlite` | 真实待执行状态，不是已完成历史的索引；只有需求包含持久队列才需要解决它 |
| 项目、section 顺序、agent jobs、远程控制登记、附件元数据等 | state schema 的独立表 | Codex 产品功能；不能把整套产品的 DB 需求转嫁给最小 Agent 服务 |
| 插件安装元数据与 manifest | 此次抽查路径使用 JSON 文件/原子临时文件写入 | 至少该路径不以 DB 为必需；本研究未穷尽全部插件相关功能 |

存储边界的自述：[codex-rs/thread-store/README.md:7-29](https://github.com/openai/codex/blob/ea27864f99f0b086cec2f9f0251b7190fb9844f1/codex-rs/thread-store/README.md#L7-L29)。DB 文件清单与恢复策略：[codex-rs/state/src/sqlite.rs:34-136](https://github.com/openai/codex/blob/ea27864f99f0b086cec2f9f0251b7190fb9844f1/codex-rs/state/src/sqlite.rs#L34-L136)。分页 schema：[codex-rs/state/thread_history_migrations/0001_thread_history.sql:1-38](https://github.com/openai/codex/blob/ea27864f99f0b086cec2f9f0251b7190fb9844f1/codex-rs/state/thread_history_migrations/0001_thread_history.sql#L1-L38)。附属数据表：[codex-rs/state/queue_migrations/0001_queued_items.sql:1-11](https://github.com/openai/codex/blob/ea27864f99f0b086cec2f9f0251b7190fb9844f1/codex-rs/state/queue_migrations/0001_queued_items.sql#L1-L11)；[codex-rs/state/memory_migrations/0001_memories.sql:1-35](https://github.com/openai/codex/blob/ea27864f99f0b086cec2f9f0251b7190fb9844f1/codex-rs/state/memory_migrations/0001_memories.sql#L1-L35)；[codex-rs/state/migrations/0014_agent_jobs.sql:1-35](https://github.com/openai/codex/blob/ea27864f99f0b086cec2f9f0251b7190fb9844f1/codex-rs/state/migrations/0014_agent_jobs.sql#L1-L35)；[codex-rs/state/migrations/0049_projects.sql:1-25](https://github.com/openai/codex/blob/ea27864f99f0b086cec2f9f0251b7190fb9844f1/codex-rs/state/migrations/0049_projects.sql#L1-L25)。插件 JSON 写入：[codex-rs/core-plugins/src/store.rs:187-240](https://github.com/openai/codex/blob/ea27864f99f0b086cec2f9f0251b7190fb9844f1/codex-rs/core-plugins/src/store.rs#L187-L240)。

## 缺库、损坏与恢复：必须分层

1. **app-server 启动层**：`try_init` 成功才启动；普通初始化失败返回错误。损坏被识别后先备份数据库及关联文件再重建，单个损坏目标的重复失败会退出。最终返回 `Some(state_db)`，并非自动进入无 DB 模式。[codex-rs/app-server/src/lib.rs:659-668](https://github.com/openai/codex/blob/ea27864f99f0b086cec2f9f0251b7190fb9844f1/codex-rs/app-server/src/lib.rs#L659-L668)；[codex-rs/app-server/src/lib.rs:1386-1453](https://github.com/openai/codex/blob/ea27864f99f0b086cec2f9f0251b7190fb9844f1/codex-rs/app-server/src/lib.rs#L1386-L1453)。
2. **底层兼容读取层**：`LocalThreadStore` 可接受无 state DB；普通列表/旧历史读取有文件降级，但 section 排序、关系/项目筛选、显式 DB-only 查询并不保证降级。README 的 SQLite-less 支持应限定在这层。[codex-rs/thread-store/src/local/list_threads.rs:24-120](https://github.com/openai/codex/blob/ea27864f99f0b086cec2f9f0251b7190fb9844f1/codex-rs/thread-store/src/local/list_threads.rs#L24-L120)；[codex-rs/thread-store/src/local/list_threads.rs:123-146](https://github.com/openai/codex/blob/ea27864f99f0b086cec2f9f0251b7190fb9844f1/codex-rs/thread-store/src/local/list_threads.rs#L123-L146)；[codex-rs/thread-store/src/local/list_threads.rs:268-350](https://github.com/openai/codex/blob/ea27864f99f0b086cec2f9f0251b7190fb9844f1/codex-rs/thread-store/src/local/list_threads.rs#L268-L350)；[codex-rs/thread-store/src/local/read_thread.rs:31-128](https://github.com/openai/codex/blob/ea27864f99f0b086cec2f9f0251b7190fb9844f1/codex-rs/thread-store/src/local/read_thread.rs#L31-L128)。
3. **新分页历史层**：`list_turns/list_items` 要求 state DB 中有适用元数据，随后访问 thread_history DB；无 state DB 直接 `Unsupported`。投影从 canonical 文件读取，自记录的 offset 接着处理，缺投影状态从 0 开始。[codex-rs/thread-store/src/local/thread_history/read.rs:102-218](https://github.com/openai/codex/blob/ea27864f99f0b086cec2f9f0251b7190fb9844f1/codex-rs/thread-store/src/local/thread_history/read.rs#L102-L218)；[codex-rs/thread-store/src/local/thread_history_materialization.rs:22-82](https://github.com/openai/codex/blob/ea27864f99f0b086cec2f9f0251b7190fb9844f1/codex-rs/thread-store/src/local/thread_history_materialization.rs#L22-L82)。
4. **恢复的模型上下文**：有独立文件读取路径，Paginated 模式逆向扫描 rollout 到足够的 compaction 边界，Legacy 模式可读取全部。数据库不是上下文内容本身；但不能据此断言 app-server 的完整 resume 入口无 DB 可运行。[codex-rs/thread-store/src/local/model_context.rs:28-80](https://github.com/openai/codex/blob/ea27864f99f0b086cec2f9f0251b7190fb9844f1/codex-rs/thread-store/src/local/model_context.rs#L28-L80)；[codex-rs/core/src/thread_manager.rs:1215-1239](https://github.com/openai/codex/blob/ea27864f99f0b086cec2f9f0251b7190fb9844f1/codex-rs/core/src/thread_manager.rs#L1215-L1239)。
5. **测试边界**：`state_recovery_restores_saved_threads` 先正常保存 turn，再破坏 state DB，重启后断言列表与读取到的 turns 保持一致。另一个测试覆盖 logs/goals 损坏后的备份与新建 thread。警告明确说 DB-only metadata 可能不可用。这证明设计意图和已有静态用例，不是本轮运行验证，也不是“所有 DB 丢失都无损”的证据。[codex-rs/app-server/tests/suite/v2/sqlite_recovery.rs:16-79](https://github.com/openai/codex/blob/ea27864f99f0b086cec2f9f0251b7190fb9844f1/codex-rs/app-server/tests/suite/v2/sqlite_recovery.rs#L16-L79)；[codex-rs/app-server/tests/suite/v2/sqlite_recovery.rs:84-195](https://github.com/openai/codex/blob/ea27864f99f0b086cec2f9f0251b7190fb9844f1/codex-rs/app-server/tests/suite/v2/sqlite_recovery.rs#L84-L195)。

## 对新服务的需求推论

已确认的边界是单实例本机子进程、承载 JSON-RPC 的 HTTP 操作、只实时的 SSE、独立历史查询、delta 不落盘、显式恢复会话。它们没有要求跨会话复杂查询、持久队列、跨文件事务或完整 Codex 产品功能，因此 **可先选文件作为候选基线，暂不引入 DB**，而不是照搬 Codex 的 DB 表。

一个可供后续决策比较的最小候选是：每个 Session 一份元数据文件及追加的完整历史记录，进程内维护活跃对象和索引；重启扫描并重建列表；SSE 从内存分发临时 delta，历史查询读已提交语义记录。恢复另行保存 agent 原生 session 标识和必要启动参数；是否可恢复仍取决于 adapter/ACP 能力，不能只靠统一历史反向重建任意 agent 的内部状态。

这只是候选，不是最终规格。文件方案仍须明确：单写者、元数据原子替换、记录半行/损坏处理、版本、完成/失败/中断内容、重启时状态、刷盘级别、分页游标、读写一致性及容量上限。当前 Codex writer 的 `flush()` 不等于本研究证明了机器断电级 fsync 保证；不应默认继承某个崩溃窗口。[codex-rs/rollout/src/recorder.rs:1908-1918](https://github.com/openai/codex/blob/ea27864f99f0b086cec2f9f0251b7190fb9844f1/codex-rs/rollout/src/recorder.rs#L1908-L1918)；[codex-rs/rollout/src/recorder.rs:2100-2113](https://github.com/openai/codex/blob/ea27864f99f0b086cec2f9f0251b7190fb9844f1/codex-rs/rollout/src/recorder.rs#L2100-L2113)。

若后续证明需要高效跨 Session 筛选排序、百万级 item 查询、原子“操作接受 + 队列入队 + 状态更新”或多写者协调，再以 **SQLite 的具体事务/查询收益** 对比文件实现成本。数据库不是概念上绝对必要；它可能是满足这些新增需求更简单的工程选择。优先把可重建查询索引与不可丢的业务事实分清，避免复制双写体系却没有恢复契约。

## 对待评审契约的直接影响

- 实时事件协议必须标明哪些通知仅为临时 delta、哪些对应历史中的完整 item；不能让客户端假设每条 SSE 都可查询。
- JSON-RPC 成功响应表示已接受、已执行还是已持久化，必须逐方法定义；文件和 SQLite 都不会自动给出这层语义。
- 历史查询游标属于历史记录序列，不应暗示它是实时 SSE 的 Last-Event-ID；运行中内容与完成历史的可见时间需要单独约定。
- item/turn 完成、中断、错误及进程重启后的最终状态需要明确；只保存成功完成内容会使已发出的部分结果在中断后消失。
- 存储选型影响确认/查询延迟与崩溃保证，但不应泄漏到公开方法名或事件结构。用户应优先评审事件协议、HTTP 上的 JSON-RPC 契约和技术选型，内部模块拆分不作为本研究结论。

## Rust 技术选型可参考的部分

Codex app-server 已使用 Tokio、Axum、Serde/serde_json、tracing、thiserror/anyhow、UUID；state crate 使用 SQLx SQLite。新项目可独立评估前一组作为异步 HTTP、子进程、结构化协议和日志基础。SQLx 仅在选定 SQLite 后纳入；JSON Schema/TypeScript 生成也按客户端需求决定。Codex 的 transport 包使用 WebSocket，不应把其端到端传输误记为已验证的 HTTP+SSE 方案。[codex-rs/app-server/Cargo.toml:29-36](https://github.com/openai/codex/blob/ea27864f99f0b086cec2f9f0251b7190fb9844f1/codex-rs/app-server/Cargo.toml#L29-L36)；[codex-rs/app-server/Cargo.toml:102-121](https://github.com/openai/codex/blob/ea27864f99f0b086cec2f9f0251b7190fb9844f1/codex-rs/app-server/Cargo.toml#L102-L121)；[codex-rs/state/Cargo.toml:16-25](https://github.com/openai/codex/blob/ea27864f99f0b086cec2f9f0251b7190fb9844f1/codex-rs/state/Cargo.toml#L16-L25)；[codex-rs/app-server-protocol/Cargo.toml:44-56](https://github.com/openai/codex/blob/ea27864f99f0b086cec2f9f0251b7190fb9844f1/codex-rs/app-server-protocol/Cargo.toml#L44-L56)；[codex-rs/app-server-transport/Cargo.toml:18-24](https://github.com/openai/codex/blob/ea27864f99f0b086cec2f9f0251b7190fb9844f1/codex-rs/app-server-transport/Cargo.toml#L18-L24)。

## 证据与发布边界

源码 checkout 为干净工作树；origin 是 `https://github.com/openai/codex.git`，GitHub CLI 的公开 commit 查询确认固定 SHA 存在。以上证据均来自该公开源码，未使用私有项目内容或本机用户数据库。本报告只发布公开源代码研究与一般设计推论；不发布原始会话、内部源码、配置、凭据或运行数据。
