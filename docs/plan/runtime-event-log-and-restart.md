# Runtime 三项改造计划

## Context

三个独立问题需要修复：
1. Event log 当前写入单一 `events.jsonl`，未来多 session 支持需要按 ACP session ID 分区
2. 用户触发操作（cancel / set_model / stop）没有写入 event log，调试困难
3. Restart agentrun 时状态序列为 `creating → stopped → creating → idle`，应改为 `restarting → stopped → creating → idle`

---

## Part 1: Event log 按 session ID 分区

### 目标路径结构

```
{stateDir}/bundles/events/{base64url(sessionID)}.jsonl
```

SessionID 来自协议层，不能直接当文件名（含 `/`、`..` 等风险）。使用 `base64.RawURLEncoding`（无 padding，字符集 `A-Za-z0-9-_`）编码文件名，可逆，原始 session ID 同时保存在每条事件的 `SessionID` 字段。

### Seq 规则

每个 session 的 seq **从 0 开始**（新文件）。`OpenEventLog` 改用 `lastValidOffset(path)` 推导 `nextSeq`，同时处理 damaged tail 截断（见下文）。

同一 session ID 对应同一文件（base64url 相同），恢复场景下 `OpenEventLog` 追加续接，`nextSeq = lastEvent.Seq + 1`。

### Damaged Tail 处理

当前 `OpenEventLog` 用 `countLines()` 推导 nextSeq，有两个问题：
1. `line count ≠ seq`（fail-closed drop 场景不等价）
2. 若文件有 damaged tail，直接追加会把坏尾巴变成 mid-file corruption，之后 `replay` 会返回错误

修复：`OpenEventLog` 调用 `lastValidOffset(path)` 获取最后有效事件的 byte offset，并在追加前 **截断** 到该 offset。

### 文件改动

#### `pkg/runtime-spec/state.go`
```go
// BundlesEventsDir returns the directory for per-session event logs.
func BundlesEventsDir(stateDir string) string {
    return filepath.Join(stateDir, "bundles", "events")
}

// SessionEventLogPath returns the JSONL log path for the given session.
// sessionID is base64 URL-encoded (RawURLEncoding, no padding) for filename safety.
func SessionEventLogPath(stateDir, sessionID string) string {
    safe := base64.RawURLEncoding.EncodeToString([]byte(sessionID))
    return filepath.Join(BundlesEventsDir(stateDir), safe+".jsonl")
}
```

旧 `EventLogPath` 确认无其他引用后删除。

#### `pkg/agentrun/server/log.go`

**新增 `lastValidOffset`** — 扫描文件，返回最后有效事件的 `Seq + 1` 和字节偏移：

```go
// lastValidOffset scans path and returns (nextSeq, byteOffset) of the last
// successfully decoded event line. nextSeq = lastEvent.Seq + 1.
// Returns (0, 0, nil) for empty or nonexistent files.
func lastValidOffset(path string) (nextSeq int, offset int64, err error) {
    f, err := os.Open(path)
    if os.IsNotExist(err) {
        return 0, 0, nil
    }
    if err != nil {
        return 0, 0, fmt.Errorf("events: scan %s: %w", path, err)
    }
    defer f.Close()

    var pos int64
    var lastValidEnd int64
    lastSeq := -1

    scanner := bufio.NewScanner(f)
    scanner.Buffer(make([]byte, 64*1024), 64*1024)
    for scanner.Scan() {
        line := scanner.Bytes()
        end := pos + int64(len(line)) + 1 // +1 for \n
        if len(bytes.TrimSpace(line)) > 0 {
            var e runapi.AgentRunEvent
            if json.Unmarshal(line, &e) == nil {
                lastValidEnd = end
                lastSeq = e.Seq
            }
        }
        pos = end
    }
    if err := scanner.Err(); err != nil {
        return 0, 0, fmt.Errorf("events: scan %s: %w", path, err)
    }
    if lastSeq < 0 {
        return 0, 0, nil
    }
    return lastSeq + 1, lastValidEnd, nil
}
```

**修改 `OpenEventLog`** — 替换 `countLines`，并在打开前截断 damaged tail：

```go
func OpenEventLog(path string) (*EventLog, error) {
    nextSeq, truncateAt, err := lastValidOffset(path)
    if err != nil {
        return nil, fmt.Errorf("events: open log %s: %w", path, err)
    }

    // O_RDWR so we can truncate; O_CREATE so new files work.
    f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o640)
    if err != nil {
        return nil, fmt.Errorf("events: open log %s: %w", path, err)
    }

    // Truncate to end of last valid event (removes damaged tail, no-op for clean files).
    if err := f.Truncate(truncateAt); err != nil {
        _ = f.Close()
        return nil, fmt.Errorf("events: truncate log %s: %w", path, err)
    }
    if _, err := f.Seek(truncateAt, io.SeekStart); err != nil {
        _ = f.Close()
        return nil, fmt.Errorf("events: seek log %s: %w", path, err)
    }

    return &EventLog{f: f, enc: json.NewEncoder(f), nextSeq: nextSeq}, nil
}
```

