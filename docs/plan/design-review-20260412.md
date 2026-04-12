## 审查结论

### 1. `docs/design/README.md`

- 文件路径：`docs/design/README.md`
- 过时内容描述：总览仍把 `Pod` 映射为 `Room`，把 `Container` 映射为外部 `Agent` / 内部 `Session`，把 `crictl` 映射为 `agent-shim-cli`；架构图仍写 Orchestrator 消费 Room Spec、agentd 包含 Room Manager、外部 API 对象是 `agent/*`；文档索引仍引用不存在的 `docs/design/orchestrator/room-spec.md`。当前代码没有 Room 包、Room ARI 方法或 room-spec；二进制是 `agentd` 和 `agentdctl`；运行实例生命周期是 `agentrun/*`，`agent/*` 只管理 AgentTemplate。
- 建议的修复方案：更新总览为当前实现词汇：Workspace、AgentTemplate、AgentRun、shim session。删除或标注 Room 为 future work；移除不存在的 room-spec 索引；将 `agent-shim-cli` 改为 `agentdctl shim`；说明 `agent/*` = AgentTemplate CRUD，`agentrun/*` = AgentRun 生命周期。

### 2. `docs/design/roadmap.md`

- 文件路径：`docs/design/roadmap.md`
- 过时内容描述：roadmap 仍说只有 agent-shim 层实现，agentd 未实现；仍列出不存在的 `cmd/agent-shim`、`cmd/agent-shim-cli`；仍规划创建已经存在的 `cmd/agentd/main.go`、`pkg/agentd`、`pkg/ari`、`pkg/meta`、`pkg/workspace`；仍以 `session/*` 作为 ARI 生命周期；建议 SQLite 元数据存储；workspace API 写为 `workspace/prepare` / `workspace/cleanup`；Room Manager 仍作为活跃阶段。当前实现已有 agentd、agentdctl、bbolt store、Workspace Manager、AgentTemplate CRUD、AgentRun 生命周期、recovery 和集成测试。
- 建议的修复方案：将 roadmap 改成当前状态矩阵。标记已实现项：agentd、agentdctl、bbolt、Workspace Manager、AgentTemplate CRUD、AgentRun 生命周期、clean-break shim RPC、部分 recovery。保留未实现或目标项：terminal operation 支持、真正的 ARI 事件 fanout、Room、workspace task/inbox、AgentRun 级 env override、hook 输出通过 ARI 持久化/查询等。

### 3. `docs/design/contract-convergence.md`

- 文件路径：`docs/design/contract-convergence.md`
- 过时内容描述：文档整体已经按 `workspace/*`、`agent/*`、`agentrun/*` 收敛，但 AgentTemplate 词汇仍写成包含 `runtimeClass` 的记录。当前 `pkg/meta/runtime.go` 和 `pkg/ari/types.go` 中 AgentTemplate 本身就是被 `AgentRun.Spec.RuntimeClass` 按名称引用的运行模板，字段是 `name`、`command`、`args`、`env`、`startupTimeoutSeconds`，没有 `runtimeClass` 字段。
- 建议的修复方案：将 AgentTemplate 定义改为“被 AgentRun 的 `runtimeClass` 字段按名称选择的模板/运行类记录”，字段只列当前 wire schema；不要写成“template 包含 runtimeClass”或“相对 runtimeClass 的 override”。

### 4. `docs/design/agentd/ari-spec.md`

- 文件路径：`docs/design/agentd/ari-spec.md`
- 过时内容描述：AgentRun 部分已基本对齐 `agentrun/*`，但 AgentTemplate schema 仍错误：`agent/set` 参数要求 `runtimeClass`，`command` 反而是可选 override，`env` 写成 map，schema 还包含 `labels`。当前代码的 `AgentTemplateSetParams` 要求 `name` 和 `command`，`args` 可选，`env` 是 `[]spec.EnvVar`，`startupTimeoutSeconds` 可选；没有 `runtimeClass`、`labels` 字段。`AgentTemplateInfo` 也没有返回 `startupTimeoutSeconds`，这是代码/文档需要明确取舍的缺口。
- 建议的修复方案：按 `pkg/ari/types.go` 修正 `agent/set|get|list|delete` schema：`name`、`command`、`args`、`env: [{name,value}]`、`startupTimeoutSeconds`。删除 `runtimeClass` / `labels` / override 叙述；补充说明 `agentrun/create.runtimeClass` 是 AgentTemplate 名称。若希望 status/list 返回 `startupTimeoutSeconds`，需要改代码；否则文档不要声明返回。

### 5. `docs/design/agentd/agentd.md`

