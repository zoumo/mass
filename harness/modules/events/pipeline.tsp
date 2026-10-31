import "./event-handlers.tsp";
import "./observation.tsp";

namespace MASS.Design;

/** 每 Session 一个实时 Broadcast；只管理 Handler，不管理历史或运行关联。 */
interface EventBroadcast {
  /** 按加入顺序串行调用 handle，required 不改变顺序。
   * required 失败停止本事件后续调用并返回错误；普通失败诊断后继续。
   * 不回滚、不自动重试或暂停后续事件；错误可能发生在部分交付之后。
   * 成功不证明异步动作、外部读取或历史提交。 */
  publish(event: SessionEvent): void | OperationError;

  /** 加入已构造实例，成功才移交所有权；不需要 name 或 order。
   * 普通 Handler 随 Broadcast 关闭，没有公共单独移除接口。 */
  addHandler(handler: EventHandler): void | OperationError;

  /** 内部创建 required=false 的队列 Handler，按加入顺序登记后才返回异步流。
   * 只有一个有界队列；Subscription 取消时通过内部句柄解除自己的登记。
   * 不补历史，不要求 Service 组装 Handler 或队列。 */
  subscribe(): EventSubscription | OperationError;

  /** 幂等停止新发布/登记/订阅并关闭全部自有 Handler。
   * 订阅结束，失败如实报告；不关闭共享存储或 Driver。 */
  close(): void | OperationError;
}
