---
type: Development Standard
title: Go 项目技术选型
description: 规定 Go 项目的 CLI、配置读取、测试、JSON-RPC 与 WebSocket 通信、结构化日志、lint 和可观测性选型。
status: stable
sources:
  - id: direction
    resource: 项目负责人在 2026-10-20 确定 Cobra、testify、Huma 和 chi
    title: Go 基础技术选型
  - id: observability-direction
    resource: 项目负责人在 2026-10-20 确定结构化日志、lint 和可观测性选型，并要求依赖提供链接
    title: 日志、质量检查与可观测性选型
  - id: http-client-direction
    resource: 项目负责人在 2026-10-20 指定 Resty 为默认 HTTP 客户端
    title: HTTP 客户端选型
  - id: jsonrpc-direction
    resource: 项目负责人在 2026-11-12 指定 sourcegraph/jsonrpc2
    title: JSON-RPC 2.0 Go 实现
  - id: configuration-direction
    resource: 项目负责人在 2026-10-20 指定 Viper 读取配置，默认配置格式采用 YAML
    title: 配置读取选型
---

# Go 项目技术选型

本规则适用于 Go 项目，不依赖具体业务或仓库布局。
选型只约束项目已有或明确要求新增的能力。
不要求同时引入全部依赖。

第三方库和工具的选型必须附官方文档或源码仓库链接，不能只写名称。具体依赖版本、配置和执行命令放在实现目录或仓库操作说明中。

## CLI 与 flag

选型：[Cobra](https://github.com/spf13/cobra)。

使用 Cobra 组织命令及管理 flag。

## 配置读取

选型：[Viper](https://github.com/spf13/viper)。

使用 Viper 读取配置，默认配置文件采用 YAML 格式。通过绑定 Cobra flag 统一处理配置优先级：显式命令行参数覆盖配置文件，配置文件覆盖默认值。使用独立 Viper 实例，避免全局配置状态相互影响。

## 测试

选型：[testify](https://github.com/stretchr/testify)。

尽可能使用 suite 组织测试。

## JSON-RPC 与 WebSocket

JSON-RPC 选型：[sourcegraph/jsonrpc2](https://github.com/sourcegraph/jsonrpc2)。

使用该库处理请求响应关联、通知和错误。方法参数、业务准入、错误脱敏与能力范围由应用合同维护；显式核对 batch、ID 和解码边界，不以库名替代协议兼容性验证。

WebSocket 适配使用其 ObjectStream 扩展点；自带 websocket 包基于 [gorilla/websocket](https://github.com/gorilla/websocket)。认证、Origin、写入期限和生命周期由绑定明确。HTTP Upgrade 与最小健康探针使用标准库 [net/http](https://pkg.go.dev/net/http)，不为 JSON-RPC 引入 REST schema 或 OpenAPI 客户端生成链路。

Handler 不得阻塞连接读循环；异步分发仍须保护初始化、在途请求和资源登记的顺序。连接取消与已接纳业务的生命周期分开管理。库发送串行化不等于已经解决背压或写入截止，须以公开行为验证。

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
