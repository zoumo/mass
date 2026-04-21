# Gastown 多 Agent 编排系统深度分析

> 源码: https://github.com/gastownhall/gastown
> 语言: Go 94.8%, 14.4k stars
> 分析日期: 2026-04-20

## 一、系统定位

Gastown 是一个面向 AI 编码 Agent 的多 Agent 编排系统，协调 Claude Code、GitHub Copilot、Codex、Gemini 等 AI Agent 在 Git 仓库上并行工作。核心能力：

- **Agent 身份持久化**：Agent 有永久身份（CV 链），跨 session 保持
- **Git worktree 隔离**：每个 worker 独立 worktree，互不干扰
- **消息驱动协调**：mail（持久）+ nudge（临时）两级消息
- **容量调度**：capacity-controlled dispatch，防止 API 限流
- **自治执行**：Agent 发现任务立即执行，无需确认（Propulsion Principle）

---

## 二、角色体系

### 2.1 两级架构

| 层级 | 角色 | 职责 |
|------|------|------|
| **Town 级** | Mayor | 全局协调者，跨 rig 通信，分发 convoy |
| | Deacon | 守护进程，收心跳，运行 patrol 监控 |
| | Boot | Deacon 看门狗，轻量级，每次心跳评估"要不要唤醒 Deacon" |
| | Dog | 基础设施批处理（Doctor/Reaper/Compactor/Backup） |
| **Rig 级** | Witness | 每 rig 一个，监控 polecat 健康，僵尸检测，生命周期管理 |
| | Refinery | 每 rig 一个，合并队列，batch-then-bisect 策略 |
| | Polecat | 临时工 worker，持久身份 + 临时 session，干完活回 IDLE 池 |
| | Crew | 人类工作空间，完整 git clone |

### 2.2 三层看门狗链

```
Daemon (Go, 机械式)          ← 不能推理 agent 状态
    ↓ (3分钟心跳)
Boot (AI, 临时)              ← 每次心跳新起，只回答"要不要唤醒 Deacon"
    ↓ (需要时)
Deacon (AI, 长期运行)        ← 持续巡逻，但无法观察自己
    ↓
Witnesses & Refineries       ← 每 rig 健康监控
```

**Boot 决策矩阵**：

| 心跳年龄 | 动作 |
|----------|------|
| < 5min (新鲜) | 无操作 |
| 5-15min + 有 mail | Nudge |
| > 15min (过期) | Wake |

### 2.3 Agent 身份归因模型

```
BD_ACTOR 格式（斜杠分隔路径）:
mayor                           # Town 级
deacon                          # Town 守护进程
gastown/witness                 # Rig 级 witness
gastown/polecats/toast          # Worker polecat
gastown/crew/joe                # 人类
```

**归因层**：
- Git commits: `GIT_AUTHOR_NAME="gastown/polecats/toast"`
- Beads 记录: `created_by`, `updated_by` 字段
- Events: 所有遥测带 `actor` 字段

---

## 三、Polecat（Worker Agent）生命周期

### 3.1 三层分离架构

| 层 | 组件 | 生命周期 | 持久性 |
|----|------|----------|--------|
| **身份** | Agent bead, CV 链, 工作历史 | 永久 | 不销毁 |
| **沙箱** | Git worktree, 分支 | 每次分配，可复用 | 跨 session 存活 |
| **Session** | Claude/Copilot 实例, 上下文窗口 | 临时 | handoff 时循环 |

### 3.2 状态机

```
IDLE ──(gt sling)──→ WORKING ──(gt done)──→ IDLE
                         │                    ↑
                         ├─(gt handoff)───────┘ (session 循环, polecat 继续)
                         │
                         ├─(session 死亡)──→ STALLED (Witness 检测)
                         ├─(自报卡住)──→ STUCK
                         └─(zombie 检测)──→ ZOMBIE
```

**关键行为**: `gt done` 不销毁 polecat：
1. Push 分支, 提交 MR
2. 清除 hook（工作完成）
3. 同步 sandbox 到 main
4. 转为 IDLE, session 死亡, sandbox 保留

### 3.3 完成保证

三条件确保最终完成：
1. 工作通过 `hook_bead` 钉在 agent bead 上
2. Sandbox 持久化（分支和 worktree 不变）
3. Witness 在崩溃后重新生成 session

---

## 四、消息系统

### 4.1 双层通信模型

| 类型 | 机制 | 持久性 | 存储 | 适用场景 |
|------|------|--------|------|----------|
| **Mail** | Beads 记录 + Dolt commit | 持久 | Dolt SQL | 协议消息(MERGE_READY, HANDOFF, HELP) |
| **Nudge** | tmux send-keys → `<system-reminder>` | 临时 | 无 | 健康检查、状态请求、唤醒信号 |

**规则**: 默认用 nudge。只在"消息必须在接收者 session 死亡后存活"时用 mail。

### 4.2 消息类型

**核心 Message 结构**:

```go
type Message struct {
    ID          string
    From        string      // 发送者地址 (e.g., "gastown/Toast")
    To          string      // 直接接收者
    Subject     string
    Body        string      // key-value 对 + markdown
    Priority    Priority    // low/normal/high/urgent
    Type        MessageType // task/escalation/scavenge/notification/reply
    Delivery    Delivery    // queue(邮箱) / interrupt(nudge)
    ThreadID    string      // 会话线程
    Queue       string      // 队列路由
    Channel     string      // 广播
    Wisp        bool        // 临时（不同步到 git）
}
```

**协议消息类型**:
- `POLECAT_DONE`: 工作完成 (Polecat → Witness)
- `MERGE_READY / MERGED / MERGE_FAILED`: 合并流水线
- `REWORK_REQUEST`: 冲突需要 rebase
- `RECOVERED_BEAD / RECOVERY_NEEDED`: 死 polecat 恢复
- `HELP`: 升级请求
- `HANDOFF`: session 上下文传递

