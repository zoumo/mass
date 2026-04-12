## 审查结论

### 1. 当前代码把“agent 目标/运行模板”命名为 AgentTemplate

- 文件路径：`pkg/meta/runtime.go`、`pkg/ari/types.go`、`pkg/ari/server.go`、`cmd/agentdctl/subcommands/agent/command.go`、`pkg/meta/runtime_test.go`、`pkg/agentd/runtimeclass.go`、相关测试
- 问题描述：当前 `agent/*` API 管理的是 `AgentTemplate`，代码类型包括 `meta.AgentTemplate`、`AgentTemplateSpec`、`AgentTemplateSetParams`、`AgentTemplateInfo`、`AgentTemplateGetResult`、`AgentTemplateListResult`、`AgentTemplateDeleteParams`，store 方法为 `SetAgentTemplate` / `GetAgentTemplate` / `ListAgentTemplates` / `DeleteAgentTemplate`。这与新的概念目标不一致：用户要求把 AgentTemplate 统一为 `Agent`，这个 `Agent` 表示 agent 目标/定义，也就是之前的 runtimeClass。
- 建议的修复方案：将 AgentTemplate 相关代码类型、注释、测试 helper、CLI 文案统一改名为 `Agent`。建议的核心命名：
  - `meta.AgentTemplate` → `meta.Agent`
  - `meta.AgentTemplateSpec` → `meta.AgentSpec`
  - `SetAgentTemplate/GetAgentTemplate/ListAgentTemplates/DeleteAgentTemplate` → `SetAgent/GetAgent/ListAgents/DeleteAgent`
  - `ari.AgentTemplateSetParams` → `ari.AgentSetParams`
  - `ari.AgentTemplateInfo` → `ari.AgentInfo`
  - `ari.AgentTemplateGetResult` → `ari.AgentGetResult`
  - `ari.AgentTemplateListResult` → `ari.AgentListResult`
  - `ari.AgentTemplateDeleteParams` → `ari.AgentDeleteParams`
  `agent/*` JSON-RPC method names可以保持不变，因为它们现在正好表达 Agent CRUD。

### 2. AgentRun 仍用 runtimeClass 字段引用 AgentTemplate

- 文件路径：`pkg/meta/models.go`、`pkg/ari/types.go`、`pkg/ari/server.go`、`cmd/agentdctl/subcommands/agentrun/command.go`、`pkg/agentd/process.go`、`pkg/agentd/runtimeclass.go`、相关测试
- 问题描述：运行实例创建和持久化仍用 `runtimeClass` 字段指向模板，例如 `AgentRunCreateParams.RuntimeClass json:"runtimeClass"`、`meta.AgentRunSpec.RuntimeClass json:"runtimeClass"`、CLI flag `--runtime-class`、ProcessManager 中 `GetAgentTemplate(agent.Spec.RuntimeClass)`。新的概念要求 `Agent` 等同于之前的 runtimeClass，因此 `AgentRun` 应直接引用 `agent`，而不是继续暴露 `runtimeClass`。
- 建议的修复方案：将 AgentRun 的模板引用字段从 `runtimeClass` 改为 `agent`：
  - `AgentRunCreateParams.RuntimeClass` → `AgentRunCreateParams.Agent`，JSON 字段 `agent`
  - `AgentRunInfo.RuntimeClass` → `AgentRunInfo.Agent`，JSON 字段 `agent`
  - `meta.AgentRunSpec.RuntimeClass` → `meta.AgentRunSpec.Agent`，JSON 字段 `agent`
  - CLI `agentdctl agentrun create --runtime-class` → `--agent`
  - 错误信息、校验信息、日志、注释、测试期望从 runtimeClass 改为 agent
  - `config.json` annotations 中当前 `runtimeClass` 建议改为 `agent`
  不需要兼容旧字段。

### 3. agentd 内部 RuntimeClass 类型已变成过渡概念

- 文件路径：`pkg/agentd/runtimeclass.go`、`pkg/agentd/process.go`、`pkg/agentd/runtimeclass_test.go`
- 问题描述：`RuntimeClass` 当前只是从 `meta.AgentTemplate` 转换出的已解析启动配置，字段为 `Name`、`Command`、`Args`、`Env`。如果 `Agent` 就是之前的 runtimeClass，那么继续保留 `RuntimeClass` 内部类型会制造第二套概念。
- 建议的修复方案：删除或重命名 `RuntimeClass` 过渡类型。可选方案：
  - 直接在 `ProcessManager.generateConfig` 中使用 `*meta.Agent`；
  - 或引入 `ResolvedAgent` 作为内部结构，但优先直接使用 `meta.Agent`，减少概念层。
  同步删除/改名 `NewRuntimeClassFromMeta` 和 `runtimeclass_test.go`。

