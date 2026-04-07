# 统一修改计划（设计契约收口 + shim-rpc 重设计 + 实现加固）

> 状态：草案
> 日期：2026-04-07
> 适用范围：`docs/design/*`、`docs/plan/*`、`pkg/*`、`cmd/*`
> 来源：
> - `docs/plan/code-improvement-plan.md`
> - `docs/plan/shim-rpc-redesign.md`
> - `docs/design/*` 现状审查

---

## 1. 目标

将当前分散的三类工作合并为一条连续的修改路线：

1. **收口设计契约**：修正 `docs/design` 中已经出现的术语漂移、职责重叠和状态语义冲突。
2. **重设计 shim-rpc**：将其从“半翻译层”收敛为“ACP 超集 + runtime 扩展”，减少协议重复描述和抽象泄漏。
3. **补齐实现层可靠性**：修复已知 bug，补全事件恢复、workspace 引用安全、Terminal 操作、测试覆盖和文档缺口。

这份计划是新的统一入口。旧计划文档保留作为背景材料，但后续执行应以本文为准。

---

## 2. 当前主要问题（合并归类）

### 2.1 设计契约冲突（P0）

#### DES-001：Room 的所有权定义冲突

当前文档中同时存在三种说法：

- `Room Spec` 说 orchestrator 拥有 room 生命周期，`agentd` “只看到独立 session，不看到 room”。
- `agentd.md` 设计了 `RoomManager`。
- `ari-spec.md` 暴露了 `room/create` / `room/status` / `room/delete`。

这三者不能同时成立。需要明确：

- Room 是 **纯编排对象（desired state）**，还是
- Room 同时也是 **agentd 持有的运行时对象（realized state）**。

#### DES-002：Session 元数据模型不完整

`agentd.md` 中 `Session` / `CreateSessionOpts` 只覆盖了 `runtimeClass`、`workspace`、`room`、`labels`、`systemPrompt`，但 `ProcessManager.Start()` 又依赖：

- `env`
- `mcpServers`
- `permissions`
- 初始 prompt / bootstrap 行为

这会直接影响：

- agentd 重启恢复
- warm / cold pause
- `session/load`
- 审计和状态重建

#### DES-003：ACP 启动序列没有收口

现有文档对以下问题存在冲突或模糊：

- `cwd` 到底来自 `workspaceId` 写入 `acpAgent.session.cwd`，还是来自 `agentRoot.path` 运行时解析
- `acpAgent.session` 是可选字段，但 runtime lifecycle 又假定 ACP session 已建立
- `systemPrompt` 被定义为静默 seed prompt，而 `ARI session/new` 同时又携带 `prompt`
- 初始 prompt 是否阻塞、是否可见、是否记入历史没有统一定义

#### DES-004：状态模型分裂但无映射表

目前至少有三套状态：

- runtime state：`creating / created / running / stopped`
- session state：`created / running / paused:warm / paused:cold / stopped`
- process status：`alive / stopped / unknown` 等

缺少统一映射表会导致：

- UI / API 展示不一致
- 重启恢复逻辑模糊
- warm / cold pause 与 runtime state 无法稳定对齐

#### DES-005：Workspace 身份与共享语义未定义

缺少以下规则：

- `workspace/prepare` 是每次新建，还是按 spec 去重
- `git` / `local` / `emptyDir` 的 identity 如何计算
- hooks 有副作用时是否允许复用
- `local` source 是否做 canonicalize（`Clean` + `EvalSymlinks`）

这会影响：

- Room 内共享 workspace 是否真实可控
- setup hooks 是否重复执行
- cleanup 是否安全
- RefCount 语义是否可靠

#### DES-006：事件恢复协议存在竞态窗口

当前方案是：

1. `GetHistory(fromSeq)` 拉历史
2. `Subscribe()` 订阅未来

两者之间存在窗口期，agentd 重连时可能漏事件。现有设计缺少“从某个 seq 原子恢复并切入 live stream”的语义。

#### DES-007：Room 消息语义定义不完整

当前仅定义：

- 目标空闲：转发 prompt
- 目标忙碌：返回 `agent busy`
- 默认不排队、不打断

但未定义：

- `room_broadcast` 的部分成功语义
- 交付顺序
- timeout
- correlation / causation 标识
- sender / receiver 的结果结构

#### DES-008：安全边界放得过后

当前安全相关内容主要放在 roadmap 的 production readiness 阶段，但如下入口是基础层就已开放的：

