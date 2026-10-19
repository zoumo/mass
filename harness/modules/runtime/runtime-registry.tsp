import "./runtime-driver.tsp";

namespace MASS.Design.Execution;

/** 按类型构造 Driver，并管理当前进程的 name → RuntimeDriver 映射。
 * 类型映射属于实现细节；Registry 不是持久资源目录。
 * Service 串行协调同名实例的登记、启动、停止与移除。
 * Registry 不调用 Driver.start/stop，也不访问 Store。 */
interface RuntimeRegistry {
  /** 校验类型和配置；不创建或登记 Driver、不启动 Agent、不修改持久恢复数据。 */
  validateConfig(type: RuntimeType, config: RuntimeConfig):
    | void
    | OperationError;

  /** 校验配置、构造并原子登记未启动 Driver；同名已登记则报错，不能覆盖原 Driver。
   * name 是 Runtime 的 metadata.name，不增加额外实例键。
   * 失败不留下新登记；不启动 Agent、不修改持久恢复数据。 */
  create(type: RuntimeType, name: string, config: RuntimeConfig):
    | RuntimeDriver
    | OperationError;

  /** 只查当前实例；未登记则报错，不隐式创建或启动。 */
  get(name: string): RuntimeDriver | OperationError;

  /** 只移除映射，不停止 Driver；Service 必须先确认运行资源已释放。
   * 停止或启动结果未知时不得移除，迟到清理不能移除后来登记的实例。 */
  remove(name: string): void | OperationError;
}
