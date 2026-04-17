# 深度体检报告与修复方案

## 方案

### 背景与目标

对 open-agent-runtime 项目进行全面体检，覆盖以下维度：
- 构建与依赖健康度
- 代码与设计文档对齐
- 测试覆盖缺口
- 代码规范合规性
- 工程基础设施完备度

目标：产出可执行的修复清单，按优先级分批处理。

---

### 一、体检结果总览

| 维度 | 状态 | 评分 |
|------|------|------|
| 构建与依赖 | ✅ 健康 | 95 |
| 架构对齐 | ⚠️ 有偏差 | 85 |
| 测试覆盖 | ⚠️ 缺口明显 | 50 |
| 代码规范 | ⚠️ 部分违规 | 80 |
| 工程基础设施 | ❌ 缺失 | 40 |
| 文档一致性 | ⚠️ 需更新 | 75 |

**综合健康分：71/100**

---

### 二、详细发现

#### 2.1 构建与依赖（✅ 健康）

- Go 1.26.1，go.mod 干净，36 个直接依赖，无版本冲突
- Makefile 有 `build`/`clean` 目标，构建通过
- 无废弃 API 使用（无 ioutil 等）
- third_party 下有一个 vet 警告（sync.RWMutex 值传递），属第三方代码，低优

#### 2.2 代码-设计对齐（⚠️ 有偏差）

**P0 — 文档与代码不一致：**

| # | 问题 | 设计文档 | 实际代码 | 影响 |
|---|------|----------|----------|------|
| A1 | `workspace/send` message 字段类型 | `string` (ari-spec.md:277, communication.md) | `[]ContentBlock` (pkg/ari/api/types.go:147) | 按文档实现的外部客户端会失败 |
| A4 | `agentrun/prompt` prompt 字段类型 | 未明确指定 | `[]ContentBlock` (pkg/ari/api/types.go:125) | 文档含糊，需明确 |

**F1 决策分析与推荐（A1/A4）：**

经调查所有调用方和消费者：

| 调用方 | 文件 | 行为 |
|--------|------|------|
| ARI Go client | `pkg/ari/client/client.go:138-149, 175-181` | 传 `[]ContentBlock` |
| massctl agentrun prompt | `cmd/massctl/commands/agentrun/command.go:~180` | 用 `TextBlock(text)` 包装 string |
| massctl workspace send | `cmd/massctl/commands/workspace/command.go:~130` | 用 `TextBlock(text)` 包装 string |
| workspace-mcp server | `cmd/mass/commands/workspacemcp/command.go:~90` | MCP input 接受 `string`，内部用 `TextBlock(text)` 包装 |
| ARI server handler | `pkg/ari/server/server.go:446-519, 204-284` | 接收和校验 `[]ContentBlock` |
| 集成测试 | `pkg/ari/server/server_test.go:649-702` | 用 `TextBlock("hello")` 构造 |

**推荐方案：以代码为准（`[]ContentBlock`），更新设计文档。**

理由：
1. 所有现有调用方（Go client、CLI、MCP、测试）均使用 `[]ContentBlock`
2. `[]ContentBlock` 与 ACP 多模态模型一致（支持 text/image/audio），改回 string 会丧失多模态能力
3. 内部 agent-run RPC（`session/prompt`）同样使用 `[]ContentBlock`，保持端到端一致
4. 无需兼容 string 输入——无已知外部客户端按 string spec 实现

**执行步骤：**
1. 更新 `docs/design/mass/ari-spec.md`：`workspace/send` 的 message 和 `agentrun/prompt` 的 prompt 字段类型改为 `ContentBlock[]`，附 ContentBlock schema 说明
2. 更新 `docs/design/workspace/communication.md`：同步 message 字段类型
3. 无需改代码

**P1 — 设计文档清理（非开放决策）：**

| # | 问题 | 详情 | 处理方式 |
|---|------|------|----------|
| A2 | `agentrun/create` 示例中的 `mcpServers` | 设计文档示例暗示可传 mcpServers，但按现有设计 mcpServers 通过 runtime config `acpAgent.session.mcpServers` 表达，workspace MCP 自动注入 | 修正文档示例，删除误导性 mcpServers 字段，或明确标注为 Future Work |
| A3 | `agentrun/create` 缺 `env` override | contract-convergence.md 已明确这是当前缺口，env 来自 host + Agent definition | 保持现状，确保文档措辞一致，标注为 Future Work |

