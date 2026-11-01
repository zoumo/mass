import "./models.tsp";
import "./session-events.tsp";
import "./event-stream.tsp";

namespace MASS.Design.Execution;

/** 绑定具体运行资源。内部维护恢复材料、运行代际及事件来源；不读写资源目录。
 * sessionId 由Service 模块从名称解析得到。命令接纳时固定原运行，旧操作不转向新运行。
 * 每个 Driver 一条事件流，由 Service 独占消费并按 sessionId 路由。 */
interface RuntimeDriver {
  /** Service 已固定 loadId/预期身份并建立接收绑定。异步恢复与回放；先验证来源。
   * 复用原生 resume 恢复同一会话后回放，成功保留原运行，回放及标记沿唯一 events 流。 */
  loadSession(
    sessionId: string,
    address: DriverSessionAddress,
    cwd: string,
    loadId: string,
  ): void | OperationError;

  /** 取消己方原 load；成功须确认本次恢复资源释放，未知报错。
   * 回放终止沿同一流表达；已结束的原生读取不发送第二个终止标记。
   * Service 本地 completed 后拒绝此操作，普通运行改用 cancel/closeSession。 */
  cancelHistoryLoad(sessionId: string, loadId: string): void | OperationError;
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

  /** 接纳时固定名称；原生 ID 不得为 . 或 ..；adapter 校验 ID 并补齐事件名称后才向共享流交付，非法返回须结算原操作资源。 */
  openSession(address: DriverSessionAddress, spec: DriverSessionSpec):
    | DriverSession
    | OperationError;

  /** 内部验证和读取恢复材料；输入与返回 ID 均不得为 . 或 ..；恢复原会话，不能以新建替代。 */
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

/** 可选的显式输入创建能力；不支持时在打开原生会话前拒绝。 */
interface CreationDriver {
  /** 仅校验，不能打开会话或提交输入。 */
  validateInitialInput(input: SessionInput): void | OperationError;

  /** 先登记工作，再提交显式输入；验证真实身份后返回原所有者的回执。 */
  openSessionWithInput(
    address: DriverSessionAddress,
    spec: DriverSessionSpec,
    input: SessionInput,
  ): DriverSession | OperationError;
}

/** 可选的显式恢复输入能力；校验先于原生副作用。 */
interface ResumptionDriver {
  validateInitialInput(input: SessionInput): void | OperationError;

  /** 验证保存身份/cwd 后定向原会话；Claude 提交新输入后等待 init。
   * 实际观察原 ID/cwd 之前不返回成功或发布事实；失败清理不撤销业务效果。 */
  resumeSessionWithInput(
    sessionId: string,
    address: DriverSessionAddress,
    spec: DriverSessionSpec,
    input: SessionInput,
  ): DriverSession | OperationError;
}
