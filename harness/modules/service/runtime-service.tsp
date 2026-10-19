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

  /** 无有效运行时，原子捕获最新 spec/generation 启动；成功后记录 appliedGeneration。
   * 已启动则返回当前运行状态，不因版本落后而重启或热更新。 */
  startRuntime(runtimeRef: string): Runtime | OperationError;

  /**
   * 由内部组装层执行统一停止流程：封闭接纳、排空会话、释放资源。
   * 单独使用 RuntimeService 接口也不能绕过协调，不能直接停止底层进程。
   */
  stopRuntime(runtimeRef: string): Runtime | OperationError;

  /** 删除实例记录；仍检查持久和在途会话引用，并确认己方运行资源已释放。 */
  deleteRuntime(runtimeRef: string): void | OperationError;
}
