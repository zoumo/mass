---
type: Documentation Convention
title: MASS Harness 知识管理约定
description: 定义唯一真相源、OKF 知识组织、写入授权、来源、审阅和维护规则。
status: stable
generated:
  by: codex/gpt-6
  at: 2026-10-19T20:38:35+08:00
sources:
  - id: okf
    resource: https://github.com/GoogleCloudPlatform/open-knowledge-format/blob/main/SPEC.md
    title: Open Knowledge Format v0.2
  - id: direction
    resource: "项目负责人确认的设计方向"
    title: 语言无关设计、负责人定义合同、使用 harness
  - id: runtime-direction
    resource: "项目负责人在 2026-10-19 本次 Runtime 职责讨论中的说明"
    title: Service 对 Store 和 Runtime 的依赖
---

# MASS Harness 知识管理约定

## 设计权威

`harness/` 是指导当前项目演进和发展的唯一真相源。它同时维护 agent 工作原则与当前架构、深层模块、API 和数据合同。仓库 README 与语言专属资料描述现有实现时，不得覆盖本包设计。

设计描述语言无关的 deep module：职责、所有权、不变量、状态变化、协作和失败规则。公共 interface、HTTP 协议、事件与持久化格式由项目负责人定义。所有语言实现必须遵守同一设计及其确认后的合同。[^direction]

源码与测试说明某个 implementation 是否符合设计。实现暂缺能力应记录为实现差距；不能把缺失能力写成系统永远不允许的行为。设计也不能把未完成的实现宣告为可用。

发生设计与合同冲突时，明确记录两侧约定和影响，交由负责人裁决。新实现、更新时间或更详细的字段表都不自动取得优先权。

## 写入授权

