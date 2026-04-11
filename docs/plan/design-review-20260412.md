## 审查结论

### 1. `docs/design/README.md`

问题描述：架构总览仍把 `Pod -> Room`、`Container -> Agent/Session`、`crictl -> agent-shim-cli` 作为当前映射，并把 `docs/design/orchestrator/room-spec.md` 列入索引；但代码中没有 Room Manager、Room ARI，也没有该设计文件。当前二进制是 `agentd` 和 `agentdctl`，直接 shim 管理是 `agentdctl shim` 子命令。

修复建议：将当前实现概念改为 Workspace、AgentTemplate、AgentRun、shim session；删除或标注 Room 为未来设计；移除不存在的 `orchestrator/room-spec.md` 索引；把 `agent-shim-cli` 改为 `agentdctl shim`；说明外部运行实例生命周期由 `agentrun/*` 提供，`agent/*` 只管理 AgentTemplate。

### 2. `docs/design/contract-convergence.md`

问题描述：authority map 和不变量仍声明外部生命周期是 `agent/create`、`agent/status`、`agent/prompt` 等，Agent identity 是 `room + name`，所有 agent 都属于 Room，`workspace/prepare` 产生 `workspaceId`，状态机包含 `created`。当前实现是 `workspace + name` 的 AgentRun，生命周期方法是 `agentrun/*`，`agent/*` 是 AgentTemplate CRUD，workspace 创建方法是 `workspace/create`，空闲状态是 `idle`。

修复建议：重写该文件作为当前实现的 source of truth：`agent/* = AgentTemplate CRUD`，`agentrun/* = AgentRun lifecycle`，`workspace/* = workspace lifecycle/message routing`；把 Room 相关内容移入未来工作；把 `workspaceId` 改为 workspace name；把 `created` 改为 `idle`；事件边界说明应改为 `agentrun/attach` 返回 shim socket，随后消费 shim 的 `session/update` 和 `runtime/stateChange`。

### 3. `docs/design/roadmap.md`

问题描述：roadmap 仍说只有 agent-shim 已实现、agentd 未实现，并列出 `cmd/agent-shim`、`cmd/agent-shim-cli`、SQLite metadata store、`session/*` ARI、`workspace/prepare` / `workspace/cleanup` 等计划项。代码中已有 `cmd/agentd`、`cmd/agentdctl`、`pkg/agentd`、`pkg/ari`、`pkg/meta`、`pkg/workspace`，metadata 使用 bbolt，ARI 为 `workspace/*`、`agentrun/*`、`agent/*`。

修复建议：把 roadmap 改成状态矩阵：标记 agentd、agentdctl、bbolt store、Workspace、AgentTemplate CRUD、AgentRun lifecycle、clean-break shim RPC、恢复流程等已实现；剩余项应包括真正的 shim `session/load` server 支持、hook 输出持久化、AgentRun 级 env overrides、Room 设计、ARI-level event fanout 等。

### 4. `docs/design/agentd/ari-spec.md`

问题描述：文档已把 identity 改成 `workspace + name`，但仍把运行实例生命周期定义在 `agent/create`、`agent/prompt`、`agent/status`、`agent/attach` 等方法下，并把 `agent/list`、`agent/delete` 用作运行实例操作。当前 `pkg/ari/server.go` 暴露的运行实例方法是 `agentrun/create|prompt|cancel|stop|delete|restart|list|status|attach`；`agent/set|get|list|delete` 管理 AgentTemplate。

修复建议：按代码拆成三个章节：Workspace methods、AgentTemplate methods (`agent/*`)、AgentRun methods (`agentrun/*`)；把所有运行实例示例和错误说明从 `agent/*` 改成 `agentrun/*`；补充 `agent/set|get|list|delete` 的 AgentTemplate schema；`AgentInfo` 改名或描述为 `AgentRunInfo`。

### 5. `docs/design/agentd/ari-spec.md`

问题描述：Events 章节声明 `agent/update` 和 `agent/stateChange` 是 attached ARI clients 的事件；当前 `agentrun/attach` 只返回 shim Unix socket path，并没有 ARI 层 fanout。真实 live notification 是 shim socket 上的 `session/update` 和 `runtime/stateChange`。

修复建议：删除 ARI-level `agent/update` / `agent/stateChange` 作为当前契约，改写为 attach-to-shim 模型；如果希望保留 ARI 层 fanout，应标为未来计划并对应修改代码。

### 6. `docs/design/agentd/ari-spec.md`

