---
type: Contract
title: Session 配置选项
description: 主 Session 初始配置、运行时单项更新与 ACP 配置观察。
status: stable
sources:
- id: acp
  resource: https://agentclientprotocol.com/protocol/v1/session-config-options
  title: ACP 当前配置选项
---

# Session 配置选项

本页规定主 Session 初始与瞬态配置，并描述 Agent 暴露的 ACP `configOptions` 及其更新快照；不描述 Runtime
`features` 与 `mcpServers` 默认值；后者的归属和合并规则见
[Runtime Session 默认配置](session-defaults.md)。

准确声明见 [session-config.tsp](session-config.tsp)。采用 ACP 当前的 select 和 boolean，不自行假设 ACP 已支持任意输入控件。id、name 必填，description、category 可选；category 是开放展示分类，例如 mode/model/model_config/thought_level，不参与执行正确性。

select 的 currentValue 为字符串选项 ID，options 为平铺 ConfigOptionValue[] 或分组 ConfigOptionGroup[]，同一数组不混用两种形态。每个值包含 value、name、可选 description；每组包含 group、name、options。boolean 的 currentValue 为布尔值，不提供 options。具体 adapter 验证 ID 唯一、currentValue 对应可用选项、分组一致性等业务约束；TypeSpec 不负责全部跨字段校验。

保持 configOptions、分组与选项的原生展示顺序。session.config_option_update 携带全部选项及当前值的完整快照，消费者替换之前的配置列表，空数组明确清空；它不是消息 delta，不采用文本追加规则。

在 ACP 连接上，boolean 选项需要原生 Client 宣告支持，adapter 负责相应协商，不能仅凭 MASS 类型存在就宣称上游支持。未知 category 不影响配置解释；未来新增控件类型时再扩展联合，不静默改写为 select。

任意 ACP setConfigOption 与 JSON Patch 不属于公共写入能力。RuntimeSpec 全量 update 与主 Session 配置是不同操作，不能相互替代。

## 初始配置与恢复

DriverSessionSpec 及 SessionSpec 可提供 `config: {model?, effort?}`，SessionLoad 提供同形字段。
仅接受对象和字符串；省略、空对象及字段空字符串按 SDK 省略，null、非字符串、NUL、
非法 UTF-8 或不能编码的值在任何原生副作用前拒绝。非空值不 trim、不改大小写、不解释
别名、枚举或 `default`，直接传入 SDK Open/Resume 的 Config。Runtime 不提供 model/effort 默认值。

初始显式配置持久保存在 Session spec，resume 重新请求这些初始值，不提供临时覆盖。
load 保存本次显式值，并在同一原生恢复连接使用它们。可变配置不属于恢复身份；
ID、cwd、账号、数据归属及原生恢复材料验证仍然必需。旧 spec 省略 config 保持可读，
不迁移个人运行数据。初始配置不是当前生效值快照。

## 运行时单项更新

SessionService.setSessionConfig 通过可选 SessionConfigDriver 定位原 SDK Session，断言
SDK ConfigController 后直接调用 SetConfig。ConfigChange 为 `{id: "model" | "effort", value: string}`，
value 必须非空、UTF-8 有效且不含 NUL；`default` 仅是普通原生字面值。每次只设置一项，
重复值仍派发。未实现接口返回 unsupported；SDK Capabilities map 没有配置项，不虚构能力。
接口存在不保证每次原生更新可用；Codex experimental API 限制及 Claude effort 处理由 SDK 负责。

setter 仅作用于当前主 Session 运行，不写回 spec、generation、appliedGeneration、恢复材料，
不缓存当前值或接受回执。初始 A、在线 B、close/resume 后重新请求 A。成功只表示请求及接受
阶段，不承诺当前 turn 切换；不从文本、耗时或 ACK 推断生效，不制造配置事件或完整目录。
ACP 完整观察仅在 SDK 提供完整原生观察时继续转发。

Service 接纳时一次解析持久身份，固定 runBinding、Driver、Session/source，并承担原运行在途责任。
仅当前有效在线运行可派发，准入沿用 prompt（包括首次状态观察前已验证 live 绑定的例外）。
offline、detached unknown、opening、closing、未完成 load 及 Runtime stop 栅栏拒绝；不隐式 start/resume。
active/running/requires_action 无需 idle。close/stop 封闭新更新；已接纳更新仍归原运行，
旧响应不能改写后来运行。网络调用不持有全局锁；取消调用者 context 不证明未执行。

不建立配置队列，不与 prompt/cancel/answer 隐式排序，不等待 idle、不重启、不自动重试、
不增加 submission binding。并发更新无全局顺序或两项原子性；需要先 model 后 effort 再 prompt
的调用者逐次等待回执。

## 接受证据与失败

ConfigReceipt 包含原请求 change 与 AcceptanceEvidence，保留缺失/false/true。
未发出 sent=false；明确原生拒绝 sent=true/nativeAccepted=false；派发未知 sent=true 且
nativeAccepted 缺失。SDK 证据较弱时 MASS 不补强，不按本地入队填 sent。
Go 保留 receipt+error；HTTP 失败只返回既有 code/reason/message，不增加 receipt envelope。

参数无效 invalid_argument/400；不支持 unsupported/422；无有效运行或栅栏 conflict/409。
SDK 明确拒绝保留 native_rejected code/reason，HTTP 为 422；确定未写入的 deadline_exceeded
为 504，派发后未知为 outcome_unknown/500。ErrorClosed/FailedPrecondition 沿用 conflict；
未知错误不得包装为可重试 unavailable。远端未收到可验证响应按未知处理，不重发。

## SDK 原生映射

| 配置 | Codex | Claude |
|---|---|---|
| 初始 model | SDK Open/Resume 的 Config → 原生 model | SDK Open/Resume 的 Config → --model |
| 初始 effort | SDK → model_reasoning_effort | SDK → --effort |
| 在线 model | SDK SetConfig → SetModel/thread settings update | SDK SetConfig → set_model |
| 在线 effort | SDK SetConfig → SetThinking/thread settings update | SDK SetConfig → get_settings 预检与 apply_flag_settings |

这些原生转换由 SDK 负责；MASS 不复制协议、effort 转换或 ultracode 规则。Claude resume
遵守 SDK 的 CLI 2.1.258 准入，不因其他 CLI 可启动而宣称恢复可用。
