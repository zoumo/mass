# 代码体检改进计划

> 状态：草案
> 日期：2026-04-07
> 基准版本：Phase 3 完成（commit fb82343）

---

## 1. 体检概述

本文档是对 open-agent-runtime 全量代码的深度体检报告，按优先级整理为可执行的改进计划。

**整体评价：** 架构设计优秀，代码风格统一，测试覆盖较好。发现 1 个 Critical Bug、若干中优先级改进项、以及若干低优先级优化项。

---

## 2. 问题清单（按优先级）

### P0 — 必须立即修复

#### BUG-001：`agentd` 优雅关闭超时单位错误

**文件：** `cmd/agentd/main.go:110`

**现状：**
```go
ctx, cancel := context.WithTimeout(context.Background(), 30)
```

`context.WithTimeout` 的第二个参数类型为 `time.Duration`，单位是纳秒。`30` 表示 30 纳秒，实际上 shutdown context 在调用后几乎立刻超时，导致所有正在运行的 session 都会被强制终止而没有经历完整的清理流程。

**预期行为：** 30 秒内完成优雅关闭。

**修复：**
```go
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
```

---

### P1 — 高优先级

#### IMP-001：`EventLog` 缺少损坏恢复机制

**文件：** `pkg/events/log.go`

**问题：** JSONL 格式的事件日志在写入过程中如果进程异常退出，可能产生不完整的末尾行。当前代码在重放日志时遇到解析错误会直接返回错误，没有 skip-and-continue 策略，导致一次不完整写入让整条事件链不可读。

**改进方向：**
- 读取时跳过无法解析的行，记录 warning
- 可选：写入时在行末追加 checksum 以便区分截断行和真正损坏的行

---

#### IMP-002：`ARI Server` 测试覆盖不足

**文件：** `pkg/ari/server.go`（500+ 行），对应测试文件覆盖率低

**未被测试的路径：**
1. `workspace/cleanup` 的 RefCount 保护逻辑（RefCount > 0 时拒绝清理）
2. `session/attach` 和 `session/detach` 完整 round-trip
3. RPC 参数校验的错误路径（缺少必填字段、类型错误等）
4. 并发场景下 `session/prompt` 的幂等性

**改进方向：** 为上述路径补充 table-driven 单元测试，attach/detach 可在集成测试中验证。

---

#### IMP-003：Terminal 操作尚未实现（Phase 1.1 遗留）

**文件：** `pkg/runtime/terminal.go`

**关联需求：** R020, R026, R027, R028, R029

**现状：** `TerminalManager` 的数据结构已定义，但 shell 命令执行、stdout/stderr 流式读取、终端销毁等核心逻辑未完成，相关集成测试缺失。

**改进方向：**
1. 完成 `TerminalManager.Create()` — fork pty/子进程，绑定工作目录
2. 完成 `TerminalManager.Output()` — 流式读取 stdout+stderr
3. 完成 `TerminalManager.Kill()` — SIGTERM → SIGKILL 升级，超时 5s
4. 补充集成测试，覆盖正常执行、超时、kill、并发多终端场景

---

### P2 — 中优先级

#### IMP-004：错误消息前缀风格不统一

**问题：** 代码中有三种错误前缀风格混用：
```go
fmt.Errorf("spec: ...")          // 带包名
fmt.Errorf("runtime: ...")       // 带组件名
fmt.Errorf("failed to ...")      // 无前缀
```

**目标风格：** `component: operation: detail`，与标准库和 Go 社区惯例对齐。

**影响文件：**
- `pkg/spec/config.go` — 部分错误缺少前缀
- `pkg/ari/server.go` — 部分 handler 错误直接透传无 wrap
- `cmd/agent-shim/main.go` — 启动错误前缀不一致

**改进方向：** 统一为 `fmt.Errorf("component: action: %w", err)` 格式，搜索全库非 `%w` 的错误串联进行修正。

---

#### IMP-005：`session/status` API 缺少运行时统计字段

**文件：** `pkg/ari/server.go`（`sessionStatus` handler）

**现状：** `session/status` 返回 session 元数据和状态枚举，但不包含：
- 进程 PID
- 已用内存（RSS）
- 运行时长
- 最后一次 prompt 的时间戳

**改进方向：** 从 `ProcessManager` 补充 PID 信息；内存/时长等监控指标可按需从 `/proc` 读取（Linux）或通过 `os.Process` 获取。

---

#### IMP-006：`agentd` 启动时的 socket 清理策略存在竞争窗口

**文件：** `cmd/agentd/main.go:86-91`

**现状：**
```go
if _, err := os.Stat(cfg.Socket); err == nil {
    os.Remove(cfg.Socket)
}
// ...
srv.Serve()  // 在这里 bind socket
```

**问题：** `Stat → Remove → Serve` 不是原子操作。如果两个 agentd 实例同时启动（容器重启场景），两者都可能通过 `Stat` 检查，然后竞争 `Remove`，最终双方都 bind 失败或只有一方成功但另一方的 Remove 删掉了有效 socket。

