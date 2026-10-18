# 设计文档入口

当前 Rust 产品是本地 Codex 和 codex-remote over HTTP/SSE。设计合同描述职责、数据含义、并发和失败规则；源码、依赖与构建步骤见[实现指南](../develop/README.md)。Go 实验的边界另见[说明](../../mass-go/README.md)。

## 当前合同

- [架构与职责](../architecture.md)、[模块依赖](crate-workspace.md)
- [HTTP API](../http-api.md)、[事件](../events.md)、[存储与恢复](../storage.md)
- [原生适配与生命周期](native-runtime-connectors.md)、[事件提交与历史](event-pipeline-and-history.md)
- [事件处理器](../handlers.md)、[交互与取消](../interactions.md)
- [单文件交付边界](single-file-delivery.md)、[数据位置与所有权](../root-migration.md)

[Runtime 架构方案](runtime-core-architecture.md)包含当前接口职责和未来扩展方向。准确已实现字段与范围以上述当前合同为准。数据库、生产 Claude/ACP、在线实例管理及 Remote 跨连接恢复尚未交付。旧版本代码、规划和原始验证日志不作为当前产品合同。