`countLines` 函数删除（不再使用）。

**测试**（`log_test.go`）：
- 空文件/不存在文件 → nextSeq=0, offset=0
- n 条正常事件 → nextSeq = lastSeq+1
- 非连续 seq（fail-closed drop 场景）→ 正确读最后 seq
- 有 damaged tail → 截断后 replay 正常

#### `pkg/agentrun/server/translator.go`

- 删除 `log *EventLog` 构造参数，新增 `stateDir string`（空 = 不记日志）
- `NewTranslator(runID, in, stateDir string, logger)` 签名更新
- `nextSeq` 初始化为 0
- `SetSessionID(id string)` 改造（在 `mu.Lock` 下执行）：
  1. 关闭旧 `t.log`（如有）
  2. 若 `t.stateDir != ""` 且 `id != ""`：
     - `os.MkdirAll(spec.BundlesEventsDir(t.stateDir), 0o750)`
     - `path := spec.SessionEventLogPath(t.stateDir, id)`
     - `t.log, err = OpenEventLog(path)` — 新文件从 seq=0 开始，旧文件续接
     - `t.nextSeq = t.log.NextSeq()`
  3. `t.sessionID = id`
- 新增 `ReadCurrentSessionLog(fromSeq int) ([]runapi.AgentRunEvent, error)`：
  ```go
  func (t *Translator) ReadCurrentSessionLog(fromSeq int) ([]runapi.AgentRunEvent, error) {
      t.mu.Lock()
      sid, stateDir := t.sessionID, t.stateDir
      t.mu.Unlock()
      if sid == "" || stateDir == "" {
          return nil, nil
      }
      return ReadEventLog(spec.SessionEventLogPath(stateDir, sid), fromSeq)
  }
  ```

#### `pkg/agentrun/server/service.go`

- 删除 `logPath string` 字段
- `New(mgr, trans, logger)` 签名简化（删除 logPath 参数）
- `watchWithReplay` 中读历史改为 `s.trans.ReadCurrentSessionLog(fromSeq)`

#### `cmd/mass/commands/run/command.go`

- 删除 `OpenEventLog` 调用和 `evLog.Close()` defer
- 更新 `NewTranslator` 调用：传 `runStateDir`（`stateDir`，不是 eventsDir）
- 更新 `runserver.New` 调用：去掉 logPath 参数

---

## Part 2: 用户触发操作的审计日志

### 覆盖范围
- `session/cancel`
- `session/set_model`
- `session/stop`

### 审计时机：用 `defer` 保证任意 return 路径都写入

参数校验失败（如 `missing modelId`）会在操作前 `return`，若在操作完成后调用 audit 会漏掉。改用具名返回值 + `defer`：

```go
func (s *Service) SetModel(ctx context.Context, req *runapi.SessionSetModelParams) (result *runapi.SessionSetModelResult, retErr error) {
    params := map[string]string{"modelId": req.ModelID}
    defer func() { s.trans.NotifyOperationAudit("set_model", params, retErr) }()

    if req.ModelID == "" {
        return nil, jsonrpc.ErrInvalidParams("missing modelId")  // audit 仍会写入
    }
    // ...
}
```

`Cancel` 和 `Stop` 同样使用具名返回值 + `defer`。

### 新增事件类型字段

#### `pkg/agentrun/api/event_types.go`
```go
type RuntimeUpdateEvent struct {
    // ... 现有字段 ...
    OperationAudit *OperationAuditEvent `json:"operationAudit,omitempty"`
}

// OperationAuditEvent records the result of a user-triggered operation.
type OperationAuditEvent struct {
    Operation string            `json:"operation"`        // "cancel", "set_model", "stop"
    Params    map[string]string `json:"params,omitempty"` // e.g. {"modelId": "claude-xxx"}
    Success   bool              `json:"success"`
    Error     string            `json:"error,omitempty"`
}
```

#### `pkg/agentrun/server/translator.go`
```go
func (t *Translator) NotifyOperationAudit(op string, params map[string]string, err error) {
    errMsg := ""
    if err != nil {
        errMsg = err.Error()
    }
    t.broadcastEvent(runapi.RuntimeUpdateEvent{
        OperationAudit: &runapi.OperationAuditEvent{
            Operation: op,
            Params:    params,
            Success:   err == nil,
            Error:     errMsg,
        },
    })
}
```

