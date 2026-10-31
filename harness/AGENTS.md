---
type: Agent Instructions
title: harness 工作与文档维护规则
description: 规定本项目知识读取、写入授权、工具选择、修改边界和 OKF 文档维护规则。
status: stable
sources:
  - id: direction
    resource: 项目负责人在 2026-10-20 明确的 harness 定位与写入授权规则
    title: 唯一真相源与人类授权
  - id: okf
    resource: https://github.com/GoogleCloudPlatform/open-knowledge-format/blob/main/SPEC.md
    title: Open Knowledge Format v0.2
---

# harness 工作与文档维护规则

## 读取顺序与唯一真相源

[harness 入口](index.md)是指导本项目当前演进和发展的唯一真相源。先读取[原则索引](principles/index.md)，再读取任务相关的[总体架构](architecture.md)、模块设计和 [API/TypeSpec 合同](typespec/README.md)。涉及 Go 实现时，还须读取 [Go 目录规范](principles/golang/directory-conventions.md)和[Go 技术选型](principles/golang/technology-stack.md)。

公共 interface、HTTP 协议、事件与持久化格式由项目负责人定义，所有语言实现遵守同一设计及其确认后的合同。遇到待决合同或冲突要求，记录约定和影响，由项目负责人决定；现有代码、测试、更新时间或更详细的字段表均不能覆盖设计。

## harness 写入授权

agent 写入 harness 之前，必须满足以下条件之一：

- 当前人类请求明确要求修改 harness 中的相关内容；
- agent 已说明拟修改范围，并事先获得人类同意。

授权包含明确范围及完成该变更必需的关联引用、索引与更新记录同步，范围内无需重复确认；扩大范围须先征询同意。普通编码、修复、测试或检查任务不自动授权修改 harness。

此规则覆盖全部文件的新增、编辑、移动、重命名、删除，以及格式化、导出、安装依赖等工具写入。调用可能写入的工具前须确认已有授权；只读检索和检查可直接进行。未获授权的建议、方案和草案留在聊天或本目录之外。审阅状态、格式检查或编译通过均不能代替写入授权。

## 工具与外部系统

外部系统操作首先使用专用 CLI。先作有界的能力发现，按需核对可执行路径、版本、命令合同、认证账号、目标环境与资源。浏览器和 UI 自动化仅在人类明确要求，或已确认 CLI 不可用/缺少所需能力后使用，并说明具体原因。

操作后读取权威状态验证结果。命令已提交、退出码为零或状态为 running 不等于最终完成。不得输出凭据或展开后的秘密；不得在未获明确指示时向他人发送消息。

## 修改与验证

修改前确认工作目录、分支与工作区状态。保留其他任务的修改、运行数据、认证材料和 scratch 证据，不覆盖、重置或批量清理无关内容。代码遵循[编码原则](principles/code-principles.md)，默认不引入兼容层。

修改主干规则时，同时检查关联引用、合同和验收场景。验证遵循[测试与验收原则](principles/testing.md)，根据改动运行必要检查，区分通过、失败和未验证；跨语言验收条件不得依赖某语言的私有函数或调度步骤。

文档检查区分 OKF 格式与 MASS 的链接、来源和必需元数据要求。OKF 消费者应容忍断链；项目可将内部断链判为文档质量失败，但不能称为 OKF 格式不兼容。[^okf]

## 设计正文与实现记录

正文只描述确定的终态规范与设计，不保留中间方案、讨论过程、被替代的接口或实现进度。变更直接更新正文，历史由 Git 保存；现有设计、合同、元数据、导航和记录保留各自归属。待决行为在对应模块或绑定中明确标记，不作为已定稿合同。

源码与测试用于核对实现，不反向改变设计。实现差距集中记录在[实现差距记录](implementation-gaps.md)，绑定检查日期、源码版本、相关工作区变更及验证证据，未执行的场景标为未验证。终态设计不代表实现已完成，也不能把缺失能力写成系统永远不允许的行为。

Module 内容边界见[modules 目录规则](modules/AGENTS.md)。根 AGENTS.md、README 和语言专属资料承载实现事实与操作说明，包括构建命令、SDK 版本、源码路径、执行器、测试运行记录和发布操作；需要时引用，不复制进设计正文或形成另一套原则。更新记录只保留设计变化历史，不承担当前实现进度查询。

## 文档组织