- `local` source 直接接宿主目录
- setup / teardown hooks 执行任意命令
- runtimeClass / session env 可注入宿主环境变量
- Room 内多个 agent 可共享同一读写目录

这些需要提前进入设计契约，而不是等到后期再补。

---

### 2.2 shim-rpc 设计问题（P0 / P1）

#### RPC-001：shim-rpc 命名与 ACP 割裂

当前 shim-rpc 使用：

- `Prompt`
- `Cancel`
- `GetState`
- `GetHistory`

ACP 使用：

- `session/prompt`
- `session/cancel`
- `session/update`

同一体系内存在两套方法命名，阅读和实现切换成本高。

#### RPC-002：shim-rpc 的“翻译层”定位不彻底

当前事件模型声称是“typed event 翻译”，但很多事件实际只是 ACP 命名的轻度改写，没有形成稳定的新抽象，反而增加维护成本。

#### RPC-003：`file_read` / `file_write` / `command` 事件基于错误假设

如果 runtime 明确不实现 ACP 的 `fs` / `terminal` client capability，那么：

- shim 不会收到这些 client-side 请求
- 这些事件不会真实发生
- 文档中的相关事件类型属于“僵尸契约”

#### RPC-004：state 中缺少 ACP `sessionId`

shim-rpc 调用需要区分：

- OAR agent ID（runtime 内部 ID）
- ACP `sessionId`（agent 在 `session/new` 返回）

当前设计没有把 `sessionId` 提升为 state 的一等字段，恢复逻辑不完整。

---

### 2.3 代码与实现层问题（P0 / P1 / P2）

以下条目来自现有 `code-improvement-plan.md`，保留并纳入统一路线：

#### BUG-001（P0）：`agentd` 优雅关闭超时单位错误

- 文件：`cmd/agentd/main.go`
- 问题：`context.WithTimeout(..., 30)` 实际为 30ns
- 修复：改为 `30*time.Second`

#### IMP-001（P1）：`EventLog` 缺少损坏恢复机制

- 文件：`pkg/events/log.go`
- 问题：尾部截断或半行会导致重放失败
- 方向：skip-and-continue，保留 warning；必要时增加 checksum

#### IMP-002（P1）：`ARI Server` 测试覆盖不足

重点补充：

- `workspace/cleanup` 的 RefCount 保护
- `session/attach` / `session/detach` round-trip
- 参数校验错误路径
- 并发 `session/prompt` 语义

#### IMP-003（P1）：Terminal 操作未实现

- 文件：`pkg/runtime/terminal.go`
- 方向：Create / Output / Kill / timeout / 集成测试

#### IMP-004（P2）：错误前缀风格不统一

统一为：`component: operation: detail`

#### IMP-005（P2）：`session/status` 缺少运行时统计字段

按需补充：

- PID
- uptime
- last prompt 时间
- 其它运行时指标（按平台能力决定）

#### IMP-006（P2）：socket 清理策略存在竞争窗口

- 文件：`cmd/agentd/main.go`
- 方向：避免 `Stat -> Remove -> Serve` 的竞态，改为带锁或 listen/retry 策略

#### IMP-007（P2）：Workspace RefCount 没有持久化或可靠重建

- 文件：`pkg/workspace/manager.go`
- 方向：持久化到 Meta Store，或从活跃 session 重建

#### IMP-008（P3）：ARI 协议文档缺口

当前已有 `docs/design/agentd/ari-spec.md`，但与实现和设计契约未完全收口，需要在本计划中作为“文档收口”工作的一部分处理，而不是再单独写一份旁路文档。

#### IMP-009（P3）：Room / Orchestrator 规划继续推进

这一项保留，但前提是先完成 DES-001 / DES-007 / DES-008 的契约收口。

#### IMP-010（P3）：依赖更新计划

继续保留为例行工作，不单独作为本轮主线。

---

## 3. 统一设计决策（本轮修改的收口方向）

### DEC-001：Room 分为 desired state 与 realized state

建议明确采用以下分层：

- **Room Spec**：orchestrator 的期望态对象
- **Room Runtime Object**：agentd 中的运行时对象（如果保留 `room/*`）

文档必须二选一并写明：

- 要么 agentd 只认 session 上的 room 元数据，不暴露 `room/*`
- 要么 agentd 公开 `room/*`，但说明自己管理的是 runtime room，而不是编排决策本身

本计划建议采用第二种：**desired / realized 双层模型**。因为现有 `agentd.md` 与 `ari-spec.md` 已经朝这个方向走了，返工成本更低。

### DEC-002：Session 拆为 Meta + Config

建议把 session 设计为两个逻辑层：

