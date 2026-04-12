# RPC 协议风格一致性对齐方案

## 背景与目标

当前 OAR 的 RPC 协议存在命名风格不一致的问题。用户明确指出 `turn_start`（snake_case）和 `stateChange`（camelCase）混用了两种风格。本方案以 ACP 协议为标准，全面审查并统一所有 RPC 协议的命名风格。

### ACP 协议命名规范（对齐标准）

| 类别 | 约定 | 示例 |
|------|------|------|
| JSON 字段名 | camelCase | `sessionId`, `toolCallId`, `stopReason` |
| RPC 方法名 | `namespace/snake_case` | `session/new`, `fs/read_text_file`, `session/set_config_option` |
| 事件/类型判别字符串 | snake_case | `tool_call`, `end_turn`, `user_message_chunk` |
| 枚举字符串值 | snake_case | `end_turn`, `in_progress`, `allow_once`, `allow_always` |

## 发现的不一致项

### Issue 1: Notification 方法名 — `runtime/stateChange`（camelCase）

**现状**：`MethodRuntimeStateChange = "runtime/stateChange"`
**ACP 标准**：多词方法段使用 snake_case（`read_text_file`, `set_config_option`, `wait_for_exit`）
**修正**：`runtime/stateChange` → `runtime/state_change`

影响文件（代码）：

| 文件 | 改动内容 |
|------|---------|
| `pkg/events/envelope.go` | 常量 `MethodRuntimeStateChange` 值改为 `"runtime/state_change"` |
| `pkg/agentd/process.go` | notification handler 匹配字符串 |
| `pkg/agentd/recovery.go` | notification handler 匹配字符串 |
| `pkg/rpc/server.go` | notification 发送处使用常量（确认已使用常量） |
| `pkg/agentd/shim_client.go` | 如有字符串匹配 |

影响文件（文档/测试）：

| 文件 | 改动内容 |
|------|---------|
| `docs/design/runtime/shim-rpc-spec.md` | 所有 `runtime/stateChange` → `runtime/state_change` |
| `docs/design/agentd/ari-spec.md` | 引用处 |
| `pkg/rpc/server_test.go` | 测试中的字符串匹配 |
| `pkg/agentd/process_test.go` | 如有 |
| `pkg/agentd/recovery_test.go` | 如有 |
| `pkg/agentd/shim_boundary_test.go` | 如有 |
| `pkg/events/translator_test.go` | 如有 |

### Issue 2: RestartPolicy 枚举值 — camelCase

**现状**：`RestartPolicyTryReload = "tryReload"`, `RestartPolicyAlwaysNew = "alwaysNew"`
**ACP 标准**：枚举字符串值使用 snake_case
**修正**：`"tryReload"` → `"try_reload"`, `"alwaysNew"` → `"always_new"`

影响文件：

| 文件 | 改动内容 |
|------|---------|
| `api/meta/types.go` | 常量值和注释 |
| `pkg/agentd/recovery.go` | 字符串比较（应已使用常量） |
| `pkg/agentd/recovery_test.go` | 测试中引用 |
| `docs/design/runtime/shim-rpc-spec.md` | 引用处 |
| `docs/design/runtime/agent-shim.md` | 引用处 |

### Issue 3: PermissionPolicy 枚举值 — kebab-case

**现状**：`"approve-all"`, `"approve-reads"`, `"deny-all"`（kebab-case）
**ACP 标准**：枚举字符串值使用 snake_case
**修正**：`"approve-all"` → `"approve_all"`, `"approve-reads"` → `"approve_reads"`, `"deny-all"` → `"deny_all"`

影响文件：

| 文件 | 改动内容 |
|------|---------|
| `api/spec/types.go` | 常量值 |
| `pkg/runtime/client.go` | 错误消息中的引用 |
| `pkg/spec/config.go` | 校验逻辑和错误消息 |
| `cmd/agentd/subcommands/shim/command.go` | flag 默认值和帮助文本 |
| `pkg/spec/config_test.go` | 测试 |
| `pkg/runtime/client_test.go` | 测试断言 |
| `pkg/agentd/process.go` | 硬编码的 `"approve-all"` |
| `pkg/rpc/server_test.go` | 测试 |
| `pkg/runtime/runtime_test.go` | 测试 |
| `docs/design/runtime/runtime-spec.md` | 规范文档 |
| `docs/design/runtime/config-spec.md` | 规范文档 |
| `pkg/spec/testdata/bundles/minimal/config.json` | 测试数据 |

### Issue 4: Go 字段命名 — `TurnId` 应为 `TurnID`

**现状**：`TurnId string` in `SessionUpdateParams`
**Go 惯例**：缩写词全大写（`SessionID`, `ShimPID`, `URL`）
**修正**：Go 字段 `TurnId` → `TurnID`，JSON tag `"turnId"` 不变

影响文件：

