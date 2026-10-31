import "../runtime/session-events.tsp";
import "../../shared/models.tsp";

namespace MASS.Design;

/** 可取消的异步事件流，封装 Broadcast 内部的一个队列 Handler。
 * 以下是逻辑能力，不规定目标语言的方法形状、等待机制或队列类型。 */
interface EventSubscription {
  /** 逻辑异步接收：按序等待下一 SessionEvent，void 表示流结束。
   * 已接纳缓冲可继续读取，耗尽后结束；不允许竞争消费同一流。
   * 不要求语言提供 receive/next 方法，可以映射为异步迭代等原生流。 */
  receive(): SessionEvent | void;

  /** 读取终止原因：正常终止为空，异常终止为 OperationError。
   * 原因在流结束对读取者可见前固定；活动中为空不表示未来一定成功。
   * 结束及重复取消不覆盖已有错误。 */
  error(): OperationError | void;

  /** 幂等取消：解除自己的内部 Handler 登记，停止新投递并唤醒接收者。
   * 不等待外部消费全部缓冲，不取消 Agent 工作或关闭 Broadcast。 */
  close(): void | OperationError;
}
