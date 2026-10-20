# MASS

[harness](harness/index.md) 是指导当前项目演进和发展的唯一真相源，包括工作原则、架构、模块设计与 API 合同。实现资料不代表已满足全部设计。

当前实现位于 [mass-go](mass-go/README.md)。运行配置、HTTP/CLI 用法和支持范围见该目录说明；HTTP v1 的验收边界见 [Go HTTP v1 验收](mass-go/docs/http-v1-acceptance.md)。

从仓库根目录执行：

| 命令 | 用途 |
|---|---|
| `make fmt` / `make fmt-go` | 格式化 Go 代码 |
| `make lint` / `make lint-go` | Go lint |
| `make test` / `make test-go` | 离线 race 测试 |
| `make build` / `make build-go` | 构建 `bin/massctl` 与 `bin/massd-go` |
| `make check` / `make check-go` | lint、测试、构建和格式检查 |
| `make prototype-go` | 隔离的合成产品入口端到端验收 |

Rust 实现已移除。可借鉴的历史场景与 case 保存在
[参考验收清单](docs/rust-reference-cases.md)，不包含实现提炼或发布检查，也不表示当前 Go 已通过这些 case。

运行数据仍保留在原位置；源码清理不迁移或删除已有数据、认证材料和历史证据。