问题描述：workspace MCP 日志描述包含 `agentID=`；当前 `cmd/agentd/subcommands/workspacemcp` 读取 `OAR_AGENTD_SOCKET`、`OAR_WORKSPACE_NAME`、`OAR_AGENT_NAME`、`OAR_STATE_DIR`，没有 `OAR_AGENT_ID` 或 `agentID` 字段。

修复建议：将 workspace MCP 环境变量和日志描述改为当前实现：`workspace=`、`agentName=` 以及相关 env vars；删除 `agentID`。

### 7. `docs/design/agentd/agentd.md`

问题描述：文档仍把外部运行对象称为 Agent，并说 `agent/create`、`agent/prompt`、`agent/stop` 等是外部运行生命周期。当前持久运行对象是 `meta.AgentRun`，由 `agentrun/*` 管理；`agent/*` 是 `meta.AgentTemplate` 的 CRUD，`agentdctl agent` 管模板，`agentdctl agentrun` 管运行实例。

修复建议：将外部运行对象统一改为 AgentRun；新增 AgentTemplate 小节，说明 `metadata.name`、`spec.command`、`spec.args`、`spec.env`、`startupTimeoutSeconds` 和 `agent/set|get|list|delete`；所有运行生命周期 API 改为 `agentrun/*`。

### 8. `docs/design/runtime/runtime-spec.md`

问题描述：runtime 状态模型仍使用 `created` 表示 bootstrap 后可接收 prompt，并说 `pid` 在 `created` 或 `running` 时必填；代码中 `pkg/spec/state_types.go` 定义的是 `creating`、`idle`、`running`、`stopped`、`error`，`pkg/runtime.Manager.Create` 写入 `StatusIdle`。文档示例、生命周期、state mapping、start/kill/delete 前置条件和 notification 示例仍大量使用 `created`。

修复建议：把 `created` 全部替换为 `idle`，并加入 `error`；修正 `pid` 条件为 `idle` 或 `running`；更新生命周期图、state mapping、operation preconditions、示例 JSON 和 notification 示例。

### 9. `docs/design/runtime/runtime-spec.md`

问题描述：文件系统布局仍声明 bundle 在 `/var/lib/agentd/bundles/<agent-id>/`，state dir 在 `/run/agentd/shim/<agent-id>/`，agentd 重启通过扫描 `/run/agentd/shim/*/agent-shim.sock` 恢复且无需记录 socket path。当前 agentd 正常启动时把 state files 与 bundle co-locate：`<BundleRoot>/<workspace>-<name>/config.json`、`state.json`、`events.jsonl`、`agent-shim.sock` 同目录，并持久化 `ShimSocketPath` / `ShimStateDir` / `ShimPID`。

修复建议：将 agentd 部署布局改为 bundle/state co-location；保留 `agentd shim --state-dir /run/agentd/shim` 作为 standalone 默认说明；恢复语义改为优先使用 metadata 中的 persisted shim path，而不是只扫描 `/run/agentd/shim/*`。

### 10. `docs/design/runtime/runtime-spec.md`

问题描述：State 定义没有记录已实现字段 `lastTurn` 和 `exitCode`，但 `pkg/spec.State` 已包含它们。

修复建议：在 State schema 中加入 `lastTurn` 和 `exitCode`，说明 `lastTurn` 保存最近一轮结果，`exitCode` 在进程退出后填充。

### 11. `docs/design/runtime/shim-rpc-spec.md`

问题描述：“实现滞后说明”仍说 `pkg/rpc`、`pkg/agentd/shim_client.go`、`cmd/agent-shim-cli` 使用 legacy PascalCase / `$/event`。当前 `pkg/rpc/server.go`、`pkg/agentd/shim_client.go`、`agentdctl shim` 已使用 `session/*` + `runtime/*` 和 `session/update` / `runtime/stateChange`。

修复建议：删除该 stale implementation-lag 段，改为说明实现已对齐 clean-break surface；`cmd/agent-shim-cli` 改为 `agentdctl shim`。

### 12. `docs/design/runtime/shim-rpc-spec.md`

问题描述：示例仍使用 `status: "created"`、`previousStatus: "created"`；`runtime/history` 说 `fromSeq` 默认 `1`；`session/subscribe` 只规范 `afterSeq`。当前实现 `runtime/history` 默认 `fromSeq=0`，`session/subscribe` 同时支持 `afterSeq` 和原子 backfill 的 `fromSeq`。

修复建议：示例改为 `idle`；把 `runtime/history` 默认值改成 `0` 或修改代码保持一致；在 `session/subscribe` 中记录 `fromSeq` 语义，以及 `fromSeq` 与 `afterSeq` 的使用边界。

### 13. `docs/design/runtime/shim-rpc-spec.md`

