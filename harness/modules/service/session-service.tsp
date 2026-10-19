import "./models.tsp";

namespace MASS.Design;

/**
 * 公共 Session 管理接口，范围是全部已纳管 Runtime 的会话。
 * Service 模块按持久归属定位运行时模块的 RuntimeDriver。
 * 公共调用者无需先取得 Runtime 实例或内部运行凭证。
 */
interface SessionService {
  /** input.spec.runtimeRef 选择目标实例，input.metadata.namespace/name 指定名称键；返回的 sessionId 在全部 Runtime 中唯一；持久身份保存成功后才公开成功，不隐式输入。 */
  @parameterVisibility(Lifecycle.Create)
  createSession(input: SessionCreate): Session | OperationError;

  /** 统一查询入口；根据持久归属解析 Runtime，不隐式恢复或启动。 */
  getSession(session: SessionRef): Session | OperationError;

  /**
   * 默认覆盖所有 namespace 和已纳管 Runtime 的持久会话，包括离线和已归档会话。
   * 按 Runtime 筛选是可选条件；查询不连接各 Agent，不导入未纳管会话。
   */
  listSessions(query?: SessionQuery): Page<Session> | OperationError;

  /** 校验后恢复原会话，延续身份与历史；失败不以新建替代。 */
  resumeSession(session: SessionRef): Session | OperationError;

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
   * 返回独立实时订阅，读取与关闭见 EventSubscription；传输映射由入口负责。
   * 结束订阅不取消 Session 工作，订阅本身不补历史。
   */
  subscribeEvents(session: SessionRef):
    | EventSubscription<SessionEvent>
    | OperationError;
}