---

## Part 3: 修复 Restart 状态序列

### 目标时序
`restarting → stopped → creating → idle`

### `restarting` 必须纳入恢复清理语义

当前 `recovery.go` 的 creating-cleanup pass 只处理 `creating` 状态。若 daemon 在 `restarting` 阶段崩溃，重启后 agent 永远卡在 `restarting`。

#### `pkg/agentd/recovery.go`

在 creating-cleanup pass 中同时清理 `restarting`：

```go
// Creating/Restarting-cleanup pass: agents bootstrapping or restarting when
// the daemon crashed will never complete — mark them as error.
for _, queryState := range []apiruntime.Status{apiruntime.StatusCreating, apiruntime.StatusRestarting} {
    agents, err := m.store.ListAgentRuns(ctx, &pkgariapi.AgentRunFilter{State: queryState})
    if err != nil {
        m.logger.Warn("recovery: failed to list agents for cleanup", "state", queryState, "error", err)
        continue
    }
    for _, agent := range agents {
        key := agentKey(agent.Metadata.Workspace, agent.Metadata.Name)
        if recoveredAgentIDs[key] {
            continue
        }
        errMsg := fmt.Sprintf("agent bootstrap lost: daemon restarted during %s phase", queryState)
        _ = m.agents.UpdateStatus(ctx, agent.Metadata.Workspace, agent.Metadata.Name,
            pkgariapi.AgentRunStatus{State: apiruntime.StatusError, ErrorMessage: errMsg})
    }
}
```

#### `pkg/runtime-spec/api/types.go`
```go
// StatusRestarting means the agent-run is being restarted.
// Transitions: restarting → stopped → creating → idle.
// Treated as transient: daemon recovery cleans up stuck-restarting agents.
StatusRestarting Status = "restarting"
```

#### `pkg/ari/server/server.go`
Restart handler 将初始的 `UpdateStatus(creating)` 改为 `UpdateStatus(restarting)`：
```go
if err := a.agents.UpdateStatus(ctx, wsName, name, pkgariapi.AgentRunStatus{
    State: apiruntime.StatusRestarting,  // 原为 StatusCreating
}); ...
```

goroutine 中保留 `Stop()` 后的 `UpdateStatus(creating)`（`watchProcess()` 写 `stopped`，goroutine 再覆盖为 `creating`）。

---

## 关键文件

| 文件 | Part | 操作 |
|------|------|------|
| `pkg/runtime-spec/state.go` | 1 | 新增 `BundlesEventsDir` / `SessionEventLogPath`（base64url 文件名）|
| `pkg/agentrun/server/log.go` | 1 | 新增 `lastValidOffset`；修改 `OpenEventLog`（截断 + 正确 nextSeq）；删除 `countLines` |
| `pkg/agentrun/server/translator.go` | 1+2 | 字段改为 `stateDir`；`SetSessionID` 改造；新增 `ReadCurrentSessionLog`；新增 `NotifyOperationAudit` |
| `pkg/agentrun/server/service.go` | 1+2 | 用 `trans.ReadCurrentSessionLog`；删除 logPath；用 `defer` 调用 audit |
| `pkg/agentrun/api/event_types.go` | 2 | 新增 `OperationAuditEvent`，扩展 `RuntimeUpdateEvent` |
| `cmd/mass/commands/run/command.go` | 1 | 传 `runStateDir`，删除 OpenEventLog |
| `pkg/runtime-spec/api/types.go` | 3 | 新增 `StatusRestarting` |
| `pkg/ari/server/server.go` | 3 | Restart 初始状态改为 `restarting` |
| `pkg/agentd/recovery.go` | 3 | cleanup pass 同时处理 `restarting` 状态 |

## 验证

```bash
make build
make lint
go test ./pkg/agentrun/server/... ./pkg/runtime-spec/... ./pkg/agentd/...
```

手动测试：
1. Part1：Restart 后检查 `{stateDir}/bundles/events/` 目录出现 base64url 命名的 `.jsonl`，文件内 seq 从 0 开始；恢复场景（同 sessionID）文件追加而非截断
2. Part2：cancel / set_model（含参数校验失败）/ stop 后用 `watch_event` 看到 `runtime_update.operationAudit` 事件
3. Part3：Restart agent 时状态序列为 `restarting → stopped → creating → idle`；模拟 daemon 在 `restarting` 阶段崩溃，重启后 agent 变 `error`
