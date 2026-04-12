# 统一日志体系方案

## 背景与目标

### 问题1：时间格式不完整
`internal/logging/logging.go` 中 pretty/default 格式使用 `time.Kitchen`（如 `5:25PM`），缺少年月日信息，不利于日志排查。

### 问题2：部分组件未统一到 slog
以下 5 个文件仍使用标准库 `log` 包，输出格式为 `2026/04/12 17:25:27 runtime: acp session/new request:`，与 slog 体系的结构化输出不一致：

| 文件 | 使用情况 |
|------|---------|
| `pkg/runtime/runtime.go:143` | `log.Printf("runtime: acp session/new request:\n%s", ...)` |
| `pkg/rpc/server.go:101,319` | `log.Printf("rpc: accept error: %v", ...)` / `log.Printf("rpc: stop error: %v", ...)` |
| `pkg/events/log.go:150` | `log.Printf("events: skipping %d damaged tail line(s)...")` |
| `cmd/agentd/subcommands/shim/command.go:102` | `log.Printf("agent-shim: rpc server error: %v", ...)` |
| `cmd/agentd/subcommands/workspacemcp/command.go:152,193,248,259,277` | 多处 `log.Printf(...)` |

**目标**：
1. 将 pretty 格式的时间改为包含年月日的格式
2. 将所有 `log.Printf` 统一迁移到 `log/slog`

## 方案

### 改动1：修改时间格式（internal/logging/logging.go）

将 `time.Kitchen`（`3:04PM`）改为 `time.DateTime`（`2006-01-02 15:04:05`）。

```go
// 改前
TimeFormat: time.Kitchen,

// 改后
TimeFormat: time.DateTime,
```

涉及行：第 27 行和第 32 行。

### 改动2：pkg/runtime/runtime.go — 注入 logger

**现状**：`runtime.Manager` 没有 logger 字段，直接使用 `log.Printf`。

**方案**：
- 给 `Manager` 结构体添加 `logger *slog.Logger` 字段
- 修改构造函数 `New(cfg, bundleDir, stateDir)` → `New(cfg, bundleDir, stateDir, logger)` 添加 `logger *slog.Logger` 参数
- 将 `log.Printf("runtime: acp session/new request:\n%s", ...)` 改为 `m.logger.Debug("acp session/new request", "body", string(debugJSON))`
  - **选择 Debug 级别的理由**：`NewSessionRequest` 包含 `McpServers` 配置，其中 `Env` 字段可能包含敏感信息（如 `OAR_AGENTD_SOCKET` 等内部路径、未来可能携带的 API key），默认 info 级别不应输出完整请求体。用户需要排查 ACP bootstrap 时可通过 `--log-level debug` 开启。
- 调用方（`cmd/agentd/subcommands/shim/command.go`）传入 logger

构造函数签名改变后需更新的调用点：
- `cmd/agentd/subcommands/shim/command.go:74` — `runtime.New(cfg, bundle, shimStateDir)` → `runtime.New(cfg, bundle, shimStateDir, logger)`
- 测试中的调用点（通过 `go test ./pkg/runtime/...` 编译验证）

### 改动3：pkg/rpc/server.go — 注入 logger

**现状**：`rpc.Server` 没有 logger 字段，2 处使用 `log.Printf`。

**方案**：
- 给 `Server` 结构体添加 `logger *slog.Logger` 字段
- 在 `New()` 构造函数中接受 logger 参数
- 替换：
  - `log.Printf("rpc: accept error: %v", err)` → `s.logger.Error("accept error", "error", err)`
  - `log.Printf("rpc: stop error: %v", err)` → `s.logger.Error("stop error", "error", err)`
- 调用方（`shim/command.go`）传入 logger

### 改动4：pkg/events/log.go — 注入 logger

**现状**：`ReadEventLog` 是一个包级函数，1 处使用 `log.Printf`。

**方案**：
- 使用 `slog.Default()` 替代，因为 `ReadEventLog` 是无状态函数，不宜改签名加 logger 参数
- 替换：`log.Printf("events: skipping %d damaged tail line(s) in %s", ...)` → `slog.Warn("skipping damaged tail lines", "count", len(lines)-i, "path", path)`

### 改动5：cmd/agentd/subcommands/shim/command.go — 创建 slog logger