- `SessionMeta`：id、state、labels、room、timestamps
- `SessionConfig`：runtimeClass、workspaceId、systemPrompt、env、mcpServers、permissions、bootstrap policy

无论代码上是否拆 struct，文档层必须先拆概念。

### DEC-003：ACP 启动序列固定化

建议收口为以下顺序：

1. start process
2. ACP `initialize`
3. ACP `session/new`（总是发送，参数可为空默认值）
4. optional `systemPrompt` seed prompt（静默）
5. runtime 进入 `created`
6. 后续外部 `session/prompt` 才进入 `running`

补充规则：

- `cwd` 来自 `agentRoot.path` 解析结果，不通过 ARI 直接写 `acpAgent.session.cwd`
- `acpAgent.session` 表示 `session/new` 的附加参数集合，不代表“是否创建 ACP session”
- `ARI session/new.prompt` 若保留，必须定义为 bootstrap prompt，并说明其可见性、阻塞行为和失败语义；若不保留，应删除该字段，统一通过后续 `session/prompt` 注入任务

### DEC-004：shim-rpc 定位为 ACP 超集

采用 `shim-rpc-redesign.md` 的方向：

- ACP 核心方法与事件：尽量对齐 ACP 命名与结构
- runtime 扩展：使用 `runtime/*`

建议命名：

| 当前 | 新命名 |
|------|--------|
| `Prompt` | `session/prompt` |
| `Cancel` | `session/cancel` |
| `Subscribe` | `runtime/subscribe` |
| `GetState` | `runtime/get_state` |
| `GetHistory` | `runtime/get_history` |
| `Shutdown` | `runtime/shutdown` |

### DEC-005：Runtime 不实现 ACP `fs` / `terminal` client capability

建议将此作为明确设计立场写入文档：

- runtime 不是 IDE，不负责 editor buffer / terminal panel 语义
- agent 通过自身工具直接操作 workspace 与 shell
- 因此 shim-rpc 删除 `file_read` / `file_write` / `command` 事件

### DEC-006：事件恢复改为 cursor / seq 驱动的原子恢复

建议修改为以下任一语义，并以此更新文档和实现：

- `runtime/subscribe(fromSeq)`：先补缺口，再进入 live
- 或 `runtime/resume(cursor)`：原子恢复

不再保留“先 GetHistory 再 Subscribe”的非原子模式作为主语义。

### DEC-007：为 workspace 预留访问模式

建议在设计层增加或预留：

- `shared-rw`
- `shared-ro`
- `per-agent-worktree`

即使本轮不实现，也要把它写成明确的扩展点，避免 Room 的共享 workspace 被默认成永远可并发写。

---

## 4. 分阶段修改计划

## Phase A — 先修致命 bug，收口最核心契约（P0）

### 目标

让文档不再互相打架；先修对运行安全有直接影响的 bug。

### 工作项

- [ ] 修复 `BUG-001`：`cmd/agentd/main.go` 的 shutdown timeout 单位错误
- [ ] 在 `docs/design/orchestrator/room-spec.md`、`docs/design/agentd/agentd.md`、`docs/design/agentd/ari-spec.md` 三者之间统一 Room 所有权模型
- [ ] 在 `docs/design/agentd/agentd.md` 中补全 session 配置字段模型（至少补概念层）
- [ ] 在 `docs/design/runtime/runtime-spec.md` 与 `docs/design/runtime/config-spec.md` 中统一 `cwd` / `agentRoot.path` / `acpAgent.session` 的语义
- [ ] 决定是否保留 `ARI session/new.prompt`；若保留，定义其 bootstrap 语义；若不保留，删掉并统一走 `session/prompt`
- [ ] 增加状态映射表：session state ↔ runtime state ↔ process state

### 交付物

- 修订后的 `docs/design/*`
- 1 个小型代码修复 PR（BUG-001）

### 完成标准

- `docs/design` 中不存在“同一职责被两份文档以相反方式描述”的条目
- Session / Runtime / Process 三套状态能够通过一张表互相映射

---

## Phase B — shim-rpc 协议收敛（P0 / P1）

### 目标

把 shim-rpc 从“半翻译层”改为“ACP 超集”，减少协议漂移。

### 工作项

- [ ] 更新 `docs/design/runtime/shim-rpc-spec.md`
  - [ ] 方法命名切换到 `session/*` + `runtime/*`
  - [ ] 统一通知事件模型
  - [ ] 删除 `file_read` / `file_write` / `command` 事件
  - [ ] 写清 `id`（OAR agent ID）与 `sessionId`（ACP sessionId）的区别
