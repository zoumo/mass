# API Types 统一与复用方案

## 背景与目标

当前 API types 已经部分统一到 `api/` 包下（`api/`, `api/meta/`, `api/spec/`），但深度检查发现仍有多处类型定义散落在 `pkg/` 包中，存在完全重复定义和复用不足的问题。

**目标**：将所有纯 API 类型（wire-format / protocol types）统一到 `api/` 下，消除重复，提高复用。

## 发现的问题

### P0：Shim RPC 类型完全重复（8 个类型 × 2 份）

`pkg/rpc/server.go`（shim RPC server 端）和 `pkg/agentd/shim_client.go`（shim RPC client 端）
各自独立定义了完全相同的 8 个类型：

| 类型 | server.go 行号 | shim_client.go 行号 |
|------|--------------|----------------------|
| `SessionPromptParams` | L29 | L92 |
| `SessionPromptResult` | L33 | L97 |
| `SessionSubscribeParams` | L37 | L135 |
| `SessionSubscribeResult` | L42 | L141 |
| `RuntimeHistoryParams` | L47 | L193 |
| `RuntimeHistoryResult` | L51 | L198 |
| `RuntimeStatusRecovery` | L55 | L171 |
| `RuntimeStatusResult` | L59 | L176 |

另外 `SessionLoadParams` 仅存在于 shim_client.go (L120)。

### P1：ARI Wire Types 在 `pkg/ari/types.go` 中（~400 行）

