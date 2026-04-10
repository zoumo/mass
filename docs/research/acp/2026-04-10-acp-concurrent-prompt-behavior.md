# ACP Concurrent Prompt Behavior Research

> Date: 2026-04-10
> Topic: 如果向一个正在运行中的 ACP session 再发送 `session/prompt`，协议和 SDK 会发生什么？

## 结论

ACP 协议本身**没有明确规定**当一个 session 正在处理 prompt turn 时，再发送一个新的 `session/prompt` 请求必须返回什么，也没有规定 agent 必须排队、必须拒绝，或必须自动取消前一个 turn。

协议**明确规定**的只有取消语义：

- 客户端如果要取消当前正在进行的 prompt turn，应发送 `session/cancel`
- agent 收到后应尽快停止当前操作
- agent 最终应对原始 `session/prompt` 返回 `cancelled` stop reason

当前 `acp-go-sdk` 的实现行为是：

- 入站请求在连接层按 goroutine 并发分发
- 对同一 `sessionId` 的新 `session/prompt`，`AgentSideConnection` 会先取消前一个 prompt 的 context
- 然后开始执行新的 `agent.Prompt(...)`

因此：

- 这不是协议级“排队”保证
- 也不是协议级“busy error”保证
- 更接近于当前 Go SDK 的一种“新 prompt 抢占旧 prompt context”的实现策略

## 协议层发现

### 1. Prompt turn 被定义为完整交互周期

ACP prompt turn 文档将一次 `session/prompt` 描述为一个完整的 interaction cycle，从用户消息开始，到 agent 完成响应结束。

来源：

- [ACP Prompt Turn](https://agentclientprotocol.com/protocol/prompt-turn)

文档中说明：

- turn 从 `session/prompt` 开始
- agent 完成最终响应后 turn 结束
- 取消当前 turn 的标准方式是 `session/cancel`

这说明协议对“正常流程”的预期是：**一个 turn 完成后，再开始下一个 turn**。

### 2. 协议没有定义“正在 running 时再发一个 prompt”的标准结果

在 ACP prompt turn 文档和 schema 中，没有找到类似下面的明确规定：

- agent 必须返回 busy 错误
- agent 必须排队后续 prompt
- agent 必须隐式取消旧 turn
- client 不得在未完成 turn 上再次发送 `session/prompt`

也就是说，**并发或重叠 prompt 的语义在协议层没有被标准化**。

来源：

- [ACP Prompt Turn](https://agentclientprotocol.com/protocol/prompt-turn)
- [ACP Protocol Schema](https://agentclientprotocol.com/protocol/schema)

### 3. 协议明确规定了取消语义

ACP 文档明确写了：

- 客户端可以通过 `session/cancel` 取消当前 prompt turn
- agent 应尽快终止进行中的模型请求和工具调用
- 在完成收尾后，agent 必须对原始 `session/prompt` 返回 `cancelled`

来源：

- [ACP Prompt Turn: Cancellation](https://agentclientprotocol.com/protocol/prompt-turn#cancellation)

这意味着：**协议明确支持的“打断当前 turn”手段是 cancel，而不是重发 prompt**。

## acp-go-sdk 实现层发现

### 1. 连接层会并发处理入站请求

在本地 `acp-go-sdk` 源码中，连接层收到带 `method` 的入站消息后，会直接启动 goroutine 分发处理：

- [/Users/jim/code/zoumo/acp-go-sdk/connection.go](/Users/jim/code/zoumo/acp-go-sdk/connection.go)

关键行为：

- `receive()` 中对入站 request 执行 `go c.handleInbound(&msg)`

这说明 SDK 连接层**允许请求并发进入 handler**，不会天然串行化同一个 session 的多个 request。

### 2. AgentSideConnection 对同一 session 的新 prompt 会取消旧 prompt context

在 `AgentSideConnection` 对 `session/prompt` 的处理逻辑中：

- 为每个 prompt 创建新的 cancellable context
- 用 `sessionId` 作为 key 存入 `sessionCancels`
- 如果该 `sessionId` 已经存在一个旧的 cancel func，会先调用旧的 `prev()`
- 然后再执行新的 `agent.Prompt(reqCtx, p)`

来源：

- [/Users/jim/code/zoumo/acp-go-sdk/agent.go](/Users/jim/code/zoumo/acp-go-sdk/agent.go)
- [/Users/jim/code/zoumo/acp-go-sdk/agent_gen.go](/Users/jim/code/zoumo/acp-go-sdk/agent_gen.go)

这意味着当前 Go SDK 的行为更接近：

1. 第二个 prompt 到来
2. SDK 先取消前一个 prompt 的 context
3. 然后开始新的 prompt

### 3. 这不是严格的“排队”，也不是严格的“busy reject”

虽然 Go SDK 会取消旧 prompt 的 context，但这不等于：

- 旧 prompt 已经完全结束
- agent 一定会立刻停止旧 turn
- 两次 prompt 一定不会短时间重叠

如果 agent 实现没有及时响应 `ctx.Done()`，就可能出现：

- 旧 prompt 还在清理
- 新 prompt 已开始执行

所以这更像**抢占式 context cancel**，而不是协议保证的串行 turn 机制。

## 对 open-agent-runtime 的设计启示

1. 不应假设 ACP 协议会帮我们定义 busy 语义
- 协议没有规定 running 时重发 prompt 的标准行为

2. 不应假设 `acp-go-sdk` 会天然排队
- 当前实现是并发分发 + 对同 session 新 prompt 取消旧 context
- 这不是可靠的排队保证

3. 如果系统需要确定性行为，必须在上层自己定义
- 要么 `running` 时拒绝新 prompt
- 要么显式先 `agent/cancel`
- 要么实现自己的 per-agent/per-run queue

4. 当前 open-agent-runtime 选择“running 时拒绝”是合理的
- 这避免把未标准化的 ACP 行为暴露成平台契约
- 也避免依赖某个 SDK 当前实现细节

## 最终判断

如果给一个正在运行中的 ACP agent 再发送一个新的 `session/prompt`：

- **ACP 协议**：没有明确规定必须返回什么
- **ACP 推荐控制方式**：发送 `session/cancel` 来取消当前 turn
- **当前 `acp-go-sdk`**：会取消旧 prompt context，并开始新的 prompt

因此，这个场景的行为应被视为：

- **协议未定义**
- **SDK 有当前实现**
- **平台若需要稳定语义，必须自行规定**
