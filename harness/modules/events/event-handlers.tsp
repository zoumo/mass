import "../runtime/session-events.tsp";
import "../../shared/models.tsp";

namespace MASS.Design;

/** 统一实时 Handler；无 name、order、运行上下文或历史提交标记。 */
interface EventHandler {
  /** 逻辑只读属性，默认 false，加入后固定；不要求语言实现为同名 getter。
   * true 表示本次 handle 必须成功才继续后面的 Handler，不调整加入顺序。 */
  required(): boolean;

  /** 串行处理统一事件；required 的成功指这次调用完成，不是后台动作完成。
   * 慢业务可自行异步处理；状态与订阅 Handler 为 required=false。
   * 不改写事件，不自动重试。 */
  handle(event: SessionEvent): void | OperationError;

  /** 幂等结束自身处理并释放资源；队列终止唤醒读取者，失败如实报告。
   * 不关闭共享存储或 Driver。 */
  close(): void | OperationError;
}
