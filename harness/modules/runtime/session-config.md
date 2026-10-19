---
type: Contract
title: Session 配置选项
description: ACP 风格的选择、分组选择与布尔选项，以及完整列表更新。
status: stable
sources:
- id: acp
  resource: https://agentclientprotocol.com/protocol/v1/session-config-options
  title: ACP 当前配置选项
---

# Session 配置选项

准确声明见 [session-config.tsp](session-config.tsp)。采用 ACP 当前的 select 和 boolean，不自行假设 ACP 已支持任意输入控件。id、name 必填，description、category 可选；category 是开放展示分类，例如 mode/model/model_config/thought_level，不参与执行正确性。

select 的 currentValue 为字符串选项 ID，options 为平铺 ConfigOptionValue[] 或分组 ConfigOptionGroup[]，同一数组不混用两种形态。每个值包含 value、name、可选 description；每组包含 group、name、options。boolean 的 currentValue 为布尔值，不提供 options。具体 adapter 验证 ID 唯一、currentValue 对应可用选项、分组一致性等业务约束；TypeSpec 不负责全部跨字段校验。

保持 configOptions、分组与选项的原生展示顺序。session.config_option_update 携带全部选项及当前值的完整快照，消费者替换之前的配置列表，空数组明确清空；它不是消息 delta，不采用文本追加规则。

在 ACP 连接上，boolean 选项需要原生 Client 宣告支持，adapter 负责相应协商，不能仅凭 MASS 类型存在就宣称上游支持。未知 category 不影响配置解释；未来新增控件类型时再扩展联合，不静默改写为 select。

本次只补齐观察结构，未增加公共 setConfigOption 或 JSON Patch。RuntimeSpec 全量 update 与这些 Agent 会话配置选项是不同的操作；不能以修改 RuntimeSpec 代替对原生会话选项的设置。
