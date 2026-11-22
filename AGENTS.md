# Agent 入口

[harness/index.md](harness/index.md) 是指导本项目当前演进和发展的唯一真相源。开始工作前，先读取 [harness 工作规则](harness/AGENTS.md)、[编码原则](harness/principles/code-principles.md)和[测试验收原则](harness/principles/testing.md)，再按任务读取架构、模块与 API 合同。

## harness 写入限制

agent 只有在当前人类请求明确要求修改 harness 中的相关内容，或已事先征得人类同意时，才可写入 harness/。这包括新增、编辑、移动、重命名、删除和工具生成的改动。普通实现任务不自动授权修改 harness；已授权范围内无需重复确认。完整规则见 [harness 写入授权](harness/AGENTS.md#harness-写入授权)。

本文件提供知识入口、写入提醒和当前项目的实现操作说明。通用原则、项目设计和合同统一维护在 harness/；实现与其不一致时记录差距，不以既有实现覆盖设计。

## 新增数据库表

实施任何新增数据库表的变更前，必须先在设计评审中说明新增表的用途和必要性，并获得明确同意。

## 当前实现与构建

从仓库根目录运行 Makefile 和 Go 命令。根目录的 `go.mod` 管理所有 Go 依赖。命令入口在 [`cmd/`](cmd/)，会话服务和进程内装配在 [`massd/`](massd/README.md)，Core 和 Trigger 在 [`superagent/`](superagent/README.md)。根 `internal/` 放命令与组件共用的私有代码。

| 命令 | 用途 |
|---|---|
| make fmt / make fmt-go | 格式化 Go 代码 |
| make lint / make lint-go | Go lint |
| make test | MASS 与 Superagent 的离线 race 测试，以及 Observer 浏览器测试 |
| make test-go | MASS 单元、集成、仿真三层离线 race 测试 |
| make test-unit-go | 仅包内单元测试 |
| make test-integration-go | 构造数据多模块集成测试与 SDK 协议探针 |
| make test-simulation-go | 录制数据经 agent-sdk-testify 回放 |
| make test-e2e-go | 显式启动真实 Agent 的 E2E，不包含在 test/check 中 |
| make build | 构建 `bin/massctl`、`bin/massd` 和带 Observer 的 `bin/superagent` |
| make build-go | 构建 `bin/massctl` 与 `bin/massd` |
| make check | 验证 MASS、Superagent 和 Observer |
| make check-go | MASS lint、三层离线测试、构建和格式检查 |
| make prototype-go | 隔离的合成产品入口端到端验收 |

默认 make test 不调用真实模型；合成测试不能替代真实 Agent 验收。源码位置变化不隐式移动现有运行数据，现有安装须显式配置原来的绝对数据目录。
