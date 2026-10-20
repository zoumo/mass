# Agent 入口

[harness/index.md](harness/index.md) 是指导本项目当前演进和发展的唯一真相源。开始工作前，先读取 [harness 工作规则](harness/AGENTS.md)、[编码原则](harness/principles/code-principles.md)和[测试验收原则](harness/principles/testing.md)，再按任务读取架构、模块与 API 合同。

## harness 写入限制

agent 只有在当前人类请求明确要求修改 harness 中的相关内容，或已事先征得人类同意时，才可写入 harness/。这包括新增、编辑、移动、重命名、删除和工具生成的改动。普通实现任务不自动授权修改 harness；已授权范围内无需重复确认。完整规则见 [harness 写入授权](harness/AGENTS.md#harness-写入授权)。

本文件提供知识入口、写入提醒和当前项目的实现操作说明。通用原则、项目设计和合同统一维护在 harness/；实现与其不一致时记录差距，不以既有实现覆盖设计。

## 当前实现与构建

根 Makefile 是当前 Go 实现的命令入口；源码位于 mass-go/。直接运行 Go 命令时进入 mass-go/。运行配置与支持范围见 [mass-go/README.md](mass-go/README.md)。

| 命令 | 用途 |
|---|---|
| make fmt / make fmt-go | 格式化 Go 代码 |
| make lint / make lint-go | Go lint |
| make test / make test-go | 离线 race 测试 |
| make build / make build-go | 构建 bin/massctl 与 bin/massd-go |
| make check / make check-go | lint、测试、构建与格式检查 |
| make prototype-go | 隔离的合成产品入口端到端验收 |

默认 make test 不调用真实模型；合成测试不能替代真实 Agent 验收。源码位置变化不隐式移动现有运行数据，现有安装须显式配置原来的绝对数据目录。