| 文件 | 改动内容 |
|------|---------|
| `pkg/events/envelope.go` | 字段名 |
| `pkg/events/translator.go` | 所有 `.TurnId` 引用 |
| `pkg/events/translator_test.go` | 所有 `.TurnId` 引用 |
| `pkg/rpc/server_test.go` | `.TurnId` 引用 |
| `pkg/agentd/shim_boundary_test.go` | `.TurnId` 引用 |

### Issue 5: ARI 规范文档术语过时

**现状**：规范文档仍使用 M008 之前的术语
**代码实际**：M008 已完成 agent=template / agentrun=instance 重命名

| 规范术语 | 代码实际 | 修正方向 |
|---------|---------|---------|
| `AgentTemplateInfo` | `AgentInfo` | 规范对齐代码 |
| `agentTemplate` (result key) | `agent` | 规范对齐代码 |
| `agentTemplates` (result key) | `agents` | 规范对齐代码 |
| agentrun/status result key `"agent"` | `"agentRun"` | 规范对齐代码 |
| agentrun/list result key `"agents"` | `"agentRuns"` | 规范对齐代码 |

## 确认已一致的部分（无需改动）

| 类别 | 现状 | 符合 ACP |
|------|------|---------|
| JSON 字段名 | 全部 camelCase（`sessionId`, `stopReason`, `socketPath`...） | ✅ |
| 事件类型判别字符串 | 全部 snake_case（`turn_start`, `tool_call`, `file_write`...） | ✅ |
| Status 枚举值 | 全部单词（`creating`, `idle`, `running`, `stopped`, `error`） | ✅ |
| WorkspacePhase 枚举值 | 全部单词（`pending`, `ready`, `error`） | ✅ |
| ARI 方法名 | 全部单词段（`workspace/create`, `agentrun/status`...） | ✅ |
| Shim 方法名 | 全部单词段（`session/prompt`, `runtime/status`...） | ✅ |

## 执行步骤

### Step 1: 重命名 `runtime/stateChange` → `runtime/state_change`

**代码改动**：

1. `pkg/events/envelope.go`：修改 `MethodRuntimeStateChange` 常量值为 `"runtime/state_change"`
2. 以下文件中的 `"runtime/stateChange"` 字面量全部替换：
   - `pkg/agentd/shim_client.go`：注释（4处）
   - `pkg/agentd/shim_client.go:260`：错误消息 `"parse runtime/stateChange"`
   - `pkg/agentd/process.go`：注释（3处）
   - `pkg/agentd/recovery.go`：注释（1处）
   - `pkg/events/translator.go`：注释（1处）
   - `pkg/events/envelope.go`：注释（1处）
   - `cmd/agentdctl/subcommands/shim/chat.go:142`：method 匹配 `"runtime/stateChange"`
   - `cmd/agentdctl/subcommands/shim/command.go:184`：case 分支 `"runtime/stateChange"`
   - `cmd/agentdctl/subcommands/shim/command.go:187`：错误消息
   - `cmd/agentdctl/subcommands/shim/CLAUDE.md`：文档引用（2处）
3. 以下测试文件中的字面量全部替换：
   - `pkg/agentd/shim_client_test.go`：queueNotification 和 assertion（6处）
   - `pkg/agentd/shim_boundary_test.go`：注释和 queueNotification（8处）
   - `pkg/agentd/process_test.go`：注释（1处）
   - `pkg/events/translator_test.go`：注释（1处）
   - `pkg/rpc/server_test.go`：如有引用

**文档改动**：

4. `docs/design/runtime/shim-rpc-spec.md`：所有 `runtime/stateChange` → `runtime/state_change`（约12处）
5. `docs/design/agentd/ari-spec.md`：引用处（1处）
6. `docs/design/README.md`：引用处（1处）
7. `docs/design/roadmap.md`：引用处（1处）
8. `docs/design/contract-convergence.md`：引用处（1处）
9. `docs/design/runtime/agent-shim.md`：引用处（2处）

**验收条件**：`rg "runtime/stateChange" pkg api cmd docs/design` 无命中（`.gsd/` 和 `docs/plan/` 中的历史记录不在收敛范围内）。

### Step 2: 重命名 RestartPolicy 枚举值 + 修正公开入口 + 增加校验

**问题背景**：当前存在两套 RestartPolicy 值的混乱：
- ARI 注释/CLI help/设计文档宣称 `"never"`, `"on-failure"`, `"always"`
- 实际恢复逻辑 (`api/meta/types.go`) 只识别 `"tryReload"`，空值按 always-new
- 无边界校验，无效值（包括 `"on-failure"`）静默落入 always-new 行为

**修正方案**：统一为 `"try_reload"` / `"always_new"`（ACP snake_case），空值等价 `"always_new"`。

**代码改动**：

