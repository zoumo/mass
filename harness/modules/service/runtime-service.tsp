import "./models.tsp";

namespace MASS.Design;

/** 公共 Runtime 管理接口。实例查询返回信息，不返回内部运行凭证。 */
interface RuntimeService {
  /** 持久登记 metadata 与 spec，分配 uid、createdAt 和 generation=1；namespace 必须为空，名称全局唯一；不启动运行资源。 */
  @parameterVisibility(Lifecycle.Create)
  createRuntime(input: RuntimeCreate): Runtime | OperationError;

  /** 查询实例信息、状态和能力，不隐式创建或启动。 */
  getRuntime(runtimeRef: string): Runtime | OperationError;

  /** 默认查询全部已登记实例；不分页。 */
  listRuntimes(query?: RuntimeQuery): Runtime[] | OperationError;

  /** 完整替换 RuntimeSpec，type/config 可改；metadata 与 status 不接受更新。
   * 只持久更新期望 spec；不受运行状态、会话引用或前置版本条件限制。
   * spec 改变时原子递增 generation；不重启运行或改写 appliedGeneration。 */
  updateRuntime(runtimeRef: string, spec: RuntimeSpec):
    | Runtime
    | OperationError;

  /** 未启动且旧资源已确认释放时，原子捕获最新 spec/generation 启动；成功后记录 appliedGeneration。
   * createSession/resumeSession 自动复用同一按名称串行协调的启动流程，并发只产生一个运行所有者。
   * 已启动则返回当前运行状态，不因版本落后而重启或热更新；不绕过停止栅栏或未知释放。 */
  startRuntime(runtimeRef: string): Runtime | OperationError;

  /**
   * 由 Service 内部的同一停止操作执行完整流程：封闭接纳、排空会话、释放资源，
   * 持续接收最终事件并结算历史提交与实时发布；组装层只注入依赖。
   * 单独使用 RuntimeService 接口也不能绕过协调，不能直接停止底层进程。
   */
  stopRuntime(runtimeRef: string): Runtime | OperationError;

  /** 删除实例记录；仍检查持久和在途会话引用，并确认己方运行资源已释放。 */
  deleteRuntime(runtimeRef: string): void | OperationError;
}