### 4. agentd 的 AgentManager 命名已经占用“Agent”但实际管理 AgentRun

- 文件路径：`pkg/agentd/agent.go`
- 问题描述：`AgentManager` 当前管理的是 `meta.AgentRun`，方法注释和错误类型也大量使用 “agent” 指运行实例。这与新的 `Agent`（agent 目标/定义）会产生歧义：`Agent` 是定义，`AgentRun` 是运行实例。
- 建议的修复方案：将运行实例管理器改为更明确的 `AgentRunManager`，错误类型和日志文案同步改成 AgentRun：
  - `AgentManager` → `AgentRunManager`
  - `ErrAgentNotFound` → `ErrAgentRunNotFound`
  - `ErrAgentAlreadyExists` → `ErrAgentRunAlreadyExists`
  - `ErrDeleteNotStopped` 文案改为 agent run
  这会扩大改动面，但能避免 `Agent` 概念再次重叠。

### 5. metadata store bucket 名称已经是 agents，但注释混用 AgentRun / AgentTemplate

- 文件路径：`pkg/meta/store.go`
- 问题描述：bbolt bucket `v1/agents` 当前存 AgentTemplate，`v1/agentruns` 存 AgentRun；这个物理 bucket 名称正好适合新概念，但注释仍写 AgentTemplate。另有旧注释同时出现 `agents/{workspace}/{name}` 与 `agentruns/{workspace}/{name}`，容易混淆。
- 建议的修复方案：保留 bucket 名称 `agents` 和 `agentruns`，只更新注释和 store API。由于当前无需兼容，不需要迁移旧 JSON schema；但必须确认所有新写入 JSON 字段使用 `agent` 而不是 `runtimeClass`。

### 6. ARI 返回结构需要避免 `agents` 同时代表 Agent 与 AgentRun

- 文件路径：`pkg/ari/types.go`、`pkg/ari/server.go`
- 问题描述：当前 `AgentRunListResult` 使用 `Agents []AgentRunInfo json:"agents"`；如果 `agent/list` 也返回 `{agents: AgentInfo[]}`，两个 API 都有 `agents` 但语义不同。虽然方法组不同，但客户端阅读时仍可能混淆。
- 建议的修复方案：在最终方案中明确 wire 形状：
  - `agent/list` 返回 `{ "agents": AgentInfo[] }`
  - `agentrun/list` 建议返回 `{ "agentRuns": AgentRunInfo[] }`，同步 CLI/tests/docs
  - `agentrun/status` 当前 `{ "agent": AgentRunInfo }` 建议改为 `{ "agentRun": AgentRunInfo }`
  这属于 breaking API 清理，符合“当前不需要兼容”。

### 7. 设计文档仍大量以 AgentTemplate / runtimeClass 描述 agent 目标

- 文件路径：`docs/design/README.md`、`docs/design/contract-convergence.md`、`docs/design/roadmap.md`、`docs/design/agentd/ari-spec.md`、`docs/design/agentd/agentd.md`、`docs/design/runtime/design.md`、`docs/design/runtime/config-spec.md`、`docs/design/workspace/workspace-spec.md`、`docs/design/runtime/agent-shim.md`
- 问题描述：设计文档已在上一轮修复中收敛到 `AgentTemplate` + `AgentRun` + `runtimeClass`，但新目标要求删除 AgentTemplate 概念，并把之前 runtimeClass 统一为 Agent。文档中的 `AgentTemplate CRUD`、`agentrun/create.runtimeClass`、`runtimeClass config`、`AgentTemplate env` 等都需要改写。
- 建议的修复方案：统一术语：
  - **Agent**：agent 目标/定义，包含 `name`、`command`、`args`、`env`、`startupTimeoutSeconds`，由 `agent/*` 管理。
  - **AgentRun**：运行实例，包含 `workspace`、`name`、`agent`、`restartPolicy`、`systemPrompt`、`labels`，由 `agentrun/*` 管理。
  - `runtimeClass` 只保留在 OCI/Kubernetes 类比或历史说明中；当前 OAR API 和代码不再使用该字段。

### 8. CLI 和用户输入文件需要同步改名

