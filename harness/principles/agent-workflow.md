---
type: Development Standard
title: Agent 在仓库内的工作方式
description: 规定唯一知识源、写入授权、工具选择、修改边界和实现工作入口。
status: stable
sources:
  - id: direction
    resource: 项目负责人在 2026-10-20 明确的 harness 定位与写入授权规则
    title: 唯一真相源与人类授权
---

# Agent 在仓库内的工作方式

## 读取顺序与唯一真相源

[harness 入口](../index.md)是指导本项目当前演进和发展的唯一真相源。先读取本目录的[原则索引](index.md)，再读取任务相关的[总体架构](../architecture.md)、模块设计和 [API/TypeSpec 合同](../typespec/README.md)。涉及 Go 实现时，还须读取 [Go 目录结构](go/directory-structure.md)和[Go 技术选型](go/technology-stack.md)。知识组织和维护遵循 [OKF 文档约定](../conventions.md)。

根 AGENTS.md、README、GLOSSARY.md 和语言专属资料提供入口、实现事实或操作说明，不形成另一套原则与设计。实现与 harness 不一致时报告实现差距；不能因现有代码、测试或其他资料的行为而擅自改写设计。遇到待决合同或相互冲突的要求，应明确指出问题，由人类决定。

## harness 写入授权

agent 写入 harness 之前，必须满足以下条件之一：

- 当前人类请求明确要求修改 harness 中的相关内容；
- agent 已说明拟修改范围，并事先获得人类同意。

授权覆盖本次明确的范围；在该范围内可以直接继续，不重复确认。普通编码、修复、测试或检查任务不自动授权修改 harness，不能把自动同步文档的习惯当作人类许可。需要增加修改范围时，先征询同意。

此规则覆盖本目录内全部文件：新增、编辑、重命名、移动、删除，以及格式化、导出、安装依赖或其他工具造成的写入。调用可能写入 harness 的工具前先确认已有授权。只读检索和检查可以直接进行；未获授权的建议、方案和草案应留在聊天或本目录之外，获得同意后再写入。

人类明确要求某项知识变更时，授权包含完成该变更必需的关联引用、索引与更新记录同步；不包含无关的设计变更。审阅状态、格式检查通过和编译通过都不能代替写入授权。

## 工具与外部系统

外部系统操作首先使用目的明确的专用 CLI。先作有界的能力发现，按需要核对可执行路径、版本、命令合同、认证账号、目标环境与资源。浏览器和 UI 自动化仅在人类明确要求，或已确认 CLI 不可用/缺少所需能力后使用，并说明具体原因。

操作后读取权威状态验证结果。命令已提交、退出码为零或状态为 running 不等于最终完成。不得输出凭据或展开后的秘密；不得在未获明确指示时向他人发送消息。

## 修改边界

修改前确认工作目录、分支与工作区状态。保留其他任务的修改、运行数据、认证材料和 scratch 证据，不覆盖、重置或批量清理无关内容。

代码遵循[编码原则](code-principles.md)，验证遵循[测试与验收原则](testing.md)。默认不引入兼容层。源码位置变化不隐式移动现有运行数据，现有安装须显式配置原来的绝对数据目录。

根据改动运行必要检查，明确报告通过、失败和未验证的范围。harness 尚未授权修改时，可报告设计与实现的差距，不能自动修订知识目录来消除差异。

## 当前实现工作入口

根 Makefile 是多语言仓库的命令入口。Rust workspace、源码、测试、examples、vendor 与 Rust 专属脚本位于 mass-rs/；直接调用 Cargo 时使用 --manifest-path mass-rs/Cargo.toml，或进入 mass-rs/。

| 命令 | 用途 |
|---|---|
| make fmt | 格式化 Rust 代码 |
| make lint | workspace/all-target/all-feature lint |
| make build | 构建 Rust workspace、二进制与示例 |
| make check | 结构、来源、格式、lint、测试与构建检查 |
| make test-native | 显式执行真实 Codex/模型验收 |
| make release / make verify-release | 原生 macOS arm64、Rust 1.96.0 的发布与验证 |

默认 make test 不调用真实模型；Linux 交付仍未验证。优先通过各 crate 的公开接口集成测试和端到端场景验证行为，不把构建、模拟测试或启动成功解释为真实 Agent 验收。语言实现的 README 记录可执行命令与当前支持范围，不能覆盖 harness 合同。
