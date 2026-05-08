# MASS 代码深度体检汇总报告

Date: 2026-04-30

## 总览

| 维度 | 报告文件 | Critical | High | Medium | Low |
|------|---------|----------|------|--------|-----|
| 代码质量 | [code-quality.md](code-quality.md) | 1 | 4 | 7 | 6 |
| 边界条件 | [boundary-conditions.md](boundary-conditions.md) | 2 | 4 | 5 | 5 |
| 测试覆盖 | [test-coverage.md](test-coverage.md) | 1 (crash) | — | — | — |
| 代码复用 | [code-reuse.md](code-reuse.md) | 0 | 1 | 5 | 1 |
| 文档完备性 | [documentation.md](documentation.md) | 0 | 2 | 4 | — |
| 代码-文档一致性 | [consistency.md](consistency.md) | 0 | 2 | 6 | 5 |

**总计**: Critical 4 / High 13 / Medium 27 / Low 17

---

## Top Critical Issues (必须立即修复)

### 1. WatchServer.Publish 无超时阻塞 — 慢消费者可卡死整条事件链
- **位置**: `pkg/watch/server.go`
- **影响**: 单个慢 watcher 阻塞所有事件推送
- **修复方向**: 加 send timeout 或 eviction 机制

### 2. WatchServer watcher goroutine 泄漏 — 无 Stop() 关闭 mailbox
- **位置**: `pkg/watch/server.go`
- **影响**: daemon 关闭后 goroutine 永远挂起
- **修复方向**: 增加 `Stop()` 方法关闭所有 watcher channel

### 3. ACP runtime `done()` 每次调用创建新的永不关闭 channel
- **位置**: `pkg/agentrun/runtime/acp/runtime.go:419`
- **影响**: goroutine 泄漏（当 conn==nil 时）
- **修复方向**: 返回 package-level 的 singleton channel

### 4. `pkg/agentd` 测试崩溃 — send on closed channel
- **位置**: `pkg/agentd` test suite
- **影响**: CI 不可靠，测试结果不可信
- **修复方向**: mock server teardown 需同步等待 jsonrpc2 goroutine 退出

---

## Top High Priority Issues (一周内修复)

| # | 问题 | 位置 | 影响 |
|---|------|------|------|
| 1 | ACP notifications silently dropped (channel full) | `pkg/agentrun/runtime/acp/client.go:120` | 事件丢失 |
| 2 | ARI server background goroutine 用 `context.Background()` | `pkg/ari/server/server.go` | 无法优雅关闭 |
| 3 | `StopDrain()` double-close race | `pkg/agentd/process.go` | panic 风险 |
| 4 | `rollbackAgentToIdle` 用 `UpdateStatus` 清空所有 metadata | `pkg/ari/server/server.go` | SocketPath/PID 丢失 |
| 5 | Workspace prep goroutine 用 `context.Background()` 不可取消 | `pkg/workspace/` | 关闭时资源泄漏 |
| 6 | `drainEvents`/`StopDrain` 交接有 race 丢事件 | `pkg/agentd/` | 事件丢失 |
| 7 | ARCHITECTURE.md 用旧术语 `session/subscribe` | `docs/ARCHITECTURE.md` | 文档误导 |
| 8 | Orchestration guide 引用不存在的 `lifecycle-hooks.md` | `docs/develop/` | 死链 |

---

## 测试覆盖现状

- **整体覆盖率**: 49.9%（641 个测试函数）
- **优秀** (>80%): workspace(93.6%), agentrun/api(87.8%), runtime-spec/api(87.9%), ext/pipeline(87.7%)
- **薄弱** (<35%): tui/chat(28.4%), watch(32.5%), compose(15.8%), mass/commands/run(13.0%)
- **缺失**: 0 fuzz tests, 0 benchmarks, 无 `-race` flag in CI
- **75% 函数零覆盖** (816 个函数中 611 个)

---

## 代码复用 Top 改进项

1. **CLI get/list/delete 模式重复** — 3 种资源几乎相同逻辑，建议抽取泛型 helper
2. **`[]T → []any` 转换重复** — 加一个 `ToAnySlice[T]` 通用函数
3. **Polling/wait 逻辑重复 3 处** — 统一到 cliutil
4. **Dead code**: `AgentRunManager.GetByWorkspaceName`, `ari/client.RawClient`, `watch.WatchServer[T]`(unused), `OutputJSON`(deprecated)
5. **stdlib reimplementation**: `splitN`, `isAbs` 可直接用 `strings.SplitN`, `filepath.IsAbs`

---

## 文档 Top 改进项

1. **CHANGELOG 落后 210+ commits** — 最后更新 2026-04-15
2. **3 个死链** in `docs/design/README.md`
3. **18+ packages 无 doc.go**
4. **ARCHITECTURE.md 缺少** `pkg/watch/` 和 `cmd/massctl/commands/ext/`
5. **新命令未文档化**: `agentrun task wait`, `agentrun debug`, `ext pipeline`

---

## 建议优先级排序

### P0 — 本周 (Critical + test crash)
1. 修复 WatchServer 阻塞 + goroutine 泄漏
2. 修复 `done()` channel 泄漏
3. 修复 agentd 测试崩溃

### P1 — 下周 (High)
4. StopDrain race condition
5. rollbackAgentToIdle 用错 API
6. ACP notification drop
7. context.Background() → shutdown-aware context

### P2 — 两周内 (Coverage + Docs)
8. 加 `-race` flag 到 CI
9. watch/tui 包测试覆盖提升到 60%+
10. 更新 CHANGELOG
11. 修复文档死链
12. ARCHITECTURE.md 补充新模块

### P3 — 一个月内 (Reuse + Polish)
13. CLI 命令泛型化
14. 清理 dead code
15. 添加 doc.go
16. Fuzz tests for JSON unmarshalling
