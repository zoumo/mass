---
type: Architecture Decision Record
title: 允许 MCP HTTP 请求头使用字面值
description: 记录 MCP HTTP 凭据的字面值和文件来源及其暴露范围。
status: stable
---

# 允许 MCP HTTP 请求头使用字面值

MASS 接受 `headers[].value` 中的字面 HTTP 请求头，包括 `Authorization`。当前允许调用者直接提交 token，便于为单个 Session 配置 HTTP MCP。字面值会随 Runtime 或 Session 的 spec 保存，拥有配置读取权限的客户端能够读到，也可能出现在备份、原生客户端配置、进程参数和诊断材料中。

调用者可以用 `headers[].valueFrom.fileKeyRef` 引用每个 Session 的私有文件。该方式让持久 spec 保存文件引用。文件内容仍是明文，实际使用的 Header 会交给原生客户端，并通过 MCP 请求发送。解析错误不得包含凭据值。文件引用由 massd 操作者为受信任的配置写入者启用。

Superagent 使用文件来源保存每个 SessionBinding 的凭据。确认旧会话已经释放后，Core 可以原子替换稳定路径上的文件，让 massd 在恢复同一个 Session 时加载新凭据。读取和覆盖规则见 [Runtime Session 默认配置](../modules/runtime/session-defaults.md#mcpservers)。