### 4.3 地址解析

```
直接: gastown/Toast, mayor/, deacon/
组:   @witnesses, @crew/gastown, @rig/gastown, @town
队列: queue:name
频道: channel:name
```

**组展开**: 查询带 `gt:agent` 标签的 beads → 展开为个体地址 → fan-out 发送

### 4.4 Mail 预算（每角色）

| 角色 | Mail 预算 | 说明 |
|------|-----------|------|
| Polecat | 0-1/session | 仅 HELP/ESCALATE |
| Witness | 协议消息 | MERGE_READY, RECOVERED_BEAD 等 |
| Refinery | 协议消息 | MERGED, MERGE_FAILED 等 |
| Dog | 零 | 结果走 event beads |

### 4.5 路由系统

**Beads 路由表** (`routes.jsonl`):
```jsonl
{"prefix":"hq-","path":"."}
{"prefix":"gt-","path":"gastown/mayor/rig"}
```

**路由流程**:
1. 从 bead ID 提取前缀 (e.g., `gt-xyz` → `gt-`)
2. 查路由表找到 rig 路径
3. 解析 beads 目录（跟随 `.beads/redirect`）

---

## 五、调度系统

### 5.1 容量控制调度

```go
type DispatchCycle struct {
    AvailableCapacity func() (int, error)      // 空闲 slot
    QueryPending      func() ([]PendingBead, error)  // 待调度工作
    Execute           func(PendingBead) error  // 执行调度
    OnSuccess         func(PendingBead) error  // 后处理
    OnFailure         func(PendingBead, error) // 错误处理
    BatchSize         int
    SpawnDelay        time.Duration
}
```

**调度公式**:
```
toDispatch = min(capacity, batchSize, readyCount)
capacity = maxPolecats - activePolecats
```

### 5.2 两种模式

| 模式 | 条件 | 行为 |
|------|------|------|
| Direct | max_polecats = -1 或 0 | 立即调度，无排队 |
| Deferred | max_polecats > 0 | 进入队列，daemon 每 3 分钟调度 |

### 5.3 熔断器

```go
func CircuitBreakerPolicy(maxFailures int) FailurePolicy {
    return func(failures int) FailureAction {
        if failures >= maxFailures {
            return FailureQuarantine  // 隔离，不再重试
        }
        return FailureRetry
    }
}
```

- 阈值: 3 次连续失败
- 动作: 关闭调度上下文，阻止进一步调度
- 重置: 需手动介入

### 5.4 状态管理

Sling context beads（临时 beads）存储调度元数据：work bead ID、target rig、formula、args、入队时间、失败计数。

File lock (`scheduler-dispatch.lock`) 防止并发调度。

---

## 六、Agent 人格与模板系统

### 6.1 角色配置层次

```
内置默认 (binary) → Town 级 (~/.gt/roles/{role}.toml) → Rig 级 (~/.gt/{rig}/roles/{role}.toml)
```

**TOML 配置**:
```toml
[role]
name = "witness"
description = "Per-rig lifecycle manager"

[health]
heartbeat_interval = "5m"
stalled_threshold = "30m"
```

### 6.2 人格模板

Markdown 文件注入在 `gt prime` 时（session 启动）：
- Theory of Operation（运作原理）
- Available Tools（可用工具）
- Rules（规则）
- Workflows（工作流）

**示例**: Polecat 目录纪律 — 警告只能在 worktree 中操作

### 6.3 指令系统（Directives）

运营者策略覆盖，Markdown 文件：
```
~/gt/directives/<role>.md              # Town 级
~/gt/<rig>/directives/<role>.md        # Rig 级（连接，rig 优先）
```

### 6.4 Formula 覆盖（Overlays）

TOML 文件修改工作流步骤：
```
~/gt/formula-overlays/<formula>.toml   # Town 级
~/gt/<rig>/formula-overlays/<formula>.toml  # Rig 级（完全替换）
```

三种模式: `replace`（替换步骤）, `append`（追加内容）, `skip`（跳过步骤）

### 6.5 上下文恢复（Seance）

跨 session 上下文恢复：
- `.runtime/.handoff` 文件防止 handoff 循环
- 存储前一个 session ID + 原因（compaction, overflow）
- 新 session 警告"不要重新执行前任的 handoff"

### 6.6 Hook 系统（Claude Code 生命周期钩子）

集中管理 Claude Code hook：
```
~/.gt/hooks-base.json              ← 共享基础（所有 agent）
~/.gt/hooks-overrides/
  ├── crew.json                    ← 覆盖所有 crew
  ├── witness.json                 ← 覆盖所有 witness
  └── gastown__crew.json           ← 覆盖 gastown 的 crew
```

**合并策略**: base → role → rig+role（更具体的优先）

**默认 hook（5 个启用）**:
- `session-prime` (SessionStart) — 运行 `gt prime`
- `pre-compact-prime` (PreCompact) — compaction 时重新 prime
- `mail-check` (UserPromptSubmit) — `gt mail check --inject`
- `pr-workflow-guard` (PreToolUse) — 阻止 force push
- `dangerous-command-guard` (PreToolUse) — 阻止破坏性命令

---

## 七、Daemon 架构

### 7.1 心跳循环

```
心跳 Tick (每 5 分钟):
├─ 查询已知 rig（本 tick 缓存）
├─ RigWorkerPool::runPerRig (并行, 最多 10 并发):
│  ├─ Witness 检查: session 存活, 卡住检测
│  ├─ Refinery 检查: 分支清理
│  ├─ Polecat 收割: 空闲超时
│  └─ 每 rig 超时: 30 秒
├─ 运行配置的 patrol:
│  ├─ Witness/Refinery/Deacon patrol
│  ├─ DoltServer/DoltRemotes/DoltBackup
│  ├─ WispReaper/DoctorDog/CompactorDog 等
├─ 处理生命周期请求 (cycle/restart/shutdown)
├─ 检测批量 session 死亡 (N 死亡 / M 秒 → 告警)
├─ 更新 daemon state.json
└─ 导出 OpenTelemetry 指标
```