| 类型 | 回答的问题 | 内容归属 |
|---|---|---|
| Architecture | 整体如何协作 | architecture.md：module 职责、依赖与端到端流程 |
| Module Design | 哪些复杂性由一个 module 承担 | modules/<module>/design.md 及同目录合同：职责、所有权、不变量、生命周期、失败和验收 |
| Contract | 调用者准确发送、接收或保存什么 | 合同随所属模块维护；传输、持久化与语言映射放在 bindings/；共享值放在 shared/ |
| Implementation Status | 当前检查发现哪些实现差距 | implementation-gaps.md |
| Glossary | 一个词具体指什么 | 根目录 [GLOSSARY.md](../GLOSSARY.md) |
| Development Standard | 开发与验证遵守什么要求 | principles/：通用原则与语言级规范 |
| Agent Instructions | 如何读取知识、取得授权、处理冲突并维护文档 | 本文件及各目录 AGENTS.md |

这些 `type` 值是 MASS 的约定，OKF 允许项目自定义类型。文件名不替代 frontmatter 的 `type` 与 `description`；index.md 用于导航，README.md 用于工具或实现的使用入口。[^okf]

一个规则只维护一份主正文；模块职责与依赖以[总体架构](architecture.md)和各模块合同为准。评估文档与 module 的 depth，看它是否降低读者和调用者必须理解的复杂性。用 deletion test 检查重复：删除页面后规则仍完整存在，应合并；独有责任会散落到多个调用者时，应保留。

`interface` 包含前置条件、排序、失败、能力与配置保证；`seam` 是 interface 所在的位置，`adapter` 是在 seam 满足它的具体实现。depth 应带来 locality（规则集中维护）和 leverage（同一 interface 支撑多个调用者与测试）。

## OKF 规则

本目录是 OKF v0.2 knowledge bundle，按目录层级和普通相对 Markdown 链接组织。每个非保留 Markdown 文件具有可解析 YAML frontmatter 和非空 `type`；concept 文件还须提供 `title`、`description` 和显式 `status`。根 index.md 只使用规范允许的 `okf_version` frontmatter，子目录 index.md 不带 frontmatter；log.md 按日期记录更新。[^okf]

`sources` 记录派生材料；逐条归因使用与 `sources[].id` 对应的 Markdown 脚注。`generated` 记录编写者及时间，`verified` 只记录实际核实，时间带明确 UTC 偏移。可引用 ACP、OKF 等外部规范和独立项目，但本包规则、接口与数据格式须可独立理解。[^okf]

MASS 按人类确认范围维护状态：未确认设计为 `draft`，已确认原则、设计和合同为 `stable`，被替代内容为 `deprecated` 并链接替代文档。stable 页中的待决事项仍需另行确认。改名、格式检查、类型引用、占位声明或编译通过均不代表设计批准，也不自动改变状态；实现与运行验收结果另行记录。

不得伪造人工验证记录。机器验证须说明实际范围（如格式和链接），不能解释为语义认可。OKF 生命周期与验证信息本身不是权限或设计批准机制。

## 接口表达

接口方法、参数、返回值与数据类型采用 TypeSpec，随所属模块维护，共享值类型位于 shared/。接口版本仅记录在 [TypeSpec 入口](typespec/README.md)元数据中，校验工具位于 typespec/。`.tsp` 是 concept 引用的合同附件，不添加 Markdown frontmatter。

模块正文和 TypeSpec 描述语言无关的逻辑能力、输入输出、顺序、失败及生命周期，不把 channel、迭代器、goroutine 或锁作为通用接口。属性读取和异步接收操作不强制各语言复制方法名或一对一方法形状。语言映射放在 bindings/ 并链接通用合同，不得削弱其语义。

未决结构须显式标记，合同明确允许的开放值不算占位。事件 `_meta`、工具 `rawInput`/`rawOutput` 见 [SessionEvent](modules/runtime/events.md)，RuntimeConfig、InteractionAnswer 见[共享类型](shared/types.tsp)，ContentBlock 见[内容块](shared/content-blocks.md)。JSON 不可承载的语言值须按 [HTTP 承载](bindings/http.md#基础规则)转换或报错，持久化编码遵循[存储合同](bindings/storage.md)。

[^okf]: 官方 SPEC.md 的 concept、来源、生命周期、索引和符合性规则；采用 v0.2。
