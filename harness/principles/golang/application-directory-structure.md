---
type: Development Standard
title: Go 应用项目目录结构
description: 规定 Go 应用的命令入口、组件业务包和共用私有代码的目录结构。
status: stable
sources:
  - id: component-layout
    resource: 项目负责人在 2026-11-22 确认统一根 cmd 和按组件组织业务包
    title: Go 应用仓库目录调整
  - id: direction
    resource: 项目负责人在 2026-10-20 要求 cmd 子目录放二进制入口，pkg 子目录放模块
    title: Go 目录结构规则
---

# Go 应用项目目录结构

本规则适用于服务端程序和命令行应用。
工具库与 SDK 遵循[工具库与 SDK 目录结构](sdk-directory-structure.md)。

## 强制规则

- 在 Go 模块根目录维护 `go.mod`、`go.sum` 和 `cmd/`。`cmd/` 每个直接子目录对应一个独立二进制，包含 `package main` 和 `main` 入口。
- `cmd/<二进制名>/main.go` 负责程序入口、进程初始化与依赖组装。单入口程序的启动参数和进程配置随 `main.go` 维护。有命令树的程序在 `subcommand/` 中按资源和操作组织子命令、参数解析、执行与终端输出。
- 按组件组织业务包。多个组件共用一个 Go 模块时，各组件在根目录下拥有自己的目录，按职责拆分子包。可复用业务模块可以放在所属组件的 `pkg/` 下。单组件应用可以直接使用根 `pkg/`。
- 组件可以在自己的入口包提供进程内装配。命令与其他组件通过该入口装配实例。业务包的依赖指向组件业务接口和共用代码，`cmd/` 作为调用方。
- 组件私有实现放在组件自己的 `internal/` 下。多个组件或根命令共用的私有代码放在根 `internal/` 下。测试专用进程随其测试维护。

## 目录示例

以下路径相对于 Go 模块根目录。按实际职责命名二进制和组件。

```text
project/
├── go.mod
├── go.sum
├── cmd/
│   ├── server/
│   │   └── main.go
│   └── cli/
│       ├── main.go
│       └── subcommand/
│           └── root.go
├── service/
│   ├── service.go     # 进程内组装入口
│   └── pkg/           # 会话、存储等业务包
├── coordinator/       # 另一组件的业务包
└── internal/         # 命令和组件共用的私有代码
```
