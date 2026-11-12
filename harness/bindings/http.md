---
type: Contract
title: HTTP 承载（已替代）
description: 原 REST 与 SSE 绑定的历史入口，当前设计改为 JSON-RPC 2.0 over WebSocket。
status: deprecated
---

# HTTP 承载（已替代）

当前设计由 [JSON-RPC 业务协议](jsonrpc.md) 与 [WebSocket 承载](websocket.md) 定义。旧 REST + SSE 绑定不再是演进合同，不要求保留兼容路由；历史正文由 Git 保存。

当前源码是否已迁移只依据 [实现差距记录](../implementation-gaps.md) 与运行证据判断，不能因设计替换而宣称旧实现已经移除。

未来重新采用 HTTP 的设计边界见 [传输独立性与后续 HTTP 承载](jsonrpc.md#传输独立性与后续-http-承载)。这不表示当前支持 HTTP JSON-RPC 或恢复旧 REST 接口。