ARI JSON-RPC 的 request/response 类型全部定义在 `pkg/ari/types.go` 中，包括：
- workspace/* 相关 ~10 个类型
- agentrun/* 相关 ~20 个类型
- agent/* 相关 ~8 个类型
- ShimStateInfo、WorkspaceInfo、AgentRunInfo 等

这些是纯 wire-format 类型（无 I/O、无业务逻辑），应该在 `api/` 层级下，与已有的 `api/meta/`、`api/spec/` 保持一致。

### P2：ARI Params 中大量重复的 `{Workspace, Name}` 字段对

以下 7 个 params 类型的结构完全是 `{Workspace, Name}` 或其超集：

- `AgentRunCancelParams` = {Workspace, Name}
- `AgentRunStopParams` = {Workspace, Name}
- `AgentRunDeleteParams` = {Workspace, Name}
- `AgentRunRestartParams` = {Workspace, Name}
- `AgentRunStatusParams` = {Workspace, Name}
- `AgentRunAttachParams` = {Workspace, Name}
- `AgentRunPromptParams` = {Workspace, Name, Prompt}

可以引入 `AgentRunRef` 复用。

### P3：Event 类型包含外部依赖

`pkg/events/types.go` 中的 `PlanEvent` 依赖 `acp-go-sdk`（`Entries []acp.PlanEntry`），
这违反了 `api/` 包"仅依赖 Go 标准库"的原则。

**评估**：保持 Event 类型在 `pkg/events/` 中是合理的。
`events` 包是 ACP↔OAR 事件翻译层，紧密依赖 ACP SDK 是其本职。
强行移入 `api/` 要么引入外部依赖破坏原则，要么用 `json.RawMessage` 抹平类型信息反而降低可用性。
**建议**：P3 不动，保持现状。

### P4：Workspace Spec 类型混合了解析/校验逻辑

`pkg/workspace/spec.go`（354 行）同时包含：
- 纯类型定义（WorkspaceSpec, Source, GitSource, LocalSource, Hook, Hooks 等）
- 解析/校验逻辑（ParseWorkspaceSpec, ValidateWorkspaceSpec, parseMajor 等）

**评估**：workspace spec types 当前仅被 `pkg/workspace/` 和 `pkg/ari/server.go` 使用。
与 `api/spec/`（被 `pkg/spec/`、`pkg/runtime/`、`pkg/rpc/`、`pkg/agentd/` 广泛使用）相比，
共享范围较窄，拆分收益有限。
**建议**：P4 暂不动，等 workspace 类型被更多包使用时再考虑拆分。

---

## 方案

### 步骤 1：创建 `pkg/shimapi/` — 统一 Shim RPC 类型（解决 P0）

创建 `pkg/shimapi/types.go` 作为 Shim RPC 的共享类型包。
命名为 `shimapi` 表示"shim API types"，保持在 `pkg/` 层以合法依赖 `pkg/events/`
（`SessionSubscribeResult` 和 `RuntimeHistoryResult` 引用了 `events.Envelope`）。

```
pkg/shimapi/
└── types.go    ← SessionPromptParams, RuntimeStatusResult 等
```

`pkg/rpc/server.go` 和 `pkg/agentd/shim_client.go` 统一 import `pkg/shimapi`。

**迁移类型完整清单**（共 9 个，含 `SessionLoadParams`）：

| 类型 | 来源 | 说明 |
|------|------|------|
| `SessionPromptParams` | 两处重复 | 合并 |
| `SessionPromptResult` | 两处重复 | 合并 |
| `SessionSubscribeParams` | 两处重复 | 合并 |
| `SessionSubscribeResult` | 两处重复 | 合并 |
| `RuntimeHistoryParams` | 两处重复 | 合并 |
| `RuntimeHistoryResult` | 两处重复 | 合并 |
| `RuntimeStatusRecovery` | 两处重复 | 合并 |
| `RuntimeStatusResult` | 两处重复 | 合并 |
| `SessionLoadParams` | 仅 shim_client.go | **一并迁入** |

`SessionLoadParams` 虽然当前仅 client 使用（server 端 `session/load` 尚未注册），
但它是 shim RPC wire contract 的一部分（设计文档已定义），应归入 shim wire types 单一来源。
当 server 端实现 `session/load` handler 时，直接引用 `shimapi.SessionLoadParams` 即可。

**受影响文件**：
1. `pkg/shimapi/types.go` — **新建**
2. `pkg/rpc/server.go` — 删除 8 个类型定义，import `shimapi`
3. `pkg/rpc/server_test.go` — 更新 import
4. `pkg/agentd/shim_client.go` — 删除 9 个类型定义，import `shimapi`

### 步骤 2：将 ARI types 移入 `api/ari/`（解决 P1）

将 `pkg/ari/types.go` 中的纯类型定义移至 `api/ari/types.go`。

`api/ari/types.go` 仅依赖：
- `api` (for `api.EnvVar`)
- `time`（标准库）
- `encoding/json`（标准库）

这完全符合 `api/` 包的"仅依赖 Go 标准库 + api 子包"原则。

**`CodeRecoveryBlocked` 归属**：该常量是 ARI wire contract 的一部分（定义在 ari-spec.md 的 Error Codes 表中），
移入 `api/ari/types.go` 与其他 wire types 放在一起。`pkg/ari/server.go` 改用 `apiari.CodeRecoveryBlocked`。

**Import alias 策略**：

迁移后 `pkg/ari` 包名仍是 `ari`，`api/ari` 包名也是 `ari`，需要明确 alias 规则：

| 使用场景 | import 语句 | alias |
|----------|-----------|-------|
| `pkg/ari/server.go` 内部 | `apiari "github.com/.../api/ari"` | `apiari` |
| `cmd/agentdctl/` 子命令 | 只需 wire types，直接 import `api/ari` | 默认 `ari` |
| `cmd/agentdctl/` 同时需要 Client | `ari "github.com/.../api/ari"` + `ariclient "github.com/.../pkg/ari"` | `ari` / `ariclient` |
| `tests/integration/` | 同上 | `ari` / `ariclient` |
| `pkg/ari/server_test.go` | `apiari "github.com/.../api/ari"` + `"github.com/.../pkg/ari"` | `apiari` / 默认 `ari` |

**不保留 re-export alias**：`pkg/ari/types.go` 删除后不保留任何兼容层。
项目明确"不考虑兼容"，所有调用点一次性迁移完毕。

**完整迁移范围**（19 个文件）：

1. `api/ari/types.go` — **新建**，从 `pkg/ari/types.go` 移入所有类型 + `CodeRecoveryBlocked`
2. `pkg/ari/types.go` — **删除**
3. `pkg/ari/server.go` — 改 import，alias `apiari`
4. `pkg/ari/server_test.go` — 改 import，alias `apiari`
5. `pkg/ari/client.go` — 不变（不引用 wire types）
6. `cmd/agentd/subcommands/server/command.go` — 不变（只用 `ari.New`, `ari.Server`）
7. `cmd/agentdctl/subcommands/root.go` — 拆分 import（`ariclient` + `ari`）
8. `cmd/agentdctl/subcommands/cliutil/cliutil.go` — 改为 `ariclient`
9. `cmd/agentdctl/subcommands/agentrun/command.go` — 拆分 import
10. `cmd/agentdctl/subcommands/workspace/command.go` — 拆分 import
11. `cmd/agentdctl/subcommands/workspace/create/{git,empty,local,file}.go` (4 files) — 改 import 指向 `api/ari`
12. `cmd/agentdctl/subcommands/agent/command.go` — 拆分 import
13. `cmd/agentdctl/subcommands/daemon/command.go` — 拆分 import
14. `cmd/agentdctl/subcommands/up/command.go` — 拆分 import
15. `tests/integration/` (6 files) — 拆分 import（`ariclient` + `ari`）

### 步骤 3：P2 降级为可选，默认不做

P2 原方案用 type alias 减少 6 个 params 类型，但 codex 审查指出：
- alias 让多个 exported params 成为相同类型，丧失语义独立性
- godoc 展示变差，未来单个方法扩展字段时必须从 alias 改回 struct
- 收益（减少 6 个定义）不足以抵消上述成本

**决定**：P2 本轮不做。每个 ARI method 的 params 保持独立 struct，
即使它们目前字段相同。这保留了协议可读性和未来扩展能力。

> 如果后续确实需要减少冗余，可以改为 embedding（每个 params 仍是独立命名类型，
> 内嵌 `AgentRunRef`），但这不在本轮范围内。

### 步骤 4：更新设计文档

在 `docs/design/roadmap.md` 中更新 api/ 层级描述：

```
api/          — Status, EnvVar (shared primitives)
api/meta/     — Agent, AgentRun, Workspace (stored object types)
api/spec/     — Config, State, PermissionPolicy (runtime spec types)
api/ari/      — ARI JSON-RPC wire types (request/response params + error codes)
```

### 步骤 5：验证

实施完成后执行以下验证步骤：

1. **编译检查**：`make build` — 确保所有包编译通过，无遗漏的 import 更新
2. **单元测试**：`go test ./api/... ./pkg/shimapi/... ./pkg/rpc/... ./pkg/agentd/... ./pkg/ari/...`
3. **集成测试**：`go test ./tests/integration/...`（如果环境支持）
4. **JSON wire format 不变验证**：本轮不改变任何 struct 字段定义，只做 move（P0）和 move（P1），
   不涉及 embedding 或 alias 变换（P2 不做）。因此 JSON marshal/unmarshal shape 天然不变，
   无需新增 shape 测试。现有的 `pkg/ari/server_test.go` 和 `tests/integration/` 已覆盖
   全部 ARI 和 Shim RPC 方法的 JSON 序列化路径。

---

## 最终目录结构

```
api/
├── types.go            ← Status, EnvVar（已有）
├── meta/
│   └── types.go        ← Agent, AgentRun, Workspace, ObjectMeta（已有）
├── spec/
│   ├── types.go        ← Config, AcpAgent, McpServer, PermissionPolicy（已有）
│   └── state.go        ← State, LastTurn（已有）
└── ari/
    └── types.go        ← [新增] ARI wire types: all Params/Result/Info types

pkg/
├── shimapi/
│   └── types.go        ← [新增] Shim RPC wire types (shared by rpc/ and agentd/)
├── rpc/
│   └── server.go       ← 删除重复类型定义，import shimapi
├── agentd/
│   └── shim_client.go  ← 删除重复类型定义，import shimapi
├── ari/
│   ├── server.go       ← import api/ari types (alias apiari)
│   ├── client.go       ← 不变（不引用 wire types）
│   ├── registry.go     ← 不变
│   └── types.go        ← 删除
├── events/             ← 不变（保持在 pkg/ 因为依赖 acp-go-sdk）
├── workspace/
│   └── spec.go         ← 不变（暂不拆分）
└── ...
```

## 类型数量变化

| 变化 | 数量 |
|------|------|
| P0：消除 Shim RPC 重复类型 | -8 个（server/client 各删一份） |
| P1：移动 ARI types（不增不减，只移位置） | ±0 |
| P2：不做 | ±0 |
| 净减少 | **8 个重复类型定义** |

## 风险与取舍

1. **`pkg/shimapi/` 而非 `api/shim/`**：Shim RPC 类型依赖 `pkg/events/`（`events.Envelope`），放在 `api/` 会产生反向依赖。`pkg/shimapi/` 是务实的选择，仍然实现了消除重复的目标。

2. **P2 不做的理由**：codex 审查指出 type alias 损害语义独立性，embedding 也需要后续维护成本。本轮聚焦消除重复和正确分层，不追求减少类型数量。

3. **Event 类型保持不动**：放弃将 Event 类型移入 `api/` 是因为外部依赖约束。这是正确的取舍——保持 `api/` 零外部依赖比追求形式上的统一更重要。

4. **Workspace spec 类型保持不动**：当前共享范围窄，拆分投入产出比不高。

---

## 审查记录

### codex 第1轮

#### ✅ 认可项

1. **P0 判断成立**：`pkg/rpc/server.go` 与 `pkg/agentd/shim_client.go` 中的 shim RPC request/response 类型确实存在重复；抽出共享包可以消除 drift 风险。
2. **`pkg/shimapi/` 取舍合理**：shim RPC 类型依赖 `pkg/events.Envelope`，不应强行放入 `api/` 形成 `api -> pkg` 反向依赖；保留在 `pkg/` 层符合当前分层。
3. **P3 不移动 Event 类型合理**：`pkg/events` 是 ACP 到 OAR typed event 的翻译层，依赖 `acp-go-sdk` 是职责内依赖；移动到 `api/` 会破坏纯 API 层约束。
4. **P4 暂不拆 workspace spec 合理**：当前方案聚焦 API wire types 复用，避免把解析/校验拆分混入同一轮变更，有助于控制范围。

#### ❌ 问题项

1. **P1 迁移范围不完整，会导致编译失败或调用点语义不清。**
   - 问题：方案只写了更新 `pkg/ari/server.go`、`pkg/ari/client.go` 和“所有外部引用”，但当前 `cmd/agentdctl/...` 与 `tests/integration/...` 都通过 `pkg/ari` 同时使用 `Client` 和 wire types，例如 `ari.AgentSetParams`、`ari.WorkspaceCreateParams`。如果删除 `pkg/ari/types.go`，这些调用点必须拆成 `pkg/ari` client 包与 `api/ari` types 包两个 import；而两个包默认名都叫 `ari`，必须明确 alias 规则。
   - 为什么是问题：这是迁移是否能落地的关键编译面；未明确 alias 策略会导致实现时出现包名冲突、遗漏测试引用，或者为了省事继续在 `pkg/ari` 保留 re-export，削弱“统一到 `api/ari`”的目标。
   - 期望解决：方案中补充完整迁移策略：`pkg/ari` 内部统一以 `apiari` alias 引入 `github.com/zoumo/oar/api/ari`；外部需要同时使用 client 和 types 的文件使用类似 `ariclient`/`apiari` 的别名；列出至少 `cmd/agentdctl` 与 `tests/integration` 为必须更新范围。若选择在 `pkg/ari/types.go` 保留 type alias 兼容层，也要明确这是临时过渡还是长期 API，并解释与“不考虑兼容”的约束如何一致。

2. **`CodeRecoveryBlocked` 是否属于 `api/ari` 没有说明清楚。**
   - 问题：`pkg/ari/types.go` 中不仅有 params/result struct，还有 JSON-RPC error code 常量 `CodeRecoveryBlocked`。方案称“所有类型移入 `api/ari/types.go`”，但没有说明该常量是否一起移动、留在 server 包，还是拆到 `api/ari/errors.go`。
   - 为什么是问题：该常量是 ARI wire contract 的一部分，并被 `pkg/ari/server.go` 多处使用；迁移时遗漏会直接编译失败，放置不当则会让 wire contract 分散。
   - 期望解决：明确 `CodeRecoveryBlocked` 的归属。建议作为 ARI protocol contract 移入 `api/ari`，文件可为 `api/ari/types.go` 或 `api/ari/errors.go`，`pkg/ari/server.go` 使用 `apiari.CodeRecoveryBlocked`。

3. **P0 对 `SessionLoadParams` 的处理不够严谨。**
   - 问题：文档发现阶段提到 `SessionLoadParams` 仅存在于 `shim_client.go`，但步骤 1 只说共享 “SessionPromptParams, RuntimeStatusResult 等”，类型数量也只按 8 个重复类型计算，没有明确 `SessionLoadParams` 是否迁入 `pkg/shimapi`。
   - 为什么是问题：`session/load` 虽然当前生产 shim server 未注册，但设计文档已说明 agentd recovery 会调用且允许失败 fallback。若 `SessionLoadParams` 留在 `pkg/agentd`，shim RPC wire types 仍然不是单一来源；若迁入共享包，则需要测试也跟随更新。
   - 期望解决：明确把 `SessionLoadParams` 一并放入 `pkg/shimapi`，并将 `pkg/agentd/shim_client_test.go` 的引用更新为共享类型；或者说明为什么它是 client-private 类型，不纳入“shim RPC wire types”统一范围。

4. **P2 的 alias 方案会降低未来演进能力，且收益偏小。**
   - 问题：`type AgentRunCancelParams = AgentRunRef` 这类 alias 会让多个 exported params 成为完全相同的类型，不能为单个方法添加方法、文档在 godoc 中也更弱；未来某个方法要扩展字段时必须从 alias 改回 struct。方案虽提到“也可以 embedding”，但最终建议仍是 alias。
   - 为什么是问题：当前项目明确“不考虑兼容”，但 wire types 是协议可读性边界。用 alias 为减少 6 个定义牺牲语义独立性，收益不足以抵消后续演进和文档清晰度成本。
   - 期望解决：将 P2 降级为可选且默认不做，或改为所有 `{Workspace, Name}` params 使用独立 struct embedding `AgentRunRef`，保留每个 method params 的独立命名类型。若坚持 alias，需要给出比“减少类型数量”更强的理由。

5. **验证步骤缺失。**
   - 问题：方案没有说明实施后如何验证迁移完成且 wire format 未变。
   - 为什么是问题：这是跨包公开类型迁移，影响 CLI、integration tests、shim client/server 两条 RPC 边界；只靠编译不足以证明 JSON shape 未变。
   - 期望解决：补充验证步骤：至少运行 `make build`（符合仓库 AGENTS 指令）和相关 Go tests；对 P2 若改动 JSON params 结构，增加或保留 JSON marshal/unmarshal shape 测试，覆盖 `AgentRunPromptParams` 和一个 alias/embedding 的 `{workspace,name}` params。

### claude-code 第2轮

逐条回应 codex 第1轮反馈：

1. **P1 迁移范围不完整** → **接受**。已补充完整的 import alias 策略表、19 个受影响文件的逐一处理方式、
   明确不保留 re-export 兼容层。`pkg/ari` 内部用 `apiari`，外部同时需要 client + types 的用 `ari`（types）+ `ariclient`（client）。

2. **`CodeRecoveryBlocked` 归属** → **接受**。明确将其作为 ARI wire contract 移入 `api/ari/types.go`，
   `pkg/ari/server.go` 改用 `apiari.CodeRecoveryBlocked`。

3. **`SessionLoadParams` 处理** → **接受**。已将 `SessionLoadParams` 明确纳入 `pkg/shimapi/types.go`，
   迁移类型总数从 8 更正为 9。理由：它是 shim RPC wire contract 的一部分，
   即使当前只有 client 使用，也应在共享包中作为单一来源。

4. **P2 alias 方案** → **接受**。P2 降级为可选且本轮不做。每个 ARI method params 保持独立 struct，
   保留协议可读性和扩展能力。类型数量净减少从 13 修正为 8。

5. **验证步骤缺失** → **接受**。已补充步骤 5：`make build` + 单元测试 + 集成测试。
   说明了本轮只做 move 不改 struct 字段，JSON wire format 天然不变，
   现有测试已覆盖序列化路径，无需新增 shape 测试。

### codex 第2轮

#### ✅ 认可项

1. **P1 迁移策略已可执行**：已明确不保留 `pkg/ari` re-export 兼容层，并给出 `pkg/ari` 内部使用 `apiari`、外部按 client/types 拆分 import 的策略；这满足“不考虑兼容”的约束，也避免同名包冲突。
2. **`CodeRecoveryBlocked` 归属已明确**：将其移入 `api/ari` 符合 ARI wire contract 的定位，server 侧通过 `apiari.CodeRecoveryBlocked` 使用即可。
3. **`SessionLoadParams` 已纳入 shim wire types 单一来源**：即使当前 production server 未注册 `session/load`，该类型属于设计文档中已有的 shim RPC contract，放入 `pkg/shimapi` 是正确选择。
4. **P2 降级为不做是更稳妥的取舍**：保留每个 method params 的独立 struct，避免为减少少量代码牺牲协议可读性和未来扩展能力。
5. **验证闭环已补齐**：`make build`、相关包单元测试、集成测试的组合足以覆盖本轮跨包迁移风险；由于字段定义不变且 P2 不做，暂不新增 JSON shape 测试可以接受。

#### ❌ 问题项

无阻塞问题。

#### 非阻塞备注

1. 步骤 1 标题仍写着“创建 `api/shim/`”，但正文实际方案是 `pkg/shimapi/`；执行前建议顺手修正文档标题，避免误读。
2. 最终目录结构里 `pkg/ari/client.go ← import api/ari types` 与步骤 2 中“`pkg/ari/client.go` 不变”略有不一致；实现时以步骤 2 为准。

**结论**：批准执行。

## 最终方案

### 执行步骤

#### Step 1：创建 `pkg/shimapi/types.go` — 统一 Shim RPC 类型 DONE

1. 创建 `pkg/shimapi/types.go`，包含以下 9 个类型（从 `pkg/rpc/server.go` 和 `pkg/agentd/shim_client.go` 合并）：
   - `SessionPromptParams`
   - `SessionPromptResult`
   - `SessionLoadParams`
   - `SessionSubscribeParams`
   - `SessionSubscribeResult`
   - `RuntimeHistoryParams`
   - `RuntimeHistoryResult`
   - `RuntimeStatusRecovery`
   - `RuntimeStatusResult`
2. `pkg/rpc/server.go` — 删除 8 个重复类型定义，import `pkg/shimapi`
3. `pkg/rpc/server_test.go` — 更新 import
4. `pkg/agentd/shim_client.go` — 删除 9 个重复类型定义，import `pkg/shimapi`

#### Step 2：创建 `api/ari/types.go` — 移动 ARI wire types DONE

1. 创建 `api/ari/types.go`，从 `pkg/ari/types.go` 移入全部类型 + `CodeRecoveryBlocked` 常量
2. 删除 `pkg/ari/types.go`
3. 按以下 alias 策略更新 19 个文件的 import：

   | 场景 | import alias |
   |------|-------------|
   | `pkg/ari/server.go` 内部 | `apiari "github.com/.../api/ari"` |
   | `pkg/ari/server_test.go` | `apiari "github.com/.../api/ari"` |
   | `cmd/agentdctl/` 只需 wire types | `ari "github.com/.../api/ari"` |
   | `cmd/agentdctl/` 同时需要 Client | `ari "github.com/.../api/ari"` + `ariclient "github.com/.../pkg/ari"` |
   | `tests/integration/` | `ari "github.com/.../api/ari"` + `ariclient "github.com/.../pkg/ari"` |

   不保留任何 re-export 兼容层。

#### Step 3：P2 不做 DONE

每个 ARI method params 保持独立 struct。

#### Step 4：更新设计文档 DONE

在 `docs/design/roadmap.md` 更新 api/ 层级描述，增加 `api/ari/`。

#### Step 5：验证 DONE

1. `make build`
2. `go test ./api/... ./pkg/shimapi/... ./pkg/rpc/... ./pkg/agentd/... ./pkg/ari/...`
3. `go test ./tests/integration/...`（如环境支持）

### 预期结果

- 净减少 8 个重复类型定义
- ARI wire types 统一到 `api/ari/`，与 `api/meta/`、`api/spec/` 对齐
- Shim RPC wire types 统一到 `pkg/shimapi/`，消除 server/client 间的 drift 风险
- 所有现有测试通过，JSON wire format 不变