### 7.2 并发模型

```go
type RigWorkerPool struct {
    concurrency int           // 并行 goroutine 数 (默认 10)
    timeout     time.Duration // 每 rig 超时 (默认 30s)
}
```

一个慢 rig 不会阻塞其他 rig。

### 7.3 Convoy 管理器

两个 goroutine:
- **Event poll**: 每 5s 查 lifecycle events，检测 issue close → 检查 convoy 完成度
- **Stranded scan**: 每 30s 查滞留 convoy，自动 feed 就绪 issue

### 7.4 Claim-Then-Execute 模式

```go
// 关键: 先删除消息，再执行动作
// 防止心跳重复处理过期消息
d.closeMessage(msg.ID)  // claim
d.executeLifecycleAction(request)  // execute
```

---

## 八、存储架构

### 8.1 Dolt SQL Server

每 town 一个 Dolt SQL 进程（端口 3307），all-on-main 写策略：

```sql
BEGIN
  UPDATE issues SET status='in_progress' WHERE id='gt-abc'
  CALL DOLT_COMMIT('-Am', 'update status')
COMMIT
```

不使用 per-worker 分支，立即跨 agent 可见。

### 8.2 三层数据平面

| 平面 | 数据 | 变化频率 | 持久性 | 存储 |
|------|------|----------|--------|------|
| 操作 | 进行中工作 | 高 | 天-周 | Dolt SQL |
| 账本 | 已完成工作 | 低 | 永久 | JSONL → git push |
| 设计 | Epic/RFC | 对话式 | 到结晶 | DoltHub (计划) |

---

## 九、关键设计原则

| 原则 | 说明 |
|------|------|
| **Propulsion Principle** | Hook 上发现工作 = 立即执行，无需确认 |
| **Zero Framework Cognition (ZFC)** | Agent 决策（调用 gt/bd），Go 代码只传输，不推理 |
| **Reality is Truth** | 物理现实（git, 文件系统, Dolt）是权威，派生状态按需计算 |
| **Persistent Identity, Ephemeral Sessions** | 身份永久，session 随时循环 |
| **Dumb Scheduler, Smart Agents** | daemon 处理安全和协调，agent 保持自治 |

---

## 十、与 MASS 对比分析

### 10.1 定位差异

| 维度 | MASS | Gastown |
|------|------|---------|
| **定位** | 单机 daemon，管理 AI agent 生命周期 | 多项目编排平台，协调大规模 agent 群 |
| **规模** | 单 host, 少量 agent | 多 rig(项目), 4-30+ agent 并行 |
| **协议** | ACP (Agent Communication Protocol) JSON-RPC | gt/bd CLI + beads + Dolt SQL |
| **存储** | bbolt (嵌入式 KV) | Dolt SQL Server (SQL 数据库) |
| **通信** | Unix socket JSON-RPC 2.0 | mail (Dolt 持久) + nudge (tmux 临时) |
| **隔离** | Workspace (git/empty/local) | Git worktree per polecat |

### 10.2 架构对比

```
MASS:                              Gastown:
┌──────────────┐                   ┌──────────────────┐
│ orchestrator │                   │ Mayor (AI 协调者)  │
│  (massctl)   │                   │ + Deacon (AI 守护) │
└──────┬───────┘                   └────────┬─────────┘
       │ ARI JSON-RPC                       │ mail/nudge
┌──────▼───────┐                   ┌────────▼─────────┐
│ agentd       │                   │ Daemon (Go 心跳)   │
│ (Go daemon)  │                   │ + Scheduler        │
└──────┬───────┘                   └────────┬─────────┘
       │ Unix socket                        │ tmux session
┌──────▼───────┐                   ┌────────▼─────────┐
│ agent-run    │                   │ Polecat/Crew      │
│ (shim 进程)  │                   │ (AI session)      │
└──────┬───────┘                   └────────┬─────────┘
       │ ACP JSON-RPC                       │ git worktree
┌──────▼───────┐                   ┌────────▼─────────┐
│ AI Agent     │                   │ AI Agent          │
│ (Claude etc) │                   │ (Claude etc)      │
└──────────────┘                   └───────────────────┘
```

### 10.3 Agent 管理对比

| 特性 | MASS | Gastown |
|------|------|---------|
| **Agent 标识** | (workspace, name) 复合键 | BD_ACTOR 路径 (rig/role/name) |
| **进程模型** | self-fork + Unix socket | tmux session + CLI 注入 |
| **状态持久化** | bbolt + state.json | Dolt SQL + agent bead |
| **恢复机制** | RecoverSessions (shim reconnect) | Witness patrol + seance (上下文恢复) |
| **Agent 人格** | 无（靠 ACP agent 自身配置） | 角色模板 + 指令 + formula overlay |

### 10.4 消息系统对比

| 特性 | MASS | Gastown |
|------|------|---------|
| **传输** | JSON-RPC 2.0 over Unix socket | Dolt SQL beads + tmux send-keys |
| **事件** | Envelope (seq, turnId, streamSeq) | NDJSON event file + Dolt events |
| **订阅** | session/subscribe + SubscribeFromSeq | mail check (poll) + nudge (push) |
| **顺序保证** | 全局单调 seq + turn 内 streamSeq | 无严格全局序，event 按时间戳 |
| **Agent 间通信** | 通过 orchestrator 转发 | 直接 mail/nudge，无需中间人 |

### 10.5 调度对比