1. `api/meta/types.go`：常量值改为 `"try_reload"` / `"always_new"`，更新注释
2. `api/ari/types.go:143`：注释从 `("never", "on-failure", "always")` 改为 `("try_reload", "always_new")`
3. `pkg/agentd/recovery.go`：已使用常量比较，无需改代码逻辑
4. 在 `agentrun/create` handler 处（`pkg/ari/server.go` 或调用链上）增加 RestartPolicy 校验：
   - 接受：空字符串、`"try_reload"`、`"always_new"`
   - 其他值：返回 `-32602 InvalidParams` 错误
5. `cmd/agentdctl/subcommands/agentrun/command.go:71`：flag help 改为 `"Restart policy: try_reload, always_new (default: always_new)"`
6. `cmd/agentdctl/subcommands/up/config_test.go:28,47`：`"on-failure"` → `"try_reload"`
7. `pkg/shimapi/types.go`：注释中的 `tryReload` 更新

**测试改动**：

8. 新增 `agentrun/create` RestartPolicy 校验测试（在 `pkg/ari/server_test.go` 或相近位置）：
   - 测试非法值（如 `"on-failure"`、`"bad-value"`）返回 `-32602 InvalidParams` 错误
   - 测试合法非空值 `"try_reload"` 正常通过
   - 测试空值正常通过（等价 `always_new` 默认行为）

**文档改动**：

9. `docs/design/agentd/ari-spec.md:223`：restartPolicy 参数说明改为 `"try_reload"` | `"always_new"`
10. `docs/design/runtime/agent-shim.md`：`tryReload` 引用更新
11. `docs/design/roadmap.md:55`：`tryReload` 引用更新

**验收条件**：`rg "tryReload|alwaysNew|on-failure|\"never\".*\"always\"" api pkg cmd docs/design` 无未解释命中。

### Step 3: 重命名 PermissionPolicy 枚举值

**代码改动**：

1. `api/spec/types.go`：`"approve-all"` → `"approve_all"`, `"approve-reads"` → `"approve_reads"`, `"deny-all"` → `"deny_all"`
2. `pkg/runtime/client.go`：错误消息中的 `deny-all`、`approve-reads` 引用
3. `pkg/spec/config.go`：校验错误消息中的值
4. `cmd/agentd/subcommands/shim/command.go:49`：flag 默认值和 help 文本
5. `pkg/agentd/process.go:403,528`：硬编码的 `"approve-all"` → `"approve_all"`（代码应改为使用 `apispec.ApproveAll` 常量）
6. `pkg/spec/testdata/bundles/minimal/config.json`：`"approve-all"` → `"approve_all"`
7. `cmd/agentd/subcommands/shim/command.go:79-81`：CLI `--permissions` 覆盖后增加校验。在 `cfg.Permissions = apispec.PermissionPolicy(permissions)` 之后调用 `cfg.Permissions.IsValid()`，无效时返回明确错误，错误消息列出 `approve_all`、`approve_reads`、`deny_all`
8. `pkg/runtime/client.go:46`：`default` 分支改为显式匹配 `case apispec.ApproveAll:`，新增 `default:` 分支返回错误（防御性编程，防止未知值静默放宽权限）

**测试改动**：

9. `pkg/spec/config_test.go`：引用更新
10. `pkg/runtime/client_test.go`：断言中的 `"deny-all"`、`"approve-reads"` 字符串更新
11. `pkg/rpc/server_test.go`：引用更新
12. `pkg/runtime/runtime_test.go`：引用更新
13. 新增 CLI `--permissions` 非法值测试：验证 `agentd shim --permissions bad-value`（或等价 `run(...)` 路径调用）返回明确错误，不会静默按 approve_all 运行

**文档改动**：

11. `docs/design/runtime/runtime-spec.md:355-357`：表格值
12. `docs/design/runtime/config-spec.md:245,251-253,261,306`：所有引用

**`docs/research/**` 处理说明**：`docs/research/acp/acp-protocol.md` 和 `docs/research/acpx.md` 中的 `approve-all` 是对上游 ACP CLI 和 OAR 现有行为的研究记录/引用，不属于当前 OAR 协议 surface 定义，保留原样不修改。

**验收条件**：`rg "approve-all|approve-reads|deny-all" api pkg cmd docs/design` 无命中。

### Step 4: Go 字段 `TurnId` → `TurnID`

1. `pkg/events/envelope.go:72`：字段 `TurnId` → `TurnID`，JSON tag `"turnId"` 保持不变
2. `pkg/events/translator.go`：所有 `.TurnId` → `.TurnID`（约7处）
3. `pkg/events/translator_test.go`：所有 `.TurnId` → `.TurnID`（约20处）
4. `pkg/rpc/server_test.go`：`.TurnId` → `.TurnID`（约2处）
5. `pkg/agentd/shim_boundary_test.go`：`.TurnId` → `.TurnID`（1处）

**验收条件**：`rg "\.TurnId\b" pkg` 无命中（排除 JSON tag）。

### Step 5: 更新 ARI 规范文档（对齐代码实际 wire format）