**现状**：shim 进程独立运行，没有初始化 slog，使用 `log.Printf`。

**方案**：
- 在 `run()` 入口使用 `internal/logging` 初始化 slog
- 日志格式和级别通过环境变量 `OAR_LOG_LEVEL` / `OAR_LOG_FORMAT` 读取，未设置或无效值时 fallback 到 `pretty`/`info`：
  ```go
  logLevel := os.Getenv("OAR_LOG_LEVEL")
  logFormat := os.Getenv("OAR_LOG_FORMAT")
  level, err := logging.ParseLevel(logLevel)
  if err != nil {
      level = slog.LevelInfo // invalid or empty → default info
  }
  if logFormat == "" {
      logFormat = "pretty"
  }
  handler := logging.NewHandler(logFormat, level, os.Stderr)
  logger := slog.New(handler)
  slog.SetDefault(logger)
  ```
- 将 logger 传入 `runtime.New` 和 `rpc.New`
- 替换 `log.Printf("agent-shim: rpc server error: %v", err)` → `logger.Error("rpc server error", "error", err)`
- 设置 `slog.SetDefault(logger)` 使 events 包中的 `slog.Warn` 也能生效

shim 是 agentd fork 出的子进程：
- shim 的 stderr 被 agentd 的 `cmd.Stderr = os.Stderr` 捕获，输出到 stderr 即可
- 日志配置通过环境变量继承自 agentd（见改动8）

### 改动6：cmd/agentd/subcommands/workspacemcp/command.go — 迁移到 slog

**现状**：workspace-mcp-server 是独立进程，使用 `log.SetPrefix` + `log.SetOutput` + 多处 `log.Printf`。

**方案**：

#### 6a. 日志初始化和文件生命周期

替换现有的 `log.SetPrefix` + `log.SetOutput` 逻辑：

```go
func run() error {
    // Determine log output target.
    var w io.Writer = os.Stderr
    if stateDir := os.Getenv("OAR_STATE_DIR"); stateDir != "" {
        logPath := filepath.Join(stateDir, "workspace-mcp-server.log")
        f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
        if err != nil {
            // Cannot open log file; warn on stderr and continue.
            fmt.Fprintf(os.Stderr, "workspace-mcp-server: failed to open log file %s: %v, falling back to stderr\n", logPath, err)
        } else {
            w = f
            defer f.Close()
        }
    }

    // Initialize slog from env (inherited from agentd via generateConfig).
    logLevel := os.Getenv("OAR_LOG_LEVEL")
    logFormat := os.Getenv("OAR_LOG_FORMAT")
    level, err := logging.ParseLevel(logLevel)
    if err != nil {
        level = slog.LevelInfo
    }
    if logFormat == "" {
        logFormat = "pretty"
    }
    handler := logging.NewHandler(logFormat, level, w)
    logger := slog.New(handler)
    slog.SetDefault(logger)
    // ... rest of run()
}
```

关键点：
- `defer f.Close()` 确保日志文件在 `run()` 返回时关闭
- log file 打开失败时用 `fmt.Fprintf(os.Stderr, ...)` 输出 warn（此时 slog 尚未初始化），然后继续用 stderr 作为 writer

#### 6b. handler 签名改造

将 logger 通过参数传入 handler 闭包：

```go
// 签名改为接受 logger 参数
func workspaceSendHandler(cfg config, logger *slog.Logger) mcp.ToolHandler { ... }
func workspaceStatusHandler(cfg config, logger *slog.Logger) mcp.ToolHandler { ... }
```

调用方：
```go
server.AddTool(&mcp.Tool{...}, workspaceSendHandler(cfg, logger))
server.AddTool(&mcp.Tool{...}, workspaceStatusHandler(cfg, logger))
```

#### 6c. 替换所有 `log.Printf` 为 slog 调用

- `log.Printf("workspace_send: target=%s needsReply=%v", ...)` → `logger.Info("workspace_send", "target", input.TargetAgent, "needsReply", input.NeedsReply)`
- `log.Printf("workspace_status: workspace=%s", ...)` → `logger.Info("workspace_status", "workspace", cfg.workspaceName)`
- `log.Printf("starting (workspace=%s, agentName=%s)", ...)` → `logger.Info("starting", "workspace", cfg.workspaceName, "agent", cfg.agentName)`
- `log.Printf("server exited: %v", err)` → `logger.Error("server exited", "error", err)`