- [ ] 更新 `docs/design/runtime/agent-shim.md`
  - [ ] 将“ACP 不穿透”改为“ACP 超集”描述
  - [ ] 增加“不实现 fs/terminal capability”的设计立场
- [ ] 更新 `docs/design/runtime/runtime-spec.md`
  - [ ] `state.json` 新增 `sessionId`
  - [ ] 在 lifecycle 中补充 `session/new` 成功后写入 `sessionId` 的要求
- [ ] 设计并落地新的事件恢复模型（`subscribe(fromSeq)` 或等价机制）

### 代码影响

- `pkg/spec/state_types.go`
- `pkg/runtime/runtime.go`
- `pkg/rpc/server.go`
- `pkg/runtime/client.go`
- `pkg/events/*`

### 完成标准

- shim-rpc 文档不再维护一套与 ACP 平行但语义重复的命名体系
- 重连补历史不存在显式竞态窗口

---

## Phase C — 恢复、持久化与安全边界（P1 / P2）

### 目标

让 agentd 的恢复逻辑、workspace 生命周期和输入边界具备可落地的可靠性。

### 工作项

- [ ] 落地 `IMP-007`：Workspace RefCount 的持久化或可靠重建
- [ ] 落地 `IMP-001`：EventLog 的截断恢复能力
- [ ] 为 SessionConfig 的持久化建模
- [ ] 在 `workspace-spec.md` 中定义 workspace identity / reuse / cleanup 语义
- [ ] 在 `workspace-spec.md` 与 `agentd.md` 中明确 hooks 的执行边界：timeout、失败处理、stdout/stderr 捕获
- [ ] 在设计文档中新增或补充以下安全约束：
  - [ ] `local.path` 的允许范围 / canonicalize 要求
  - [ ] `${VAR}` 环境变量展开的 allowlist 或策略
  - [ ] Room 共享 workspace 的风险说明与访问模式预留
- [ ] 落地 `IMP-006`：socket 绑定竞态修复

### 完成标准

- agentd 重启后不会因 RefCount 丢失而错误删除被 session 使用中的 workspace
- 损坏的事件日志不会让完整历史完全不可读
- 文档中已明确所有高风险入口的边界条件

---

## Phase D — Runtime 能力补齐与 Room 语义细化（P1 / P2）

### 目标

补齐当前 roadmap 中最影响可用性的运行时能力，并把 Room 的消息模型从“可想象”变成“可实现”。

### 工作项

- [ ] 落地 `IMP-003`：Terminal 操作实现
- [ ] 落地 `IMP-005`：`session/status` 运行时统计字段
- [ ] 在 `room-spec.md` / `ari-spec.md` / `agentd.md` 中补齐 Room 消息语义：
  - [ ] 单播 / 广播的返回结构
  - [ ] busy 处理与 timeout
  - [ ] 顺序保证与幂等边界
  - [ ] sender / receiver 标识与 correlation 字段
- [ ] 若继续推进 Phase 4，则把 `IMP-009` 分解为：
  - [ ] `RoomManager` 运行时对象
  - [ ] room CRUD
  - [ ] 点对点与广播路由
  - [ ] shared workspace 访问模式

### 完成标准

- `room_send` / `room_broadcast` 的交付语义足够明确，开发者无需猜测失败与部分成功的行为
- Terminal 相关需求不再停留在 roadmap stub 状态

---

## Phase E — 测试、文档与收尾（P1 / P3）

### 目标

把前四阶段的结果变成可回归验证、可对外解释的稳定接口。

### 工作项

- [ ] 落地 `IMP-002`：ARI Server 测试补齐
- [ ] 为 shim-rpc 新语义补充协议测试与重连测试
- [ ] 为 state / recovery / event cursor 补集成测试
- [ ] 落地 `IMP-004`：统一错误前缀风格
- [ ] 收口 ARI 文档，不再让实现和规范分叉
- [ ] 保留 `IMP-010` 为例行依赖更新工作

### 完成标准

- 新旧接口的迁移路径清晰
- 核心状态机、恢复语义、workspace 生命周期都有自动化验证

---

## 5. 优先级总览