- 文件路径：`cmd/agentdctl/subcommands/agent/command.go`、`cmd/agentdctl/subcommands/agentrun/command.go`、相关手册/示例
- 问题描述：`agentdctl agent` 文案仍是 “agent template”；`agentrun create` 仍要求 `--runtime-class`。用户输入 YAML 当前按 `meta.AgentTemplate` 解析。
- 建议的修复方案：
  - `agentdctl agent` 文案改为 “Agent target/definition management” 或直接 “Agent management”
  - YAML 类型改为 `meta.Agent`
  - 错误信息从 `agent-template YAML` 改为 `agent YAML`
  - `agentrun create --agent <name>` 替代 `--runtime-class`

### 9. 测试和验证范围

- 文件路径：`pkg/meta/runtime_test.go`、`pkg/agentd/runtimeclass_test.go`、`pkg/agentd/process_test.go`、`pkg/agentd/recovery_test.go`、`pkg/ari/server_test.go`、`cmd/agentdctl/**` 相关测试或编译面
- 问题描述：这次改名会触及 public structs、JSON 字段、CLI flags、测试 fixture、错误字符串、文档示例，单点改名容易漏。
- 建议的修复方案：修复后必须运行 `make build`。建议聚焦测试：
  - `go test ./pkg/meta ./pkg/ari ./pkg/agentd`
  - `go test ./pkg/spec ./pkg/runtime ./pkg/rpc ./pkg/events ./pkg/workspace`
  - 如时间允许运行 `go test ./...`

## 讨论记录

### claude-code 第1轮

逐项对照实际代码核实结果：

#### 第1项：AgentTemplate → Agent 重命名 — **同意**

代码验证：
- `meta.AgentTemplate`（`pkg/meta/runtime.go:38`）、`meta.AgentTemplateSpec`（`runtime.go:21`）确认存在。
- Store 方法 `SetAgentTemplate/GetAgentTemplate/ListAgentTemplates/DeleteAgentTemplate`（`runtime.go:57-179`）确认存在。
- ARI 类型 `AgentTemplateSetParams`/`AgentTemplateInfo`/`AgentTemplateGetResult`/`AgentTemplateListResult`/`AgentTemplateDeleteParams`（`types.go:331-396`）确认存在。
- `agent/*` JSON-RPC method names 在 `server.go:211-218` 已注册为 `agent/set`、`agent/get`、`agent/list`、`agent/delete`，方法名不需要改。

概念上同意：AgentTemplate 是 agent 定义，命名为 Agent 更自然，类比 OCI 的 Image（定义）vs Container（实例）。

#### 第2项：AgentRun.runtimeClass → agent — **同意**

代码验证：
- `AgentRunCreateParams.RuntimeClass string json:"runtimeClass"`（`types.go:139`）确认存在。
- `AgentRunInfo.RuntimeClass string json:"runtimeClass"`（`types.go:275`）确认存在。
- `meta.AgentRunSpec.RuntimeClass string json:"runtimeClass"`（`models.go:50`）确认存在。
- CLI `--runtime-class` flag（`agentrun/command.go:69`）确认存在。
- ProcessManager 中 `m.store.GetAgentTemplate(ctx, agent.Spec.RuntimeClass)`（`process.go:218`）确认存在。
- config annotations `annotations["runtimeClass"] = rc.Name`（`process.go:354`）确认存在。

Agent 就是之前的 runtimeClass，引用字段改名为 `agent` 语义更清晰。同意。

#### 第3项：RuntimeClass 内部过渡类型 — **同意**

代码验证：
- `RuntimeClass` struct（`runtimeclass.go:11-23`）字段为 `Name`、`Command`、`Args`、`Env`，是 `meta.AgentTemplateSpec` 的严格子集。
- `NewRuntimeClassFromMeta`（`runtimeclass.go:26-33`）只做字段拷贝。
- 调用处在 `process.go:225`：`runtimeClass := NewRuntimeClassFromMeta(rt)`，然后传给 `generateConfig`。

同意删除或重命名。倾向直接使用 `*meta.Agent`（改名后），减少一层间接。如果 `generateConfig` 需要的字段恰好是 `AgentSpec` 全部字段，就不需要中间类型。

#### 第4项：AgentManager → AgentRunManager — **同意**

代码验证：
- `AgentManager`（`agent.go:52`）所有方法操作的是 `meta.AgentRun`：`Create(agent *meta.AgentRun)`、`Get` returns `*meta.AgentRun`、`List` returns `[]*meta.AgentRun`。
- 错误类型 `ErrAgentNotFound`（`agent.go:16`）、`ErrAgentAlreadyExists`（`agent.go:39`）、`ErrDeleteNotStopped`（`agent.go:27`）的错误信息均说 "agent"，但实际管理的是 AgentRun。
- 如果 `Agent` 现在是定义/模板，`AgentManager` 管理 `AgentRun` 会造成歧义。