- 文件路径：`docs/design/agentd/agentd.md`
- 过时内容描述：AgentRun、状态机、bootstrap、shim layout 已基本对齐当前实现，但 AgentTemplate Manager 仍说模板记录 `runtimeClass`、相对 runtime class 的 `command/args/env` override、`labels`。当前实现没有这些字段；`ProcessManager` 是用 AgentRun 的 `runtimeClass` 字段去 `GetAgentTemplate`，再从模板取 `command/args/env` 生成 runtime class。
- 建议的修复方案：重写 AgentTemplate Manager 小节：AgentTemplate 是命名运行模板，字段为 `name`、`command`、`args`、`env`、`startupTimeoutSeconds`；AgentRun 的 `runtimeClass` 选择 AgentTemplate；删除 override 和 labels 表述。

### 6. `docs/design/runtime/runtime-spec.md`

- 文件路径：`docs/design/runtime/runtime-spec.md`
- 过时内容描述：文档大部分已从 `created` 改为 `idle`，但仍残留“runtime returns `created`”和“not [`created`](#state)”等旧状态引用。当前 `pkg/spec/state_types.go` 状态为 `creating`、`idle`、`running`、`stopped`、`error`，没有 `created`；`pid` 注释在代码中也仍写 “created or running”，但设计文档应以 `idle` 为准。
- 建议的修复方案：删除所有作为状态值出现的 `created`，改为 `idle` 或 “successfully created” 这类自然语言。同步检查 lifecycle、operation precondition、示例和状态映射，确保只有 `creating/idle/running/stopped/error`。

### 7. `docs/design/runtime/design.md`

- 文件路径：`docs/design/runtime/design.md`
- 过时内容描述：文档前半部分已改为 `agentrun/create` / `agentrun/prompt` 和 `idle`，但 OCI 对照表仍写 `ARI session/new`、`ARI session/prompt after runtime reaches created`；Durable State Gaps 仍把 bundle path、shim socket path、bootstrap config snapshot 列为 S03 缺口。当前 `AgentRunStatus` 已持久化 `ShimSocketPath`、`ShimStateDir`、`ShimPID`、`BootstrapConfig`，正常 agentd 启动的 bundle/state/socket 共置于 `<bundleRoot>/<workspace>-<name>/`。
- 建议的修复方案：将残留对照表改为 `agentrun/create` 和 `agentrun/prompt` after `idle`。Durable gaps 应拆成“已实现持久化字段”和“仍缺口”：例如 OAR runtime ID ↔ ACP sessionId、resolved cwd、时间戳/失败阶段、hook 输出等。

### 8. `docs/design/runtime/agent-shim.md`

- 文件路径：`docs/design/runtime/agent-shim.md`
- 过时内容描述：文档末尾说 `session/load` 在 `ShimClient` 中作为可失败恢复尝试，且“shim server 已注册该方法”。当前 `pkg/agentd/shim_client.go` 确实会调用 `session/load`，但生产 `pkg/rpc/server.go` 没有注册 `session/load`，只注册 `session/prompt`、`session/cancel`、`session/subscribe`、`runtime/status`、`runtime/history`、`runtime/stop`。相关测试里的 `session/load` 是 mock shim 支持。
- 建议的修复方案：将说明改为：agentd client 在 `tryReload` recovery policy 下会尝试 `session/load`，该调用允许失败并 fallback；当前生产 shim RPC server 尚未实现/稳定暴露 `session/load`。不要写成 server 已注册。

### 9. `docs/design/workspace/workspace-spec.md`

- 文件路径：`docs/design/workspace/workspace-spec.md`
- 过时内容描述：文档仍使用 `session/new` env override、sessions、Room、`workspaceId`。当前外部运行对象是 AgentRun，workspace identity 是 name；`AgentRunCreateParams` 没有 env override。文档还要求 hook stdout/stderr 和失败状态必须对 caller 可见，但 ARI `workspace/create` 只接收 `name`、`source`、`labels`，没有接收 hooks；`workspace/status` 也不返回 hook 输出。代码层面的 `pkg/workspace` 支持 hooks，但 ARI 入口未把 hooks 从请求传入 `WorkspaceSpec`。
- 建议的修复方案：将 session/Room/`workspaceId` 全部改为 AgentRun/workspace name。环境说明改为：hooks 使用 agentd host env，agent process 使用父进程环境 + AgentTemplate env，无 AgentRun 级 env override。明确区分底层 Workspace Spec 支持 hooks 与当前 ARI `workspace/create` 尚未暴露 hooks；hook 输出通过 ARI 持久化/查询是目标缺口。

### 10. `docs/design/workspace/communication.md`

- 文件路径：`docs/design/workspace/communication.md`
- 过时内容描述：文档描述 `replyTo`、`threadId`、auto-reply、task/inbox、`workspace_task_*` MCP 工具、pending reply 队列，并在 API 扩展中写 `agent/create` 增加 `description` / `capabilities`。当前实现只有 `workspace/send`，参数为 `workspace`、`from`、`to`、`message`、`needsReply`；只在 prompt 前加 envelope，`needsReply=true` 时加入 `reply-to` 和 `reply-requested=true`；不支持 threadId、replyTo 参数、auto-reply、inbox、task 数据模型或 `workspace_task_*` 工具。
- 建议的修复方案：重构为“已实现 Message v0”和“未来 Task/Inbox”。已实现部分只描述 `workspace/send` + envelope + idle-only 投递约束；未实现的 thread、auto-reply、inbox、task、`workspace_task_*` 标为 future work。将任何运行实例 API 扩展从 `agent/create` 改为 `agentrun/create`，或移入 future API 设计。

## 讨论记录

### claude-code 第1轮

逐项对照实际代码核实结果：

#### 第1项：`docs/design/README.md` — **同意**

代码验证：
- README 第18行 `Pod → Room`、第19行 `Container → Agent/Session`、第21行 `crictl → agent-shim-cli` 均已过时。当前二进制为 `agentd` + `agentdctl`（非 `agent-shim-cli`）。
- README 第58行 Orchestrator 仍写"消费 Room Spec"，第72行仍有 Room Manager。当前代码中没有 Room 包或 Room ARI 方法。
- README 第142行引用 `orchestrator/room-spec.md`，已确认该目录和文件不存在。
- README 没有提及 `agentrun/*` 方法组，仅写 `agent/*`。实际代码中 ARI server 注册了 `workspace/*`、`agentrun/*`、`agent/*` 三组方法（`pkg/ari/server.go:177-223`）。

修复建议合理，完全同意。

#### 第2项：`docs/design/roadmap.md` — **同意**

代码验证：
- roadmap 第5-6行仍说"只有 agent-shim 层实现"，但 `cmd/agentd`、`cmd/agentdctl`、`pkg/agentd`、`pkg/ari`、`pkg/meta`、`pkg/workspace` 均已存在并有完整实现。
- 第11行仍列 `cmd/agent-shim`、`cmd/agent-shim-cli`；实际已改为 `cmd/agentd`、`cmd/agentdctl`。
- 第68-69行规划创建 `cmd/agentd/main.go`、`pkg/agentd/`，这些早已存在。
- 第90行建议 SQLite，实际使用 bbolt（`pkg/meta/runtime.go` 使用 `go.etcd.io/bbolt`）。
- 第125行仍写 `session/new`、`session/prompt`；实际 ARI 方法为 `agentrun/create`、`agentrun/prompt`。
- 第155行仍写 `workspace/prepare` / `workspace/cleanup`；实际为 `workspace/create` / `workspace/delete`。

roadmap 严重过时，改为状态矩阵的建议合理。完全同意。

#### 第3项：`docs/design/contract-convergence.md` AgentTemplate runtimeClass — **同意**

代码验证：
- contract-convergence.md 第20行 AgentTemplate 定义列出字段为 `name, command, args, env, runtimeClass, startupTimeoutSeconds`。
- 实际 `pkg/meta/runtime.go:21-34` `AgentTemplateSpec` 字段为：`Command`、`Args`、`Env []spec.EnvVar`、`StartupTimeoutSeconds *int`。**没有 `runtimeClass`**。
- 实际 `pkg/ari/types.go:331-346` `AgentTemplateSetParams` 字段为：`Name`（required）、`Command`（required）、`Args`、`Env`、`StartupTimeoutSeconds`。**没有 `runtimeClass`**。
- `runtimeClass` 实际在 `AgentRunCreateParams.RuntimeClass`（`types.go:139`），是 `agentrun/create` 的参数，引用 AgentTemplate 的名字。

AgentTemplate 本身就是被 `agentrun/create.runtimeClass` 按名字引用的模板。完全同意。

#### 第4项：`docs/design/agentd/ari-spec.md` AgentTemplate schema — **同意**

代码验证：
- ari-spec.md 第155-165行 `agent/set` params 写 `runtimeClass: required`、`command: optional`、`env: map`、`labels: map`。
- 实际代码 `AgentTemplateSetParams`（`types.go:331-346`）：`Name` required、`Command` **required**、`Args` optional、`Env []spec.EnvVar`（不是 map）、`StartupTimeoutSeconds` optional。**无 `runtimeClass`、无 `labels`**。
- `AgentTemplateInfo`（`types.go:350-368`）返回：`Name`、`Command`、`Args`、`Env`、`CreatedAt`、`UpdatedAt`。**未返回 `startupTimeoutSeconds`**。

补充发现：`startupTimeoutSeconds` 在 `SetParams` 和 `meta.AgentTemplateSpec` 中均存在且可存储到 DB，但 `AgentTemplateInfo` 和 `agentTemplateToInfo`（`server.go:1230-1239`）未包含此字段。这是代码缺口——写入但不返回。文档若声明返回需改代码，否则不应声明。

完全同意，修复建议合理。

#### 第5项：`docs/design/agentd/agentd.md` AgentTemplate Manager — **同意**

代码验证：
- agentd.md 第43-49行描述 AgentTemplate 包含 `runtimeClass`、override `command/args/env`、`labels`。
- 实际代码如第3/4项所述，AgentTemplate 没有 `runtimeClass` 和 `labels`，`command/args/env` 是直接字段而非 override。
- `pkg/agentd/process_manager.go` 中 `ProcessManager.Start` 通过 `store.GetAgentTemplate(agentRun.Spec.RuntimeClass)` 用 AgentRun 的 `runtimeClass` 查找 AgentTemplate，然后从模板取 `command/args/env` 生成 config。

完全同意。

#### 第6项：`docs/design/runtime/runtime-spec.md` 残留 `created` — **同意**

代码验证：
- `pkg/spec/state_types.go:7-22` 定义的状态常量为：`creating`、`idle`、`running`、`stopped`、`error`。**无 `created`**。
- runtime-spec.md 第174行："before the runtime returns `created`" — 应改为 `idle`。
- runtime-spec.md 第288行："Attempting to `start` an agent that is not [`created`](#state)" — 应改为 `idle`。
- `state_types.go:56-57` PID 注释已正确写为 `idle or running`。

完全同意。

#### 第7项：`docs/design/runtime/design.md` OCI 对照表和 Durable State Gaps — **同意**

代码验证：
- design.md 第260行 OCI 对照表 Step 1 仍写 `ARI session/new`，应为 `agentrun/create`。
- design.md 第270行 Step 7 写 "`session/prompt` after runtime reaches `created`"，应为 `agentrun/prompt` after `idle`。
- Durable State Gaps（第273-285行）仍列 bundle path、shim socket path、bootstrap config snapshot 为缺口。
- 实际 `contract-convergence.md:107-111` 已明确记录 `AgentRunStatus` 持久化了 `ShimSocketPath`、`ShimStateDir`、`ShimPID`、`BootstrapConfig`。`agentd.md:209-215` 亦同。
- `pkg/meta/` 的 AgentRunStatus 结构体实际也包含这些字段。

完全同意。Gaps 应拆为"已持久化"和"仍缺口"两部分。

#### 第8项：`docs/design/runtime/agent-shim.md` session/load — **同意**

代码验证：
- agent-shim.md 第185行写 "shim server 已注册该方法"。
- 实际 `pkg/rpc/server.go:138-157` 只注册了 6 个方法：`session/prompt`、`session/cancel`、`session/subscribe`、`runtime/status`、`runtime/history`、`runtime/stop`。**`session/load` 未注册**。
- `pkg/agentd/shim_client.go:127-132` 确实有 `Load` 方法调用 `session/load`，但这是 client 端。生产 server 端不处理此方法，调用会返回 `MethodNotFound`。

文档应改为：client 端在 tryReload policy 下尝试 `session/load`，该调用允许失败；生产 shim server 尚未实现。完全同意。

#### 第9项：`docs/design/workspace/workspace-spec.md` session/Room/workspaceId — **同意**

代码验证：
- workspace-spec.md 第4行 "for one or more sessions" — 应为 AgentRuns。
- 第147行 "ARI `session/new` env overrides" — 应为 AgentTemplate env（实际无 AgentRun 级 env override）。
- 第148行 env 优先级第3层仍写 "`session/new` env overrides layered last" — 实际无此层，`AgentRunCreateParams` 无 `env` 字段。
- 第157行 "multiple sessions"、第158行 "Room members"、"`workspaceId`" — 应为 AgentRuns、workspace name。
- 第135行 "hook stdout/stderr and failure status must be observable to the caller" — 但 ARI `workspace/create`（`types.go:30-39`）无 hooks 参数，`workspace/status` 结果（`types.go:57-69`）不返回 hook 输出。底层 `pkg/workspace` 支持 hooks，但 ARI 入口未暴露。

完全同意。

#### 第10项：`docs/design/workspace/communication.md` — **同意，但有补充**

代码验证：
- `WorkspaceSendParams`（`types.go:101-117`）只有 `Workspace`、`From`、`To`、`Message`、`NeedsReply`。**无 `threadId`、无独立 `replyTo` 参数**。
- envelope 构建（`server.go:532-537`）只用 `from` 和 `needsReply`，`reply-to` 硬编码等于 `from`。无 `threadId`。
- ARI server 无 `workspace/taskCreate` 等方法（`server.go:177-223`）。
- `AgentRunCreateParams` 无 `description`、`capabilities`。
- 不存在 Inbox 数据结构、PendingReply 或 auto-reply 逻辑。

**补充意见**：communication.md 更像是一个 **forward-looking design proposal**，而非声称已实现的规范。建议将其重构为两部分时，明确标注文档性质：
1. "已实现 Message v0" — 描述当前 `workspace/send` + envelope + idle-only 投递
2. "设计提案：Task/Inbox" — 明确标注为 **future design**，不是当前实现

同意审查结论，修复建议合理。

---

**汇总**：10 项审查结论全部 **同意**，代码验证均一致。第4项补充了一个代码缺口（`startupTimeoutSeconds` 写入但不返回），第10项补充了文档性质标注建议。

## 最终方案

第 1 轮讨论已达成一致：`claude-code` 对 10 项审查结论全部同意，无异议；无需进入第 2 轮讨论。

执行范围：修复 `docs/design/` 下设计文档，使其与当前代码实现对齐；除第 4 项明确指出的 `startupTimeoutSeconds` 返回缺口外，不主动修改代码。若选择修复该代码缺口，必须同步更新 ARI 类型、转换函数和测试。

修复原则：

1. 当前实现词汇以 `Workspace`、`AgentTemplate`、`AgentRun`、shim `session/*` 为准。
2. 公共 ARI 边界以 `workspace/*`、`agent/*` AgentTemplate CRUD、`agentrun/*` AgentRun 生命周期为准。
3. `AgentTemplate` 是被 `agentrun/create.runtimeClass` 按名称引用的运行模板，当前字段为 `name`、`command`、`args`、`env`、`startupTimeoutSeconds`；不要写成包含 `runtimeClass` 的 override record。
4. Room、workspace task/inbox、auto-reply、threadId/replyTo、`workspace_task_*`、ARI 级事件 fanout、AgentRun 级 env override、hook 输出通过 ARI 持久化/查询等未实现内容必须标为 future work / design proposal，不能写成当前能力。
5. runtime 状态以 `creating`、`idle`、`running`、`stopped`、`error` 为准；删除作为状态值出现的 `created`。
6. shim RPC 以生产 server 当前注册的方法为准：`session/prompt`、`session/cancel`、`session/subscribe`、`runtime/status`、`runtime/history`、`runtime/stop`。`session/load` 只描述为 agentd client 在 `tryReload` recovery policy 下的可失败尝试，当前生产 shim server 尚未稳定暴露该方法。
7. agentd 正常启动的 shim 文件布局以 bundle/state/socket 共置于 `<bundleRoot>/<workspace>-<name>/` 为准；持久化字段 `ShimSocketPath`、`ShimStateDir`、`ShimPID`、`BootstrapConfig` 已实现，不应继续列为缺口。
8. Workspace Spec 底层支持 hooks，但当前 ARI `workspace/create` 尚未暴露 hooks，`workspace/status` 也不返回 hook 输出；文档必须区分底层 spec 能力与 ARI 暴露能力。

建议执行顺序：

1. 先修 `docs/design/agentd/ari-spec.md`、`docs/design/agentd/agentd.md`、`docs/design/contract-convergence.md` 的 AgentTemplate schema 和 `runtimeClass` 语义。
2. 再修 `docs/design/runtime/runtime-spec.md`、`docs/design/runtime/design.md`、`docs/design/runtime/agent-shim.md` 的 `created`、durable gaps、`session/load` 描述。
3. 接着修 `docs/design/workspace/workspace-spec.md`、`docs/design/workspace/communication.md`，明确当前 `workspace/send` v0 与 future Task/Inbox。
4. 最后修总览与规划：`docs/design/README.md`、`docs/design/roadmap.md`。
5. 修复后运行 `make build`。如时间允许，再运行聚焦测试：
   - `go test ./pkg/spec ./pkg/runtime ./pkg/rpc ./pkg/events`
   - `go test ./pkg/ari ./pkg/agentd ./pkg/workspace ./pkg/meta`