移除 `log.SetPrefix`，slog 不需要前缀。

### 改动7：移除未使用的 `"log"` import

完成上述改动后，移除所有不再使用的 `"log"` import。

### 改动8：日志配置传递链 — agentd → shim / workspace-mcp

**现状**：`agentd server` 通过 `--log-level` / `--log-format` flags 配置日志，但 fork 出的 shim 和 workspace-mcp 子进程无法获取这些配置。

**方案**：将 logLevel 和 logFormat 存入 `agentd.Options`（或 `ProcessManager` 字段），在 fork 子进程时通过环境变量传递。

#### 8a. 扩展 `agentd.Options` 或 `ProcessManager`

在 `ProcessManager` 中添加 `logLevel` 和 `logFormat` 字段（因为 `ProcessManager` 已经持有 logger）：

```go
type ProcessManager struct {
    // ... existing fields
    logLevel  string // "debug", "info", "warn", "error"
    logFormat string // "pretty", "text", "json"
}
```

修改 `NewProcessManager` 签名，添加 logLevel, logFormat 参数。调用方 `cmd/agentd/subcommands/server/command.go` 传入。

#### 8b. forkShim 传递环境变量

在 `forkShim` 中设置 shim 进程的环境变量：

```go
cmd := exec.Command(shimBinary, args...)
cmd.Env = append(os.Environ(),
    "OAR_LOG_LEVEL="+m.logLevel,
    "OAR_LOG_FORMAT="+m.logFormat,
)
cmd.Stderr = os.Stderr
```

#### 8c. generateConfig 传递给 workspace-mcp

在 `generateConfig` 的 workspace-mcp MCP server env 中添加日志配置：

```go
Env: []api.EnvVar{
    {Name: "OAR_AGENTD_SOCKET", Value: m.socketPath},
    {Name: "OAR_WORKSPACE_NAME", Value: agent.Metadata.Workspace},
    {Name: "OAR_AGENT_NAME", Value: agent.Metadata.Name},
    {Name: "OAR_STATE_DIR", Value: stateDir},
    {Name: "OAR_LOG_LEVEL", Value: m.logLevel},
    {Name: "OAR_LOG_FORMAT", Value: m.logFormat},
},
```

#### 8d. 无效值处理策略

shim 和 workspace-mcp 读取 `OAR_LOG_LEVEL` / `OAR_LOG_FORMAT` 时：
- 空值或无效值 → fallback 到 `info` / `pretty`（与 agentd 默认值一致）
- 使用 `logging.ParseLevel` 自带的错误处理，不输出额外 warn（无效值在 agentd 启动时就会被 flags 验证拦截，传到子进程的值理论上是有效的）

### 改动9：验证步骤

实施完成后执行以下验证清单：

1. **残留检查**：`rg "log\.Printf|\"log\"" pkg cmd internal --type go` — 确认除 `_test.go` 外无标准 `log` 包残留
2. **编译验证**：`make build`（AGENTS.md 要求的构建方式）
3. **单元测试**：`go test ./...` — 确认构造函数签名变更未破坏测试
4. **日志格式验证**：手动启动 `agentd server`，确认终端日志显示年月日时间格式
5. **环境变量传递验证**：用 `--log-level debug` 启动 agentd，创建 agent run，确认 shim 日志也输出 debug 级别

### 改动10：同步设计文档 — docs/design/agentd/ari-spec.md

**现状**：`docs/design/agentd/ari-spec.md` 第 453-458 行的 workspace-mcp-server 环境变量表只列出 4 个变量：`OAR_AGENTD_SOCKET`、`OAR_WORKSPACE_NAME`、`OAR_AGENT_NAME`、`OAR_STATE_DIR`。

**方案**：在环境变量表末尾新增 2 行：

```markdown
| Variable | Required | Meaning |
|---|---|---|
| `OAR_AGENTD_SOCKET` | yes | Path to agentd's Unix socket |
| `OAR_WORKSPACE_NAME` | yes | The workspace this server instance belongs to |
| `OAR_AGENT_NAME` | no | The agent name for sender identification |
| `OAR_STATE_DIR` | no | State directory for log output |
| `OAR_LOG_LEVEL` | no | Log level (debug, info, warn, error); defaults to `info` |
| `OAR_LOG_FORMAT` | no | Log format (pretty, text, json); defaults to `pretty` |
```