**✅ 对齐良好的部分：**
- ARI 方法名完全匹配 spec（workspace/*, agent/*, agentrun/*）
- Agent-run RPC 方法完全匹配 run-rpc-spec
- 状态机 5 状态转换正确
- config.json 结构匹配
- 三层管理器（Workspace/AgentRun/Process）均已实现
- ARIView() 内部字段剥离正确
- 恢复与持久化字段匹配

#### 2.3 测试覆盖（⚠️ 缺口明显）

**现状**：57 个测试文件，49% 包覆盖率

**关键缺口（无测试的包）：**

| 优先级 | 包 | 文件数 | 风险 |
|--------|---|--------|------|
| P0 | `pkg/agentrun/api/` | 5 | 复杂联合类型无 JSON 往返测试 |
| P0 | `pkg/ari/api/` | 4 | 领域模型无验证测试 |
| P1 | `pkg/runtime-spec/api/` | 4 | 配置结构无校验测试 |
| P2 | `pkg/tui/component/` | 5/9 | 部分 UI 组件无测试 |

**CLI 命令测试现状**：多数 CLI 命令无测试（8/10 子目录缺失）

| 子目录 | 有测试 |
|--------|--------|
| `cmd/mass/commands/run/` | ✅ `command_test.go` |
| `cmd/mass/commands/server/` | ❌ |
| `cmd/mass/commands/workspacemcp/` | ❌ |
| `cmd/massctl/commands/compose/` | ✅ `config_test.go` |
| `cmd/massctl/commands/agent/` | ❌ |
| `cmd/massctl/commands/agentrun/` | ❌ |
| `cmd/massctl/commands/cliutil/` | ❌ |
| `cmd/massctl/commands/daemon/` | ❌ |
| `cmd/massctl/commands/workspace/` | ❌ |
| `cmd/massctl/commands/workspace/create/` | ❌ |

**已有测试质量（✅ 优秀）：**
- 表驱动测试广泛使用
- 边界用例覆盖良好
- testify 使用规范（require/assert 区分）
- 集成测试完善（tests/integration/）

#### 2.4 代码规范合规（⚠️ 部分违规）

**P0 — 错误处理反模式：store 层缺少类型化错误**

**问题根源**：`pkg/agentd/store/` 层所有错误均使用 `fmt.Errorf()` 返回纯字符串消息，无 sentinel 或 typed error。上层通过 `strings.Contains(err.Error(), ...)` 匹配，脆弱且违反 Go 惯例。

**错误链路现状：**
```
Store 层（fmt.Errorf 纯字符串）
  ↓ "store: workspace X already exists"
  ↓ "store: agent Y/Z does not exist"
AgentD Manager 层（strings.Contains 匹配）
  ↓ 转为 ErrAgentRunAlreadyExists / ErrAgentRunNotFound
ARI Server 层（strings.Contains 匹配 或 errors.As）
  ↓ 映射为 JSON-RPC 错误码
```

**涉及 string 匹配的位置（共 5 处）：**

| 文件 | 行号 | 匹配字符串 | 触发场景 |
|------|------|------------|----------|
| `pkg/ari/server/server.go` | ~104 | `"already exists"` | workspace 创建重复 |
| `pkg/ari/server/server.go` | ~196 | `"does not exist"` | workspace 删除不存在 |
| `pkg/agentd/agent.go` | ~82 | `"already exists"` | agent 创建重复 |
| `pkg/agentd/agent.go` | ~139 | `"does not exist"` | agent 状态更新不存在 |
| `pkg/agentd/agent.go` | ~169 | `"does not exist"` | agent 状态转换不存在 |

**Store 层错误来源（7 处 fmt.Errorf）：**

| 文件 | 行号 | 消息 |
|------|------|------|
| `pkg/agentd/store/workspace.go` | ~38 | `"store: workspace %s already exists"` |
| `pkg/agentd/store/workspace.go` | ~115 | `"store: workspace %s does not exist"` |
| `pkg/agentd/store/workspace.go` | ~148 | `"store: workspace %s does not exist"` |
| `pkg/agentd/store/agentrun.go` | ~46 | `"store: agent %s/%s already exists"` |
| `pkg/agentd/store/agentrun.go` | ~170 | `"store: agent %s/%s does not exist"` |
| `pkg/agentd/store/agentrun.go` | ~213 | `"store: agent %s/%s does not exist"` |
| `pkg/agentd/store/agentrun.go` | ~262 | `"store: agent %s/%s does not exist"` |

**修复方案：**

Step 1 — 在 `pkg/agentd/store/` 新增 `errors.go`：
```go
package store

import "errors"

var (
    ErrAlreadyExists = errors.New("store: already exists")
    ErrNotFound      = errors.New("store: not found")
)

type ResourceError struct {
    Op       string // "create", "update", "delete", "transition"
    Resource string // "workspace", "agent"
    Key      string // "ws-name" or "ws/agent-name"
    Err      error  // sentinel (ErrAlreadyExists / ErrNotFound)
}

func (e *ResourceError) Error() string {
    return e.Op + " " + e.Resource + " " + e.Key + ": " + e.Err.Error()
}

func (e *ResourceError) Unwrap() error { return e.Err }
```

Step 2 — 替换 store 层 7 处 `fmt.Errorf` 为 `&ResourceError{...}`

Step 3 — 替换 `pkg/agentd/agent.go` 3 处 `strings.Contains` 为 `errors.Is(err, store.ErrAlreadyExists)` / `errors.Is(err, store.ErrNotFound)`

Step 4 — 替换 `pkg/ari/server/server.go` 2 处 `strings.Contains` 为 `errors.Is` 或 `errors.As`

Step 5 — 补充测试：
- `pkg/agentd/store/errors_test.go`：验证 `errors.Is(resourceErr, ErrAlreadyExists)` 等
- 更新 `pkg/agentd/agent_test.go`：覆盖重复创建、缺失更新、状态转换缺失路径
- 更新 `pkg/ari/server/server_test.go`：验证 JSON-RPC 错误码映射

**参考模式**：`pkg/workspace/errors.go` 中 `WorkspaceError`/`GitError`/`HookError` 的 typed error + `Unwrap()` 实现。

**P1 — Context 传播修复（按语义分类）：**

经逐一调查 11 处 `context.Background()` 使用，按语义分为四类：

**A 类 — 已正确（4 处，无需修改）：**

| 文件 | 行号 | 函数 | 说明 |
|------|------|------|------|
| `pkg/agentd/process.go` | ~166 | stateChange handler | ✅ 已有 5s timeout |
| `pkg/agentd/process.go` | ~341 | bootstrap state sync | ✅ 已有 5s timeout |
| `pkg/agentd/process.go` | ~696 | watchProcess | ✅ 已有 5s timeout |
| `pkg/agentd/recovery.go` | ~336 | watchRecoveredProcess | ✅ 已有 5s timeout |

**B 类 — Daemon-owned 生命周期操作，需加 bounded timeout（2 处）：**

| 文件 | 行号 | 函数 | 当前问题 | 修复 |
|------|------|------|----------|------|
| `pkg/ari/server/server.go` | ~743 | recordPromptDeliveryFailure | 裸 Background()，多次 DB 操作无超时 | `context.WithTimeout(context.Background(), 5*time.Second)` — 与 A 类 DB 操作保持一致 |
| `pkg/agentd/process.go` | ~471 | createBundle (GetWorkspace) | 裸 Background()，DB 查询无超时 | `context.WithTimeout(context.Background(), 5*time.Second)` — 与 A 类 DB 操作保持一致 |

**说明**：这两处是纯 DB/store 操作，不涉及外部 I/O，5s 与现有 A 类模式一致。

**C 类 — 长生命周期后台操作，保持 Background 但依赖既有超时策略（3 处）：**

| 文件 | 行号 | 函数 | 语义分析 | 处理方式 |
|------|------|------|----------|----------|
| `pkg/ari/server/server.go` | ~130 | workspace/create goroutine | workspace prepare 包含 git clone + hook，时长不可预测 | **不加固定 timeout**。workspace prepare 的超时应由 workspace source/hook 自身控制。当前 `Prepare()` 已接受 ctx 参数，未来如需超时应在 workspace-spec 中定义 `prepareTimeoutSeconds` 并在 Manager.Prepare 内部实现。本次记录为**设计缺口**。 |
| `pkg/ari/server/server.go` | ~343 | agentrun/create goroutine | agent start 包含 config 生成、进程 fork、socket 等待 | **不加固定 timeout**。`ProcessManager.Start()` 内部 `waitForSocket` 已有 90s 硬编码超时（process.go:611），进程 fork 和 config 生成是快速操作。设计文档定义了 `startupTimeoutSeconds` 但尚未实现。本次记录为**设计缺口**：应实现 Agent definition 的 `startupTimeoutSeconds`，替代 90s 硬编码。 |
| `pkg/ari/server/server.go` | ~583 | agentrun/restart goroutine | stop（5s RPC + 10s graceful + 5s kill）+ start（含 90s socket wait） | **不加固定 timeout**。Stop 已有内部分段超时（process.go:743-754），Start 有 90s socket 超时。整体超时应由未来 `startupTimeoutSeconds` + shutdown policy 组合控制。 |

**D 类 — Fire-and-forget prompt delivery（2 处）：**

| 文件 | 行号 | 函数 | 语义分析 | 处理方式 |
|------|------|------|----------|----------|
| `pkg/ari/server/server.go` | ~276 | workspace/send prompt goroutine | `client.Prompt()` 是阻塞 RPC（`jsonrpc.Call`），会等到 agent turn 完成。正常 turn 可能持续数分钟。 | **不加固定短 timeout**。当前语义是"派发后等 turn 完成再更新状态"，这是正确的——turn 期间 agent 处于 running 状态，turn 结束后 runtime 自动转回 idle，由事件 watcher 捕获。异常情况（进程崩溃/断连）由 `watchProcess` goroutine 处理（检测到进程退出后标记 stopped）。 |
| `pkg/ari/server/server.go` | ~509 | agentrun/prompt prompt goroutine | 同上 | **同上**。`recordPromptDeliveryFailure` 仅在 `Prompt()` 返回错误时触发，用于处理连接失败等异常，不会误触发于正常长 turn。 |

**D 类详细说明**：
- `runclient.Client.Prompt()` 无内部超时，调用 `jsonrpc.Call()` 阻塞直到 agent turn 完成
- 已有 `SendPrompt()`/`CallAsync()` 非阻塞替代方案（用于 TUI 场景），但 ARI server 有意使用阻塞模式以确保状态一致性
- 异常保护链路已完整：进程退出 → `watchProcess` → 标记 stopped；client 断连 → `Prompt()` 返回错误 → `recordPromptDeliveryFailure`
- 不应改为非阻塞 dispatch，否则需要额外的状态回收机制
- Cancel 可通过 `session/cancel` RPC 中断正在进行的 turn

**设计缺口记录（本次不修复，记录待后续实现）：**

| 缺口 | 说明 | 建议 |
|------|------|------|
| workspace prepare timeout | workspace/create goroutine 无超时上限 | 在 workspace-spec 中新增 `prepareTimeoutSeconds`，默认 300s |
| agent startup timeout | 90s 硬编码于 `waitForSocket`，设计文档有 `startupTimeoutSeconds` 但未实现 | 实现 Agent definition 的 `startupTimeoutSeconds`，替代硬编码 |

**P2 — Import 分组违规（3 处）：**

| 文件 | 问题 |
|------|------|
| `pkg/ari/server/server.go` | 外部与内部 import 混排 |
| `pkg/agentd/process.go` | 分组不清晰 |
| `pkg/agentrun/server/service.go` | acp 外部 import 位置错误 |

修复：运行 `gci write` 或手动按 stdlib → external → internal 三组排列。

**✅ 合规良好：**
- 命名规范符合 Go 惯例
- 无 init() 滥用、无裸 return、无全局可变状态
- 错误包装（fmt.Errorf + %w）使用良好
- 结构化错误类型设计良好（workspace/errors.go）

#### 2.5 工程基础设施（❌ 缺失严重）

| 项目 | 状态 |
|------|------|
| CI/CD pipeline | ❌ 无 GitHub Actions / GitLab CI |
| Makefile test 目标 | ❌ 无 `make test`/`make lint`/`make coverage` |
| Lint 集成 | ⚠️ 有 .golangci.yaml 但无执行入口 |
| Graphify 缓存 | ⚠️ 陈旧分析文件占 ~7MB |

#### 2.6 工作区状态

当前 `git status --short` 显示以下脏文件，分为三类：

**A — 既有文档变更（11 个，来自此前命名重构同步）：**

| 文件 |
|------|
| `docs/ARCHITECTURE.md` |
| `docs/design/README.md` |
| `docs/design/contract-convergence.md` |
| `docs/design/mass/ari-spec.md` |
| `docs/design/mass/mass.md` |
| `docs/design/runtime/agent-run.md` |
| `docs/design/runtime/design.md` |
| `docs/design/runtime/run-rpc-spec.md` |
| `docs/design/runtime/runtime-spec.md` |
| `docs/design/runtime/why-no-runa.md` |
| `docs/design/workspace/communication.md` |

**B — 既有代码变更（2 个，来源待用户确认）：**

| 文件 |
|------|
| `pkg/agentrun/runtime/acp/runtime.go` |
| `pkg/tui/chat/chat.go` |

**C — 本方案文档（1 个，未跟踪）：**

| 文件 |
|------|
| `docs/plan/deep-healthcheck-20260417.md` |

**提交边界规则：**
- 本体检修复**不得**覆盖、格式化或顺手修改 A 类和 B 类既有变更
- A 类和 B 类变更需用户确认后独立提交，不混入体检修复 PR
- 体检修复应在干净基线上进行（先 stash 或在独立分支操作）

#### 2.7 杂项

- TUI 中 1 个 TODO：`pkg/tui/component/tools.go` — tool renderer 未实现
- README.md 13KB，结构良好

---

### 三、修复方案（按优先级）

#### P0 — 必须立即修复（影响正确性）

| # | 任务 | 预估改动 | 验收标准 |
|---|------|----------|----------|
| F1 | 更新设计文档：`workspace/send` message 和 `agentrun/prompt` prompt 字段类型改为 `ContentBlock[]` | ari-spec.md + communication.md 文档更新 | 文档中 workspace/send message 和 agentrun/prompt prompt 字段标注为 `ContentBlock[]`；`make build` 通过 |
| F2 | Store 层新增 typed errors + 替换 5 处 `strings.Contains` | 新增 `store/errors.go` ~30 行；修改 store 7 处 + agent.go 3 处 + server.go 2 处；新增/更新 3 个测试文件 | `go test ./pkg/agentd/store/... ./pkg/agentd/... ./pkg/ari/server/...` 全部通过；`grep -r 'strings.Contains(err.Error()' pkg/` 在非 third_party Go 文件中结果为 0 |

#### P1 — 短期修复（影响可维护性）

| # | 任务 | 预估改动 | 验收标准 |
|---|------|----------|----------|
| F4 | 补 API 包 JSON 往返测试：`agentrun/api/`, `ari/api/` | ~300 行测试 | `go test ./pkg/agentrun/api/... ./pkg/ari/api/...` 通过；覆盖 EventPayload union type discriminator、ContentBlock 序列化 |
| F5 | Context 修复：2 处裸 Background DB 操作加 5s timeout（与既有模式一致） | ~10 行改动 | `go test ./pkg/ari/server/... ./pkg/agentd/...` 通过；`recordPromptDeliveryFailure` 和 `createBundle.GetWorkspace` 使用 `WithTimeout` |
| F6 | 设计文档清理：删除 `agentrun/create` 示例中误导性 `mcpServers` 字段；统一 `env` override 表述为 Future Work | 文档更新 | ari-spec.md 中 `agentrun/create` 示例无 mcpServers；contract-convergence.md 中 env override 标注为 Future Work |
| F7 | 补 Makefile 目标：`test`, `lint`, `coverage` | ~20 行 | `make test` 运行 `go test ./...` 通过；`make lint` 运行 `golangci-lint run` 通过 |

#### P2 — 中期优化（影响工程效率）

| # | 任务 | 预估改动 | 验收标准 |
|---|------|----------|----------|
| F9 | 添加 CI/CD pipeline（需先确认托管平台） | 新文件 | CI 流水线运行 build + lint + test 通过 |
| F10 | 修复 import 分组（3 个文件） | ~10 行 | `make lint` 通过（gci 检查无报错） |
| F11 | 补 CLI 命令测试（8 个无测试子目录） | ~500 行测试 | `go test ./cmd/...` 通过 |
| F12 | 清理 graphify 缓存文件 | 配置 .gitignore | `.graphify_*.json` 在 .gitignore 中 |

#### 设计缺口（记录，本次不修复）

| 缺口 | 说明 | 建议 |
|------|------|------|
| workspace prepare timeout | workspace/create goroutine 无超时上限 | 在 workspace-spec 中新增 `prepareTimeoutSeconds`，默认 300s |
| agent startup timeout | 90s 硬编码于 `waitForSocket`（process.go:611），设计文档有 `startupTimeoutSeconds` 但未实现 | 实现 Agent definition 的 `startupTimeoutSeconds`，替代硬编码 |

---

### 四、风险与取舍

1. **F1 选择以代码为准**：所有调用方已使用 `[]ContentBlock`，无已知外部客户端按 string 实现。若未来出现 string 输入需求，可在 JSON 解码层做兼容（先 try ContentBlock[]，fallback string → 包装为 TextBlock），但当前不需要。
2. **F2 store 层错误改造**：涉及 store/agent/server 三层联动修改，需确保 `errors.Is` 链路完整。已有 `workspace/errors.go` 作为参考模式，风险可控。
3. **F5 不改 prompt delivery timeout**：`workspace/send` 和 `agentrun/prompt` 的 fire-and-forget goroutine 保持阻塞直到 turn 完成。正常 turn 可能持续数分钟，不能加短 timeout。异常由 `watchProcess`（进程退出检测）和 `recordPromptDeliveryFailure`（连接失败处理）覆盖。
4. **F5 不改 workspace prepare / agent start timeout**：已有内部超时机制（waitForSocket 90s、stop 分段超时），固定外层 timeout 不能表达正确语义。超时策略应作为设计缺口单独解决（实现 `startupTimeoutSeconds`、`prepareTimeoutSeconds`）。
5. **工作区既有变更不在本方案范围**：13 个既有脏文件（11 文档 + 2 代码）需用户确认后独立提交，体检修复在干净基线上进行。

## 审查记录

### codex 第1轮

#### ✅ 认可项

- 覆盖面基本完整：构建、设计对齐、测试、代码规范、基础设施、文档一致性都被纳入体检范围，方向正确。
- `workspace/send` 的 wire shape 不一致是有效问题：`docs/design/mass/ari-spec.md` 与 `docs/design/workspace/communication.md` 均写 `message: string`，代码 `pkg/ari/api/types.go` 使用 `[]ContentBlock`，且 CLI/MCP 调用方按 `ContentBlock` 包装文本。该问题确实影响按文档实现的外部客户端。
- 错误处理反模式判断成立：`pkg/ari/server/server.go` 与 `pkg/agentd/agent.go` 依赖 `strings.Contains(err.Error(), ...)` 匹配存储层错误文案，维护性和正确性都较弱。
- Context 传播风险判断成立：`recordPromptDeliveryFailure` 与 `ProcessManager` 内部多处重新创建 `context.Background()`，会绕过调用方取消和 deadline。
- CI、Makefile test/lint/coverage、API wire-shape 测试缺口属于真实工程风险，纳入 P1/P2 合理。

#### ❌ 问题项

1. **P0/F3 "提交 8 个待提交文档变更"事实不准确，且不应作为正确性 P0 修复项。**
   - 问题是什么：当前 `git status --short` 显示 11 个已修改文档文件，另有本方案文档未跟踪，不是 8 个；同时"提交文档"是版本管理动作，不是运行时正确性修复。
   - 为什么是问题：错误的工作区基线会导致后续执行误提交、漏提交或把他人未完成文档混入修复 PR；将其列为 P0 会模糊真正必须立即修复的 wire/API correctness 问题。
   - 期望如何解决：将 F3 从 P0 correctness 中移出，改成独立的"工作区治理/提交边界"步骤；列出精确文件清单、来源、是否属于本方案范围，并明确是否需要用户确认后再提交。

2. **D2/D3 被列为开放决策，但现有设计文档已有当前结论，方案没有先尊重 authoritative docs。**
   - 问题是什么：`docs/design/mass/mass.md` 和 `docs/design/workspace/workspace-spec.md` 已明确 `agentrun/create` 没有 AgentRun-level env override，env 来自 host + Agent definition；`docs/design/mass/ari-spec.md` Future Work 也写明 AgentRun env override 是目标缺口而非当前能力。`mcpServers` 也不是当前 `AgentRunSpec` 字段，runtime config 层通过 `acpAgent.session.mcpServers` 表达，并且 workspace MCP 会自动注入。
   - 为什么是问题：把已经有设计结论的事项重新列成开放决策，会造成方案与 `docs/design` 不一致，违反仓库要求"Code changes must be aligned with docs/design"；后续执行可能擅自扩展协议面。
   - 期望如何解决：D2/D3 应改为"确认是否要变更既有设计"的显式变更请求。若不变更当前设计，应把 F8 收敛为文档清理：删除/修正旧示例中的 `mcpServers`/`env` 暗示，或明确它们属于 runtime config/Future Work，而不是立即加字段。

3. **F1 没有给出推荐决策和影响面，无法执行。**
   - 问题是什么：方案只给出 A/B 两个选项，没有根据 ACP ContentBlock、现有 CLI/MCP 调用方、`agentrun/prompt` 类型、向后兼容成本给出推荐路径。
   - 为什么是问题：P0 修复的核心取舍未收敛，执行者无法判断是改文档、改 API，还是提供兼容解码；直接改任一侧都可能破坏 `workspace_send`、`massctl workspace send`、agentrun prompt 统一的多模态模型。
   - 期望如何解决：补充决策依据并给出默认方案。当前更稳妥的方向倾向于以代码为准，将 ARI 文档中的 `message`/`prompt` 明确为 `ContentBlock[]`，同时评估是否需要临时接受 string 作为兼容输入；若选择改代码为 string，必须列出所有调用方和测试更新清单。

4. **错误处理修复方案缺少底层错误类型设计，`errors.As()` 不能凭空生效。**
   - 问题是什么：当前字符串匹配来自 `pkg/agentd/store` 返回的 `fmt.Errorf("... already exists")` / `fmt.Errorf("... does not exist")`。如果 store 层不暴露 sentinel 或 typed error，仅在上层改 `errors.As()` 无法解决问题。
   - 为什么是问题：修复范围会被低估，且可能只把字符串匹配移动到别处；API 错误码仍会依赖不可验证的文案。
   - 期望如何解决：在方案中明确新增 store 层错误契约，例如 `store.ErrAlreadyExists`、`store.ErrNotFound` 或携带 resource/key 的 typed errors；同步更新 `AgentRunManager`、workspace adapter 和相关 store/ARI 测试，确保 `errors.Is/As` 覆盖重复创建、缺失更新、状态迁移缺失等路径。

5. **Context 修复的验收标准不完整，后台任务生命周期需要单独说明。**
   - 问题是什么：F5 只写"接受 ctx 参数"，但 `agentrun/create` 启动 goroutine 的语义并不等同于请求生命周期；如果直接把请求 ctx 传入后台启动，请求返回后可能取消，导致 agent-run 启动被错误中断。
   - 为什么是问题：机械替换 `context.Background()` 可能引入竞态或启动失败；需要区分同步请求路径、daemon 级后台生命周期、DB 状态更新的 bounded timeout。
   - 期望如何解决：把 F5 拆成两类：请求链路内操作必须传递调用方 ctx；异步 daemon-owned goroutine 使用服务级 root context 或明确的 lifecycle context，并派生 bounded timeout。方案需说明 `recordPromptDeliveryFailure`、process watcher、`buildBundle` workspace lookup、`agentrun/create` 启动 goroutine各自的目标语义和测试方式。

6. **测试计划缺少可验证验收命令和最小通过标准。**
   - 问题是什么：方案列出要补测试，但没有说明每批修复后必须运行哪些命令，也没有声明 `make build`、`go test ./...`、lint/coverage 的通过条件。
   - 为什么是问题：体检方案无法验证完成状态，容易停留在"添加若干测试"而非证明 wire shape、错误码、context 取消等行为正确。
   - 期望如何解决：为 P0/P1/P2 分别加入验收标准。至少包括 `make build`、针对变更包的 `go test`、新增 API JSON round-trip/error mapping/context cancellation 测试；新增 Makefile 后再以 `make test`/`make lint` 作为统一入口。

### claude-code 第2轮

**逐条回应：**

1. **F3 文件数与分类** — 接受。实际 11 个已修改文件，非 8 个。已从 P0 移出，改为独立「工作区状态」章节（§2.6），列出精确文件清单，标注为命名重构同步，不在本修复范围内，建议用户确认后独立提交。

2. **D2/D3 开放决策** — 接受。现有设计文档已有结论。已取消 D2/D3 作为开放决策项，改为 F6 文档清理任务：删除误导性 mcpServers 示例，统一 env override 为 Future Work，尊重 authoritative docs。

3. **F1 推荐方案** — 接受。已补充完整决策分析：列出 6 个调用方的实际行为，给出推荐方案（以代码 `[]ContentBlock` 为准更新文档），附理由（所有调用方已用 ContentBlock、与 ACP 多模态一致、无已知外部 string 客户端）。结论：只改文档，不改代码，无需 string 兼容。

4. **错误处理底层设计** — 接受。已补充完整修复链路：Step 1 新增 `store/errors.go`（sentinel + ResourceError typed error + Unwrap）→ Step 2 替换 store 7 处 fmt.Errorf → Step 3-4 替换 agent.go 3 处 + server.go 2 处 strings.Contains 为 errors.Is → Step 5 补充 3 层测试。参考 workspace/errors.go 模式。

5. **Context 语义分类** — 接受。已将 11 处 `context.Background()` 逐一分类为三类：
   - 4 处已正确（process.go/recovery.go 已有 5s timeout）
   - 5 处需加 bounded timeout（均为 daemon-owned goroutine，保持 Background 但加 WithTimeout）
   - 2 处 fire-and-forget 需验证 client.Prompt 内部超时
   明确说明：不使用请求 ctx（避免请求返回后取消），只在 Background 外加 WithTimeout。

6. **验收标准** — 接受。已为 P0/P1/P2 每项任务补充具体验收命令和通过条件。

### codex 第2轮

#### ✅ 认可项

- 第1轮反馈的大方向已基本吸收：F3 已从 P0 移出，D2/D3 不再作为开放决策，F1 已给出以 `[]ContentBlock` 为准更新文档的推荐方案。
- F1 的推荐路径合理：当前 Go client、CLI、workspace MCP 和 ARI server 都按 `[]ContentBlock` 工作；在"当前无需兼容"的项目约束下，更新 authoritative docs 比回退代码为 `string` 更稳妥。
- F2 已补到底层错误契约：新增 store sentinel + typed `ResourceError` + `Unwrap()`，再由上层用 `errors.Is/As` 映射，是可执行方向。
- 验收标准比第1轮明确很多，已经能指导分批验证。

#### ❌ 问题项

1. **工作区状态仍不准确，提交边界不能按当前方案执行。**
   - 问题是什么：当前 `git status --short` 除 11 个文档文件和本方案文档外，还有 `pkg/agentrun/runtime/acp/runtime.go` 与 `pkg/tui/chat/chat.go` 两个已修改代码文件。方案 §2.6 仍写"11 个已修改文档文件 + 1 个未跟踪文件"，遗漏了代码变更。
   - 为什么是问题：这会直接影响后续修复的安全边界。若执行者按方案认为只有文档脏变更，可能在测试、格式化、提交或回滚时误处理用户/其他 agent 的代码改动。
   - 期望如何解决：更新 §2.6，列出所有当前脏文件，并把两类变更分开：既有文档同步、既有代码改动、本方案文档。明确本体检修复不得覆盖或顺手格式化 `pkg/agentrun/runtime/acp/runtime.go`、`pkg/tui/chat/chat.go`，除非用户确认它们属于本修复范围。

2. **F5 的 timeout 策略仍不严谨，尤其是 prompt delivery 不能套任意 10s timeout。**
   - 问题是什么：方案对 `workspace/send` / `agentrun/prompt` 两处 fire-and-forget goroutine 写"确认 client.Prompt 内部是否有超时；若无，包装 10s timeout"。实际 `runclient.Client.Prompt` 只是把传入 ctx 交给 JSON-RPC `Call`，没有内部超时；而该调用语义会阻塞到 agent 完成当前 turn。正常 agent turn 可能远超 10s。
   - 为什么是问题：给 prompt turn 套 10s timeout 会把正常长任务误判为失败，触发 `recordPromptDeliveryFailure`，还可能把 agent-run 状态错误标为 error/非 idle。这是行为回归，不是单纯防挂死。
   - 期望如何解决：不要把 prompt turn 纳入固定短超时修复。需要二选一并写清楚：A) 保持当前长 turn 语义，依赖 `cancel`/进程 watcher/断连处理，不加 prompt timeout；或 B) 若 ARI 语义真的是"只保证已派发"，改用已有 `SendPrompt`/`CallAsync` 或新增非阻塞 dispatch 机制，并补状态回收测试。无论选哪种，都不能默认 10s。

3. **F5 对 workspace prepare / agent start / restart 的 30s/60s timeout 是拍脑袋常量，缺少设计依据。**
   - 问题是什么：`workspace/create` 的 prepare 可能包含 git clone 和 hook；`agentrun/create`/restart 的 start 已经把 ctx 传入 `ProcessManager.Start`，而启动时长应受 Agent definition 的 `startupTimeoutSeconds` 或 daemon policy 约束。方案直接写 30s/60s，没有说明来源。
   - 为什么是问题：任意短 timeout 会让慢仓库 clone、慢 hook 或慢 agent bootstrap 在正常场景下失败；任意长 timeout 也不能真正表达生命周期策略。
   - 期望如何解决：将这些 timeout 改为"使用既有配置/新增明确 daemon policy"。例如 agent start 使用 Agent definition 的 startup timeout 或 ProcessManager 内部已有等待策略；workspace prepare 若当前无配置，应先作为设计缺口记录，或使用保守 daemon 常量并在文档/测试中说明默认值和覆盖方式。

4. **测试覆盖现状仍有事实错误。**
   - 问题是什么：方案仍写 `cmd/massctl/commands/` 和 `cmd/mass/commands/` "全部/零测试"，但当前仓库已有 `cmd/massctl/commands/compose/config_test.go` 和 `cmd/mass/commands/run/command_test.go`。
   - 为什么是问题：体检报告作为后续计划的输入，应准确描述缺口；"零测试"会误导任务范围和优先级。
   - 期望如何解决：改成"多数 CLI 命令无测试"并列出具体无测试子包/命令，避免把已有测试覆盖抹掉。

### claude-code 第3轮

**逐条回应：**

1. **工作区状态遗漏代码文件** — 接受。已更新 §2.6 为三类：A 类（11 文档）、B 类（2 代码：runtime.go、chat.go）、C 类（1 方案文档）。明确提交边界规则：本修复不得覆盖/格式化 B 类文件。

2. **Prompt delivery timeout** — 接受。经调查：`runclient.Client.Prompt()` 无内部超时，阻塞直到 turn 完成（可能数分钟）。已明确选择方案 A：保持长 turn 语义，不加固定短 timeout。异常保护由 `watchProcess`（进程退出）和 `recordPromptDeliveryFailure`（连接失败）覆盖。Cancel 通过 `session/cancel` RPC 实现。已有 `SendPrompt()`/`CallAsync()` 非阻塞替代但 ARI server 有意使用阻塞模式。

3. **workspace prepare / agent start timeout** — 接受。经调查：`waitForSocket` 已有 90s 硬编码超时，`Stop` 有分段超时（5s RPC + 10s graceful + 5s kill）。这些不是裸 Background 的问题——它们已经有内部超时策略。不应在外层再套固定 timeout。已改为记录设计缺口：实现 `startupTimeoutSeconds`（替代 90s 硬编码）和 `prepareTimeoutSeconds`。F5 范围收窄为仅 2 处纯 DB 操作加 5s timeout。

4. **CLI 测试事实错误** — 接受。已改为精确列出 10 个子目录的测试状态表：2 个有测试（compose、run），8 个无测试。措辞改为"多数 CLI 命令无测试"。

### codex 第3轮

#### ✅ 认可项

- F1 已可执行：以当前代码 `[]ContentBlock` 为准修正文档，符合 ACP 多模态模型和现有调用方行为。
- F2 已可执行：store 层 sentinel/typed error + 上层 `errors.Is/As` + 三层测试的修复链路完整。
- F5 已收敛到低风险范围：不再给 prompt turn、workspace prepare、agent start/restart 添加拍脑袋 timeout，仅对纯 DB/store 后台操作加 5s bounded timeout，并把 startup/prepare timeout 作为设计缺口记录。
- CLI 测试现状已修正为“多数无测试”，避免了第2轮的事实错误。

#### ❌ 问题项

1. **RISK — 工作区状态仍可能漂移。**
   - 问题是什么：第3轮方案列出 A 类 11 个文档、B 类 2 个代码、C 类方案文档；但当前 `git status --short` 还显示 `pkg/tui/chat/CLAUDE.md` 已修改，方案未列入。
   - 为什么是问题：执行期间工作区持续变化，若不先冻结/确认边界，仍可能误把既有改动混入体检修复。
   - 最稳妥处理意见：批准执行，但执行前必须重新运行 `git status --short`，把所有当时存在的脏文件视为“既有变更保护集”。除非用户明确确认属于本修复范围，否则不得覆盖、格式化、提交这些既有变更；体检修复应在干净基线、stash 后基线或独立 worktree/分支中进行。

2. **RISK — F5 分类表有轻微数量表述错误。**
   - 问题是什么：B 类标题写“需加 bounded timeout（3 处）”，表格实际只有 2 处。
   - 为什么是问题：不影响技术方向，但可能让执行者寻找不存在的第 3 处。
   - 最稳妥处理意见：执行时按表格和 P1/F5 任务为准，只修改 `recordPromptDeliveryFailure` 与 `createBundle` 中 `GetWorkspace` 的纯 DB/store 操作；不要额外扩大 context 改动范围。

#### 结论

第3轮剩余问题均可通过执行边界控制规避，不再阻塞方案。批准执行。

## 最终方案

经 3 轮审查通过。以下为最终可执行方案。

### 执行前置条件

1. 运行 `git status --short`，把所有当时存在的脏文件记录为**既有变更保护集**
2. 不得覆盖、格式化、提交保护集中的任何文件，除非用户明确确认
3. 体检修复应在干净基线上进行（stash 或独立分支/worktree）

### P0 — 必须立即修复

**F1：更新设计文档 ContentBlock 类型**

步骤：
1. 更新 `docs/design/mass/ari-spec.md`：`workspace/send` 的 message 字段和 `agentrun/prompt` 的 prompt 字段类型改为 `ContentBlock[]`，附 ContentBlock schema 说明
2. 更新 `docs/design/workspace/communication.md`：同步 message 字段类型为 `ContentBlock[]`
3. 不改代码

验收：文档中 workspace/send message 和 agentrun/prompt prompt 字段标注为 `ContentBlock[]`；`make build` 通过。

**F2：Store 层 typed errors + 消除 strings.Contains**

步骤：
1. 新增 `pkg/agentd/store/errors.go`：定义 `ErrAlreadyExists`、`ErrNotFound` sentinel + `ResourceError` typed error（含 Op/Resource/Key/Err 字段 + Unwrap()）
2. 替换 `pkg/agentd/store/workspace.go` 3 处 `fmt.Errorf` 为 `&ResourceError{...}`
3. 替换 `pkg/agentd/store/agentrun.go` 4 处 `fmt.Errorf` 为 `&ResourceError{...}`
4. 替换 `pkg/agentd/agent.go` 3 处 `strings.Contains` 为 `errors.Is(err, store.ErrAlreadyExists)` / `errors.Is(err, store.ErrNotFound)`
5. 替换 `pkg/ari/server/server.go` 2 处 `strings.Contains` 为 `errors.Is`
6. 新增 `pkg/agentd/store/errors_test.go`：验证 `errors.Is(resourceErr, ErrAlreadyExists)` 等
7. 更新 `pkg/agentd/agent_test.go`：覆盖重复创建、缺失更新、状态转换缺失路径
8. 更新 `pkg/ari/server/server_test.go`：验证 JSON-RPC 错误码映射

验收：`go test ./pkg/agentd/store/... ./pkg/agentd/... ./pkg/ari/server/...` 全部通过；`grep -r 'strings.Contains(err.Error()' pkg/` 在非 third_party Go 文件中结果为 0。

### P1 — 短期修复

**F4：补 API 包 JSON 往返测试**

步骤：
1. 新增 `pkg/agentrun/api/` 测试：EventPayload union type discriminator、ContentBlock 序列化往返
2. 新增 `pkg/ari/api/` 测试：领域模型 JSON 序列化往返

验收：`go test ./pkg/agentrun/api/... ./pkg/ari/api/...` 通过。

**F5：Context 修复（仅 2 处纯 DB 操作）**

步骤：
1. `pkg/ari/server/server.go` ~743 `recordPromptDeliveryFailure`：将 `ctx := context.Background()` 改为 `ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)` + `defer cancel()`
2. `pkg/agentd/process.go` ~471 `createBundle` 中 `GetWorkspace`：同上，加 5s timeout

不修改范围（明确排除）：
- workspace/send 和 agentrun/prompt 的 fire-and-forget prompt goroutine — 保持长 turn 阻塞语义
- workspace/create、agentrun/create、agentrun/restart goroutine — 已有内部超时策略（waitForSocket 90s、stop 分段超时）

验收：`go test ./pkg/ari/server/... ./pkg/agentd/...` 通过；`recordPromptDeliveryFailure` 和 `createBundle.GetWorkspace` 使用 `WithTimeout`。

**F6：设计文档清理**

步骤：
1. 删除 `docs/design/mass/ari-spec.md` 中 `agentrun/create` 示例里的 `mcpServers` 字段（或标注为 Future Work）
2. 统一 `docs/design/contract-convergence.md` 中 env override 表述为 Future Work

验收：ari-spec.md 中 agentrun/create 示例无 mcpServers；contract-convergence.md 中 env override 标注为 Future Work。

**F7：补 Makefile 目标**

步骤：
1. 添加 `test` 目标：运行 `go test ./...`
2. 添加 `lint` 目标：运行 `golangci-lint run`
3. 添加 `coverage` 目标：运行 `go test -coverprofile=coverage.out ./...`

验收：`make test` 通过；`make lint` 通过。

### P2 — 中期优化

**F9：CI/CD（需先确认托管平台）**
**F10：import 分组修复（3 文件）**
**F11：CLI 命令测试（8 个无测试子目录）**
**F12：清理 graphify 缓存**

验收：`make build && make test && make lint` 全部通过。

### 设计缺口（记录，本次不修复）

| 缺口 | 说明 |
|------|------|
| workspace prepare timeout | workspace/create goroutine 无超时上限，建议新增 `prepareTimeoutSeconds`，默认 300s |
| agent startup timeout | 90s 硬编码于 `waitForSocket`（process.go:611），建议实现 Agent definition 的 `startupTimeoutSeconds` |
