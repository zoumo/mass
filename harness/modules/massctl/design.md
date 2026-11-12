---
type: Module Design
title: massctl 模块设计
description: 定义面向人和脚本的资源式命令、参数寻址、结果输出及生命周期边界。
status: draft
sources:
  - id: direction
    resource: 项目负责人在 2026-10-20 的 massctl 设计讨论
    title: 资源与操作采用 subcommand，并在 harness 中定义 CLI 能力
---

# massctl 模块设计

## 状态与范围

负责人已明确命令采用资源、操作两级 subcommand，并确认 runtime create 使用名称位置参数、--type 与常用配置 flag，配置未覆盖时使用 --config-file 或 --config-json。其余内容为能力与交互草案，供逐项讨论；未确认条目不表示已经定稿，本页也不表示实现已经提供这些能力。[^direction]

## 职责与依赖

语言实现的程序入口与可复用模块遵循对应语言的目录规范；Go 见 [Go 目录规范](../../principles/golang/application-directory-structure.md)。

massctl 是面向终端用户和脚本的客户端模块，提供 `massctl <resource> <operation> [target] [flags]` 形式的 interface。模块隐藏请求构造、Session 引用转换、WebSocket 认证与连接、JSON-RPC 响应及订阅通知，使调用者无需理解内部操作字符串或调用 envelope。

massctl 通过 [JSON-RPC over WebSocket](../../bindings/websocket.md) 调用 [RuntimeService](../service/runtime-service.tsp) 与 [SessionService](../service/session-service.tsp)。业务入口为 `/rpc`。Service 拥有业务校验、生命周期、持久化和操作编排；massctl 不直接访问 Store、Registry、Driver、原生 Agent 或服务端数据目录，不维护另一套资源状态。

模块拥有命令发现、参数校验、输入读取、请求等待、终端输出与进程退出行为。session create/resume 由 Service 在业务校验后按需启动所属 Runtime，CLI 不额外发送 runtime start。查询、历史和 watch 不触发启动；prompt 不隐式恢复离线 Session。不隐式发送初始 prompt、不重试副作用请求，不通过停止共享 Runtime 处理单 Session 错误。

## 能力清单与命令

资源名称建议使用单数。除 `version` 等本地元命令外，操作归属于资源子命令。

### Runtime

| 命令 | 输入 | 能力与结果 |
|---|---|---|
| `runtime create NAME` | `--type TYPE`、常用配置 flag；补充输入支持 `--config-file FILE` 或 `--config-json JSON`；可选 `--features-json`、`--mcp-servers-json` | 创建命名资源并设置 Session 默认值，返回 Runtime；不启动 |
| `runtime get NAME` | 无业务请求体 | 查询资源、期望配置、当前状态与已协商能力 |
| `runtime list` | 无筛选参数 | 查询全部 Runtime，不分页；首版不提供筛选（已确认） |
| `runtime update NAME` | 配置 flag、`--config-file FILE` 或 `--config-json JSON`；可选 `--features-json`、`--mcp-servers-json`；不提供 --type | CLI 先获取当前 spec，保留 type 与未修改的 Session 默认值；flag 更新对应字段，完整 config 替换原配置，再提交完整 RuntimeSpec；不热更新、不重启（流程已确认） |
| `runtime start NAME` | 无业务请求体 | 启动或取得当前运行状态，不隐式重启已运行实例 |
| `runtime stop NAME` | 无业务请求体 | 通过 Service 排空并释放所属运行资源 |
| `runtime delete NAME` | 无业务请求体 | 删除实例记录，保留 Service 的引用和释放检查 |

runtime start NAME 的规则已确认：不增加专属参数。已停止时按最新期望配置启动；已运行时返回当前 Runtime，不因配置更新而重启。成功返回 Runtime，json 模式输出完整资源。首版不提供 --restart；需要应用新配置时显式执行 runtime stop NAME，再执行 runtime start NAME。

runtime stop NAME 的规则已确认：不增加专属参数，由 Service 协调会话排空与运行资源释放。成功返回 Runtime，停止后保留配置、Session 身份与历史。结果未知或超时明确报错，不宣告已停止。首版不提供 --force，不绕过释放检查。

