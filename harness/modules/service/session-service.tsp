import "./models.tsp";

namespace MASS.Design;

/**
 * 公共 Session 管理接口，范围是全部已纳管 Runtime 的会话。
 * Service 模块按持久归属定位运行时模块的 RuntimeDriver。
 * 公共调用者无需先取得 Runtime 实例或内部运行凭证。
 */
interface SessionService {
  /** 新空目标异步恢复同一原生会话并回放；先建接收绑定，完成本地提交/分发后可直接输入。
   * 接受不等于存在或完成，结果查询 Session.status.historyLoad；普通 resume 不回放。 */
  @parameterVisibility(Lifecycle.Create)
  loadSession(input: SessionLoad): SessionLoadAccepted | OperationError;

  /** 取消尚未本地完成的原 load 并释放本次恢复资源；最终清理查询状态。
   * 已 completed 拒绝，不误取消后续正常输入或其他客户端工作。 */
  cancelHistoryLoad(session: SessionRef, loadId: string):
    | HistoryLoadStatus
    | OperationError;
  /** input.spec.runtimeRef 选择目标实例，input.metadata.namespace/name 指定名称键；校验后按需启动所属 Runtime，启动失败不打开会话。
   * 返回的 sessionId 在全部 Runtime 中唯一；持久身份保存成功后才公开成功；仅显式 initialInput 在同一创建所有者内发送一次。 */
  @parameterVisibility(Lifecycle.Create)
  createSession(input: SessionCreate): SessionCreated | OperationError;

  /** 统一查询入口；根据持久归属解析 Runtime，不隐式恢复或启动。 */
  getSession(session: SessionRef): Session | OperationError;

  /**
   * 默认覆盖所有 namespace 和已纳管 Runtime 的持久会话，包括离线和已归档会话。
   * 按 Runtime 筛选是可选条件；查询不连接各 Agent，不导入未纳管会话。
   */
  listSessions(query?: SessionQuery): Page<Session> | OperationError;

  /** 校验后按需启动所属 Runtime，再恢复原会话，延续身份与历史；不恢复其他 Session。
   * 未结算旧责任阻止冲突激活；失败不以新建替代。服务重启不自动调用本操作。 */
  resumeSession(session: SessionRef, input?: SessionResume):
    | SessionResumed
    | OperationError;

  /** 确认释放会话运行资源后成功，保留持久身份与历史。 */
  archiveSession(session: SessionRef): Session | OperationError;

  /** 返回实际接受证据；接受不等于执行完成，结果未知时不自动重发。 */
  sendPrompt(session: SessionRef, input: SessionInput):
    | InputReceipt
    | OperationError;

  /** 回答指定交互；过期交互不能转向恢复后的新请求。 */
  answerInteraction(
    session: SessionRef,
    interaction: InteractionRef,
    answer: InteractionAnswer,
  ): InteractionReceipt | OperationError;

  /** 固定接纳时的当前工作；控制确认不等于工作已经终止。 */
  cancel(session: SessionRef): CancelReceipt | OperationError;

  /** 只读取已提交历史，不启动 Runtime、不重演业务动作。 */
  listHistoryEvents(session: SessionRef, query?: HistoryQuery):
    | HistoryPage
    | OperationError;

  /**
   * 解析 Session 后委托唯一 Broadcast.subscribe，返回可取消的异步 EventSubscription；
   * Handler 和队列由 Broadcast 组装，取消订阅解除内部登记；语言/传输映射各自负责。
   * 结束订阅不取消 Session 工作，订阅本身不补历史。
   */
  subscribeEvents(session: SessionRef): EventSubscription | OperationError;
}