| 特性 | MASS | Gastown |
|------|------|---------|
| **调度模型** | compose YAML 声明式 | capacity-controlled dispatch cycle |
| **容量控制** | 无内置限流 | max_polecats + batch_size + spawn_delay |
| **熔断器** | 无 | CircuitBreakerPolicy (3次失败隔离) |
| **工作分配** | 手动 prompt/compose | gt sling (自动分配) + hook (即发即做) |

---

## 十一、可借鉴之处

### 11.1 Agent 身份持久化 ⭐⭐⭐

**Gastown 做法**: Agent 有永久 CV 链，记录所有工作历史，支持能力匹配路由。

**启发**: MASS 的 agent 身份是 `(workspace, name)` 复合键，但缺乏工作历史和能力追踪。可以：
- 在 agent metadata 中增加历史任务记录
- 支持基于能力的 agent 选择

### 11.2 三层 Agent 架构（身份/沙箱/Session 分离）⭐⭐⭐

**Gastown 做法**: 身份永久，沙箱跨 session 复用，session 随时循环。

**启发**: MASS 当前 agent-run 绑定了 session + workspace。可以考虑：
- 支持 session 循环而不销毁 workspace 状态
- agent restart 时复用已有的工作目录

### 11.3 双层消息系统（持久 + 临时）⭐⭐⭐

**Gastown 做法**: mail（Dolt 持久，跨 session 存活）+ nudge（tmux 注入，零成本）。明确的 mail 预算限制每个角色的消息量。

**启发**: MASS 的 ARI event 机制是统一的。可以考虑：
- 区分"必须持久化的协调消息"和"临时通知"
- Agent 间通信不一定都走 daemon 转发

### 11.4 容量调度 + 熔断器 ⭐⭐

**Gastown 做法**: `min(capacity, batchSize, readyCount)` 公式 + 3 次失败熔断。

**启发**: MASS compose 模式缺少容量控制和熔断。大规模 agent 场景需要：
- 限制并发 agent 数，防止 API 限流
- 失败自动降级而非反复重试

### 11.5 Propulsion Principle（自驱执行）⭐⭐

**Gastown 做法**: Hook 上发现工作就执行，不等确认。

**启发**: MASS 依赖 orchestrator 主动 prompt。可以考虑：
- Agent 自主拉取待办工作的模式
- 减少 orchestrator 作为瓶颈

### 11.6 多级看门狗链 ⭐⭐

**Gastown 做法**: Daemon(机械) → Boot(AI 轻量) → Deacon(AI 长期) → Witness/Refinery。

**启发**: MASS 的恢复机制是 RecoverSessions 一把梭。可以考虑：
- 分层健康检查，不同频率不同深度
- 轻量级 liveness 检查 + 深度 health 检查分离

### 11.7 Agent 人格模板 + 指令覆盖 ⭐⭐

**Gastown 做法**: 内置模板 + town 级指令 + rig 级指令 + formula overlay，四层配置。

**启发**: MASS 通过 config.json 配置 agent，但缺少人格/行为模板。可以考虑：
- Agent 定义中加入角色模板（system prompt 模板）
- 支持运营者覆盖策略（不改代码就能调整 agent 行为）

### 11.8 Claim-Then-Execute 模式 ⭐

**Gastown 做法**: 处理消息前先删除（claim），防止心跳重复处理。

**启发**: MASS 的事件处理可以借鉴这个模式，防止恢复时重复投递。

---

## 十二、Gastown 的不足/MASS 的优势

| 维度 | MASS 优势 | Gastown 不足 |
|------|-----------|-------------|
| **协议标准化** | ACP JSON-RPC 标准协议 | gt/bd CLI 调用，紧耦合 |
| **进程管理** | self-fork + Unix socket，程序化控制 | tmux session + send-keys，脆弱 |
| **事件顺序** | 全局 seq + turn 内 streamSeq，严格有序 | NDJSON + 时间戳，无严格全局序 |
| **API 设计** | ARI JSON-RPC 2.0，结构化 RPC | CLI 输出解析，易碎 |
| **恢复精度** | shim socket reconnect + SubscribeFromSeq | tmux session 检测 + seance 上下文恢复 |
| **轻量级** | 嵌入式 bbolt, 无外部依赖 | 依赖 Dolt SQL Server, tmux, git |

---

## 十三、总结

Gastown 是一个面向**大规模 AI agent 群编排**的系统，MASS 是面向**单机 agent 运行时管理**的系统。两者解决的问题层次不同：

- **MASS** 解决的是"如何可靠地启动、监控、恢复一个 AI agent 进程"
- **Gastown** 解决的是"如何协调 30+ AI agent 在多个项目上并行工作"

最值得 MASS 借鉴的三点：
1. **身份/沙箱/Session 三层分离** — 让 agent restart 不丢失工作上下文
2. **双层消息系统** — 持久协调消息 + 轻量临时通知
3. **容量调度 + 熔断** — 大规模场景的安全阀

Gastown 的 tmux + CLI 解析方案相比 MASS 的 ACP JSON-RPC 是明显倒退。MASS 在协议标准化和进程管理精度上有清晰优势。但 Gastown 在**多 agent 编排层面**的设计（角色体系、消息协议、调度策略、人格模板）值得深入学习。

---

## 十四、深入分析：人格注入机制

### 14.1 核心机制

**一句话**：Claude Code 启动时触发 SessionStart hook，hook 调用 `gt prime --hook`，该命令把角色模板渲染后输出到 stdout，Claude Code 把 stdout 内容当作 session context 读入。

### 14.2 完整注入流程