runtime delete NAME 的规则已确认：不增加专属参数，只删除 Runtime 资源记录，不能删除正在运行的 Runtime，也不自动调用 stop；运行中的实例必须先完成 runtime stop，再执行删除。存在 Session 引用或尚未释放的运行资源时，由 Service 拒绝删除；CLI 的本地状态判断不能替代 Service 的最终检查。不级联删除 Session 和历史，不提供 --force，不交互确认。成功时 human 模式显示删除提示，json 模式 stdout 留空，退出码为 0。

Runtime 类型由配置合同决定；CLI 不自行增加类型发现操作，也不把类型名当作 Runtime 实例名。

已确认的创建输入形式为 `massctl runtime create NAME --type TYPE [配置参数]`，不将类型增加为 create 下的子命令。首版提供 --executable PATH，映射 config.executable；其他配置字段使用 --config-file 或 --config-json 输入完整 RuntimeConfig。测试专用 scenario 不作为正式 CLI flag。此首版字段清单不限制后续扩展。

`--features-json` 接收 SessionFeatures JSON 对象或 `null`，`--mcp-servers-json` 接收 MCPServer 数组或 `null`。它们写入 RuntimeSpec 顶层，不进入 RuntimeConfig，也不替代配置输入。Runtime 只保存和校验公共结构，不启动 MCP 或改变 Runtime 运行；默认值由后续 session create/resume/load 与 Session 显式值合并。

创建必填项与默认行为已确认：NAME 与 --type 必填，不默认选择 Runtime 类型。配置参数可省略，全部省略时提交空配置 `{}`，由对应 Runtime 类型校验并应用其默认值；CLI 不自行填充类型专属默认配置。Runtime 创建只登记资源，不启动运行资源；后续 session create/resume 由 Service 按需启动，也可提前使用独立的 runtime start 命令。

更新流程已确认：runtime update NAME 先获取 Runtime，使用当前 spec.type 解释配置参数，不要求或提供 --type。配置输入分为两种语义：

- 显式配置 flag（首版为 --executable）直接写入或覆盖对应配置字段，其余字段保留。只有用户实际传入的 flag 参与更新，不以 flag 默认值覆盖旧配置。
- --config-file 或 --config-json 输入完整 RuntimeConfig，直接替换原 spec.config；未出现在新 config 中的旧字段不保留。CLI 不将此输入解释为递归合并或 JSON Merge Patch，null 的有效性由配置合同判断。
- `--features-json` 与 `--mcp-servers-json` 是独立的 Session 默认值输入，可与上述任一配置输入组合。更新时未传对应 flag 保留当前默认值，显式 `null` 清除；create 未传则不写该默认值。

两种方式都保留原 type，最终调用 updateRuntime 提交完整 RuntimeSpec。获取失败时不提交更新；不将部分配置直接发送为 runtime.update 的 spec。此处为 CLI 的读取、构造、写回能力，不改变 Service 的完整替换合同。更新不自动重启或热更新当前运行。

输入组合规则已确认：配置 flag、--config-file、--config-json 三种方式互斥；多个配置 flag 可组合。Session 默认值 flag 不属于这三种配置方式，可与其中任意一种组合。runtime update 未提供任何配置或 Session 默认值输入时为参数错误，不发起更新。create 的类型配置全部省略时仍按已确认规则提交 `{}`。首版不提供 -f resource.json 等完整资源文件创建入口。

协议当前不提供条件更新，读取与提交之间不是原子事务；并发更新可能覆盖其他调用方的变更，CLI 不能声称这是受资源版本保护的修改。

### Session

下表的 TARGET 为位置参数 NAME_OR_ID，使用下一节已确认的统一寻址规则。

