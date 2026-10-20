---
type: Development Standard
title: Go 项目技术选型
description: 规定 Go 项目的 CLI、测试、HTTP、结构化日志、lint 和可观测性选型。
status: stable
sources:
  - id: direction
    resource: 项目负责人在 2026-10-20 确定 Cobra、testify、Huma 和 chi
    title: Go 基础技术选型
  - id: observability-direction
    resource: 项目负责人在 2026-10-20 确定结构化日志、lint 和可观测性选型，并要求依赖提供链接
    title: 日志、质量检查与可观测性选型
---

# Go 项目技术选型

本规则适用于 Go 项目，不依赖具体业务或仓库布局。

第三方库和工具的选型必须附官方文档或源码仓库链接，不能只写名称。具体依赖版本、配置和执行命令放在实现目录或仓库操作说明中。

## CLI 与 flag

选型：[Cobra](https://github.com/spf13/cobra)。

使用 Cobra 组织命令及管理 flag。

## 测试

选型：[testify](https://github.com/stretchr/testify)。

尽可能使用 suite 组织测试。

## HTTP 框架

选型：[Huma v2](https://huma.rocks/) + [chi](https://github.com/go-chi/chi)。

使用 chi 路由，配合 Huma 定义 HTTP 接口并生成 OpenAPI。通过统一脚手架注册路由与请求、响应类型，由代码生成具体 schema 和 OpenAPI 文档，避免手工维护重复的接口定义。

## 结构化日志

选型：标准库 [log/slog](https://pkg.go.dev/log/slog)。

- 使用 slog 的结构化字段表达上下文，保持字段名和级别含义一致，避免把可查询字段拼入消息文本。
- 程序入口统一配置日志；可复用模块不自行更改全局输出目标或级别。
- 使用 JSONHandler 提供机器可读日志，TextHandler 用于本地阅读。日志与 CLI 业务输出分别处理，避免污染 stdout 上的命令结果。
- 日志不得记录认证秘密；请求、响应与业务内容按明确的记录和脱敏策略处理。

## lint 与格式检查

选型：[golangci-lint](https://golangci-lint.run/)。

配置基线见 [golangci-lint 模板](templates/golangci-lint.yaml)，采用 v2 配置格式。新项目可将模板复制为 Go 模块根目录的 `.golangci.yaml`，再按需要调整；模板不自动替换现有项目配置。

- 使用 golangci-lint 统一执行检查，显式维护启用的规则、格式器和必要例外。
- 配置随源码管理，开发环境与自动检查使用同一份规则，不依赖个人全局配置。
- 忽略项限定到必要范围并说明原因；不能通过全局关闭规则掩盖局部问题。
- lint 不替代编译、测试、race 检查或真实外部系统验收。

## 可观测性

选型：[OpenTelemetry Go](https://opentelemetry.io/docs/languages/go/)。使用官方 API/SDK 采集指标和调用追踪，日志继续使用 slog。

- 统一采用 OpenTelemetry Go API/SDK，通过 context 传递追踪上下文；日志关联有效的 trace ID 和 span ID。
- 程序入口负责 Provider、Exporter 的初始化、配置和关闭；可复用模块只负责埋点，不自行设置全局 Provider。
- 指标标签使用可控的低基数字段；请求 ID、会话 ID 和任意用户内容不作为指标标签。
- 导出失败不改变业务操作的成功或失败结果；遥测缓冲、导出与退出刷新应有明确容量和时间边界。
- 采集后端、Exporter、采样率、指标名称及部署配置由具体项目确定，不在通用选型中固定。
