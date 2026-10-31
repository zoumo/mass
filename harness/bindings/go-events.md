---
type: Contract
title: Go 实时订阅语言映射
description: 把语言无关的异步订阅流映射到 Go 的只读事件 channel、Err 和 Close。
status: stable
sources:
  - id: direction
    resource: 项目负责人在 2026-10-21 确认通用 Subscription 为异步流，Go 使用只读 channel、Err 和 Close，暂不实现
    title: 订阅的 Go 映射
---

# Go 实时订阅语言映射

本页只规定 Go 如何表达 [Events 异步流合同](../modules/events/design.md#subscription-的异步流合同)，不改变模块职责、实时事件类型或跨语言语义。它是待实现的语言映射，不是现有源码或运行验收证明。[^direction]

```go
type Subscription interface {
    Events() <-chan SessionEvent
    Err() error
    Close() error
}
```

| 通用能力 | Go 表达 |
|---|---|
| 按序异步接收 SessionEvent | 从 Events() 返回的只读 channel 接收；重复调用返回同一 channel |
| 流结束 | channel 关闭；已有缓冲事件仍可读完，之后接收表明结束 |
| 终止原因 | Err()；正常结束为 nil，异常为对应 OperationError 语义的 error |
| 主动取消订阅 | Close()；解除内部 Handler 登记，终止流，不取消 Agent 工作 |

每个 Subscription 以一个有界 buffered channel 承载队列 Handler 接纳的事件，没有前置转发 channel 或额外搬运队列。调用方只有接收权限，不关闭 channel；生产侧和终止操作由 Subscription 实现协调。

异常终止时先保存最终错误，再让 channel 关闭对读取者可见；Err 与接收、Close 并发安全，重复取消保留原终止原因。活动期间 Err() 为 nil 不意味着流已正常结束。Close() 返回的清理错误不替代 Err() 所表达的流终止原因。

队列满时异常终止并解除内部登记，不无限等待接收者。Close() 幂等，不等待 channel 缓冲被外部读完；已进入的发送须与关闭同步，不能向已关闭 channel 发送，也不能让自身注销等待当前 handle 结束。结束后没有新事件入队，已接纳缓冲仍可读出。

这份映射不要求其他语言使用 channel，不规定其方法名；也不规定 goroutine 数量、锁类型或具体调度实现。容量和清理期限不在此指定未经确认的默认值。

[^direction]: 当前人类请求要求先更新 harness，再同步 spec 和 tickets，暂时不要实现。