| 编号 | 描述 | 优先级 | 类型 | 主要文件 |
|------|------|--------|------|---------|
| BUG-001 | agentd 优雅关闭超时单位错误 | P0 | 代码 | `cmd/agentd/main.go` |
| DES-001 | Room 所有权冲突 | P0 | 设计 | `docs/design/orchestrator/room-spec.md`, `docs/design/agentd/*` |
| DES-002 | Session 模型不完整 | P0 | 设计 | `docs/design/agentd/agentd.md` |
| DES-003 | ACP 启动序列未收口 | P0 | 设计 | `docs/design/runtime/*`, `docs/design/agentd/ari-spec.md` |
| DES-004 | 状态模型缺少映射表 | P0 | 设计 | `docs/design/runtime/*`, `docs/design/agentd/*` |
| DES-006 | 事件恢复存在竞态 | P0 | 设计/代码 | `docs/design/runtime/shim-rpc-spec.md`, `pkg/events/*`, `pkg/rpc/*` |
| RPC-001 | shim-rpc 命名与 ACP 割裂 | P1 | 协议 | `docs/design/runtime/shim-rpc-spec.md`, `pkg/rpc/*` |
| RPC-003 | file_read/file_write/command 僵尸事件 | P1 | 协议 | `docs/design/runtime/shim-rpc-spec.md`, `pkg/events/*` |
| IMP-001 | EventLog 损坏恢复 | P1 | 代码 | `pkg/events/log.go` |
| IMP-002 | ARI 测试补齐 | P1 | 测试 | `pkg/ari/*` |
| IMP-003 | Terminal 操作实现 | P1 | 代码 | `pkg/runtime/terminal.go` |
| DES-005 | Workspace identity / reuse 语义 | P1 | 设计 | `docs/design/workspace/workspace-spec.md`, `docs/design/agentd/agentd.md` |
| DES-007 | Room 消息语义不完整 | P1 | 设计 | `docs/design/orchestrator/room-spec.md`, `docs/design/agentd/ari-spec.md` |
| DES-008 | 安全边界前置 | P1 | 设计 | `docs/design/workspace/*`, `docs/design/agentd/*` |
| IMP-006 | socket 绑定竞态 | P2 | 代码 | `cmd/agentd/main.go` |
| IMP-007 | RefCount 持久化/重建 | P2 | 代码 | `pkg/workspace/manager.go`, `pkg/meta/*` |
| IMP-004 | 错误前缀统一 | P2 | 整洁度 | 多文件 |
| IMP-005 | session/status 统计字段 | P2 | API | `pkg/ari/server.go` |
| IMP-009 | Room / Orchestrator Phase 4 | P3 | 规划 | 新模块 |
| IMP-010 | 依赖更新 | P3 | 维护 | `go.mod` |

---

## 6. 推荐执行顺序

### 第一批（立即执行）

1. `BUG-001`
2. `DES-001` / `DES-002` / `DES-003` / `DES-004`
3. `DES-006`

### 第二批（协议与恢复）

4. `RPC-001` / `RPC-003` / `RPC-004`
5. `IMP-001`
6. `IMP-007`
7. `IMP-006`

### 第三批（能力补齐）

8. `IMP-003`
9. `DES-005` / `DES-007` / `DES-008`
10. `IMP-002` / `IMP-005` / `IMP-004`

### 第四批（后续演进）

11. `IMP-009`
12. `IMP-010`

---

## 7. 验证与完成定义

本计划完成时，至少应满足以下条件：

### 设计层

- [ ] `docs/design` 中不存在同一概念被多份文档以相反方式定义的情况
- [ ] Room / Session / Runtime / Workspace / Event 五条主线各有唯一主语义
- [ ] ACP bootstrap、session 状态、event 恢复都可用一页图或一张表讲清楚

### 协议层

- [ ] shim-rpc 与 ACP 的关系明确：哪些透传，哪些是 runtime 扩展
- [ ] `sessionId` 与 runtime `id` 的双 ID 模型被文档和代码同时承认
- [ ] 事件恢复不存在显式竞态窗口

### 实现层

- [ ] shutdown timeout bug 已修复
- [ ] EventLog 可跳过损坏尾部
- [ ] Workspace RefCount 可持久化或可靠重建
- [ ] Terminal 操作可用
- [ ] socket 绑定竞态已收敛

### 测试层

- [ ] ARI 核心路径有自动化测试覆盖
- [ ] shim-rpc 重连 / 历史补全有测试覆盖
- [ ] workspace cleanup 的引用保护有测试覆盖

---

## 8. 说明

这份统一计划不要求一次性全部完成。它的作用是把当前三条并行但彼此交叉的工作线：

- 设计收口
- 协议重设计
- 实现加固

放进同一条依赖明确的路线里。后续如果拆 milestone，应优先沿本计划的 Phase A → B → C 顺序推进，而不是直接跳到 Room / Orchestrator 的功能扩展。
