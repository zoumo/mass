# Agent 工作原则

- [仓库工作方式](agent-workflow.md) — 规定知识读取、harness 写入授权、CLI 优先、修改边界及实现工作入口。
- [语言无关编码原则](code-principles.md) — 定义清晰、简单、简洁、可维护、一致与避免重复的要求。
- [测试与验收原则](testing.md) — 定义公开接口验证、真实 Agent 验收、测试隔离与结果声明的要求。

- [Go 项目目录结构](go/directory-structure.md) — 强制 cmd 子目录放二进制入口，pkg 子目录放模块。
- [Go 项目技术选型](go/technology-stack.md) — Cobra 管理 CLI/flag、testify（优先 suite）、Huma v2 + chi。
