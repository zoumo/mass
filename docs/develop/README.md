# 实现与验证指南

[设计入口](../design/README.md)规定职责与行为，本目录说明当前源码如何实现这些合同。

- [Rust 工作区与依赖](rust-workspace.md)
- [Codex 客户端与来源](codex-client.md)
- [原生适配实现](native-runtime-record.md)
- [Handler 构造接口](handler-implementation.md)
- [发布工具链](release-implementation.md)
- [语言无关编码原则](rules/code-principle.md)

验证入口见[测试指南](../testing.md)和[构建交付](../single-file-delivery.md)。默认检查不调用模型；真实模型验证须显式执行 `make test-native`。验证结果只适用于实际执行的源码和环境。