agent 只有在人类明确要求或事先同意的范围内才能写入本目录。完整规则见 [agent 工作原则](principles/agent-workflow.md#harness-写入授权)；新增、修改、移动、删除及工具生成改动均受该规则约束。知识维护、同步文档、更新索引或验证工具不具有独立于人类授权的写入许可。

## 文档组织

| 类型 | 回答的问题 | 内容归属 |
|---|---|---|
| Architecture | 整体如何协作 | module 职责、依赖与端到端流程 |
| Module Design | 哪些复杂性由一个 module 承担 | 同目录维护职责、TypeSpec、interface 保证、生命周期、失败和验收 |
| Contract | 调用者准确发送、接收或保存什么 | 模块拥有的合同随模块维护；HTTP 与存储映射放在 bindings |
| Glossary | 一个词具体指什么 | 领域定义与需要避免的混用 |
| Documentation Convention | 设计文档如何维护 | 本文件的项目约定 |
| Development Standard | agent 如何工作、开发与验证遵守什么要求 | principles/ 中的工作方式、编码原则与测试验收 |
| Agent Instructions | agent 进入知识目录时先遵守什么 | harness/AGENTS.md 入口与写入提醒 |

文档按职责命名：整体逻辑架构位于 architecture.md；各模块主设计位于 modules/<module>/design.md；index.md 用于导航，README.md 用于工具或实现的使用入口。文件名不替代 frontmatter 的 type 与 description。

这些 `type` 值是 MASS 的约定。OKF 允许项目自定义类型，不规定上述分类。[^okf]

一个规则只维护一份主正文。Service 维护业务输入输出、状态与操作编排，Store 提供持久目录、分页及历史存取；运行时模块维护类型、配置校验和实际执行。依赖从Service 模块指向运行时模块，运行时仅依赖共享值类型。停止接纳与排空协调属于Service 模块，具体资源释放属于 Driver。接口与职责随各自模块同目录维护。[^runtime-direction]

评估文档与 module 的 depth，看它是否降低读者和调用者必须理解的复杂性。用 deletion test 检查重复说明：删除页面后若规则仍完整存在，应合并页面；若独有责任会散落到多个调用者，应保留该责任。

`interface` 包含前置条件、排序、失败、能力与配置保证。`seam` 是这组 interface 所在的位置，`adapter` 是在 seam 满足它的具体实现。depth 应带来 locality（规则集中维护）和 leverage（同一 interface 支撑多个调用者与测试）。

## OKF 规则

本目录是 OKF v0.2 knowledge bundle，按目录层级和普通 Markdown 链接组织。principles/ 维护 agent 工作原则，modules/ 维护深层模块设计与接口，shared/ 维护共享值与内容，bindings/ 维护传输和持久化映射，typespec/ 维护合同版本与校验工具。每个非保留 Markdown 文件具有可解析 YAML frontmatter 和非空 `type`。根 `index.md` 只使用规范允许的 `okf_version` frontmatter，子目录 index.md 不带 frontmatter；`log.md` 按日期记录更新。[^okf]

项目进一步约定 concept 文件提供 `title`、`description` 和显式 `status`。内部正文使用普通相对链接，便于在源码托管平台和本地阅读；OKF 支持这种形式。可保留 ACP、OKF 等外部规范与独立项目参考；本包的规则、接口和数据格式须可独立理解。

`sources` 记录派生材料。正文需要逐条归因时，使用与 `sources[].id` 对应的 Markdown 脚注。`generated` 记录当前内容的编写者及时间，`verified` 只记录实际发生的内容核实；时间带明确 UTC 偏移。[^okf]

尚未确认的设计标为 `draft`。人类已经确认的当前原则、设计和合同标为 `stable`；被替代的内容使用 `deprecated` 并链接替代文档。上述流程是 MASS 的编辑约定；OKF 的生命周期和验证信息本身不是权限或设计批准机制。

文档状态按人类确认的设计范围维护。stable 页面中明确列出的待决事项仍需另行确认；产品实现与运行验收结果单独记录。只修改文件名或通过编译不会自动改变文档状态。

不得因为格式检查成功而填写“设计已批准”。不得伪造人工验证记录。若增加机器验证记录，必须说明它验证了什么，例如格式和链接，不能把它解释为语义认可。

## 当前规则与待决事项

正文直接描述当前设计。被替代的接口、结构和表示从正文删除，不在同一知识包维护互相冲突的候选版本。尚未决定的行为在对应模块或绑定中明确标记，不因编译通过而视为已经定稿。

默认值、容量、错误和格式规则按负责人确认的合同维护。与当前合同不一致的实现记录为实现差距，不反向改变设计。

## 接口表达

接口方法、参数、返回值与数据类型采用 TypeSpec，分别位于 `modules/service/`、`modules/runtime/` 和 `modules/store/`，共享值类型位于 `shared/`。接口版本仅记录在 [TypeSpec 入口](typespec/README.md)元数据中，校验工具位于 `typespec/`，历史差异由 Git 保存。

`.tsp` 是 OKF concept 文档引用的合同附件，不给它添加 Markdown frontmatter。TypeSpec 的引用类型、占位声明和编译成功均不代表设计已批准；未决结构需要显式标记；已明确允许的开放值不算占位。

## 修改与验证

在取得相应写入授权后，修改一个主干规则时，同时检查引用它的合同和验收场景。设计验证通过公共 interface 观察结果，避免把某语言的私有函数或调度步骤写成跨语言验收条件。

文档检查分两部分：OKF 格式检查，以及 MASS 的链接、来源和必需元数据检查。OKF 消费者应容忍断链；项目可以把内部断链作为文档质量失败，但不能将它称为 OKF 格式不兼容。[^okf]

构建命令、SDK 版本、源码路径、测试运行记录和发布操作保留在实现资料中。需要参考时记录来源，不复制到设计规则中。

[^direction]: 本次讨论中明确的文档设计与目录要求。
[^okf]: 官方 SPEC.md 的 concept、来源、生命周期、索引和符合性规则；采用 v0.2。

[^runtime-direction]: 当前讨论确定 Service 依赖 Store 持久化资源、依赖 Runtime 执行运行操作；HTTP 只做协议映射，具体接口由负责人定义。

## 明确允许的动态值

统一事件的顶层 _meta 明确采用 Record<unknown>，即字符串键、任意类型值的扩展 map，对应 Go map[string]any。此处 unknown 不是待决标记，不限制为 JsonValue。工具 rawInput/rawOutput 表达动态业务值；其具体编码仍有待决；共享 ContentBlock 已定义 ACP 五种类型。RuntimeConfig、InteractionAnswer 等开放值见 [共享类型](shared/types.tsp)；当前没有显式未定义的公共类型。
