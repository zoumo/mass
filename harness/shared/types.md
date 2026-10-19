---
type: Contract
title: 共享开放值与接受证据
description: Runtime 配置、能力、操作引用、动态回答与接受证据的宽松合同。
status: stable
sources:
- id: direction
  resource: 项目负责人在 2026-10-20 关于其余类型暂时不限制的讨论
  title: 共享类型采用开放定义
---

# 共享开放值与接受证据

准确声明见 [types.tsp](types.tsp)。这些类型已经给出定义，开放字段或 unknown 是有意允许动态值，不是尚未完成的占位。

| 类型 | 表示与责任 |
|---|---|
| RuntimeType | 字符串；Registry 决定支持的类型，不设置全局枚举 |
| RuntimeConfig | Record<unknown>；具体类型解释并校验字段，秘密仅保存引用 |
| CapabilitySet | Record<boolean>；具体 Driver 定义能力名，true 表示支持，false 表示明确不支持，缺失为未宣告 |
| SubmissionRef / WorkRef / InteractionRef | 不透明字符串，不规定 UUID、前缀或可解析结构；有效范围由原 Session 与原运行绑定校验 |
| InteractionAnswer | 任意动态值，可为标量、数组、对象或 null；对应交互负责解释与校验 |
| AcceptanceEvidence / InteractionEvidence | 只报告真实观察的 sent、nativeAccepted、nativeRecorded，各字段可缺省 |

配置可包含 command/args/env/secretRefs 或 url/authRef，但它们不是每种类型都必须支持的字段。能力键例如 session.resume、session.cancel、interaction.answer 只是命名示例，不是封闭列表；未宣告能力不能推断为支持，能力存在也不能绕过当前状态和业务准入。

引用必须与原操作关联，不能根据任意字符串指向恢复后的新工作。输入 receipt 中的 submission 与交互 receipt 中的 interaction 固定接纳目标；它们本身不提供客户端幂等或公开操作查询能力。

接受证据省略字段表示未知，false 表示有明确否定证据。sent 需要实际向原生端交付的证据，仅进入 MASS 本地队列不能填 true。nativeAccepted 表示原生端接受，nativeRecorded 表示原生端记录；后者不是 MASS 历史提交。成功的接受回执必须有 nativeAccepted=true 或 nativeRecorded=true 等实际接受证据，只有 sent=true 或空证据不能宣称接受成功，应按 outcome_unknown 或确定拒绝处理。不观察到某个阶段时不填造布尔值，也不因为后续阶段存在就伪造此前阶段的观察。

接受不表示工作执行完成。InteractionEvidence 复用同一结构，但证据必须对应指定的原交互；过期回答明确拒绝，不能转发给新交互。动态值的具体持久与传输编码仍须在对应绑定中定义，编码失败不能静默丢字段。