同意改名为 `AgentRunManager`，错误类型同步改为 `ErrAgentRunNotFound`、`ErrAgentRunAlreadyExists`。

#### 第5项：Store bucket 名称和注释 — **同意**

代码验证：
- `bucketAgentTemplates = []byte("agents")`（`store.go:26`）— 物理 bucket 名 `agents` 存 Agent 定义，改名后正好吻合。
- `bucketAgentRuns = []byte("agentruns")`（`store.go:25`）— 存 AgentRun。
- 注释（`store.go:15-21`）有误：写 `agents/{workspace}/{name} → AgentRun JSON blob` 但实际 AgentRun 在 `agentruns/{workspace}/{name}`。`agents/{name}` 才是 AgentTemplate。注释需要修正。

同意保留 bucket 名不变，只更新注释和 store API 名。

#### 第6项：ARI 返回结构 agents 歧义 — **同意**

代码验证：
- `AgentRunListResult.Agents []AgentRunInfo json:"agents"`（`types.go:262`）。
- `AgentRunStatusResult.Agent AgentRunInfo json:"agent"`（`types.go:240`）。
- 改名后 `agent/list` 也会返回 `{ "agents": AgentInfo[] }`，与 `agentrun/list` 的 `{ "agents": AgentRunInfo[] }` 在 JSON key 上冲突。

同意改为：
- `agentrun/list` → `{ "agentRuns": AgentRunInfo[] }`
- `agentrun/status` → `{ "agentRun": AgentRunInfo }`
- `agent/list` → `{ "agents": AgentInfo[] }`

当前不需要兼容，正是做 breaking change 的好时机。

#### 第7项：设计文档术语 — **同意**

这是第1-6项的文档侧结果。所有设计文档中的 `AgentTemplate`、`runtimeClass` 需改为 `Agent`、`agent` 引用。应在代码改完后同步修改文档。同意。

#### 第8项：CLI 和用户输入改名 — **同意**

代码验证：
- `agent/command.go:20`：`Short: "Agent template management commands"` — 需改为 `"Agent management"`。
- `agent/command.go:34`：`"Apply (create or update) an agent template from a YAML file"` — 需去掉 "template"。
- `agent/command.go:39`：`var tmpl meta.AgentTemplate` — 改为 `meta.Agent`。
- `agent/command.go:44`：`"agent-template YAML must have..."` — 改为 `"agent YAML..."`。
- `agentrun/command.go:69`：`--runtime-class` flag — 改为 `--agent`。

同意。

#### 第9项：测试和验证范围 — **同意**

这是机械性的验证步骤。`make build` 确保编译通过，聚焦测试覆盖改名涉及的包。同意。

---

**汇总**：9 项审查结论全部 **同意**，无异议。代码中所列类型、字段、方法、CLI flag、store bucket、ARI wire shape 均已逐项验证确认存在，改名方案合理。

## 最终方案

第 1 轮讨论已达成一致：`claude-code` 对 9 项审查结论全部同意，无异议；无需进入第 2 轮讨论。

执行目标：把代码和设计文档中 `AgentTemplate` 的概念和字段统一为 `Agent`。这里的 `Agent` 表示 agent 目标 / 定义 / 可运行配置，等同于之前的 `runtimeClass`；`AgentRun` 表示实际运行实例。

兼容性：本项目当前不需要兼容旧 API 或旧 metadata JSON，因此允许 breaking rename。不要保留 `runtimeClass` / `AgentTemplate` alias，除非只出现在历史说明或 OCI/Kubernetes 类比中。

核心术语：

1. **Agent**：agent 目标 / 定义。由 `agent/*` 管理。字段为 `name`、`command`、`args`、`env`、`startupTimeoutSeconds`。
2. **AgentRun**：运行实例。由 `agentrun/*` 管理。字段包括 `workspace`、`name`、`agent`、`restartPolicy`、`systemPrompt`、`labels`。
3. **runtimeClass**：从当前 OAR API、metadata JSON、CLI flag、代码类型、设计规范中移除。只允许在 OCI/Kubernetes 对照或历史迁移说明中作为类比出现。

代码修复要求：

