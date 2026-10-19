import "./models.tsp";

namespace MASS.Design;

/** 声明式选择在进入业务队列前执行，不影响核心历史提交。 */
model EventHandlerRegistration {
  name: string;
  delivery: "all" | "committed" = "committed";

  @minItems(1)
  types?: SessionEventType[];

  /** 由该处理器自行校验的动态配置；不输出明文秘密。 */
  config: Record<unknown> = #{};
}

/** 每个持久 Session 与登记组合构造一次；归档与恢复复用已有健康实例。 */
interface EventHandlerFactory {
  /** session 是构造时的只读快照，不能用其 status 作为未来动作的准入证据。
   * void 表示该 Session 不适用，OperationError 为构造失败。 */
  create(session: Session, config: Record<unknown>):
    | EventHandler
    | OperationError
    | void;
}

/** 只交给当前一次事件回调的操作能力；实现捕获原运行和操作目标。
 * 无法匹配当前运行时明确拒绝，不隐式打开/恢复、不重新定向。
 * cancel 固定原事件关联的工作，answer 固定原交互；无法证明关联时拒绝。
 * 回调返回后失效，不允许保存后异步重用。 */
interface EventHandlerCommands {
  sendPrompt(input: SessionInput): InputReceipt | OperationError;
  answerInteraction(interaction: InteractionRef, answer: InteractionAnswer):
    | InteractionReceipt
    | OperationError;
  cancel(): CancelReceipt | OperationError;
}

/** 每个实例串行处理；事件只读，修改副本不影响其他消费者或历史。 */
interface EventHandler {
  /** committed 表示该事件已提交到历史，非持久类型为 false。
   * commands 仅在回调存续期间使用；接纳的命令仍由原操作结算。
   * 回调失败停用本消费关系，不撤销已接受动作，也不自动重试。 */
  handle(
    event: SessionEvent,
    committed: boolean,
    commands: EventHandlerCommands,
  ): void | OperationError;

  /** Service 在结束交付后调用；实现须幂等。失败保留诊断，不宣称释放成功。 */
  close(): void | OperationError;
}

/** 受信任的进程内扩展入口，不是远程插件协议或新的 HTTP 方法。 */
interface EventHandlerService {
  /** 名称唯一；对现有持久 Session 及后续创建的 Session 建立消费关系。
   * 登记不回放历史，独立构造失败记诊断并停用相应关系，不回滚其他关系。 */
  register(
    registration: EventHandlerRegistration,
    factory: EventHandlerFactory,
  ): void | OperationError;

  /** 停止接纳，排空并关闭该登记的全部关系；确定完成后才移除。
   * 超时或关闭失败不宣称完成，同名登记不能覆盖尚未释放的旧关系。 */
  unregister(name: string): void | OperationError;

  /** 停止接纳并排空、关闭该 Session 的关系；已停用时幂等。 */
  disable(session: SessionRef, name: string): void | OperationError;

  /** 旧实例已关闭后重新构造；不补历史、不重试失败动作，不激活 Session。
   * 健康启用关系重复调用为幂等；不适用 Session 返回 void 且不创建实例。 */
  enable(session: SessionRef, name: string): void | OperationError;
}