问题描述：`session/prompt` 描述为对应上层 ARI `session/prompt`；当前上层 public ARI 是 `agentrun/prompt` 或 `workspace/send`，`session/prompt` 只存在于 agentd 与 shim 之间。

修复建议：将该句改为“对应上层 `agentrun/prompt` / `workspace/send` 转发到 shim 的 runtime 侧落点”，并强调 `session/*` 是内部 shim boundary。

### 14. `docs/design/runtime/agent-shim.md`

问题描述：文档仍说每个 OAR session 对应一个 shim、M005 外部 ARI 从 `session/*` 迁移到 `agent/*`、当前实现仍使用 legacy PascalCase / `$/event`；当前外部运行对象是 AgentRun，public ARI 是 `agentrun/*`，shim RPC 已是 clean-break surface。

修复建议：把外部术语改为 AgentRun runtime instance，保留 “session” 只描述 shim RPC / ACP boundary；删除 legacy implementation-lag 段；把 `agent/*` 改为 `agentrun/*`，并说明 `agent/*` 已用于 AgentTemplate。

### 15. `docs/design/runtime/agent-shim.md`

问题描述：文档把 `session/load` 作为 agent-shim 职责。当前 `pkg/agentd/shim_client.go` 有 `Load` 调用，recovery 在 `tryReload` 策略下尝试 `session/load`，但 `pkg/rpc/server.go` 没有实现 `session/load` dispatch；失败被视为可回退行为。

修复建议：标注 `session/load` 目前是 agentd recovery 的可选尝试 / 未来 shim server 能力，不是当前已支持的 shim RPC；若要让文档保持规范，应同步实现 `session/load` server 方法。

### 16. `docs/design/runtime/config-spec.md`

问题描述：MCP server schema 只允许 `"http"` 和 `"sse"`，但 `pkg/spec.McpServer` 和 `pkg/runtime.convertMcpServers` 已支持 `"stdio"`；`pkg/agentd/process.go` 还为每个 AgentRun 注入名为 `workspace` 的 stdio MCP server，并设置 `OAR_AGENTD_SOCKET`、`OAR_WORKSPACE_NAME`、`OAR_AGENT_NAME`、`OAR_STATE_DIR`。

修复建议：补充 `stdio` MCP server schema：`name`、`command`、`args`、`env`；说明 `args` 和 `env` 为 ACP 兼容需要显式数组；记录 agentd 自动注入 workspace MCP server 的当前行为。

### 17. `docs/design/runtime/config-spec.md`

问题描述：文档称 `acpAgent.systemPrompt` 必须在 Create 阶段作为 bootstrap 语义落实，不是外部工作 turn；但当前 `pkg/runtime.Manager.Create` 在 `session/new` 后通过内部 ACP Prompt 发送 systemPrompt 来实现 seeding。文档还说 agent 启动后等待 ARI `session/prompt`，当前 public daemon prompt 是 `agentrun/prompt`。

修复建议：二选一对齐：若 prompt-based seeding 可接受，文档应明确这是当前 bootstrap-compatible internal prompt；若不可接受，则把它列为 runtime implementation drift。所有 public ARI 入口改为 `agentrun/prompt`，只把 shim 内部入口称为 `session/prompt`。

### 18. `docs/design/runtime/design.md`

问题描述：bootstrap flow、state mapping 和 config generation 仍使用 ARI `session/new`、`session/prompt`、OAR `sessionId`、`created`、`paused:warm` / `paused:cold`，并描述 Room 共享 workspace。当前 public flow 是 `workspace/create` -> `agentrun/create` -> `agentrun/status` -> `agentrun/prompt`，状态为 `idle`，没有 warm/cold pause，Room 未实现。

修复建议：将该设计说明改为当前 AgentRun 流程；把 OAR `sessionId` 改为 AgentRun runtime ID / shim session boundary；删除当前态 warm/cold pause 和 Room 共享描述，或标为未来设计；把 `created` 改为 `idle`。

### 19. `docs/design/runtime/design.md`

问题描述：Durable State Gaps 仍把 bundle path、shim socket path、bootstrap config snapshot、last known state transition metadata 统称为 S03 未解决；当前 `pkg/agentd/process.go` 已持久化 `ShimSocketPath`、`ShimStateDir`、`ShimPID`、`BootstrapConfig`，`pkg/spec.State` 也包含 `lastTurn` / `exitCode`。

修复建议：将 durable-state 表格拆成“已实现”和“剩余缺口”：已实现 persisted shim path/state dir/pid/bootstrap config 和 last-turn/exit-code runtime state；剩余缺口再描述 ACP session 映射、完整 replay/cleanup/cross-client hardening 等。