**关于 shim 的 env 文档**：shim 的环境变量（`OAR_LOG_LEVEL`/`OAR_LOG_FORMAT`）通过 `ProcessManager.forkShim` 的 `cmd.Env` 注入，属于 agentd 内部进程管理的实现细节，不属于公开 ARI 契约。当前 `docs/design/runtime/agent-shim.md` 描述的是 shim 的 CLI flags（`--bundle`、`--id`、`--state-dir`、`--permissions`），不含 env 变量表。shim 的日志 env 不需要加入设计文档，因为：
- shim 不是由外部调用者启动的，只由 agentd 内部 fork
- 这两个 env 是 agentd→shim 的内部传递通道，不构成外部接口

### 不改动的部分

`cmd/agentdctl/` 下的 `fmt.Print/Printf/Println` — 这些是 CLI 用户交互输出（如 `"Agent run stopped"`、`"Workspace deleted"`），不是日志，应保持 fmt 输出。

## 风险与取舍

1. **签名变更传播**：`runtime.Manager`、`rpc.Server` 新增 logger 参数会影响所有调用点和测试。需全面检查 `NewManager` 和 `rpc.New` 的所有调用方。

2. **shim/workspacemcp 日志格式变化**：这两个独立进程的日志格式会从 Go 标准 log 格式变为 slog 格式。如果有外部工具解析这些日志，需要注意兼容性。但根据项目 CLAUDE.md 中 "No need to consider compatibility Now"，可以直接改。

3. **slog.Default() 依赖**：events 包使用 `slog.Default()`，要求调用方在使用前已设置好默认 logger。当前 shim 和 workspacemcp 的 `run()` 入口都会最先初始化 logger，所以没有问题。

4. **取舍：时间格式选择**：`time.DateTime`（`2006-01-02 15:04:05`）足够日志排查需求，不包含时区信息。如果需要时区可用 `time.RFC3339`，但会更长。推荐 `time.DateTime`，简洁且包含年月日。

## 审查记录

### codex 第1轮

#### ✅ 认可项

1. **目标明确且可验证**：pretty 日志时间从 `time.Kitchen` 改为包含年月日的格式，以及仓库内 `log.Printf` 全量迁移到 `slog`，都可以通过 `rg "log\.Printf"`、构建和日志样例验证。

2. **`cmd/agentdctl` 不纳入迁移是正确边界**：CLI 面向用户的 `fmt.Print*` 输出不是进程日志，继续保持标准输出语义合理。

3. **给有状态组件显式注入 logger 的方向正确**：`runtime.Manager`、`rpc.Server` 是长期对象，显式持有 `*slog.Logger` 比在内部散落使用全局 logger 更利于组件化和测试。

4. **`workspace-mcp` 保持现有输出目标的原则正确**：当前它在 `OAR_STATE_DIR` 存在时写文件，否则写 stderr；迁移到 slog 时保留这个行为是必要的。

#### ❌ 问题项

1. **shim / workspace-mcp 的日志级别和格式来源不明确，且 `OAR_LOG_LEVEL` / `OAR_LOG_FORMAT` 当前没有传递链。**
   - 问题是什么：方案说 shim 可通过 `OAR_LOG_LEVEL` / `OAR_LOG_FORMAT` 读取配置，但当前 `agentd server` 只有 `--log-level` / `--log-format` flags，没有对应环境变量；`ProcessManager.forkShim` 也没有设置 shim 的 env；`generateConfig` 给 workspace-mcp 只传了 `OAR_AGENTD_SOCKET`、`OAR_WORKSPACE_NAME`、`OAR_AGENT_NAME`、`OAR_STATE_DIR`。
   - 为什么是问题：如果按方案实现，子进程日志配置会实际退回硬编码默认值，用户通过 agentd flags 配置的日志格式/级别不会传播；这会造成“统一日志体系”的配置语义不统一。
   - 期望如何解决：修订方案需明确二选一：要么定义子进程固定默认 `pretty/info` 且不声称继承 agentd 配置；要么把 logLevel/logFormat 放入 `agentd.Options` 或 `ProcessManager`，fork shim 时显式设置 env，并在 `generateConfig` 的 workspace-mcp env 中传递同一配置。同时说明无效 env 值的处理策略。

