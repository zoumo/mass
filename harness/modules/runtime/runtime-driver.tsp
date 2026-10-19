import "./models.tsp";
import "./session-events.tsp";
import "./event-stream.tsp";

namespace MASS.Design.Execution;

/** 绑定具体运行资源。内部维护恢复材料、运行代际及事件来源；不读写资源目录。
 * sessionId 由Service 模块从名称解析得到。命令接纳时固定原运行，旧操作不转向新运行。
 * 每个 Driver 一条事件流，由 Service 独占消费并按 sessionId 路由。 */
interface RuntimeDriver {
  /** 取得本 Driver 唯一事件流，多个 Session 共用；不是广播订阅。
   * Service 在 start 前取得并开始接收；取得本身不启动 Agent。
   * 重复取得报错，不创建第二条流或原生连接；流终止后不能重新订阅补历史。
   * 保证同一 Session 内已接纳事件的顺序，不承诺跨 Session 的业务顺序。
   * 有界背压、隔离、停止排空及错误终止见 events.md。 */
  events(): EventStream<SessionEvent> | OperationError;

  /** 取得本类型的运行资源；重复启动不创建第二个所有者。 */
  start(): CapabilitySet | OperationError;

  /** Service 模块排空会话后调用；停止期间继续消费事件，不能先关闭接收。
   * 正常停止释放运行资源并在已接纳事件交付后结束流；EOF 不代表业务成功。
   * 仍有在途使用时拒绝，结果未知时保留清理责任，不假装正常终止。 */
  stop(): void | OperationError;

  /** 接纳时固定名称；adapter 补齐事件名称与真实 sessionId 后才向共享流交付。 */
  openSession(address: DriverSessionAddress, spec: DriverSessionSpec):
    | DriverSession
    | OperationError;

  /** 内部验证和读取恢复材料；恢复原会话，不能以新建替代。 */
  resumeSession(
    sessionId: string,
    address: DriverSessionAddress,
    spec: DriverSessionSpec,
  ): DriverSession | OperationError;

  sendPrompt(sessionId: string, input: SessionInput):
    | InputReceipt
    | OperationError;

  answerInteraction(
    sessionId: string,
    interaction: InteractionRef,
    answer: InteractionAnswer,
  ): InteractionReceipt | OperationError;

  cancel(sessionId: string): CancelReceipt | OperationError;

  /** 成功表示当前会话运行已释放；保留恢复所需持久材料。 */
  closeSession(sessionId: string): void | OperationError;
}