逐段核对 `api/ari/types.go` 的 JSON tag，更新 `docs/design/agentd/ari-spec.md`：

**术语替换**：

1. `AgentTemplateInfo` → `AgentInfo`（全文替换）
2. `Agent definition Schema` 标题下的表格：对齐 `AgentInfo` 字段

**result key 修正（以代码 JSON tag 为准）**：

3. `agent/set` Result：`AgentTemplateInfo` → `AgentInfo`
4. `agent/get` Result：`{agentTemplate: AgentTemplateInfo}` → `{agent: AgentInfo}`（对应代码 `json:"agent"`）
5. `agent/list` Result：`{agentTemplates: AgentTemplateInfo[]}` → `{agents: AgentInfo[]}`（对应代码 `json:"agents"`）
6. `agentrun/status` Result：示例中 `"agent"` key → `"agentRun"`（对应代码 `AgentRunStatusResult.AgentRun json:"agentRun"`）
7. `agentrun/status` 下方文本 "shimState" 段的示例 JSON 更新
8. `agentrun/list` Result：`{agents[]}` → `{agentRuns[]}`（对应代码 `AgentRunListResult.AgentRuns json:"agentRuns"`）

**其他文档**：

9. `docs/design/agentd/agentd.md`：`AgentTemplates` 术语更新

**验收条件**：`rg "AgentTemplateInfo|agentTemplate[^s]|agentTemplates" docs/design` 无命中。

### Step 6: 统一验收

执行以下检查，全部通过即为完成：

```bash
# 编译
make build

# 全量测试
go test ./...

# 全局残留检查（api pkg cmd docs/design 范围内无未解释命中）
rg "runtime/stateChange" pkg api cmd docs/design           # 预期：0 命中
rg "tryReload|alwaysNew" api pkg cmd docs/design            # 预期：0 命中
rg "approve-all|approve-reads|deny-all" api pkg cmd docs/design  # 预期：0 命中
rg "AgentTemplateInfo|\"agentTemplate\"|\"agentTemplates\"" docs/design  # 预期：0 命中
rg "\.TurnId\b" pkg                                         # 预期：0 命中
```

**排除范围**：`.gsd/`、`docs/plan/`、`docs/research/` 中的历史记录、计划文档、上游协议研究不在收敛范围内。

## 风险与取舍

1. **无线上兼容性问题**：CLAUDE.md 明确说 "No need to consider compatibility Now"，所以直接改即可
2. **blast radius 可控**：每个 Issue 影响范围明确，通过 `rg` 收敛条件可以确保不遗漏
3. **Go 的 `TurnId` → `TurnID` 重命名**是纯编译期可验证的，不影响 wire format（JSON tag 不变）
4. **PermissionPolicy 改动最广**：涉及 CLI flag 默认值、测试数据文件、多个 error message，需要仔细逐文件修改
5. **RestartPolicy 校验是新增行为**：当前无校验，无效值静默 fallback。增加校验是更稳妥的做法，在 `agentrun/create` handler 中拒绝未知值，防止用户误配置
6. **`docs/research/**` 保留原样**：研究文档记录的是上游 ACP 协议或历史行为调研，不属于 OAR 现行协议 surface，不在本次对齐范围内

## 审查记录

### codex 第1轮

#### ✅ 认可项

1. **对齐目标方向正确**：将 RPC 方法名、多词枚举值统一到 snake_case，符合文档中列出的 ACP 风格规则；`TurnId` → `TurnID` 也符合 Go 缩写词惯例且 JSON tag 不变，不改变 wire format。
2. **核心代码入口已识别**：`pkg/events/envelope.go`、`api/meta/types.go`、`api/spec/types.go`、`pkg/spec/config.go`、`pkg/runtime/client.go`、`cmd/agentd/subcommands/shim/command.go` 等主要协议/配置边界都在方案范围内。
3. **ARI 术语过时问题判断成立**：`docs/design/agentd/ari-spec.md` 中 `AgentTemplateInfo` / `agentTemplate(s)` 与当前 `api/ari/types.go` 的 `AgentInfo` / `agent(s)` 不一致，修正文档对齐代码是合理方向。

#### ❌ 问题项

1. **`runtime/stateChange` 的替换范围不完整，会导致规范与测试仍保留旧 wire method**
   - 问题是什么：方案只点名了部分文档和测试，但全局搜索显示仍有未纳入范围的旧方法名，例如 `pkg/agentd/shim_client_test.go` 中的 queued notification/assertion/error 断言，以及 `docs/design/README.md`、`docs/design/roadmap.md`、`docs/design/contract-convergence.md`、`docs/design/runtime/agent-shim.md` 等设计文档。
   - 为什么是问题：本方案目标是 RPC 协议风格一致性。如果测试 fixture 仍发送 `runtime/stateChange`，`ShimClient` 的 handler 改为只接受 `runtime/state_change` 后相关测试会失败；如果设计文档仍写旧方法名，则违反仓库要求“Code changes must be aligned with docs/design”。
   - 期望如何解决：Step 1 必须改为以 `rg "runtime/stateChange"` 为收敛条件，明确包括 `pkg/agentd/shim_client_test.go`、所有 `docs/design/**` 命中项，以及相关注释/错误消息。验收条件应写明：除历史计划文档或明确标注为旧协议的研究材料外，`rg "runtime/stateChange" pkg api cmd docs/design` 无命中。