2. **`runtime.Manager` 的 session/new 日志从无条件输出改为 Debug 会改变可见性，方案未说明这是有意行为。**
   - 问题是什么：现有 `log.Printf("runtime: acp session/new request...")` 在任何日志级别下都会输出；方案改为 `m.logger.Debug(...)`，而默认级别是 info，因此默认运行时这条日志会消失。
   - 为什么是问题：这不只是格式迁移，而是日志可观测性的行为变更；如果这条日志用于排查 ACP bootstrap，默认不可见可能降低诊断能力。反过来，如果请求体可能包含 MCP server env 或敏感配置，降级到 Debug 是合理的，但必须写清楚原因。
   - 期望如何解决：修订方案需明确该日志的目标级别和理由。若选择 Debug，应补充“避免默认输出 session bootstrap 细节/潜在敏感字段”；若选择 Info，应说明为什么默认输出完整请求体可接受。

3. **`workspace-mcp` 的 handler 改造步骤不完整。**
   - 问题是什么：当前 `workspaceSendHandler(cfg)` 和 `workspaceStatusHandler(cfg)` 没有 logger 参数，也不是方法；方案中的替换示例使用 `logger.Info(...)`，但没有说明 logger 如何进入这两个闭包。
   - 为什么是问题：按文档直接执行会卡在实现细节上，容易引入包级变量或临时使用 `slog.Default()`，与“显式 logger”的迁移风格不一致。
   - 期望如何解决：修订方案需明确 handler 签名改为 `workspaceSendHandler(cfg config, logger *slog.Logger)` / `workspaceStatusHandler(cfg config, logger *slog.Logger)`，或明确统一使用 `slog.Default()`，并说明选择理由。

4. **`workspace-mcp` 文件日志的资源生命周期未覆盖。**
   - 问题是什么：迁移到 slog 后仍需要持有 `*os.File`，并在 `run()` 返回前关闭；当前方案只说 handler writer 指向文件，没有说明文件关闭。
   - 为什么是问题：虽然现有标准 log 代码也没有显式 close，但改造时应顺手保证文件描述符生命周期清晰，尤其这是长期子进程、日志文件路径由 state dir 决定。
   - 期望如何解决：修订方案补充：打开日志文件成功后保存 `f`，`defer f.Close()`；logger 初始化失败 fallback 到 stderr 后继续用 stderr logger 记录 warn。

5. **验证步骤缺失。**
   - 问题是什么：方案没有列出实施后需要跑哪些验证。
   - 为什么是问题：这次会改公共构造函数签名、测试调用点、独立子进程初始化和日志输出路径，仅靠人工检查容易漏编译错误或残留 import。
   - 期望如何解决：修订方案需加入验证清单，至少包括：`rg "log\.Printf|\"log\"" pkg cmd internal` 检查残留、`make build`（按 AGENTS.md）、以及相关 Go 测试或 `go test ./...`。

### claude-code 第2轮

逐条回应 codex 第1轮反馈：

1. **日志配置传递链** — ✅ 接受。新增改动8，明确方案：`ProcessManager` 持有 `logLevel`/`logFormat` 字段，`forkShim` 通过 `cmd.Env` 传递 `OAR_LOG_LEVEL`/`OAR_LOG_FORMAT`，`generateConfig` 在 workspace-mcp env 中传递同样的变量。子进程读取时无效/空值 fallback 到 `info`/`pretty`。

2. **session/new 日志级别变更** — ✅ 接受。在改动2中补充了选择 Debug 的理由：`NewSessionRequest` 包含 `McpServers.Env`，可能携带敏感配置信息（内部 socket 路径、未来的 API key），默认 info 不应输出完整请求体。用户可通过 `--log-level debug` 开启。

3. **workspace-mcp handler 签名** — ✅ 接受。在改动6b中明确：handler 签名改为 `workspaceSendHandler(cfg config, logger *slog.Logger)` / `workspaceStatusHandler(cfg config, logger *slog.Logger)`，保持显式 logger 注入风格一致。