### 20. `docs/design/runtime/why-no-runa.md`

问题描述：职责表仍把生命周期操作写成 legacy `Prompt` / `Cancel` / `Shutdown` / `GetState`，并链接 `[config.md](config-spec.md)`；当前 shim RPC 是 `session/prompt`、`session/cancel`、`session/subscribe`、`runtime/status`、`runtime/history`、`runtime/stop`，直接 CLI 是 `agentdctl shim`。

修复建议：把 legacy 操作名改成 clean-break shim method names；将链接文本改成 `config-spec.md`。

### 21. `docs/design/workspace/workspace-spec.md`

问题描述：文档仍说 workspace 为 “one or more sessions” 准备，env 边界使用 ARI `session/new` overrides，shared workspace 通过 Room members 和 `workspaceId` 描述。当前外部运行对象是 AgentRun，workspace identity 是 name，AgentRun create params 没有 env overrides，Room 不存在。

修复建议：把 session/Room/`workspaceId` 用语改为 AgentRun/workspace name；env precedence 改为 inherited daemon/host env + AgentTemplate env，明确 AgentRun-specific env overrides 当前未实现。

### 22. `docs/design/workspace/workspace-spec.md`

问题描述：Top-Level Shape 示例给出空 `{}` 的 `metadata` 和 `source`，但 `pkg/workspace.WorkspaceSpec` 要求 `metadata.name` 和 `source.type`；ARI `workspace/create` 只接收 `name`、raw `source`、`labels`，不接收 hooks。虽然 lower-level workspace manager 支持 hooks，`handleWorkspaceCreate` 没有把 hooks 从 ARI params 传入。

修复建议：示例补充 `metadata.name` 和 `source.type`；明确当前 ARI `workspace/create` 不支持 hooks 字段，hook 支持只存在于 lower-level `workspace.WorkspaceSpec` / manager，除非扩展 ARI params 并持久化 hooks。

### 23. `docs/design/workspace/workspace-spec.md`

问题描述：文档声明 hook stdout/stderr 和 failure status 必须对 caller 可观察；当前 ARI workspace status 只有 `phase` 和 `path`，hook 失败只落为 `phase=error`，没有持久化 stdout/stderr 或失败详情。

修复建议：将 hook 输出可观察性标为目标缺口，或实现 ARI/status 持久化 hook output/error detail 后再保留为当前保证。

## 讨论记录

### claude-code 第1轮

逐项核实结果（对照实际代码验证）：

