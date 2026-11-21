---
type: Contract
title: Runtime Session 默认配置
description: Runtime 保存 features 与 MCP 默认值，Session 在每次打开操作时按字段和名称合并。
status: stable
sources:
  - id: direction
    resource: 项目负责人在 2026-11-08 确认的 Runtime Session 默认配置与合并规则
    title: features 按字段覆盖，MCP server 按名称覆盖
---

# Runtime Session 默认配置

Runtime spec 顶层的 `features` 与 `mcpServers` 是该 Runtime 下 Session 的默认配置。它们不属于 `RuntimeConfig`，不改变 Runtime 类型专属配置的校验与启动语义。与它们对应的准确结构见 [Runtime 模型](models.tsp)和 [Service 模型](../service/models.tsp)。

Session spec 可以携带同名字段作为显式覆盖；历史 load 的输入也在顶层携带这两个字段。Runtime 启动和 Driver 构造不消费默认值。Service 在 createSession、resumeSession 和 loadSession 选择或启动所属 Runtime 后，读取当前持久 Runtime spec，将默认值与 Session 显式值合并，再把完整的 DriverSessionSpec 交给 Driver。合并结果只用于本次原生操作，不写回 Session spec、状态或恢复材料。

## features

`features` 是带固定字段名的开关集合，当前包含默认开启的 `agentQuestions`。合并按字段进行：

- Session 显式提供某字段时，该字段覆盖 Runtime 默认值。
- Session 省略某字段时，继承 Runtime 默认值。
- Runtime 与 Session 都省略时，使用该字段的 schema 默认值。
- 显式 `false` 是有效覆盖，不能按缺省值处理。

合并后的请求交给具体 Driver。Driver 支持某项功能时得到请求值；请求开启但 Driver 不支持时，`DriverSession.effectiveFeatures` 对应值为 `false`，不因该降级拒绝 Session。请求关闭始终得到 `false`。其他不支持的会话选项仍明确失败。

## mcpServers

每个 MCP server 以 `name` 为合并键。Runtime 默认列表提供基准顺序；Session 同名项替换整个 server 对象并保留基准位置，Session 新名称按 Session 列表顺序追加。最终列表中的 server 名唯一。

- Session 传空列表表示没有覆盖项，不删除 Runtime 默认项。
- 同一个 Runtime 默认列表或 Session 覆盖列表内部不得出现重复名称。
- `stdio` 要求 `command`，可使用 `args`、`env`、`envFrom` 和可选 `cwd`；`http` 与 `sse` 要求 `url`，可使用 `headers` 和 `headersFrom`。各 transport 不接受另一组字段。
- `envHeaders` 与 `bearerTokenEnvVar` 是 HTTP 的可选认证配置；SSE 不接受这两个字段。具体 Runtime 类型决定 transport、字段和名称约束；不支持项在该 Session 的原生副作用前拒绝。
- `url` 只接受 HTTPS；HTTP 只允许回环主机。URL 不得包含 userinfo 凭据。
- `env` 和 `headers` 的条目使用 `name`，并提供 `value` 或 `valueFrom.fileKeyRef`。HTTP 请求头可以使用字面值。凭据的暴露范围见 [ADR-0002](../../adr/0002-mcp-http-header-values.md)。
- 文件引用使用 massd 所在主机上的绝对路径。文件以 `KEY='value'` 定义条目。`fileKeyRef` 读取指定 `key`。`envFrom` 和 `headersFrom` 的 `fileRef` 导入所有键值，后列出的来源覆盖先列出的来源，显式条目优先。Header 名比较时忽略大小写。
- 每次原生 open、resume 和 load 重新读取文件。文件或指定的键缺失时，该次操作失败。解析值只用于该次原生配置，持久 spec 保存文件引用。

Runtime 写入时校验 provider-neutral 的结构、transport 字段组合和列表内名称唯一性。类型专属支持由对应 Driver 在 open、resume 或 load 时校验。MCP 配置只声明原生 Session 使用的 server；打开成功不证明 server 已连接、工具可用或远端身份可信。

合并后的列表为 `nil` 时表示 Runtime 与 Session 都未声明 MCP 配置，provider 可继续使用自身环境中的配置。合并结果为非 `nil` 时，包括空列表，表示本次 Session 的完整有效 MCP server 集合；adapter 必须排除 provider 用户、项目或本地配置中的其他 server。

- Claude 显式列表使用 native strict MCP configuration。
- Codex 没有等价 strict 开关。Runtime 必须显式提供不同于 ambient 的、已存在的绝对 `env.CODEX_HOME`；该目录的 `config.toml`（若存在）不得在任意层级声明 `mcp_servers`。create、resume 与 load 还必须在线程配置中把 Session cwd 及其祖先标记为 untrusted，排除项目级 `.codex/config.toml`。不满足时在创建或恢复原生 thread 前失败。Codex 不展开普通 `headers` 或 stdio `env` 中的 `${ENV_VAR}`；adapter 必须映射其原生环境变量引用能力或解析后传入，不能把未解析引用当作有效凭据。

## 生效与恢复

Runtime 更新采用完整替换。直接通过 RuntimeService/JSON-RPC 更新时，省略 `features` 或 `mcpServers` 会清除对应默认值；CLI 的部分更新流程先读取当前 spec，未提供对应 flag 时保留旧值，显式 `null` 清除。Runtime 自己的 `mcpServers: []` 是已声明的空默认列表，不提供默认 server，但合并结果仍为非 nil，因此会排除 ambient server；Runtime 省略 `mcpServers` 则保留 ambient。Session 自己的空列表只表示没有覆盖项，仍继承 Runtime 默认项。

修改 `features` 或 `mcpServers` 不需要重启 Runtime。下一次 createSession、resumeSession 或 loadSession 读取最新持久 spec 并应用，已经打开且仍有效的 Session 不被热更新。Session 未显式覆盖的值也不会在打开时复制进 Session spec，因此后续 Runtime 更新仍可在下一次打开操作中生效。

Runtime 的 `config` 仍按运行绑定快照影响 Driver 构造和启动；Session 默认值是独立于该快照的每次操作输入。修改默认值不改变当前 Driver 的类型配置、`appliedGeneration` 或既有 Session 的当前运行绑定。

恢复材料不得把上一次解析出的 `features` 或 `mcpServers` 当作原生身份或恢复目标的一部分。Runtime 默认值改变后，resume 或 load 仍验证原 Runtime 归属、cwd、原生身份和稳定恢复材料，然后带上本次重新合并的 Session 选项建立原生连接；默认值变化本身不构成恢复身份不一致。

## 验收要求

- Runtime start 和 Driver 构造不读取或应用 `features`、`mcpServers`。
- Runtime 更新后无需重启，下一次 create/resume/load 使用新默认值；已经打开的 Session 保持原运行。
- feature 字段分别验证继承、同名覆盖、显式关闭及 schema 缺省。
- MCP 验证同名替换保持位置、新名称按 Session 顺序追加、空 Session 列表不清空默认项，以及重复名称被拒绝。
- 显式 MCP 列表为空时，Claude strict mode 和 Codex 隔离目录都必须排除 ambient server；Codex 未配置隔离目录时明确失败。
- 非回环明文 URL、URL userinfo、SSE 的 HTTP-only 认证字段必须被拒绝。
- 文件引用校验覆盖目录边界、缺失文件与键、合并优先级及恢复时重新读取。
- 显式 Session MCP 在 Runtime 启动和原生副作用前完成公共结构校验；类型不支持在本次原生副作用前失败。
- 解析后的默认值不进入 Session spec 或持久恢复材料；改变默认值后仍可恢复原 Session。