| 命令 | 输入 | 能力与结果 |
|---|---|---|
| `session load NAME` | 必填 `-n NAMESPACE --runtime NAME --cwd ABS_PATH --session-id NATIVE_ID`；可选 `--model`、`--effort`、`--features-json`、`--mcp-servers-json` | 异步恢复同一会话并回放到新空目标；显式值成为新 Session 覆盖项；本地完成后直接继续输入 |
| `session cancel-load TARGET` | 必填 `--load-id ID` | 取消原历史读取，返回当前状态；通过 get 查询最终结束 |
| `session create NAME` | 必填 `-n NAMESPACE --runtime NAME --cwd PATH`，可选系统提示、额外目录、`--model`、`--effort`、`--features-json` 与 `--mcp-servers-json` | 创建并登记 Session，返回持久身份及可选输入回执；仅显式输入发送一次 |
| `session set-config TARGET` | 必填 `--id model\|effort --value VALUE` | 当前主 Session 瞬态单项更新，返回 ConfigReceipt，不回写 spec |
| `session get TARGET` | 无业务请求体 | 查询资源，不隐式恢复 |
| `session list` | 只保留分页参数 `--limit`、`--after` | 查询全部 namespace 与 Runtime 的持久目录，包含离线会话；首版不提供筛选（已确认） |
| `session resume TARGET` | 可选互斥 `--text TEXT` 或 `--input-file FILE` | 恢复原身份，可显式提交一次新输入；失败不新建替代 |
| `session close TARGET` | 无业务请求体 | 从任意状态 close Session；成功为 offline，保留身份、历史与恢复材料 |
| `session prompt TARGET` | 必填 `--text TEXT` | 首版只发送文本，返回 InputReceipt；不支持文件或 JSON 输入（已确认） |
| `session cancel TARGET` | 无业务请求体 | 固定接纳时工作并请求取消，返回 CancelReceipt |
| `session history TARGET` | 只保留分页参数 `--limit`、`--after` | 查询已提交历史及同步进度；首版不提供类型筛选（已确认） |
| `session watch TARGET` | 无专属参数、无业务请求体 | 建立独立实时订阅，观察订阅建立后的事件；不补历史、不自动重连，Ctrl-C 只关闭观察（已确认） |

Session 创建参数已确认：NAME、-n/--namespace、--runtime 与 --cwd 必填；可选 --system-prompt TEXT 或 --system-prompt-file FILE（二者互斥），以及可重复 --additional-dir PATH，分别对应 systemPrompt 和 additionalDirs。`--features-json` 与 `--mcp-servers-json` 写入 Session spec 的显式 features/mcpServers，与当前 Runtime 默认值合并后交给 Driver；CLI 不回写合并结果。创建省略输入时不发送 prompt；可选互斥 --text TEXT 与 --input-file FILE，后者读取 UTF-8 SessionInput JSON 并保留内容块结构。输入不进入 spec 或恢复配置，receipt 不代表完成；不支持字段在副作用前拒绝。文件 - 读取 stdin，两个文件参数不得同时读 stdin。cwd 与额外目录按 Agent 所在环境解释，不以本机路径是否存在决定有效性；额外目录不授予权限。