```
Claude Code 启动
    │
    ├─ 触发 SessionStart hook
    │  ↓
    │  执行: gt prime --hook && gt mail check --inject
    │  ↓
    │  Claude Code 发送 stdin JSON:
    │  {"session_id":"uuid","source":"startup","hook_event_name":"SessionStart"}
    │
    ↓
gt prime --hook 执行链:
    │
    ├─ 1. 读 stdin → 拿到 session_id → 写入 .runtime/session_id
    ├─ 2. 设 GT_AGENT_READY=1 到 tmux env（告诉外界"我活了"）
    ├─ 3. 检测角色（从路径或 GT_ROLE 环境变量推断）
    ├─ 4. 渲染角色模板 → 输出到 stdout
    ├─ 5. 加载运营指令（directives）→ 追加到 stdout
    ├─ 6. 查有没有 hook 上的工作 → 如果有，输出"自治模式"指令
    ├─ 7. 检查邮箱 → 注入未读消息
    └─ 8. 输出启动指令
    
    ↓
Claude Code 把所有 stdout 内容作为 session context 吸收
    ↓
Agent 获得了完整的角色人格 + 当前工作上下文
```

### 14.3 角色检测：靠目录路径

不需要配置文件指定角色，直接从工作目录推断：

```go
// detectRole(cwd, townRoot) 路径推断规则：
"mayor/"                      → Mayor
"<rig>/witness/rig/"          → Witness
"<rig>/refinery/rig/"         → Refinery
"<rig>/polecats/<name>/"      → Polecat
"<rig>/crew/<name>/"          → Crew
"deacon/"                     → Deacon
"deacon/dogs/boot/"           → Boot
"deacon/dogs/<name>/"         → Dog
```

每个 agent 的 tmux session 启动在对应目录下，`gt prime` 一跑就知道自己是谁。也支持 `GT_ROLE` 环境变量强制指定。

### 14.4 人格模板：Go embed 的 Markdown 模板

每个角色有一个 `.md.tmpl` 文件，编译进二进制：

```go
//go:embed roles/*.md.tmpl messages/*.md.tmpl
var templateFS embed.FS
```

| 角色 | 模板文件 | 大小 | 内容概要 |
|------|---------|------|----------|
| Polecat | `roles/polecat.md.tmpl` | 19KB | 身份声明、单任务焦点、目录纪律、Propulsion Principle、完成协议 |
| Crew | `roles/crew.md.tmpl` | 20KB | 人类工作空间规范、工具使用、协作规则 |
| Deacon | `roles/deacon.md.tmpl` | 17KB | 巡逻协议、收件箱处理、健康检查流程 |
| Refinery | `roles/refinery.md.tmpl` | 15KB | 合并队列处理、冲突解决、质量门控 |
| Mayor | `roles/mayor.md.tmpl` | 14KB | 全局协调、convoy 管理、升级处理 |
| Witness | `roles/witness.md.tmpl` | 12KB | 健康检查、僵尸检测、生命周期管理 |
| Dog | `roles/dog.md.tmpl` | 7KB | 批处理任务、基础设施维护 |
| Boot | `roles/boot.md.tmpl` | 4KB | Deacon 看门狗决策逻辑 |

模板用 Go template 语法替换变量：

```markdown
> **Recovery**: Run `{{ cmd }} prime` after compaction, clear, or new session

**YOU ARE IN: `{{ .RigName }}/polecats/{{ .Polecat }}/`** — This is YOUR worktree. Stay here.
```

模板数据结构：

```go
type RoleData struct {
    Role          string   // "polecat"
    RigName       string   // "greenplace"
    TownRoot      string   // "/Users/steve/ai"
    Polecat       string   // "furiosa"
    DefaultBranch string   // "main"
    IssuePrefix   string   // "gt-"
    Polecats      []string // 所有 polecat 名列表
    MayorSession  string   // mayor 的 tmux session 名
    DeaconSession string   // deacon 的 tmux session 名
    // ...
}
```

### 14.5 Polecat 模板核心内容（约 20KB）

1. **身份声明** — 你是哪个 rig 的哪个 polecat
2. **IDLE Polecat Heresy** — 完成后必须跑 `gt done`（核心规则，防止 polecat 空转）
3. **单任务焦点** — 只做 hook 上的任务，不搞别的
4. **目录纪律** — 只能在自己的 worktree 里操作，不准 cd 到别处
5. **Propulsion Principle** — 发现工作立即执行，不等确认
6. **启动协议** — 先 announce role，再开始干活
7. **关键命令速查** — gt prime, gt done, gt handoff, bd show 等
8. **完成协议** — 质量门控（测试通过、lint 清洁），`gt done` 要求
9. **自管理生命周期** — 什么时候 handoff（上下文满了），什么时候 escalate（搞不定）

### 14.6 四层覆盖机制

```
1. 内置模板（编译进二进制）        ← 最底层，定义角色基本人格
     ↓ 被覆盖
2. CLAUDE.md（写入 worktree）      ← 仅 Polecat，持久化在文件系统
     ↓ 被覆盖
3. Town 级指令 (directives)         ← 运营者全局策略
   ~/gt/directives/<role>.md
     ↓ 被覆盖
4. Rig 级指令                       ← 项目级策略（最高优先级）
   ~/gt/<rig>/directives/<role>.md
```

Town 级和 Rig 级指令都存在时，两者**连接**（town 在前，rig 在后），rig 级有最后发言权。

### 14.7 CLAUDE.md 专门给 Polecat 生成

Polecat spawn 时额外在 worktree 里生成 `CLAUDE.md`（或 `CLAUDE.local.md`）：

```go
func CreatePolecatCLAUDEmd(worktreePath, rigName, polecatName string) {
    content := polecatCLAUDEmd  // 编译进去的模板
    content = strings.ReplaceAll(content, "{{rig}}", rigName)
    content = strings.ReplaceAll(content, "{{name}}", polecatName)
    // 如果已有 CLAUDE.md（仓库自带的）→ 写到 CLAUDE.local.md（不污染 tracked 文件）
    // 如果没有 → 写到 CLAUDE.md
    // 用 PolecatLifecycleMarker = "IDLE POLECAT HERESY" 防止重复写入
}
```