2. **RestartPolicy 公开入口未对齐，方案漏掉 ARI/CLI 仍宣称另一套枚举**
   - 问题是什么：`api/ari/types.go` 对 `AgentRunCreateParams.RestartPolicy` 的注释仍写 `"never", "on-failure", "always"`；`cmd/agentdctl/subcommands/agentrun/command.go` 的 `--restart-policy` help 也写同一套值；`docs/design/agentd/ari-spec.md` 的 `agentrun/create.restartPolicy` 表格也是同一套值。实际恢复逻辑只识别 `meta.RestartPolicyTryReload`，空值按 always-new 行为处理。
   - 为什么是问题：如果只改 `api/meta` 常量为 `try_reload` / `always_new`，公开 ARI、CLI 和设计规范仍会诱导调用方传入无效值；当前代码又没有边界校验，无效值会静默落入 always-new 行为，导致用户以为配置生效但实际没有 warm resume。
   - 期望如何解决：Step 2 必须覆盖 `api/ari/types.go` 注释、`cmd/agentdctl/subcommands/agentrun/command.go` help、`docs/design/agentd/ari-spec.md` 参数说明、`docs/design/roadmap.md`、`docs/design/runtime/agent-shim.md`、`pkg/shimapi/types.go` 和相关测试注释。方案还需要明确是否在 `agentrun/create` / `agentdctl up` 配置解析处校验 `restartPolicy`：若不加校验，必须给出理由；更稳妥的处理是接受空值、`try_reload`、`always_new`，其他值返回明确错误。

3. **Step 3 的 PermissionPolicy 验收条件不足，容易漏掉 CLI 和设计文档中的旧 kebab-case**
   - 问题是什么：方案列出了主要文件，但没有把 `cmd/agentd/subcommands/shim/command.go` 的 flag 默认值/help、`pkg/runtime/client_test.go` 的错误断言、`docs/design/runtime/config-spec.md` / `runtime-spec.md` 示例表格等作为全局收敛条件；同时 `docs/research/acp*.md` 中也有 `approve-all` 字样，需区分哪些是 OAR 现行协议、哪些只是上游/历史研究记录。
   - 为什么是问题：权限策略是用户可见配置和 CLI 参数。遗漏任一入口都会造成“代码接受 snake_case，但文档或命令行帮助仍指导用户使用 kebab-case”的不一致。
   - 期望如何解决：Step 3 增加明确验收：`rg "approve-all|approve-reads|deny-all" api pkg cmd docs/design` 无命中；`docs/research/**` 若保留上游 ACP CLI 原文或历史记录，需要在方案中说明不属于当前 OAR 协议 surface。

4. **ARI 文档修订只列了术语替换，未覆盖具体响应示例和 `agentrun/list/status` 结果键**
   - 问题是什么：`docs/design/agentd/ari-spec.md` 中 `agentrun/status` 示例仍使用 `"agent"` 作为 result key，`agentrun/list` 文本仍写 `{agents[]}`；当前代码是 `AgentRunStatusResult{AgentRun ... json:"agentRun"}` 和 `AgentRunListResult{AgentRuns ... json:"agentRuns"}`。
   - 为什么是问题：这不是命名风格偏好，而是 wire contract 文档错误。若只做表层术语替换，示例仍会与 `api/ari/types.go` / `pkg/ari/server.go` 不一致。
   - 期望如何解决：Step 5 明确要求逐段更新 `agent/set/get/list`、`agentrun/list/status` 的 Result 行、JSON 示例、schema 描述，并以 `api/ari/types.go` 的 JSON tag 为准逐项核对。

5. **验证步骤过于粗略，没有为“全局无旧协议名残留”设定机械检查**
   - 问题是什么：每步都写了运行测试，但没有列出必须执行的 `rg` 检查，也没有指定设计文档一致性的检查口径。
   - 为什么是问题：这些改动大量涉及字符串常量、注释、文档和测试 fixture，`go test` 只能覆盖编译和部分行为，不能保证规范文本与公开帮助文本已收敛。
   - 期望如何解决：最终方案增加统一验收清单：`make build`、`go test ./...`，以及针对旧值的 `rg` 检查：`runtime/stateChange`、`tryReload|alwaysNew`、`approve-all|approve-reads|deny-all`、`AgentTemplateInfo|agentTemplate|agentTemplates` 在 `api pkg cmd docs/design` 范围内无未解释命中。

### claude-code 第2轮

逐条回应 codex 第1轮反馈：

