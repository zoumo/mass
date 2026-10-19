import "../../shared/models.tsp";

namespace MASS.Design;

/** Session 列表的不透明游标，绑定过滤条件；不与历史游标混用。 */
alias PageCursor = string;

model PageRequest {
  /** 排他游标，绑定除 limit 外的过滤条件；排序与并发规则见 Service 文档。 */
  after?: PageCursor;

  /** 默认 100；实现若有容量上限，超限明确报错，不静默截断请求值。 */
  @minValue(1)
  limit?: integer = 100;
}

/** 每个调用者独立的实时订阅，逻辑操作为异步；不补历史。 */
interface EventSubscription<Event> {
  /** 不允许并发 next；void 为正常结束，OperationError 为异常终止。
   * 终止后的读取保持原终止结果，不自动重连。
   * 慢消费者队列溢出时以 OperationError 结束，不伪装为完整交付。 */
  next(): Event | OperationError | void;

  /** 幂等结束本订阅、释放等待与缓冲；后续 next 为 void。
   * 已经异常终止的订阅保留原错误；不取消工作或关闭 Session。 */
  close(): void | OperationError;
}
