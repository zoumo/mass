import "./models.tsp";

namespace MASS.Design.Internal;

scalar OperationRef;
scalar StopBarrier;
scalar SessionDrainProof;

// 仅供Service 模块内部协调；运行时模块不依赖这些接口或资源 DTO。
/**
 * 组装层专用的 RuntimeService 停止控制，SessionService 不负责直接调用。
 * 公共 stopRuntime 按 Runtime 名称固定原 Driver，再通过组装层执行完整流程。
 */
interface RuntimeStopControl {
  /**
   * 原子封闭新 Driver 获取及 open/resume/input/answer，登记本次停止责任。
   * 覆盖已有使用及尚无 sessionId 的打开预留；同实例重复停止复用原操作。
   * cancel/close 与已接纳事实交付仍可推进，查询始终允许。
   */
  beginStop(runtimeRef: string, operation: OperationRef):
    | StopBarrier
    | OperationError;

  /**
   * 校验同一 barrier 的 SessionDrainProof，并再次确认使用预留全部释放。
   * 调用原 Driver.stop 释放本实例拥有的运行资源；确认后更新资源状态。
   * 未知时保留栅栏与停止所有权，不静默重新开放接纳。
   */
  finishStop(barrier: StopBarrier, drained: SessionDrainProof):
    | Runtime
    | OperationError;
}

/** 组装层专用的 SessionService 停止协作；RuntimeService 不依赖此接口。 */
interface SessionStopControl {
  /**
   * 按 barrier 处理全部会话与创建/恢复预留，限制新消费动作。
   * 原操作结算、执行关闭、已接纳事实持久提交及内部运行使用结算均确定完成，
   * 才返回证明。未知时不返回完成证明；迟到资源仍归原操作清理。
   * 保留持久会话、历史和非运行资源消费者；不重演业务动作。
   */
  drainRuntimeSessions(barrier: StopBarrier):
    | SessionDrainProof
    | OperationError;
}