Claude Code 启动时自动读 CLAUDE.md + SessionStart hook 的 stdout 注入 = 完整角色认知。

### 14.8 自治模式触发

`gt prime` 发现 agent 的 hook 上有待办工作时，输出自治指令：

```markdown
## 🚨 AUTONOMOUS WORK MODE 🚨
Work is on your hook. After announcing your role, begin IMMEDIATELY.

Hooked bead: gt-xyz
Title: Fix authentication bug
Description: ...

Formula steps:
1. Read the issue
2. Create a branch
3. Implement fix
4. Run tests
5. gt done
```

### 14.9 Compact/Resume 快速路径

Claude Code 做 context compaction 时也触发 `gt prime`，但走快速路径：

```go
if source == "compact" || source == "resume" {
    // 只输出一行恢复提示，不重做完整初始化
    fmt.Printf("> **Recovery**: Context %s complete. You are **%s** (%s).\n",
               source, actor, role)
    return  // 秒回
}
```

### 14.10 Hook 配置

```json
// settings-autonomous.json
"SessionStart": [{
    "matcher": "",
    "hooks": [{
        "type": "command",
        "command": "{{GT_BIN}} prime --hook && {{GT_BIN}} mail check --inject"
    }]
}]
```

### 14.11 与 MASS 对比

| | MASS | Gastown |
|--|------|---------|
| 人格注入时机 | config.json 启动参数，ACP bootstrap | SessionStart hook stdout 注入 |
| 人格内容 | ACP agent 自身决定（MASS 不管） | 20KB 角色模板 + 多层指令覆盖 |
| 角色检测 | 无（agent 自己知道自己是谁） | 从目录路径自动推断 |
| Context 恢复 | shim reconnect + SubscribeFromSeq | handoff marker + seance + checkpoint |
| 运营覆盖 | 无内置机制 | 4 层覆盖：模板→CLAUDE.md→town 指令→rig 指令 |

**启发**：MASS 把人格交给 agent 实现者管，Gastown 由平台统一管理所有 agent 人格——这让它能"换一行指令就改变所有 polecat 的行为"。MASS 可以考虑在 agent 定义中支持 system prompt 模板 + 运营覆盖层。

---

## 十五、深入分析：消息转发机制

### 15.1 核心机制

Gastown 的消息转发**不是**直接进程间通信，而是**数据库持久化 + 异步通知**：

```
发送方                          接收方
  │                               │
  ├─ bd create (写 Dolt DB) ────→ 持久化到 beads ────→ 接收方 poll 读取
  │                                                     (gt mail check)
  └─ async 通知 ──→ tmux nudge (如果 idle) ──────→ 接收方看到提醒
                  └→ nudge queue (如果 busy) ───→ 下个 turn 边界读取
```

两条路径：
- **硬路径**（mail）：消息写入 Dolt SQL → 接收方通过 `gt mail check` 主动拉取
- **软路径**（nudge）：tmux send-keys 注入 `<system-reminder>` 到 agent session

### 15.2 发送流程 (`gt mail send`)

```
runMailSend()
  ├─ 生成消息 ID: msg-abc123, 线程 ID: thread-xyz789
  ├─ 地址解析: Resolver.Resolve(to) 处理直接/组/队列/频道地址
  ├─ 验证接收者存在（查 beads agent 记录 或 workspace 目录）
  ├─ Fan-out:
  │   ├─ 直接地址 → 每个接收者一份拷贝
  │   ├─ 队列 → 单份，worker claim
  │   └─ 频道 → 广播
  └─ 对每个接收者:
     router.sendToSingle()
       ├─ 地址标准化: "gastown/crew/max" → "gastown/max"
       ├─ 构建 labels: gt:message, from:X, msg-type:X, delivery:pending
       └─ 执行:
          bd create --assignee gastown/max \
            --labels "gt:message,from:gastown/Toast,delivery:pending" \
            -d "消息内容" -- "主题"
          → 返回 bead ID: hq-wisp-000123
```

消息**先持久化到 Dolt**，然后才异步通知。

### 15.3 异步通知（非阻塞）

```go
// router.notifyRecipient() — 异步 goroutine，不阻塞发送方
1. 检查接收方 DND 状态，muted 就跳过
2. 地址 → tmux session 名映射
3. WaitForIdle(3秒) — 等 agent 空闲（prompt 可见）
4. 如果空闲:
   → tmux send-keys 直接注入:
     "[from gastown/Toast] 📬 You have new mail. Subject: Hello.
      Run 'gt mail inbox' to read."
5. 如果忙碌:
   → 写入 nudge queue:
     .runtime/nudge_queue/<session>/1713618000-abc.json
6. 起 60 秒 watcher，轮询等空闲后 drain
7. 30 秒后发送 reply-reminder（提醒用 gt mail reply 而不是直接说话）
```

### 15.4 接收方读取（UserPromptSubmit hook）

每次 agent 提交 prompt 时，hook 自动触发：

```bash
# hooks-base.json 配的 UserPromptSubmit hook
gt mail check --inject
```

`gt mail check --inject` 做的事：

```
1. 查 mailbox: bd list --assignee=identity --label=gt:message
2. 按优先级分层格式化:
   - Urgent → "立刻停下来处理"
   - High → "当前任务边界处理"
   - Normal/Low → "空闲前处理"
3. 输出 <system-reminder>...</system-reminder> 到 stdout
4. Drain nudge queue（把攒的临时通知也一起输出）
5. 两阶段确认:
   bd label add <id> delivery-acked-by:gastown/max
   bd label add <id> delivery-acked-at:2026-04-21T...
   bd label add <id> delivery:acked
```

最多 8 个并发 bd 子进程处理 ack（bounded parallelism）。

### 15.5 Nudge 队列设计

Nudge 不是简单的 tmux send-keys，有文件系统队列 + 原子 claim 机制：

