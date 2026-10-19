import "../../shared/models.tsp";

namespace MASS.Design.Execution;

/** Driver 的单消费者事件流，不是公共订阅或持久历史游标。
 * next 是逻辑上的异步读取；具体语言绑定负责等待及取消表达。 */
interface EventStream<Event> {
  /** 等待并取下一事件；void 仅表示正常终止，不表示 Session 或工作成功。
   * OperationError 表示流异常终止，不作为虚构 SessionEvent 投递。
   * 不允许并发读取；终止后的读取保持原终止结果，不隐式重连或补历史。
   * 读取等待取消不隐式取消 Agent 工作或关闭 Session；消费责任仍归 Service。 */
  next(): Event | OperationError | void;
}