`session close` 使用 SessionService.closeSession 的同一业务合同。命令只接收 TARGET，以 params.session 发送引用；成功时输出 `offline` Session，失败或结果未知时以非零状态退出。重复执行同一 close 是安全幂等重试，完整生命周期语义见 [Session close](../service/design.md#session-close)。

Session 不提供持久更新、删除、任意 ACP 配置选项写入和待决交互补查。主配置支持下述已确认的初始值与瞬态 setter。`close` 不提供 `delete` 别名；本版本不保留 archive 命令、兼容别名或回退路径。进程内 EventHandler 管理不作为 CLI 远程能力暴露。

Session 恢复可选互斥 --text TEXT 或 --input-file FILE，使用与创建相同的 SessionInput 读取及校验规则；省略保持零输入，显式空输入拒绝。这是新输入，不是历史或恢复配置；同一原生 ID/cwd 验证前不报告成功，receipt 不表示完成，结果未知不自动重试。

session resume 不提供 features/mcpServers flag：Session 没有 updateSession，恢复使用已持久化的 Session 显式值，并在调用 Driver 前与当前 Runtime 默认值重新合并。

session prompt 的首版输入范围已确认：仅提供 --text TEXT，将文本构造为 SessionInput 中的一个 text ContentBlock；不提供 --input-file、--input-json、多模态内容输入或隐式 stdin 读取。此限制属于 CLI 首版范围，不缩减 Service/JSON-RPC 的 SessionInput 合同。answer 延后讨论，首版不暴露命令，不将先前建议的参数作为已确认合同。

### 本地元能力

- `massctl --help`、资源帮助和操作帮助：展示可用命令、必需参数、互斥规则与示例；未知命令或多余参数报错。
- `massctl version`：显示本地客户端版本，不把客户端版本当成服务端版本。
- 不带操作的根命令或资源命令只显示帮助，不发送业务请求。

元能力范围已确认：提供 help、version，首版不增加健康检查命令、配置管理命令和 completion 命令。不以 Runtime 列表或 `/readyz` 成功宣告模型可用。

## 统一目标寻址

Runtime 使用位置参数 NAME，名称全局唯一，不接受 namespace。

Session 目标统一使用位置参数 NAME_OR_ID，不提供目标 ID flag，不设置默认 namespace；set-config 的 --id 只表示配置项。所有针对已有单个 Session 的命令（get、resume、close、cancel、prompt、answer、history、watch、set-config）采用同一已确认规则：

```sh
massctl session get review -n demo
massctl session get '原始会话ID'
```

- 显式提供 `-n/--namespace` 时，位置参数按该 namespace 下的名称解释；namespace 必须非空。
- 不提供 namespace 时，位置参数按全局 sessionId 解释；不依据字符串形状猜测类型，也不执行名称与 ID 之间的查询回退。
- CLI 接受原始 sessionId 与交互引用，由客户端按 JSON 字符串原样传输；不做 URL 或 base64url 编码，不裁剪或规范化原始 ID。
- `session create` 只使用 namespace/name，sessionId 由 Agent 返回。
- `session list` 不接收目标位置参数和 namespace，首版只接受分页参数，不提供业务筛选。

## 输入与参数校验

常用字段使用显式 flag，复杂或未覆盖的 Runtime 配置支持文件和内联 JSON 输入。命令不接受 `--op` 或要求用户构造统一 Params envelope。

| 输入参数 | 内容 |
|---|---|
| `--config-file` | RuntimeConfig 本身 |
| `--config-json` | 内联 JSON 编码的 RuntimeConfig 本身 |
| `--features-json` | SessionFeatures 对象或 `null`；Runtime 命令设置默认值，Session create/load 设置显式覆盖 |
| `--mcp-servers-json` | MCPServer 数组或 `null`；Runtime 命令设置默认列表，Session create/load 设置显式覆盖 |
| `--system-prompt` | 文本，原样作为 systemPrompt，与 --system-prompt-file 互斥 |
| `--system-prompt-file` | UTF-8 文本，原样作为 systemPrompt |

所有文件参数支持 `-` 读取 stdin；一次调用最多一个输入源占用 stdin。无文件参数的查询与控制命令不读取 stdin。JSON 文件只接受一个完整值，拒绝尾随额外值；固定字段和开放字段的校验边界沿用 JSON-RPC 与业务合同。

prompt 的 --text 值直接作为文本，不解释为文件路径或 JSON。缺少必需输入、重复单值参数、非法分页值及互斥参数在发请求前报错。answer 的输入形式与校验规则延后讨论。

## 连接、认证与请求等待

全局参数已确认：`--url URL`、`-o/--output human|json`、`--timeout DURATION`。服务地址优先级为显式 --url、环境变量 MASS_URL、默认 ws://127.0.0.1:24368/rpc；--url 指定完整 WebSocket endpoint，不自动补 REST 路径、探测其他实例或回退 HTTP。

认证从 `MASS_TOKEN` 读取，按 WebSocket Upgrade 合同放入 Authorization 头，不提供明文 token 命令行参数，不在输出、错误或诊断中泄露秘密。部署、TLS 与实例级权限遵循 WebSocket 合同；CLI 不赋予 namespace 租户隔离含义。

客户端只连接已配置的服务，不负责拉起或停止 massd。普通请求复用普通 RPC 连接；watch 使用独立订阅连接，同一客户端多个观察分别建连和认证。订阅 Close/Ctrl-C 只释放该观察；客户端整体关闭才释放自己拥有的全部连接，不取消 Agent 工作。慢观察连接被驱逐不得连带关闭其他观察或普通 RPC。

超时规则已确认：普通请求默认 30 秒，--timeout 0 表示不限时；watch 只限制建连及等待 initialize 和 session.subscribe 成功响应的时间，不限制就绪后的订阅持续时间。请求截止或进程中断只结束本次等待，不自动取消 Agent 工作、close Session 或停止 Runtime。没有可验证响应的副作用请求按结果未知处理，不自动重发。

## 输出与退出

默认 human 输出与 -o json 已确认：列表使用表格，资源查询使用摘要，操作显示对应资源或回执。表格应保留名称、namespace（适用时）、Runtime 归属与状态，Runtime 摘要区分期望 generation 与 appliedGeneration。

`-o json` 输出完整业务结果，不增加统一成功 envelope；Session 列表保留 items/next，历史保留 events/next/syncedEventId。Runtime 删除成功没有业务结果，不制造空对象或 null 响应。

stdout 只承载结果，stderr 承载错误、订阅 ready 和诊断。JSON 模式下错误以 OperationError 结构输出到 stderr，保留 code/reason/message；本地参数和文件错误也需有稳定分类，具体取值待定。错误不依赖解析 message 来分类。

输入成功只表示回执定义的接受证据，保留 acceptance 中缺失、false、true 的区别；取消确认不表示工作已经结束。人类输出不能把这些回执显示为“任务完成”。

退出码已确认：成功 0，一般失败（包括业务错误、传输错误与结果未知）1，参数错误 2，Ctrl-C 130。错误格式和退出码是脚本 interface 的一部分，需保持稳定。

## 分页、历史与实时观察

分页默认值已确认：--limit 默认 100，--after 接续上一页游标。Session 列表与历史默认请求一页，保留服务端 next，不自动翻页；human 模式也必须显示可继续分页的游标。after 不接受 eventId，也不在 CLI 内解析。首版两者都只提供 --limit、--after，不提供筛选；JSON-RPC/Service 的过滤能力保持不变。

watch 在独立 WebSocket 完成 initialize 和 session.subscribe 后确认就绪，再向 stderr 输出就绪提示；json 模式下 stdout 每行输出一个原始 SessionEvent，不混入 ready、心跳或控制帧。human 模式展示同一流的可读事件，不拼造完整历史。

历史查询与实时订阅是独立操作，不提供暗示无缝衔接的 `history --follow`。watch 不补历史、不自动重连；正常关闭成功结束，异常关闭、队列溢出和意外 EOF 明确失败。idle 或一次输入结束不自动终止观察。Ctrl-C 释放本订阅，不发送 cancel，退出码为 130。

需要完整观察发送后的进展时，调用者先建立 watch 并确认 ready，再单独发送 prompt；回执与跨连接事件不保证到达次序。本版不提供隐含这一编排的 `prompt --wait/--watch`。

## 验收场景

| 场景 | 必须观察到的行为 |
|---|---|
| 根、资源、操作帮助 | 可发现全部已实现操作；缺少操作不调用服务；未知命令、多余参数失败 |
| 名称与 ID 寻址 | 带 namespace 按名称，不带按 ID；所有单会话命令一致，无默认 namespace 和查询回退；特殊字符 ID 无损传递 |
| Runtime 创建、更新与启停 | 创建不启动；更新只替换期望 spec；停止通过 Service 协调，不绕过引用与释放检查 |
| Session 按需启动与重启后查询 | create/resume 无需先 start；查询、history、watch 不启动；重启后不显示旧 idle 为当前可用，按 Service 校正结果返回 offline/unknown |
| Session close | close 从任意状态调用同一 JSON-RPC 方法；成功为 offline，失败为 closing 且可重试，offline 重试成功，不停止 Runtime |
| CLI 配置更新 | 先获取当前 spec 并保留 type；显式 flag 只覆盖对应字段，完整 config 替换全部配置，最后提交完整 spec；获取失败不写回，不宣称具备并发条件更新保护 |
| CLI Runtime Session 默认值 | create 写入顶层 features/mcpServers；update 未传保留旧值、显式 null 清除；不进入 config，不启动或热更新 Runtime |
| CLI Session 覆盖 | create/load 的 flags 只写 Session 显式值；空 MCP 数组不删除 Runtime 默认项，fetch 或构造失败不发起副作用请求 |
| 查询离线 Session | 查询和历史不激活 Runtime 或恢复 Session；prompt 失败并要求先显式 resume |
| 文本输入 | --text 正确构造单个 text 内容块；首版拒绝 prompt 文件/JSON 输入，不隐式读取 stdin |
| 分页 | Session 列表与历史都只提供分页，游标准确传递；保留 next/syncedEventId，不默认隐藏后续页 |
| 输入与取消回执 | 保留原始接受证据和取消结果，不输出工作完成的虚假结论 |
| 副作用请求断线或超时 | 不重试，不自动清理业务资源，不宣告确定未执行 |
| watch ready、事件与终止 | 控制帧不混入事件 JSONL；异常明确；关闭订阅后 Agent 工作继续 |
| 机器输出与错误 | stdout 可独立解析；错误在 stderr，code/reason/message 与非零退出一致 |
| 认证与连接 | 使用 `/rpc` 和 Upgrade Bearer 头；秘密不进入 URL、日志或输出 |

CLI/JSON-RPC 合同可通过隔离的服务或 WebSocket 协议替身验证；涉及原生输入、取消、close、恢复和观察期间继续工作的端到端结论，仍需按[测试验收原则](../../principles/testing.md)执行真实 Agent 验收。帮助、构建和离线测试通过不代表 Agent 能力已验证。

## 待决清单

1. 本地参数、文件读取等错误的具体 code/reason，以及 human 输出具体字段仍可细化；实现应遵循已确认的错误结构、输出通道和退出码。
2. answer 延后且不纳入首版；不自动引入交互式聊天或 TUI。

页面保持 draft，以便继续细化上述条目。CLI 语法和进程 I/O 在本模块维护；业务数据结构继续引用现有 TypeSpec，不为相同资源复制 CLI DTO。

[^direction]: 本次讨论明确要求资源与操作采用子命令、新增 harness 模块，并确认 runtime create 暴露常用配置 flag、以 --config-file 或 --config-json 覆盖其余配置；未确认条目保持草案。

## 异步原生 load

load 与 cancel-load 遵循[异步历史设计](../service/load.md)。load 成功退出只表示接纳，输出 loadId 及 Session，不写“历史已全部完成”；load 本身恢复同一会话并回放，成功后无需额外 resume；不提供隐式等待或自动重试。load 的 `--features-json` 与 `--mcp-servers-json` 作为新 Session 显式值持久保存，并与当前 Runtime 默认值合并用于本次恢复。session get 查询 status.historyLoad，区分原生验证/穷尽、本地完成、失败和取消。--timeout 只限制请求等待；Ctrl-C 不自动取消已接纳回放，取消用 cancel-load 和原 loadId。session history 仍只读已提交记录，watch 只观察订阅之后同一 Broadcast 的事件。

## 主 Session 配置

session create/load 增加可选 `--model VALUE` 与 `--effort VALUE`，映射到初始 config，
非空值原样传递，显式空字符串按 SDK 省略；非法 UTF-8/NUL 在请求前拒绝。
load 保存本次显式值；resume 使用持久初始值，不提供配置覆盖 flags。
Runtime 不增加 model/effort 默认值。

`session set-config TARGET --id model|effort --value VALUE` 使用既有 TARGET 寻址；
此处 --id 是配置项 ID。value 非空，逐项直接转发，JSON 输出 ConfigReceipt，失败非零退出。
setter 不写回 spec，不等待 idle、不重启、不重试；成功不声明当前值生效，
close/resume 重新请求初始值。失败使用既有三字段错误，不伪造 receipt。