**写入**：
```go
filename := fmt.Sprintf("%d-%s.json", timestamp.UnixNano(), randomSuffix())
os.WriteFile(queueDir/filename, jsonData, 0644)
// 队列上限: 50 条
```

**读取（Drain）**：
```go
// 原子 claim: rename 为 .claimed 后缀
os.Rename("xxx.json", "xxx.json.claimed.abc123")
// 只有一个 drainer 能成功 rename（文件系统原子操作）
// 读取 → 跳过已过期的 → 删除 .claimed 文件
```

| 属性 | 值 |
|------|-----|
| 队列深度上限 | 50 条 |
| 普通消息 TTL | 30 分钟 |
| 紧急消息 TTL | 2 小时 |
| 防重复投递 | rename-based 原子 claim |
| 孤儿恢复 | .claimed 超 5 分钟自动恢复为 .json |
| 顺序保证 | 按文件名排序（timestamp-based FIFO） |

### 15.6 三种 Nudge 投递模式

| 模式 | 行为 | 适用场景 |
|------|------|----------|
| **wait-idle**（默认）| 等 15 秒看 agent 是否空闲，空闲直接注入，忙碌排队 | 一般通知 |
| **queue** | 直接写文件队列，不等待 | ACP session、非 Claude agent（Gemini/Codex） |
| **immediate** | 强制 tmux send-keys，不管忙不忙 | 紧急情况 |

非 Claude agent（Gemini、Codex）没有 prompt 检测能力，降级为 queue 模式 + 后台 poller（每 10 秒轮询一次）。

### 15.7 地址解析系统

```
解析顺序:
1. 显式前缀: "group:", "queue:", "channel:", "list:", "announce:"
2. @前缀: @town, @rig/X, @crew/X, @witnesses
3. 含 "/": Agent 地址，查 beads/workspace 验证
4. 裸名: 按 group → queue → channel 顺序查找
5. 歧义: 匹配多种类型时要求显式前缀
```

**组展开**（`@witnesses` → 所有 witness agent）：
- 查 beads 中带 `gt:agent` 标签的记录
- 按 `role_type` 和 `rig` 过滤
- 递归展开，有**循环检测**
- 去重后 fan-out

### 15.8 两阶段投递确认

```
Phase 1 (发送时):  label = "delivery:pending"
Phase 2 (读取后): label += "delivery:acked"
                         + "delivery-acked-by:gastown/max"
                         + "delivery-acked-at:2026-04-21T10:30:00Z"
```

幂等设计：重试 ack 时复用已有时间戳，不会产生重复记录。

### 15.9 消息格式

```go
type Message struct {
    ID          string      // msg-{8字节hex}
    From        string      // "gastown/Toast"
    To          string      // 直接接收者
    Subject     string
    Body        string      // key-value 对 + markdown
    Priority    Priority    // low/normal/high/urgent
    Type        MessageType // task/escalation/scavenge/notification/reply
    Delivery    Delivery    // queue(邮箱轮询) / interrupt(nudge 推送)
    ThreadID    string      // 会话线程
    Wisp        bool        // true = 临时，不同步到 git 历史
    Queue       string      // 队列路由（与 To 互斥）
    Channel     string      // 广播（与 To 互斥）
    ClaimedBy   string      // 队列消息被谁 claim 了
}
```

自动判断 wisp（临时消息）：subject 匹配 `POLECAT_DONE`、`MERGED`、`LIFECYCLE:` 等模式时自动标记为 wisp。

### 15.10 完整消息旅程示例

```
1. SEND: gt mail send gastown/crew/max --subject "Fix bug" -m "Details..."
   └→ bd create --assignee gastown/max --labels delivery:pending -- "Fix bug"
   └→ 返回 hq-wisp-000123

2. STORE: 消息持久化到 Dolt SQL

3. NOTIFY (async):
   ├→ agent idle? → tmux send-keys "📬 You have new mail..."
   └→ agent busy? → 写入 .runtime/nudge_queue/gt-crew-max/...json

4. RECEIVE: agent 提交下一个 prompt
   └→ UserPromptSubmit hook 触发
   └→ gt mail check --inject
   └→ 查 mailbox → 格式化 → 输出 <system-reminder>
   └→ drain nudge queue → 输出额外通知
   └→ ack delivery

5. READ: agent 执行 gt mail read hq-wisp-000123
   └→ 显示完整消息内容

6. ACK: delivery:pending → delivery:acked
```

### 15.11 与 MASS 对比

| | MASS | Gastown |
|--|------|---------|
| 传输层 | JSON-RPC over Unix socket（程序化、同步） | Dolt DB 持久化 + tmux send-keys（CLI + 文件） |
| 投递保证 | RPC 同步调用，立即送达 | 两阶段：pending → acked，异步最终一致 |
| 空闲检测 | 不需要（RPC 直接送达 shim） | tmux prompt 检测 + nudge queue 降级 |
| Agent 间通信 | 必须经 agentd 转发 | agent 直接 mail/nudge，无中间人 |
| 消息持久化 | event log (NDJSON) | Dolt SQL（带 git 版本历史） |
| 消息类型分级 | 统一 event 类型 | mail（持久）vs nudge（临时），按优先级分层展示 |

**启发**：MASS 的 JSON-RPC 方案在精度和可靠性上更优（同步、类型安全、无 tmux 依赖）。但 Gastown 的"持久 + 临时"双层消息、优先级分层展示、mail 预算控制等设计理念值得借鉴。

---

## 十六、多运行时适配：Hook 依赖与降级机制

Gastown 并不完全依赖 Claude Code 的 Hook 机制。它通过 preset 注册表 + 降级矩阵，支持 11+ 种 AI agent 运行时。

### 16.1 运行时 Preset 注册表

源码位置：`internal/config/agents.go`

每个运行时通过 `AgentPresetInfo` 描述其能力：