**反馈1（`runtime/stateChange` 替换范围不完整）**：接受。Step 1 已扩展为完整的文件清单，新增覆盖：
- `pkg/agentd/shim_client_test.go`（6处）
- `pkg/agentd/shim_boundary_test.go`（8处）
- `cmd/agentdctl/subcommands/shim/chat.go`、`command.go`、`CLAUDE.md`
- `docs/design/README.md`、`roadmap.md`、`contract-convergence.md`、`agent-shim.md`
- 增加明确验收条件：`rg "runtime/stateChange" pkg api cmd docs/design` 无命中

**反馈2（RestartPolicy 公开入口未对齐）**：接受。Step 2 重写为完整方案：
- 识别到当前存在两套值的混乱：ARI/CLI 宣称 `never/on-failure/always`，实际只识别 `tryReload`
- 统一为 `try_reload`/`always_new`（ACP snake_case），覆盖 `api/ari/types.go` 注释、CLI help、设计文档
- 新增校验：在 `agentrun/create` handler 中拒绝未知值（接受空值、`try_reload`、`always_new`）
- 修正 `cmd/agentdctl/subcommands/up/config_test.go` 中的 `"on-failure"` 旧值

**反馈3（PermissionPolicy 验收条件不足）**：接受。Step 3 已补充：
- 完整列出 CLI flag（`cmd/agentd/subcommands/shim/command.go`）、测试断言（`client_test.go`）、设计文档（`config-spec.md`、`runtime-spec.md`）
- 明确说明 `docs/research/**` 中的 `approve-all` 是上游 ACP CLI 研究记录，不属于 OAR 协议 surface，保留原样
- 增加验收条件：`rg "approve-all|approve-reads|deny-all" api pkg cmd docs/design` 无命中

**反馈4（ARI 文档修订只做术语替换不够）**：接受。Step 5 重写为逐段核对方案：
- 以 `api/ari/types.go` 的 JSON tag 为准
- 明确列出 `agentrun/status` result key `"agent"` → `"agentRun"`、`agentrun/list` `"agents"` → `"agentRuns"` 等具体修正项
- 要求更新所有 JSON 示例

**反馈5（验证步骤缺少全局残留检查）**：接受。新增 Step 6 统一验收清单：
- `make build` + `go test ./...`
- 5 条 `rg` 残留检查命令，覆盖所有旧值
- 明确排除范围：`.gsd/`、`docs/plan/`、`docs/research/`

### codex 第2轮

#### ✅ 认可项

1. **第1轮指出的全局替换范围已基本闭环**：Step 1/3/5 已补充 `cmd/**`、`docs/design/**`、测试 fixture、CLI help、错误消息和 JSON 示例，并新增 `rg` 收敛条件，足以防止旧 wire 名称和旧枚举值在现行协议 surface 中残留。
2. **RestartPolicy 的方案方向正确**：识别并消除了 `never/on-failure/always` 与实际 `tryReload`/默认 always-new 的双轨混乱；新增 `agentrun/create` 边界校验是必要的，否则无效值会静默降级。
3. **ARI 文档对齐口径更严谨**：Step 5 明确以 `api/ari/types.go` JSON tag 为准，覆盖 `agent`/`agents` 以及 `agentRun`/`agentRuns` 的 result key 和示例，解决了 wire contract 文档错误。
4. **排除范围说明合理**：`docs/research/**` 保留上游协议/历史研究记录原文是可接受的，前提是 `docs/design/**`、`api/**`、`pkg/**`、`cmd/**` 全部收敛。

#### ❌ 问题项

1. **`agentd shim --permissions` 覆盖值绕过 `ValidateConfig`，Step 3 需要补充校验**
   - 问题是什么：`cmd/agentd/subcommands/shim/command.go` 当前流程是先 `spec.ParseConfig` + `spec.ValidateConfig(cfg)`，然后在 `cmd.Flag("permissions").Changed` 时执行 `cfg.Permissions = apispec.PermissionPolicy(permissions)`。也就是说 CLI 覆盖值没有再次校验。`pkg/runtime/client.go` 对未知值又走 `default` 分支，等价 approve-all。
   - 为什么是问题：本次把公开权限枚举从 kebab-case 改成 snake_case 后，用户传旧值 `--permissions approve-all` 或任意拼写错误都可能绕过校验进入 runtime，并被当作 approve-all 处理。这会让错误配置静默放宽权限，是比命名不一致更严重的行为风险。
   - 期望如何解决：Step 3 增加一项代码改动：CLI 覆盖 `cfg.Permissions` 后必须重新校验该字段，或者直接在覆盖点调用 `cfg.Permissions.IsValid()`，无效时返回明确错误，错误消息列出 `approve_all`、`approve_reads`、`deny_all`。同时补一个针对 `agentd shim --permissions bad-value` 或等价 `run(...)` 路径的测试，确保不会静默按 approve-all 运行。

