---
okf_version: "0.2"
---

# MASS Harness

本目录是指导当前项目演进和发展的唯一真相源，按 OKF v0.2 组织与管理。它维护 agent 在仓库内工作的原则，以及当前项目的架构、深层模块、API 与数据合同。源码、测试和实现资料用于核对实现状态，不能覆盖这里的设计。

agent 写入本目录之前，须有人类明确要求或事先同意；具体范围与工具写入规则见 [harness 写入授权](AGENTS.md#harness-写入授权)。已授权范围内无需重复确认。

## Agent 如何工作

- [工作原则索引](principles/index.md) — 通用编码、测试验收与语言级规范。
- [harness 工作规则](AGENTS.md) — 读取顺序、写入授权、CLI 优先与修改边界。

## 当前项目设计

- [知识管理约定](conventions.md) — 唯一真相源、OKF 组织、来源、审阅与维护规则。
- [领域词汇](glossary.md) — 统一 Runtime、实例、Session、事实与消费的含义。
- [总体架构](architecture.md) — module 职责、依赖与端到端协作。

## 模块

- [massctl](modules/massctl/design.md)：资源与操作两级子命令、目标寻址、输入输出、请求等待及实时观察的客户端能力草案。
- [Service](modules/service/design.md)：RuntimeService、SessionService，负责业务输入输出、资源状态和操作编排。
- [Store](modules/store/design.md)：MetaStore 管理资源 CRUD 与 generation，HistoryStore 提供批量追加、查询与同步进度；文件保存完整数据，SQLite 为可重建索引。
- [Runtime](modules/runtime/design.md)：RuntimeRegistry、RuntimeDriver，按类型校验配置并执行实际运行操作。

依赖方向：HTTP/CLI → Service → Store 与 Runtime。Runtime 不接收资源目录或分页，Store 不执行 Agent 操作。

Service 的详细职责：[Session](modules/service/session.md)、[历史](modules/service/history.md)、[事件处理器](modules/service/handlers.md)。Runtime 的事件合同见 [SessionEvent](modules/runtime/events.md)。

## 接口与承载映射

- [TypeSpec 入口](typespec/README.md)：当前接口版本、声明和校验工具。
- [HTTP v1](bindings/http.md)：以 `/apis/v1` 为业务路径前缀的资源式 REST JSON API、错误映射与可选 SSE 实时观察。
- [持久化](bindings/storage.md)：资源与历史的存储表示。

## 原则与验收

- [测试与验收原则](principles/testing.md)：公开接口验证、真实 Agent 验收、测试隔离与结果声明。
- [语言无关编码原则](principles/code-principles.md)：清晰、简单、简洁、可维护、一致与避免重复。
- Go 实现规则：[目录结构](principles/go/directory-structure.md)、[技术选型](principles/go/technology-stack.md)。

远端认证、秘密、身份与资源所有权要求随 [Runtime](modules/runtime/design.md#远端安全要求) 维护。

## 架构决策

- [ADR-0001：暂不强制限制 HTTP 请求体大小等容量边界](adr/0001-defer-http-capacity-limits.md)。

## 更新记录

- [更新记录](log.md) — 记录当前设计的整理与变更。

共享开放值见 [types.tsp](shared/types.tsp) 与[语义说明](shared/types.md)，统一历史查询见 [history-models.tsp](shared/history-models.tsp)，进程内处理器接口见 [event-handlers.tsp](modules/service/event-handlers.tsp)。