```go
type AgentPresetInfo struct {
    Name                   AgentPreset
    SupportsHooks          bool   // 是否支持 hook 机制
    PromptMode             string // "arg"（支持命令行传 prompt）或 "none"
    HooksProvider          string // hook 提供者："claude", "gemini", "opencode", "copilot" 等
    HooksDir               string // hook 配置目录：".claude", ".gemini" 等
    ReadyPromptPrefix      string // 就绪提示符前缀
    HasTurnBoundaryDrain   bool   // 是否支持 turn 边界触发 drain
}
```

### 16.2 各运行时支持情况

| 运行时 | SupportsHooks | PromptMode | HasTurnBoundaryDrain | 备注 |
|--------|:---:|:---:|:---:|------|
| Claude Code | ✅ | arg | ✅ | 完整支持，标准流程 |
| Gemini CLI | ✅ | arg | ❌ | 有自己的 hooks 目录 `.gemini` |
| OpenCode | ✅ | arg | ❌ | hooks provider = "opencode" |
| Copilot CLI | ✅ | arg | ❌ | hooks provider = "copilot" |
| Codex | ❌ | none | ❌ | 无 hook，无 prompt 参数 |
| Auggie | ❌ | none | ❌ | 无 hook |
| Amp | ❌ | none | ❌ | 无 hook |

### 16.3 四格降级矩阵

源码位置：`internal/runtime/runtime.go` → `StartupFallbackInfo`

根据 `SupportsHooks` × `PromptMode` 两个维度，形成四种启动策略：

```
┌──────────────────────┬──────────────────────────────┬──────────────────────────────┐
│                      │ 有 Prompt (arg)              │ 无 Prompt (none)             │
├──────────────────────┼──────────────────────────────┼──────────────────────────────┤
│ 有 Hook              │ 标准流程                     │ beacon 通过 nudge 发送       │
│ (SupportsHooks=true) │ SessionStart hook            │                              │
│                      │ → gt prime --hook            │                              │
│                      │ → 自动注入人格               │                              │
├──────────────────────┼──────────────────────────────┼──────────────────────────────┤
│ 无 Hook              │ beacon 含 "Run gt prime"     │ 全部通过 nudge 投递          │
│ (SupportsHooks=false)│ + 延迟 nudge 发工作指令      │ (tmux send-keys)             │
│                      │ 如：Amp (若支持 prompt)      │ 如：Codex, Auggie            │
└──────────────────────┴──────────────────────────────┴──────────────────────────────┘
```

### 16.4 降级机制详解

#### Beacon 机制（人格注入降级）

源码位置：`internal/session/startup.go` → `FormatStartupBeacon()`

```go
type BeaconConfig struct {
    Recipient               string
    Sender                  string
    IncludePrimeInstruction bool   // 无 hook 时 = true
    ExcludeWorkInstructions bool   // 工作指令通过单独 nudge 发送时 = true
}
```

标准流程中，`SessionStart hook` 自动执行 `gt prime --hook`，人格模板直接输出到 stdout 被 agent 读取。

无 hook 时，系统在 tmux 里发送一个 beacon 消息，其中包含：
```
Run `gt prime` to initialize your context.
```
agent 看到这条指令后会主动执行 `gt prime`，效果相同——从"自动触发"变为"提示 agent 主动执行"。

#### Nudge Poller（消息投递降级）

源码位置：`internal/nudge/poller.go`

标准流程中，`UserPromptSubmit hook` 在每次 turn 边界触发 `gt drain`，检查 nudge queue 并投递消息。

无 hook 或无 `HasTurnBoundaryDrain` 的运行时，启动一个后台 **nudge poller**，每 10 秒轮询 nudge queue，发现新消息通过 tmux send-keys 投递。

### 16.5 降级后的能力对比

| 能力 | 有 Hook（Claude Code） | 无 Hook（Codex 等） |
|------|:---:|:---:|
| 人格自动注入 | 自动（hook 触发） | 需 agent 主动执行 `gt prime` |
| 消息实时性 | 即时（hook 事件驱动） | 最多延迟 10 秒（轮询） |
| Turn 边界感知 | 精确（UserPromptSubmit） | 无（依赖轮询时机） |
| 工作自动启动 | hook 拿到 persona 后立即执行 | 需等 beacon + 延迟 nudge |
| Mail 投递 | drain 精确触发 | poller 批量检查 |

### 16.6 核心设计理念

**tmux 是最底层通用传输层。** Gastown 没有把 Hook 当作唯一通道——tmux send-keys 是所有终端 agent 都支持的能力，Hook 只是在此之上的加速器。

这种分层设计：
- **Hook 层**：事件驱动，精确、即时，但依赖运行时支持
- **tmux 层**：轮询 + send-keys，通用、降级，所有终端 agent 都可用
- **Dolt 层**：持久化存储，与传输层无关，任何 agent 都可通过 `gt mail` 命令读写

### 16.7 与 MASS 对比

| | MASS | Gastown |
|--|------|---------|
| 运行时耦合 | ACP 协议抽象层，运行时无关 | Hook 优先 + tmux 降级，多运行时适配 |
| 人格注入 | agent 配置文件（静态） | hook → gt prime（动态模板渲染） |
| 传输通用性 | Unix socket（程序化，需 ACP 支持） | tmux（终端级，任何 CLI agent 可用） |
| 降级策略 | 无需降级（ACP 是统一接口） | 四格矩阵：hook × prompt 组合降级 |

**启发**：MASS 通过 ACP 协议实现了运行时无关，但这要求 agent 实现 ACP。Gastown 的 tmux 通用传输层思路值得注意——对于不支持 ACP 的 agent，tmux send-keys 可以作为最低公约数的通信手段。降级矩阵的设计模式（按能力维度组合）也可借鉴到 MASS 的 agent 适配策略中。