**改进方向：** 使用文件锁（`flock`）在 socket bind 前持有锁，或者直接调用 `net.Listen` 然后对 `EADDRINUSE` 做二次尝试（Remove + Retry），避免手动的 stat-then-remove 竞争。

---

#### IMP-007：`WorkspaceManager` 引用计数没有持久化

**文件：** `pkg/workspace/manager.go`

**现状：** RefCount 存储在内存 `map[string]int` 中，agentd 重启后归零。如果 agentd 在 session 存活期间崩溃并重启，RefCount = 0，下一个 `workspace/cleanup` 调用会直接删除仍被 session 使用的工作区。

**改进方向：**
- 将 RefCount 持久化到 SQLite（`meta.Store`）
- 或者在 agentd 启动时从存活的 session 重建 RefCount

---

### P3 — 低优先级 / 规划项

#### IMP-008：ARI 协议缺乏文档

**现状：** ARI 协议只存在于代码实现，没有独立的协议规范文档。外部开发者需要阅读 `pkg/ari/server.go` 才能理解接口语义。

**改进方向：** 在 `docs/design/` 下新增 `ari-protocol.md`，描述每个 RPC 方法的：
- 输入参数 schema（含必填/可选）
- 返回值 schema
- 错误码定义
- 状态前置条件（如 `session/prompt` 要求 session 处于 running 状态）

---

#### IMP-009：Orchestrator / Room 层（Phase 4）规划

**文件：** `pkg/meta/schema.sql`（Room 表已存在但未使用）

**现状：** Room 相关的数据库 schema 已经设计完成，但上层的 `RoomManager`、`Orchestrator` 和多 agent 协调逻辑尚未开始。

**改进方向：** 按照 `.gsd/milestones/M001-tvc4z0` 的路线图启动 Phase 4，优先实现：
1. `RoomManager` — Room CRUD，成员管理
2. Room 内的消息路由（广播 vs 点对点）
3. Orchestrator 入口（类比 agentd 但管理 Room 而非单个 Session）

---

#### IMP-010：依赖更新计划

**文件：** `go.mod`

当前依赖版本：

| 包 | 当前版本 | 备注 |
|----|---------|------|
| `github.com/coder/acp-go-sdk` | v0.6.3 | ACP 协议核心，关注 breaking change |
| `github.com/mattn/go-sqlite3` | v1.14.38 | CGO，稳定 |
| `github.com/sourcegraph/jsonrpc2` | v0.2.1 | 稳定，但可考虑维护更活跃的分支 |
| `github.com/spf13/cobra` | v1.10.2 | 可升级到 v1.9+ |

**改进方向：** 每个 milestone 完成后执行 `go get -u ./...` + 回归测试，保持依赖新鲜度。重点关注 `acp-go-sdk` 的 changelog，ACP v0.7+ 可能影响 `session/new` 握手流程。

---

## 3. 改进优先级总览

| 编号 | 描述 | 优先级 | 涉及文件 | 工作量 |
|------|------|--------|---------|--------|
| BUG-001 | 关闭超时单位错误 | P0 | `cmd/agentd/main.go` | XS（1行）|
| IMP-001 | EventLog 损坏恢复 | P1 | `pkg/events/log.go` | S |
| IMP-002 | ARI Server 测试补全 | P1 | `pkg/ari/` | M |
| IMP-003 | Terminal 操作实现 | P1 | `pkg/runtime/terminal.go` | L |
| IMP-004 | 错误消息前缀统一 | P2 | 多文件 | S |
| IMP-005 | session/status 运行时统计 | P2 | `pkg/ari/server.go` | S |
| IMP-006 | socket 清理竞争窗口 | P2 | `cmd/agentd/main.go` | S |
| IMP-007 | RefCount 持久化 | P2 | `pkg/workspace/manager.go` | M |
| IMP-008 | ARI 协议文档 | P3 | `docs/design/` | M |
| IMP-009 | Orchestrator Phase 4 | P3 | 新模块 | XL |
| IMP-010 | 依赖更新 | P3 | `go.mod` | XS |

---

## 4. 执行建议

### 近期（本 sprint）
1. 立即修复 **BUG-001**，合并入主干
2. 修复 **IMP-006** socket 竞争窗口（与 BUG-001 同批次，成本低）
3. 修复 **IMP-004** 错误消息前缀（可作为代码整洁 PR 顺手完成）

### 中期（下一 milestone 前）
4. 实现 **IMP-003** Terminal 操作（Phase 1.1 正式交付）
5. 补全 **IMP-002** ARI Server 测试
6. 实现 **IMP-007** RefCount 持久化（提升生产可靠性）
7. 实现 **IMP-001** EventLog 恢复机制

### 长期（Phase 4+）
8. 输出 **IMP-008** ARI 协议文档
9. 启动 **IMP-009** Orchestrator 规划与实现
10. 例行执行 **IMP-010** 依赖更新