2. **RestartPolicy 新增校验需要明确测试覆盖**
   - 问题是什么：Step 2 提到在 `agentrun/create` handler 增加校验，但没有明确要求新增 invalid-value 测试。
   - 为什么是问题：这是新增行为，且修复的是之前“无效值静默 fallback”的边界问题。没有测试时容易只改注释/help 或只改 happy path，漏掉真正的拒绝逻辑。
   - 期望如何解决：Step 2 测试改动中明确新增 `agentrun/create` 传入非法 `restartPolicy` 返回 `-32602 InvalidParams` 的测试；同时保留空值、`try_reload`、`always_new` 的可接受路径测试，至少覆盖非法值和一个合法非空值。

### claude-code 第3轮

逐条回应 codex 第2轮反馈：

**反馈1（`agentd shim --permissions` 覆盖值绕过 `ValidateConfig`）**：接受。Step 3 新增两项代码改动：
- `command.go:79-81`：CLI `--permissions` 覆盖后立即调用 `cfg.Permissions.IsValid()`，无效时返回明确错误
- `pkg/runtime/client.go`：`default` 分支改为显式匹配 `ApproveAll`，新增 `default:` 返回错误（防御性编程）
- 新增 CLI 非法 permissions 值测试

**反馈2（RestartPolicy 新增校验需要明确测试覆盖）**：接受。Step 2 测试改动中明确新增 3 个测试场景：
- 非法值（`"on-failure"`、`"bad-value"`）→ `-32602 InvalidParams`
- 合法非空值 `"try_reload"` → 正常通过
- 空值 → 正常通过（默认 always_new 行为）

### codex 第3轮

#### ✅ 认可项

1. **PermissionPolicy 安全边界已补齐**：Step 3 已明确要求 `agentd shim --permissions` 覆盖后立即校验 `cfg.Permissions.IsValid()`，并把 runtime 端从默认 approve-all 改为显式 `ApproveAll` 分支加未知值错误返回，解决了非法权限值静默放宽权限的问题。
2. **PermissionPolicy 测试覆盖已补齐**：新增 CLI 非法 `--permissions` 值测试，能够验证覆盖路径不会绕过配置校验。
3. **RestartPolicy 校验测试已补齐**：Step 2 明确覆盖非法值返回 `-32602 InvalidParams`、合法非空值 `try_reload`、空值默认行为，足以约束新增边界校验。
4. **整体执行与验收闭环成立**：方案覆盖代码、CLI、测试、`docs/design` 规范和全局 `rg` 残留检查，并按仓库要求使用 `make build` 验证构建。

#### ❌ 问题项

无阻断问题。当前方案足够严谨，可以执行。

## 最终方案

已通过审查，以下为完整执行方案。

### Step 1: 重命名 `runtime/stateChange` → `runtime/state_change` <!-- DONE -->

**代码改动**：

1. `pkg/events/envelope.go`：修改 `MethodRuntimeStateChange` 常量值为 `"runtime/state_change"`
2. 以下文件中的 `"runtime/stateChange"` 字面量全部替换：
   - `pkg/agentd/shim_client.go`：注释（4处）+ 错误消息（1处）
   - `pkg/agentd/process.go`：注释（3处）
   - `pkg/agentd/recovery.go`：注释（1处）
   - `pkg/events/translator.go`：注释（1处）
   - `pkg/events/envelope.go`：注释（1处）
   - `cmd/agentdctl/subcommands/shim/chat.go:142`：method 匹配
   - `cmd/agentdctl/subcommands/shim/command.go:184,187`：case 分支 + 错误消息
   - `cmd/agentdctl/subcommands/shim/CLAUDE.md`：文档引用（2处）
3. 测试文件：
   - `pkg/agentd/shim_client_test.go`（6处）
   - `pkg/agentd/shim_boundary_test.go`（8处）
   - `pkg/agentd/process_test.go`（1处）
   - `pkg/events/translator_test.go`（1处）
   - `pkg/rpc/server_test.go`（如有）

**文档改动**：

4. `docs/design/runtime/shim-rpc-spec.md`（约12处）
5. `docs/design/agentd/ari-spec.md`（1处）
6. `docs/design/README.md`（1处）
7. `docs/design/roadmap.md`（1处）
8. `docs/design/contract-convergence.md`（1处）
9. `docs/design/runtime/agent-shim.md`（2处）

**验收**：`rg "runtime/stateChange" pkg api cmd docs/design` 无命中。

### Step 2: 重命名 RestartPolicy 枚举值 + 修正公开入口 + 增加校验 <!-- DONE -->

统一为 `"try_reload"` / `"always_new"`（ACP snake_case），空值等价 `"always_new"`。

**代码改动**：