1. 将 `AgentTemplate` 系列重命名为 `Agent` 系列：
   - `meta.AgentTemplate` → `meta.Agent`
   - `meta.AgentTemplateSpec` → `meta.AgentSpec`
   - `SetAgentTemplate/GetAgentTemplate/ListAgentTemplates/DeleteAgentTemplate` → `SetAgent/GetAgent/ListAgents/DeleteAgent`
   - `ari.AgentTemplateSetParams` → `ari.AgentSetParams`
   - `ari.AgentTemplateInfo` → `ari.AgentInfo`
   - `ari.AgentTemplateGetResult` → `ari.AgentGetResult`
   - `ari.AgentTemplateListResult` → `ari.AgentListResult`
   - `ari.AgentTemplateDeleteParams` → `ari.AgentDeleteParams`
2. 保持 JSON-RPC 方法名 `agent/set`、`agent/get`、`agent/list`、`agent/delete` 不变，但语义改为 Agent CRUD。
3. 将 AgentRun 的模板引用字段从 `runtimeClass` 改为 `agent`：
   - Go 字段：`RuntimeClass` → `Agent`
   - JSON 字段：`runtimeClass` → `agent`
   - CLI：`agentdctl agentrun create --runtime-class` → `--agent`
   - 校验、错误信息、日志、测试 fixture、示例全部同步。
4. 删除或重命名内部 `RuntimeClass` 过渡类型。优先直接让 `ProcessManager.generateConfig` 使用 `*meta.Agent`，减少概念层。
5. 将当前管理 `meta.AgentRun` 的 `AgentManager` 改名为 `AgentRunManager`，错误类型和文案同步改为 AgentRun：
   - `ErrAgentNotFound` → `ErrAgentRunNotFound`
   - `ErrAgentAlreadyExists` → `ErrAgentRunAlreadyExists`
   - `ErrDeleteNotStopped` 文案改为 agent run
6. 保留 bbolt bucket 名称：
   - `v1/agents` 存 Agent 定义
   - `v1/agentruns/{workspace}/{name}` 存 AgentRun
   更新 `pkg/meta/store.go` 注释，修正当前混淆。
7. 明确 ARI wire shape：
   - `agent/list` 返回 `{ "agents": AgentInfo[] }`
   - `agent/get` 返回 `{ "agent": AgentInfo }`
   - `agent/set` 返回 `AgentInfo`
   - `agentrun/list` 返回 `{ "agentRuns": AgentRunInfo[] }`
   - `agentrun/status` 返回 `{ "agentRun": AgentRunInfo, "shimState": ... }`
   - `AgentRunInfo` 中字段为 `agent`，不是 `runtimeClass`
8. `config.json` annotations 中当前 `runtimeClass` 建议改为 `agent`。
9. `startupTimeoutSeconds` 当前能写入 `meta.AgentTemplateSpec` 但不在 `AgentTemplateInfo` 返回。改名时一起补齐到 `AgentInfo`，并更新 `agentToInfo` 转换和测试。

设计文档修复要求：

1. `docs/design/README.md`、`contract-convergence.md`、`roadmap.md`、`agentd/ari-spec.md`、`agentd/agentd.md`、`runtime/design.md`、`runtime/config-spec.md`、`workspace/workspace-spec.md`、`runtime/agent-shim.md` 中的 `AgentTemplate` 改为 `Agent`。
2. 将 `agentrun/create.runtimeClass` 改为 `agentrun/create.agent`。
3. 将 “runtimeClass config/env” 改为 “Agent config/env”。
4. 文档中保留 OCI 对照时，可以写 “Kubernetes runtimeClassName 类比到 OAR Agent 名称”，但当前 API 不再使用 `runtimeClass` 字段。
5. 保持 `AgentRun` 与 `Agent` 的边界清晰：Agent 无运行进程，AgentRun 有 shim/runtime state。

建议执行顺序：

1. 先改代码类型和 store API：`pkg/meta`、`pkg/ari/types.go`、`pkg/ari/server.go`。
2. 再改 agentd 内部管理器和 ProcessManager：`pkg/agentd`。
3. 接着改 CLI：`cmd/agentdctl/subcommands/agent`、`cmd/agentdctl/subcommands/agentrun`，以及 workspace MCP 输出字段如有涉及。
4. 更新测试 fixture 和期望。
5. 最后更新 `docs/design/` 和相关计划/手册中的术语。

验证要求：

1. 必须运行 `make build`。
2. 必须运行：
   - `go test ./pkg/meta ./pkg/ari ./pkg/agentd`
3. 建议运行：
   - `go test ./pkg/spec ./pkg/runtime ./pkg/rpc ./pkg/events ./pkg/workspace`
   - `go test ./...`
4. 如果因为当前工作区其他未提交改动导致测试失败，需要在回复中说明失败是否与本次改名相关。
