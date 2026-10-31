---
type: Development Standard
title: Go 工具库与 SDK 目录结构
description: 规定供其他程序导入的工具库和 SDK 的目录结构。
status: stable
---

# Go 工具库与 SDK 目录结构

SDK 指软件开发工具包。

工具库与 SDK 允许平铺组织。
公开包直接放在 Go 模块根目录或根目录的子目录。
不强制创建 `cmd/`、`pkg/` 或 `subcommand/`。
包按职责划分，无需把全部文件放在同一个包中。

```text
library/
├── go.mod
├── go.sum
├── client.go
├── client_test.go
├── options.go
└── transport/
    ├── transport.go
    └── transport_test.go
```

如果工具库附带可执行程序，程序入口放在 `cmd/`。
库代码继续使用平铺结构，不因此迁入 `pkg/`。
