# 事件处理器合同

处理器由系统构造时登记。当前生产服务默认没有业务处理器，没有动态加载、网络注册、插件配置或数据库投影。具体调用面与验证步骤见[实现指南](develop/handler-implementation.md)，本文只规定行为。

## 选择与观察

处理器可声明消费全部事件、已提交事件或指定公共类型。筛选发生在进入其交付队列之前，业务回调不参与核心提交。

处理器接收不可变事件和来源上下文。上下文包含 Session 身份、具体连接身份、实时或已提交类别及原 Attachment 的命令能力。处理器可以创建自己的副本，不能修改原事件身份、seq 或 payload，不能拦截提交、删除历史或改变 SSE 所见的已提交事件。

只有提交核心能确认已提交事件。提交后通知不保证持久消费进度；可靠同步需要自行保存进度并从 Journal 补读，不能把收到通知当成业务动作已经完成。

## 顺序和生命周期

每个 Session 与登记处理器组合维持一个顺序消费关系，一次只执行一个事件。实时事件保持入口顺序但不获得持久 seq。不同处理器和 Session 独立推进，登记顺序不决定业务完成顺序。有依赖的动作应由同一业务流程协调。

归档或断线保留非运行资源消费者。恢复同一 Session 时继续原消费顺序，不新建旁路跳过旧队列，也不重新运行已存在的处理器构造。每条排队交付保留原连接上下文。历史查询和恢复验证不重新投递旧历史。被停用的组合在服务重启前保持停用。

未完成创建若在原生打开、元信息保存或安装连接前失败，系统清理其临时消费者，取消待处理动作，并在关闭期限内尝试释放资源，不依赖下一次请求触发。只有元信息与连接安装成功后才接受该 Session 的消费者。

## 容量和失败

队列有非零有限容量，另最多有一条正在执行的交付。入队不等待消费者腾出空间，也不为每个事件生成无限并行动作。

过载停用该组合并记录诊断，排空已经接纳的队列后尝试关闭。处理失败或未捕获异常停用该组合，丢弃尚未处理的队列并尝试关闭。构造和关闭异常也记录诊断；它们不阻止 Journal、SSE 或其他健康消费者。诊断不能递归产生更多会话事件。

处理器是受信任的服务内扩展，不是安全沙箱。业务动作不能在提交、生命周期或命令准入的临界区内等待网络、输入、取消或归档。长耗时工作不能阻塞共享执行资源。对不协作或永久阻塞的扩展不承诺有限时间完成关闭。

## 命令和停止

处理器输入、取消和配置操作复用公共校验、命令准入与实际能力限制。命令保持原 Attachment，关闭后拒绝旧工作，不重找新连接或隐式恢复。适配在交付命令或审批前重新检查原连接与控制代际。处理器引发的新事件仍经正常队列交付，业务自行避免无限动作循环。

服务停止先结束新接纳并排空已接纳核心事实，再清理运行资源，最后在一个总消费者期限内排空交付并关闭。超时会终止剩余交付，不能声称关闭动作完成。SSE 消费已排队事件后结束。归档会话保留消费者是有意的服务期状态，不提供崩溃后动作恢复或恰好一次保证。

精确原生边界见[适配合同](design/native-runtime-connectors.md)，存储边界见[Journal](storage.md)，当前验证分组与证据见[测试说明](testing.md)和迁移验证。

Runtime Ticket 02 scopes Handler consumers by stable runtime instance identity and the opaque Agent-returned sessionId. The Rust SessionIdentity carries runtime_instance_id for this internal namespace; it is not another public Session ID. Equal Agent IDs in different instances do not share consumers. Explicit resume of the same pair retains its consumer but supplies fresh attachment-bound commands.

Runtime Ticket 03 makes the bound command contract typed: prompt returns InputReceipt and cancel returns Result<(), CommandError>. CommandError carries a MASS classification, sanitized message, operationId and any observed acceptance evidence. HTTP uses the same handle and separately encodes its result into JSON-RPC. Handlers never parse an external method name, RPC code, SDK error or native cancellation generation. Each failed call has its own operationId even when cancellation shares one in-flight result.
