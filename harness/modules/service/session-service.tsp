import "./models.tsp";

namespace MASS.Design;

/**
 * 公共 Session 管理接口，范围是全部已纳管 Runtime 的会话。
 * Service 模块按持久归属定位运行时模块的 RuntimeDriver。
 * 公共调用者无需先取得 Runtime 实例或内部运行凭证。
 */
interface SessionService {
  /** 新空目标异步恢复同一原生会话并回放；可选 features/mcpServers 是本次显式覆盖，
   * 与当前 Runtime Session 默认值合并后交给 Driver。先建接收绑定，完成本地提交/分发后可直接输入。
   * 接受不等于存在或完成，结果查询 Session.status.historyLoad；普通 resume 不回放。 */
  @parameterVisibility(Lifecycle.Create)
  loadSession(input: SessionLoad): SessionLoadAccepted | OperationError;

  /** 取消尚未本地完成的原 load 并释放本次恢复资源；最终清理查询状态。
   * 已 completed 拒绝，不误取消后续正常输入或其他客户端工作。 */
  cancelHistoryLoad(session: SessionRef, loadId: string):
    | HistoryLoadStatus
    | OperationError;
  /** input.spec.runtimeRef 选择目标实例，input.metadata.namespace/name 指定名称键；
   * input.spec 中的 features/mcpServers 是 Session 显式值，与当前 Runtime 默认值合并后交给 Driver。
   * 校验后按需启动所属 Runtime，启动失败不打开会话。
   * 返回的 sessionId 在全部 Runtime 中唯一；持久身份保存成功后才公开成功；仅显式 initialInput 在同一创建所有者内发送一次。 */
  @parameterVisibility(Lifecycle.Create)
  createSession(input: SessionCreate): SessionCreated | OperationError;

  /** 统一查询入口；根据持久归属解析 Runtime，不隐式恢复或启动。 */
  getSession(session: SessionRef): Session | OperationError;

  /**
   * 默认覆盖所有 namespace 和已纳管 Runtime 的持久会话，包括离线会话。
   * 按 Runtime 筛选是可选条件；查询不连接各 Agent，不导入未纳管会话。
   */
  listSessions(query?: SessionQuery): Page<Session> | OperationError;

  /** 从 offline 或重启后的 unknown 接纳；校验后按需启动所属 Runtime，读取当前 Session 默认值
   * 并与持久 Session 显式值合并，再恢复原会话，
   * 延续身份与历史且不恢复其他 Session。unknown 失败仍为 unknown，不重放旧输入、
   * 工具或交互。未结算旧责任阻止冲突激活；失败不以新建替代。服务重启不自动调用本操作。 */
  resumeSession(session: SessionRef, input?: SessionResume):
    | SessionResumed
    | OperationError;

  /**
   * 从任意状态接纳并立即建立关闭栅栏：新的 prompt、answer、cancel、resume 被拒绝，
   * get/list/history/watch 保持可用。需要 SDK close 的路径以 SDK Session close 成功
   * 为唯一 native 成功条件；随后仍须结算已接纳交付，结算失败保持 closing，完成后才写入
   * offline。未知且当前无 SDK 句柄时完成 MASS 内部清理并进入 offline；opening 等尚无句柄时接管原
   * 操作，不新建 open/resume。offline 重试直接成功。失败或结果未知保持 closing；
   * 重复调用幂等，closing 可再次 close 显式重试。不隐式 resume、不停止 Runtime，
   * 也不删除身份、历史、恢复材料、Broadcast 或订阅。
   */
  closeSession(session: SessionRef): Session | OperationError;

  /**
   * 仅对当前运行绑定且状态为 idle、running 或 requires_action 的 Session 派发；
   * 不隐式 resume 或启动 Runtime，其他状态在产生副作用前失败并要求先 resume。
   * 返回实际接受证据；接受不等于执行完成，结果未知时不自动重发。
   */
  sendPrompt(session: SessionRef, input: SessionInput):
    | InputReceipt
    | OperationError;

  /** 仅对当前已有有效运行的 Session 直接派发，不隐式恢复；无有效运行时在
   * 派发前返回当前状态并要求先 resume，过期交互不能转向恢复后的新请求。 */
  answerInteraction(
    session: SessionRef,
    interaction: InteractionRef,
    answer: InteractionAnswer,
  ): InteractionReceipt | OperationError;

  /** 仅对当前已有有效运行的 Session 固定接纳时的当前工作，不隐式恢复；
   * 无有效运行时在派发前返回当前状态并要求先 resume。控制确认不等于工作已经终止。 */
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