1. `api/meta/types.go`：常量值 `"tryReload"` → `"try_reload"`, `"alwaysNew"` → `"always_new"`，更新注释
2. `api/ari/types.go:143`：注释改为 `("try_reload", "always_new")`
3. 在 `agentrun/create` handler（`pkg/ari/server.go` 或调用链）增加校验：接受空值/`try_reload`/`always_new`，其他值返回 `-32602 InvalidParams`
4. `cmd/agentdctl/subcommands/agentrun/command.go:71`：flag help 改为 `"Restart policy: try_reload, always_new (default: always_new)"`
5. `cmd/agentdctl/subcommands/up/config_test.go:28,47`：`"on-failure"` → `"try_reload"`
6. `pkg/shimapi/types.go`：注释中 `tryReload` 更新

**测试改动**：

7. 新增 `agentrun/create` RestartPolicy 校验测试：
   - 非法值（`"on-failure"`、`"bad-value"`）→ `-32602 InvalidParams`
   - 合法非空值 `"try_reload"` → 正常通过
   - 空值 → 正常通过

**文档改动**：

8. `docs/design/agentd/ari-spec.md:223`：restartPolicy 改为 `"try_reload"` | `"always_new"`
9. `docs/design/runtime/agent-shim.md`：`tryReload` 更新
10. `docs/design/roadmap.md:55`：`tryReload` 更新

**验收**：`rg "tryReload|alwaysNew|on-failure|\"never\".*\"always\"" api pkg cmd docs/design` 无未解释命中。

### Step 3: 重命名 PermissionPolicy 枚举值 + 校验补齐 <!-- DONE -->

**代码改动**：

1. `api/spec/types.go`：`"approve-all"` → `"approve_all"`, `"approve-reads"` → `"approve_reads"`, `"deny-all"` → `"deny_all"`
2. `pkg/runtime/client.go`：错误消息更新；`default` 分支改为显式匹配 `ApproveAll`，新增 `default:` 返回错误
3. `pkg/spec/config.go`：校验错误消息更新
4. `cmd/agentd/subcommands/shim/command.go:49`：flag 默认值和 help 文本
5. `cmd/agentd/subcommands/shim/command.go:79-81`：CLI 覆盖后调用 `cfg.Permissions.IsValid()`，无效时返回错误
6. `pkg/agentd/process.go:403,528`：改用 `apispec.ApproveAll` 常量
7. `pkg/spec/testdata/bundles/minimal/config.json`：`"approve-all"` → `"approve_all"`

**测试改动**：

8. `pkg/spec/config_test.go`、`pkg/runtime/client_test.go`、`pkg/rpc/server_test.go`、`pkg/runtime/runtime_test.go`：引用更新
9. 新增 CLI `--permissions bad-value` 非法值测试

**文档改动**：

10. `docs/design/runtime/runtime-spec.md:355-357`
11. `docs/design/runtime/config-spec.md:245,251-253,261,306`

**排除**：`docs/research/**` 中的 `approve-all` 是上游研究记录，保留原样。

**验收**：`rg "approve-all|approve-reads|deny-all" api pkg cmd docs/design` 无命中。

### Step 4: Go 字段 `TurnId` → `TurnID` <!-- DONE -->

1. `pkg/events/envelope.go:72`：字段重命名，JSON tag `"turnId"` 不变
2. `pkg/events/translator.go`：`.TurnId` → `.TurnID`（约7处）
3. `pkg/events/translator_test.go`（约20处）
4. `pkg/rpc/server_test.go`（约2处）
5. `pkg/agentd/shim_boundary_test.go`（1处）

**验收**：`rg "\.TurnId\b" pkg` 无命中。

### Step 5: 更新 ARI 规范文档 <!-- DONE -->

以 `api/ari/types.go` JSON tag 为准逐段核对 `docs/design/agentd/ari-spec.md`：

1. `AgentTemplateInfo` → `AgentInfo`（全文）
2. `agent/set` Result：→ `AgentInfo`
3. `agent/get` Result：`{agentTemplate: AgentTemplateInfo}` → `{agent: AgentInfo}`
4. `agent/list` Result：`{agentTemplates: AgentTemplateInfo[]}` → `{agents: AgentInfo[]}`
5. `agentrun/status` Result 示例：`"agent"` key → `"agentRun"`
6. `agentrun/list` Result：`{agents[]}` → `{agentRuns[]}`
7. `docs/design/agentd/agentd.md`：术语更新

**验收**：`rg "AgentTemplateInfo|\"agentTemplate\"|\"agentTemplates\"" docs/design` 无命中。

### Step 6: 统一验收 <!-- DONE -->

```bash
make build
go test ./...
rg "runtime/stateChange" pkg api cmd docs/design
rg "tryReload|alwaysNew" api pkg cmd docs/design
rg "approve-all|approve-reads|deny-all" api pkg cmd docs/design
rg "AgentTemplateInfo|\"agentTemplate\"|\"agentTemplates\"" docs/design
rg "\.TurnId\b" pkg
```

所有 `rg` 检查预期 0 命中。排除范围：`.gsd/`、`docs/plan/`、`docs/research/`。