| # | 结论 | 说明 |
|---|------|------|
| 1 | **同意** | `docs/design/README.md` 确认仍有 Pod→Room、Container→Agent/Session 映射（L18-19），引用不存在的 `orchestrator/room-spec.md`（L140），使用 `agent-shim-cli`（L21）。均已过时。 |
| 2 | **同意** | `contract-convergence.md` 确认仍使用 `agent/create`、Room identity (`room + name`, L52)、`workspace/prepare`（L34）、`created` 状态（L54）。代码已用 workspace-based identity 和 `idle` 状态。 |
| 3 | **同意** | `roadmap.md` 确认仍说仅 agent-shim 已实现（L5），列出 `cmd/agent-shim`、SQLite、`session/*` ARI、`workspace/prepare`。实际 agentd 已实现，用 bbolt 和 `agentrun/*`。 |
| 4 | **同意** | `ari-spec.md` 确认把运行实例生命周期放在 `agent/create`（L148）、`agent/prompt`（L217）等下。`pkg/ari/server.go` L191-209 已暴露 `agentrun/*` 方法，`agent/*` 用于 AgentTemplate CRUD。 |
| 5 | **同意** | Events 章节（L359-369）声明 `agent/update` / `agent/stateChange`。`handleAgentRunAttach`（L1011-1048）仅返回 `{socketPath}`，无 ARI 层事件推送。 |
| 6 | **同意** | ari-spec.md L388 声明 `agentID=` 日志字段。`cmd/agentd/subcommands/workspacemcp/command.go` L252 实际打印 `workspace=`、`agentName=`，环境变量为 `OAR_AGENT_NAME`，无 `agentID`。 |
| 7 | **同意** | `agentd.md` 确认仍用 Agent 表示运行实例、`agent/create`/`agent/prompt`/`agent/stop` 作为生命周期。代码已是 `meta.AgentRun` + `agentrun/*`。 |
| 8 | **同意** | `runtime-spec.md` L24 定义 `created` 状态并在多处使用。`pkg/spec/state_types.go` L8-22 实际定义 `creating`/`idle`/`running`/`stopped`/`error`。`created` → `idle` 漂移已确认。 |
| 9 | **同意** | `runtime-spec.md` L82-89 仍用分离路径。`pkg/agentd/process.go` L459 `stateDir := bundlePath` 且有注释 "State directory is co-located with the bundle directory"。 |
| 10 | **同意** | `runtime-spec.md` State 定义（L14-38）无 `lastTurn`/`exitCode`。`pkg/spec/state_types.go` L66-72 已有 `LastTurn *LastTurn` 和 `ExitCode *int`。 |
| 11 | **同意** | `shim-rpc-spec.md` L33-39 仍有 legacy implementation-lag 段。`pkg/rpc/server.go` L139-151 已使用 `session/prompt`、`runtime/status` 等 clean-break 名称，lag 段已过时。 |
| 12 | **同意** | 示例用 `"status": "created"`（L60, L199 等）。`runtime/history` 文档默认 `fromSeq=1`（L229），代码 `pkg/rpc/server.go` L281 默认 `fromSeq=0`。`session/subscribe` 文档仅记录 `afterSeq`（L150），代码还支持 `fromSeq`（L38-39）提供原子 backfill。 |
| 13 | **同意** | `session/prompt` 确为 shim 内部 RPC。公共 ARI 入口为 `agentrun/prompt`（L192）和 `workspace/send`（L188）。文档应区分内外层。 |
| 14 | **同意** | `agent-shim.md` L13 仍说 "每个 OAR session 对应一个独立的 agent-shim 进程"，L29 描述 M005 从 `session/*` 迁移到 `agent/*`。代码已用 `agentrun/*` 和 clean-break surface，文档 L180 的 legacy PascalCase 描述已不准确。 |
| 15 | **同意** | `agent-shim.md` L87 列出 `session/load`。`pkg/agentd/shim_client.go` L127 有 `Load()` 调用 `session/load`，但 `pkg/rpc/server.go` method dispatch 中未实现 server 端。应标注为可选/未来能力。 |
| 16 | **同意** | `config-spec.md` L186-187 仅列 `"http"` 和 `"sse"`。`pkg/spec/types.go` L91 支持 `"stdio"`，`pkg/agentd/process.go` L361-373 注入 stdio workspace MCP server。 |
| 17 | **同意** | `config-spec.md` L116-117 描述 systemPrompt 为 bootstrap 语义。`pkg/runtime/runtime.go` L152 通过内部 ACP `Prompt` 发送——这是 bootstrap-compatible internal prompt，文档应明确。ARI 层引用的 `session/prompt`（L118, L275）应改为 `agentrun/prompt`。 |
| 18 | **同意** | `design.md` 确认仍使用 `session/new`（L48, L84 等）、`sessionId`（L92, L106 等）、`paused:warm`/`paused:cold`（L107）、`Room`（L131, L154 等）。全部为过时术语。 |
| 19 | **同意** | `design.md` L272-285 列出 S03 Durable State Gaps。`pkg/agentd/process.go` L275-291 已持久化 `ShimSocketPath`/`ShimStateDir`/`ShimPID`/`BootstrapConfig`；`pkg/spec/state_types.go` 已有 `LastTurn`/`ExitCode`。Gap 表应拆分已实现/剩余。 |
| 20 | **同意** | `why-no-runa.md` L58 仍用 `Prompt`/`Cancel`/`Shutdown`/`GetState`。`pkg/agentd/shim_client.go` L17-27 已用 `session/prompt`、`session/cancel`、`runtime/stop`、`runtime/status`。 |
| 21 | **同意** | `workspace-spec.md` L3 说 "one or more sessions"，L159 引用 "Room members" 和 `workspaceId`。代码用 AgentRun、workspace name。 |
| 22 | **同意** | Top-Level Shape 示例（L8-14）空 `metadata`/`source` 有误导性——`pkg/workspace/spec.go` L253 要求 `metadata.name` 非空，L257 要求 `source.type`。ARI `workspace/create` 确实无 hooks 字段（`ari/types.go` L30-39）。 |
| 23 | **同意** | `workspace-spec.md` L135 要求 hook stdout/stderr 可观察。ARI `WorkspaceStatusResult`（`ari/types.go` L57-69）仅有 `Name`/`Phase`/`Path`/`Members`，无 hook 输出字段。`HookError` 仅记录日志未暴露 API。应标为目标缺口。 |

**总结：全部 23 项同意，无异议。** 审查定位精准，每项漂移均经代码行级验证确认。修复建议方向正确，可直接作为文档对齐工作的任务列表。

## 最终方案

