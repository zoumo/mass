---
type: Development Standard
title: Go 项目技术选型
description: 规定 Go 项目的 CLI、flag、测试和 HTTP 框架选型。
status: stable
sources:
  - id: direction
    resource: 项目负责人在 2026-10-20 确定 Cobra、testify、Huma 和 chi
    title: Go 基础技术选型
---

# Go 项目技术选型

本规则适用于 Go 项目，不依赖具体业务或仓库布局。

| 领域 | 选型 | 使用要求 |
|---|---|---|
| CLI 与 flag | Cobra | 使用 Cobra 组织命令及管理 flag |
| 测试 | testify | 尽可能使用 suite 组织测试 |
| HTTP 框架 | Huma v2 + chi | 使用 chi 路由，配合 Huma 定义 HTTP 接口并生成 OpenAPI |

通过统一脚手架注册路由与请求、响应类型，由代码生成具体 schema 和 OpenAPI 文档，避免手工维护重复的接口定义。
