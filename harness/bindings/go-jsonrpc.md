---
type: Contract
title: Go JSON-RPC 绑定
description: 规定 sourcegraph/jsonrpc2 的职责、WebSocket 适配、异步分发、上下文和错误边界。
status: draft
sources:
  - id: direction
    resource: 项目负责人在 2026-11-12 指定 github.com/sourcegraph/jsonrpc2
    title: JSON-RPC Go 库选型
  - id: library
    resource: https://github.com/sourcegraph/jsonrpc2
    title: sourcegraph/jsonrpc2
---

# Go JSON-RPC 绑定

使用 [sourcegraph/jsonrpc2](https://github.com/sourcegraph/jsonrpc2) 实现 [JSON-RPC 业务协议](jsonrpc.md) 的请求关联、Call/Notify/Reply 和连接生命周期。此选型不授权库行为覆盖 MASS 的业务合同。具体版本由实施时固定在 Go 依赖清单，不通过本设计文档宣称已经接入。[^direction]

## 库与 MASS 的职责

| 能力 | 归属 |
|---|---|
| 正常请求/响应关联、通知、RPC 错误结构 | jsonrpc2.Conn |
| WebSocket 消息 I/O | ObjectStream 适配；可复用库的 websocket 包并补齐绑定要求 |
| JSON 及信封预校验、明确的 profile 拒绝 | MASS 的薄消息适配 |
| 方法参数与 Service 调用、业务错误映射 | MASS Handler |
| 初始化、活动订阅位置、就绪/终止排序 | MASS 连接状态 |
| 已接纳操作生命周期和副作用结算 | Service |

库的 websocket 适配基于 [gorilla/websocket](https://github.com/gorilla/websocket)，直接 ReadJSON/WriteJSON 不等于完成认证、文本类型校验、重复键校验、大小/写入期限和错误帧约束。优先复用 ObjectStream 扩展点，不复制一套正常 RPC 关联器或引入通用传输注册框架。[^library]

业务方法不接受或返回 *jsonrpc2.Conn、*websocket.Conn、Request、Response 等库对象。库限定在协议入口和客户端；未来 HTTP 绑定可复用 Service 与方法数据，不必把有状态 Conn 强塞进无状态 HTTP handler。

## 明确的兼容范围

上游 README 标注 batch 未支持，ID 实现也不覆盖所有 JSON-RPC 数值/null 形态。因此绑定采用 jsonrpc.md 中明确的单对象、命名参数、字符串或非负安全整数 ID profile；不承诺完整 batch 支持，也不能仅凭库名宣称完整标准兼容。[^library]

ObjectStream 在交给 Conn 前校验原始消息：`jsonrpc` 必须为 `"2.0"`，信封结构、重复键、请求 ID 和文本类型符合合同。缺失 ID 是通知，显式 null ID 是不支持的请求，两者不能在库解码后混为一谈；非负整数范围在进入库前判断，不能把负数转成 uint64 后继续执行。

畸形 JSON/不支持信封由适配输出标准错误码及 null ID，写入仍走同一串行路径；读错误不能无区别映射为普通 EOF。业务参数固定字段严格解码，answer 的缺失和显式 null 分别处理；开放 JSON 值保留数值精度。实现须验证这些检查确实发生，不能用库默认 json.Unmarshal 代替。

不扩展 batch 支持、不维护库 fork。未来需要完整标准形态时，先修订 profile 并验证所选库的适配方案。

## Handler 与并发

Handler 不能长时间阻塞 Conn 的读循环。业务调用异步执行，读循环继续处理其他请求/响应，避免长 create/stop 阻塞 cancel、answer 或查询。可以复用 AsyncHandler 的异步模式，但它本身不提供初始化顺序、订阅名额、请求准入和任务回收保证。

连接状态先同步完成初始化检查、在途 ID 检查和订阅位置预留，再派发耗时任务；不能仅用 AsyncHandler 包住全部处理后依赖 goroutine 调度决定 initialize/subscribe 的先后。拒绝业务时不执行副作用，响应可以按 ID 乱序返回。

所有后台处理都有所属连接或 Service 的明确责任，不为每个事件无限创建 goroutine；发送等待和事件缓冲有界。不得因为连接结束就释放仍被已接纳操作使用的依赖。

## 上下文与退出

库的 Handler context 随连接关闭取消；它只控制接纳前检查、等待响应和观察。已被 Service 接纳的操作使用 Service 自身管理的生命周期，不把库 context 作为工作存活的唯一条件。

Call 的等待截止不等于发送已经中止，更不代表服务端没有执行。库的发送路径不能仅靠 context 被取消就当作已解除阻塞；WebSocket 写入须有期限和关闭唤醒。写入超时后连接不能继续当作健康复用，受影响的副作用结果按未知处理。

本地超时后的迟到响应不触发重试。连接内 pending 调用和处理任务须在响应或连接关闭时回收；长期无响应不能默默累积无限在途状态。实现须验证所固定版本的等待取消与 pending 回收行为，不把超时等同于库已经删除该请求。

客户端 Close 只关闭自己的连接/订阅，不发送 Runtime stop 或 Session close。订阅专用连接让网络失败和本地慢消费者的关闭不影响普通 RPC。

## 发送与订阅顺序

服务端所有成功响应、错误和通知经同一有序写入路径，不能绕过库的发送串行化从另一个 goroutine 直接写 WS。预校验错误若在 Conn 之外产生，也必须使用同一底层写入互斥与期限。

subscribe 成功响应确定先于其通知写出，才开放事件转发；不能依赖 HandlerWithError 自动发送结果与另一个事件 goroutine 的调度竞争。unsubscribe 关闭转发入口，按合同结算已排入发送序列的事件、唯一 closed 和成功响应。事件网络发送不在 Broadcast 的 publish 调用内执行。

## 错误与日志

Service OperationError 映射为 jsonrpc2.Error，固定数值 -32000，Data 保存完整业务 code/reason/message。其他内部 Go error 先脱敏并映射为 -32603，不能让 HandlerWithError 自动把任意 err.Error() 当作公开协议错误或产生未定义的 code=0。

标准解析、信封、方法与参数错误和业务错误分别映射。序列化失败明确报错，不能 panic 后静默断流或把失败包装成成功结果。库日志接入统一日志策略，不记录 token、完整用户内容或未经脱敏的原生错误。

## 验收

以公开消息验证字符串/数字 ID、null/缺失区别、非法 JSON、batch 拒绝、未知方法和业务错误；验证初始化/订阅竞争、长调用与取消并发、响应发送门控、连接关闭后已接纳工作继续、写入超时和 pending 资源回收。

验证独立订阅连接的背压与客户端本地慢消费；只测试库自身 Call 成功不能证明 MASS 的认证、生命周期或订阅语义成立。

[^direction]: 本次人类明确指定该 JSON-RPC 实现库，仍只更新设计。
[^library]: 以官方 README 和 Conn/Handler/ObjectStream/ID 的公开接口核对职责；上游限制与固定版本的差异需在实现验收中显式检查。
