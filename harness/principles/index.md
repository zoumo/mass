# Agent 工作原则

本目录只维护可跨项目复用的原则与语言级规范，内容边界见 [目录规则](AGENTS.md)。项目专属构建命令和实现操作说明放在仓库根目录或对应实现目录的 AGENTS.md、README 中。

- [语言无关编码原则](code-principles.md) — 定义清晰、简单、简洁、可维护、一致与避免重复的要求。
- [测试与验收原则](testing.md) — 定义公开接口验证、真实 Agent 验收、测试隔离与结果声明的要求。

- [Go 目录规范](golang/directory-conventions.md) — 先判断项目性质，再读取应用或工具库规范。
- [Go 项目技术选型](golang/technology-stack.md) — CLI、配置读取、测试、HTTP 框架与客户端、结构化日志、lint 与可观测性选型及使用要求。