4. **workspace-mcp 文件生命周期** — ✅ 接受。在改动6a中补充：打开成功后 `defer f.Close()`；打开失败时用 `fmt.Fprintf(os.Stderr, ...)` 输出 warn（此时 slog 未初始化），然后继续用 stderr writer。

5. **验证步骤** — ✅ 接受。新增改动9，包含 5 项验证清单：残留检查、make build、go test、日志格式验证、环境变量传递验证。

### codex 第2轮

#### ✅ 认可项

1. **第1轮的 5 个问题均已正面回应**：日志配置传递链、`session/new` Debug 级别理由、workspace-mcp handler 显式注入 logger、文件 close、验证清单都已补齐，方案主体已经可执行。

2. **`session/new` 降为 Debug 的理由成立**：`NewSessionRequest` 包含 MCP server 配置和 env，默认 info 输出完整 body 的风险高于收益；通过 `--log-level debug` 开启排障符合可观测性与安全性的平衡。

3. **日志配置通过 env 传给独立子进程的方案合理**：shim 和 workspace-mcp 都是 self-fork/stdio 子进程，用 `OAR_LOG_LEVEL` / `OAR_LOG_FORMAT` 传递 daemon 已验证的配置，侵入面较小。

#### ❌ 问题项

1. **新增 workspace-mcp 环境变量未同步 `docs/design`，与仓库约束冲突。**
   - 问题是什么：修订方案计划在 workspace-mcp 的 MCP server env 中新增 `OAR_LOG_LEVEL` / `OAR_LOG_FORMAT`，但 `docs/design/agentd/ari-spec.md` 的 `workspace-mcp-server` 环境变量表目前只列出 `OAR_AGENTD_SOCKET`、`OAR_WORKSPACE_NAME`、`OAR_AGENT_NAME`、`OAR_STATE_DIR`。
   - 为什么是问题：AGENTS.md 明确要求代码变更必须 aligned with `docs/design`。workspace-mcp 的环境变量表是该子命令配置契约的设计记录；新增 env 不同步设计文档，会让实现和设计集漂移。
   - 期望如何解决：修订方案需新增一个改动项，明确同步 `docs/design/agentd/ari-spec.md` 的 workspace-mcp env 表，加入 `OAR_LOG_LEVEL`（no，日志级别，默认 `info`）和 `OAR_LOG_FORMAT`（no，日志格式，默认 `pretty`）。如认为 shim 的 env 配置也属于设计契约，也需指出对应设计文档位置或说明当前无公开契约、仅为 agentd 内部传递。

### claude-code 第3轮

回应 codex 第2轮反馈：

1. **workspace-mcp 环境变量同步设计文档** — ✅ 接受。新增改动10，明确在 `docs/design/agentd/ari-spec.md` 的 workspace-mcp-server env 表中新增 `OAR_LOG_LEVEL`（no，默认 info）和 `OAR_LOG_FORMAT`（no，默认 pretty）。同时说明 shim 的日志 env 属于 agentd 内部实现细节、不构成公开契约，无需加入设计文档。

### codex 第3轮

#### ✅ 认可项

1. **设计一致性问题已补齐**：改动10 明确同步 `docs/design/agentd/ari-spec.md` 的 workspace-mcp-server 环境变量表，覆盖 `OAR_LOG_LEVEL` / `OAR_LOG_FORMAT` 的 required 状态、取值和默认值，满足 AGENTS.md 中代码变更需对齐 `docs/design` 的要求。

2. **shim env 不写入设计契约的边界说明可接受**：当前设计文档把 shim RPC surface、runtime state、socket/state 布局作为契约；`ProcessManager.forkShim` 注入日志 env 属于 agentd 内部进程管理细节，不是外部 ARI 或 shim RPC 调用方依赖的接口。保留在实现方案中说明即可。

3. **最终方案已具备实施闭环**：方案覆盖代码改动、构造函数签名传播、子进程日志配置传递、workspace-mcp 文件日志生命周期、设计文档同步和验证步骤；剩余风险均已在风险与取舍中说明。

#### ❌ 问题项

无阻塞问题。批准执行。

## 最终方案

按上文「方案」章节执行，包含改动1-10，并按改动9完成验证。
