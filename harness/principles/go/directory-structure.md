---
type: Development Standard
title: Go 项目目录结构
description: 强制规定 Go 二进制入口与模块的目录位置。
status: stable
sources:
  - id: direction
    resource: 项目负责人在 2026-10-20 要求 cmd 子目录放二进制入口，pkg 子目录放模块
    title: Go 目录结构规则
---

# Go 项目目录结构

本规则适用于 Go 项目，不依赖具体业务或仓库布局。

## 强制规则

- `cmd/` 的子目录放可编译成二进制的程序。每个直接子目录对应一个独立二进制，包含 `package main` 和 `main` 入口。
- `pkg/` 的子目录放模块。每个直接子目录对应一个职责明确的模块，模块内部可以继续划分子包。
- `cmd/<二进制名>/main.go` 负责程序入口、进程初始化与依赖组装；该程序的命令树、flag、命令执行与终端输出代码放入同目录下的 `subcommand/` 包，按资源和操作组织，不放入 `pkg/`。
- 可复用业务模块放入 `pkg/`，不得依赖 `cmd/`。`subcommand/` 可以依赖这些模块，但不能承担其业务规则与生命周期编排。

## 目录示例

以下路径相对于 Go 模块根目录。二进制和模块名称按实际职责确定：

```text
project/
├── go.mod
├── go.sum
├── cmd/
│   ├── server/        # 可编译为 server 二进制
│   └── cli/           # 可编译为 cli 二进制
│       ├── main.go    # 程序入口
│       └── subcommand/
│           ├── root.go
│           └── resource.go
└── pkg/
    ├── modulea/       # 模块 A
    └── moduleb/       # 模块 B
```

这里的模块指职责明确的代码模块，不要求每个子目录都有独立的 go.mod。示例名称不作为固定目录要求。
