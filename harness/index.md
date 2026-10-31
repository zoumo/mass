---
okf_version: "0.2"
---

# MASS Harness

本目录是指导当前项目演进和发展的唯一真相源，按 OKF v0.2 组织与管理。它维护 agent 在仓库内工作的原则，以及当前项目的架构、深层模块、API 与数据合同。源码、测试和实现资料用于核对实现状态，不能覆盖这里的设计。

agent 写入本目录之前，须有人类明确要求或事先同意；具体范围与工具写入规则见 [harness 写入授权](AGENTS.md#harness-写入授权)。已授权范围内无需重复确认。

## Agent 如何工作

- [工作原则索引](principles/index.md) — 通用编码、测试验收与语言级规范。
- [harness 工作规则](AGENTS.md) — 读取顺序、写入授权、CLI 优先、修改边界与知识文档维护。

## 当前项目设计

- [领域词汇](../GLOSSARY.md) — 统一 Runtime、实例、Session、事实与消费的含义。
- [总体架构](architecture.md) — module 职责、依赖与端到端协作。

## 模块

- [massctl](modules/massctl/design.md)：资源与操作两级子命令、目标寻址、输入输出、请求等待及实时观察的客户端能力草案。
- [Service](modules/service/design.md)：RuntimeService、SessionService，负责业务输入输出、资源与原运行校验和操作编排。
- [Events](modules/events/design.md)：每 Session 一个 EventBroadcast，统一管理 Handler；按加入顺序执行 Handler，required 决定失败是否中止；subscribe 返回异步流，封装队列 Handler；只管实时流，不管历史或运行关联。
- [MetaStore](modules/metastore/design.md)：资源 CRUD、generation 与并发版本；配置选择唯一权威后端，默认 SQLite，可选文件。
- [EventStore](modules/eventstore/design.md)：事件批量追加、查询与同步进度；文件权威，SQLite 为可重建的完整查询副本，自动从已验证消费位置续同步。
- [Runtime](modules/runtime/design.md)：Registry 只校验类型配置并构造 Driver；Driver 执行实际运行操作，当前运行映射归 Service。

依赖方向：HTTP/CLI → Service → MetaStore、EventStore、Events 与 Runtime。Service 独占当前运行映射及完整停止协调，组装层只注入依赖；Service 管理 Session → Broadcast 映射，直接协调 EventStore 持久提交后，将统一 SessionEvent 交给对应 Broadcast；状态等 Handler 由 Service 装配。Events 核心不直接依赖存储或 Service；存储模块不相互调用，Runtime 不接收资源目录或分页。

原生历史导入见 [异步 load](modules/service/load.md)：先绑定接收，恢复同一原生会话后沿单 Driver 流回放、转换、校验、提交和分发；本地完成后直接继续输入。接纳、原生恢复/穷尽与本地完成分别表达。

Service 的详细职责：[Session](modules/service/session.md)、[历史](modules/service/history.md)。实时分发与订阅见 [Events](modules/events/design.md)。Runtime 的事件合同见 [SessionEvent](modules/runtime/events.md)。

## 接口与承载映射

- [TypeSpec 入口](typespec/README.md)：当前接口版本、声明和校验工具。
- [HTTP v1](bindings/http.md)：以 `/apis/v1` 为业务路径前缀的资源式 REST JSON API、错误映射与可选 SSE 实时观察。
- [Go 实时订阅映射](bindings/go-events.md)：通用异步流在 Go 中表达为只读 channel、Err 与 Close；不规定其他语言的流类型。
- [持久化](bindings/storage.md)：资源与历史的存储表示。

## 原则与验收

- [测试与验收原则](principles/testing.md)：公开接口验证、真实 Agent 验收、测试隔离与结果声明。
- [语言无关编码原则](principles/code-principles.md)：清晰、简单、简洁、可维护、一致与避免重复。
- Go 实现规则：[目录结构](principles/golang/application-directory-structure.md)、[技术选型](principles/golang/technology-stack.md)。

远端认证、秘密、身份与资源所有权要求随 [Runtime](modules/runtime/design.md#远端安全要求) 维护。

## 架构决策

- [ADR-0001：暂不强制限制 HTTP 请求体大小等容量边界](adr/0001-defer-http-capacity-limits.md)。

## 更新记录

- [更新记录](log.md) — 记录设计变化历史。
- [实现差距记录](implementation-gaps.md) — 集中维护绑定日期、源码版本与检查证据的实现差距。

共享开放值见 [types.tsp](shared/types.tsp) 与[语义说明](shared/types.md)，统一历史查询见 [history-models.tsp](shared/history-models.tsp)，进程内处理器接口见 [event-handlers.tsp](modules/events/event-handlers.tsp)。
