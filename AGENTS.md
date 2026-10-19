# Agent 入口

[harness/index.md](harness/index.md) 是指导本项目当前演进和发展的唯一真相源。开始工作前，先读取 [agent 工作原则](harness/principles/agent-workflow.md)、[编码原则](harness/principles/code-principles.md)和[测试验收原则](harness/principles/testing.md)，再按任务读取架构、模块与 API 合同。

## harness 写入限制

agent 只有在当前人类请求明确要求修改 harness 中的相关内容，或已事先征得人类同意时，才可写入 harness/。这包括新增、编辑、移动、重命名、删除和工具生成的改动。普通实现任务不自动授权修改 harness；已授权范围内无需重复确认。完整规则见 [agent 工作原则](harness/principles/agent-workflow.md#harness-写入授权)。

本文件只提供入口和写入提醒。项目原则、设计和合同统一维护在 harness/；实现与其不一致时记录差距，不以既有实现覆盖设计。
