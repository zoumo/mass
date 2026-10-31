---
type: Development Standard
title: Go 目录规范
description: 先判断项目性质，再按需读取目录结构规范。
status: stable
---

# Go 目录规范

1. 检查项目说明、公开包和程序入口。
2. 按主要交付物判断项目性质。
3. 只读取对应的目录结构规范。

| 项目性质 | 判断依据 | 读取文档 |
| --- | --- | --- |
| 应用项目 | 主要交付可运行的服务或命令行程序 | [应用目录结构](application-directory-structure.md) |
| 工具库或软件开发工具包（SDK） | 主要供其他程序导入 | [工具库与 SDK 目录结构](sdk-directory-structure.md) |

工具库附带可执行程序时，仍按工具库规范组织。
包含多个 Go 模块时，按模块分别判断。
性质无法确定时，说明疑点并询问用户。
